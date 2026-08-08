package maps

import (
	"context"
	"fmt"
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
	State   string  `json:"state"` // up | down | partial | nodata
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
	queries := []struct {
		expr string
		dst  map[key]float64
	}{
		{`rate(netinv_if_in_octets_total[5m]) * 8`, rateIn},
		{`rate(netinv_if_out_octets_total[5m]) * 8`, rateOut},
		{`netinv_if_speed_bps`, speed},
		{`netinv_if_oper_status`, oper},
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
		if l.AEndpoint != nil {
			k := key{l.AEndpoint.DeviceID, fmt.Sprint(l.AEndpoint.IfIndex)}
			ll.InBPS, ll.OutBPS = rateIn[k], rateOut[k]
			cap := linkCapacity(l, speed[k], wan)
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
