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

	out := &LiveData{AsOf: time.Now().UTC().Format(time.RFC3339)}
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
	for _, l := range def.Links {
		ll := LinkLive{ID: l.ID, State: "nodata"}
		if l.AEndpoint != nil {
			k := key{l.AEndpoint.DeviceID, fmt.Sprint(l.AEndpoint.IfIndex)}
			ll.InBPS, ll.OutBPS = rateIn[k], rateOut[k]
			cap := float64(l.BandwidthBPS)
			if cap == 0 {
				cap = speed[k]
			}
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
