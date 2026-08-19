package maps

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	alertvm "github.com/freezxp/netinv/backend/internal/alerting/adapters/vm"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// LiveData is the ≤30s-fresh payload every viewer of a map shares (FR-MAP-05,
// doc 05 §7).
type LiveData struct {
	AsOf  string     `json:"as_of"`
	Nodes []NodeLive `json:"nodes"`
	Links []LinkLive `json:"links"`
}

type NodeLive struct {
	ID    string `json:"id"`
	State string `json:"state"` // up | warning | critical | unreachable | unknown
}

type LinkLive struct {
	ID      string  `json:"id"`
	InBPS   float64 `json:"in_bps"`
	OutBPS  float64 `json:"out_bps"`
	UtilIn  float64 `json:"util_in"`
	UtilOut float64 `json:"util_out"`
	State   string  `json:"state"` // up | down | partial | nodata | stale
	// CapacityBPS is what utilisation was divided by, 0 when unknown. Sent so
	// the UI can distinguish "idle" from "no capacity to measure against" —
	// both of which otherwise read as 0%.
	CapacityBPS float64 `json:"capacity_bps"`
	// DataAgeS is how old the newest counter sample behind these figures is.
	// The map carries the last known bandwidth forward through a gap rather
	// than dropping to nodata — a link that was busy a minute ago is far
	// better described by that number than by a blank — but carrying a value
	// forward *silently* is how a stale reading gets mistaken for a live one,
	// so the age travels with it and the state says `stale` past the
	// threshold. 0 means the sample is current.
	DataAgeS int `json:"data_age_s"`
}

// newLiveData starts from empty slices rather than nil. A map with no links
// yet must marshal as `[]`, not `null`, or any consumer that iterates the
// payload breaks on a brand-new map — precisely when someone is looking at it.
func newLiveData() *LiveData {
	return &LiveData{
		AsOf:  time.Now().UTC().Format(time.RFC3339),
		Nodes: []NodeLive{},
		Links: []LinkLive{},
	}
}

type LiveAssembler struct {
	Store *Store
	VM    *alertvm.Reader
	Redis *redis.Client
	// PollInterval sizes the rate window and how far a value may be carried.
	// Read per request because the cadence is editable from the UI, and a rate
	// window shorter than it spans one sample and returns nothing — which is
	// the very gap this carry-forward exists to cover. nil falls back to 60s.
	PollInterval func() time.Duration
}

// staleAfter is when a carried value stops being described as current. Two
// minutes is longer than a poll cycle at the default cadence and shorter than
// anyone would call a link "live" without qualification.
const staleAfter = 120

// windows returns the rate window and how far back a value may be carried.
//
// The carry window is bounded on purpose. Forward-filling without a limit is
// how a dead link keeps showing yesterday's traffic; the map should fall back
// to nodata once the data is old enough that nobody should be acting on it.
func (a *LiveAssembler) windows() (rate, carry time.Duration) {
	var poll time.Duration
	if a.PollInterval != nil {
		poll = a.PollInterval()
	}
	if poll <= 0 {
		poll = 60 * time.Second
	}
	rate = 4 * poll
	if rate < 5*time.Minute {
		rate = 5 * time.Minute
	}
	carry = 6 * rate
	if carry > time.Hour {
		carry = time.Hour
	}
	return rate, carry
}

// dur renders a duration for MetricsQL, which rejects Go's "1h0m0s" form.
func dur(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
}

func (a *LiveAssembler) Live(ctx context.Context, mapID string) (*LiveData, error) {
	cacheKey := "map:" + mapID + ":live"
	if a.Redis != nil {
		if raw, err := a.Redis.Get(ctx, cacheKey).Bytes(); err == nil {
			var cached LiveData
			if jsonUnmarshal(raw, &cached) == nil {
				return &cached, nil
			}
		}
	}
	def, _, err := a.Store.Load(ctx, mapID, "published")
	if err != nil {
		return nil, err
	}

	// One batched query set for the whole map (doc 05 §7).
	type key struct {
		dev string
		ifi string
	}
	rateIn := map[key]float64{}
	rateOut := map[key]float64{}
	speed := map[key]float64{}
	oper := map[key]float64{}
	lastSeen := map[key]float64{} // unix seconds of the newest counter sample
	rw, carry := a.windows()
	// last_over_time over a subquery rather than a bare instant rate: an
	// instant query returns nothing the moment the rate window has fewer than
	// two samples in it, which happens on every collection gap — a poller
	// restart, a redeploy, a device that stopped answering a minute ago — and
	// the whole map drops to grey while the network is fine. Carrying the last
	// computed rate forward keeps the map showing what the link was doing
	// until something newer arrives.
	queries := []struct {
		expr string
		dst  map[key]float64
	}{
		{fmt.Sprintf(`last_over_time((rate(netinv_if_in_octets_total[%s]) * 8)[%s:%s])`,
			dur(rw), dur(carry), dur(rw)), rateIn},
		{fmt.Sprintf(`last_over_time((rate(netinv_if_out_octets_total[%s]) * 8)[%s:%s])`,
			dur(rw), dur(carry), dur(rw)), rateOut},
		{fmt.Sprintf(`last_over_time(netinv_if_speed_bps[%s])`, dur(carry)), speed},
		{fmt.Sprintf(`last_over_time(netinv_if_oper_status[%s])`, dur(carry)), oper},
		// The age of the newest raw sample, which is what makes the carried
		// value honest rather than merely present.
		//
		// tlast_over_time, not timestamp(last_over_time(...)). The latter
		// returns the timestamp of the rollup's own result — computed on
		// VictoriaMetrics' evaluation grid — which came back as a uniform 270s
		// for every series on a fleet whose newest samples were 14 to 64
		// seconds old. Every link on a healthy map reported itself stale.
		{fmt.Sprintf(`tlast_over_time(netinv_if_in_octets_total[%s])`,
			dur(carry)), lastSeen},
	}
	for _, q := range queries {
		series, err := a.VM.Query(ctx, q.expr)
		if err != nil {
			return nil, errx.Wrap(errx.KindTransient, err, "live query")
		}
		for _, s := range series {
			q.dst[key{s.Labels["device_id"], s.Labels["if_index"]}] = s.Value
		}
	}
	// An ifIndex is not a stable identifier, so the one saved into the map
	// document when the link was drawn is only a snapshot. Agents renumber:
	// a pilot gateway rebooted and moved ppp2 from ifIndex 76 to 41, and every
	// link still pointing at 76 went flat while the interface itself was busy.
	//
	// maps.map_links already carries the stable interface row id alongside each
	// link, so the current index can be resolved at render time. The document's
	// value stays as the fallback: a link drawn against a device that has since
	// been deleted still renders as nodata rather than failing the whole map.
	curIdx := map[string]string{} // link id + "/a"|"/b" → current ifIndex
	if rows, err := a.Store.Pool.Query(ctx, `
		SELECT l.link_key,
		       ia.if_index::text, ib.if_index::text
		FROM maps.map_links l
		LEFT JOIN inventory.interfaces ia ON ia.id = l.a_if_id
		LEFT JOIN inventory.interfaces ib ON ib.id = l.b_if_id
		WHERE l.map_id = $1`, mapID); err == nil {
		for rows.Next() {
			var linkKey string
			var aIdx, bIdx *string
			if rows.Scan(&linkKey, &aIdx, &bIdx) != nil {
				continue
			}
			if aIdx != nil {
				curIdx[linkKey+"/a"] = *aIdx
			}
			if bIdx != nil {
				curIdx[linkKey+"/b"] = *bIdx
			}
		}
		rows.Close()
	}

	// Node states: device reachability + worst active alert (one query each).
	icmpUp := map[string]float64{}
	if series, err := a.VM.Query(ctx, `netinv_icmp_up`); err == nil {
		for _, s := range series {
			icmpUp[s.Labels["device_id"]] = s.Value
		}
	}
	worst := map[string]string{}
	rows, err := a.Store.Pool.Query(ctx, `
		SELECT device_id, min(CASE severity WHEN 'critical' THEN 1 ELSE 2 END)
		FROM alerting.alert_instances
		WHERE state IN ('firing','acknowledged','flapping') AND device_id IS NOT NULL
		GROUP BY device_id`)
	if err == nil {
		for rows.Next() {
			var dev string
			var sev int
			if rows.Scan(&dev, &sev) == nil {
				if sev == 1 {
					worst[dev] = "critical"
				} else {
					worst[dev] = "warning"
				}
			}
		}
		rows.Close()
	}

	out := newLiveData()
	for _, n := range def.Nodes {
		state := "unknown"
		if n.Kind == "device" && n.DeviceID != "" {
			switch {
			case icmpUp[n.DeviceID] == 0 && hasKey(icmpUp, n.DeviceID):
				state = "unreachable"
			case worst[n.DeviceID] != "":
				state = worst[n.DeviceID]
			case hasKey(icmpUp, n.DeviceID):
				state = "up"
			}
		}
		out.Nodes = append(out.Nodes, NodeLive{ID: n.ID, State: state})
	}
	wan := a.wanCapacities(ctx, def)
	for _, l := range def.Links {
		ll := LinkLive{ID: l.ID, State: "nodata"}
		ep, mirrored := linkEndpoint(l)
		if ep != nil {
			side := "/a"
			if mirrored {
				side = "/b"
			}
			ifi := fmt.Sprint(ep.IfIndex)
			if cur, ok := curIdx[l.ID+side]; ok && cur != "" {
				ifi = cur
			}
			k := key{ep.DeviceID, ifi}
			in, eg := rateIn[k], rateOut[k]
			if mirrored {
				// in/out stay relative to the link's own A side, so reading
				// from the far end swaps them: what B receives is what A sent.
				in, eg = eg, in
			}
			ll.InBPS, ll.OutBPS = in, eg
			cap := linkCapacity(l, speed[k], wan)
			ll.CapacityBPS = cap
			if cap > 0 {
				ll.UtilIn = 100 * ll.InBPS / cap
				ll.UtilOut = 100 * ll.OutBPS / cap
			}
			switch oper[k] {
			case 1:
				ll.State = "up"
			case 2:
				ll.State = "down"
			}
			// Age travels with the carried value. A link that has been quiet
			// for six minutes is still showing its last known throughput, and
			// an operator has to be able to tell that from a live reading —
			// otherwise a stopped poller looks exactly like a steady link.
			if ts, ok := lastSeen[k]; ok {
				if age := int(time.Since(time.Unix(int64(ts), 0)).Seconds()); age > 0 {
					ll.DataAgeS = age
					if age > staleAfter && ll.State != "down" {
						ll.State = "stale"
					}
				}
			}
		}
		out.Links = append(out.Links, ll)
	}
	if a.Redis != nil {
		if raw, err := jsonMarshal(out); err == nil {
			_ = a.Redis.Set(ctx, cacheKey, raw, 15*time.Second).Err()
		}
	}
	return out, nil
}

func hasKey(m map[string]float64, k string) bool {
	_, ok := m[k]
	return ok
}

// wanCapacities loads the operator-stated uplink rate of every device on the
// map, for links whose own interfaces report no speed.
func (a *LiveAssembler) wanCapacities(ctx context.Context, def *Definition) map[string]float64 {
	ids := make([]string, 0, len(def.Nodes))
	for _, n := range def.Nodes {
		if n.DeviceID != "" {
			ids = append(ids, n.DeviceID)
		}
	}
	for _, l := range def.Links {
		for _, ep := range []*Endpoint{l.AEndpoint, l.BEndpoint} {
			if ep != nil && ep.DeviceID != "" {
				ids = append(ids, ep.DeviceID)
			}
		}
	}
	out := map[string]float64{}
	if len(ids) == 0 {
		return out
	}
	rows, err := a.Store.Pool.Query(ctx, `
		SELECT id, wan_capacity_bps FROM inventory.devices
		WHERE id = any($1) AND wan_capacity_bps IS NOT NULL`, ids)
	if err != nil {
		return out // capacity is a nicety; never fail the map over it
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var bps int64
		if rows.Scan(&id, &bps) == nil {
			out[id] = float64(bps)
		}
	}
	return out
}

// linkEndpoint picks the interface that describes a link, and reports whether
// its directions must be mirrored to stay relative to the link's A side.
//
// A link often has only one pollable end — a mesh AP running no SNMP agent, an
// ISP cloud, any plain node — and one interface describes the whole wire
// either way. Reading only the A endpoint left those blank for no better
// reason than which way round the link happened to be drawn.
func linkEndpoint(l Link) (*Endpoint, bool) {
	if l.AEndpoint != nil {
		return l.AEndpoint, false
	}
	if l.BEndpoint != nil {
		// What B receives is what A sent, so in and out swap.
		return l.BEndpoint, true
	}
	return nil, false
}

// linkCapacity decides what to divide traffic by, most specific first:
//
//  1. a capacity set on the link — an operator overriding everything;
//  2. the A-side interface's own ifSpeed, which is right for physical links;
//  3. the slower of the two ends' uplink rates. A tunnel's interfaces report
//     no speed, and a site-to-site tunnel can only run as fast as the smaller
//     of the two circuits carrying it (FR-MAP-08).
//
// Returns 0 when nothing is known, which leaves the link uncoloured rather
// than inventing a denominator.
func linkCapacity(l Link, ifSpeed float64, wan map[string]float64) float64 {
	if l.BandwidthBPS > 0 {
		return float64(l.BandwidthBPS)
	}
	if ifSpeed > 0 {
		return ifSpeed
	}
	slowest := 0.0
	for _, ep := range []*Endpoint{l.AEndpoint, l.BEndpoint} {
		if ep == nil {
			continue
		}
		c, ok := wan[ep.DeviceID]
		if !ok || c <= 0 {
			// One end unknown means the bottleneck is unknown. Guessing from
			// the other end would overstate the capacity and under-report
			// utilisation, which is the wrong way to be wrong.
			return 0
		}
		if slowest == 0 || c < slowest {
			slowest = c
		}
	}
	return slowest
}
