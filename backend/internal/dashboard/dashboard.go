// Package dashboard — cached aggregate assembly for the NOC panels
// (doc 09 §8). v1 uses cache-aside with short TTLs: every viewer shares one
// computed payload per panel (doc 05 §7's goal); the background refresher
// variant becomes worthwhile with many concurrent viewers.
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	alertvm "github.com/freezxp/netinv/backend/internal/alerting/adapters/vm"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type Service struct {
	Pool    *pgxpool.Pool
	VM      *alertvm.Reader
	Redis   *redis.Client
	Checker authz.Checker
	TTL     time.Duration // cache TTL, default 15s
}

func (s *Service) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(s.Checker, authz.MetricsRead))
		g.Get("/dashboard/summary", s.cached("summary", s.summary))
		g.Get("/dashboard/top", s.top) // cached per list param inside
		g.Get("/dashboard/heatmap", s.cached("heatmap", s.heatmap))
		g.Get("/dashboard/watchlist", s.cached("watchlist", s.watchlist))
		g.Get("/dashboard/device-health", s.cached("device-health", s.deviceHealth))
	})
}

func (s *Service) ttl() time.Duration {
	if s.TTL == 0 {
		return 15 * time.Second
	}
	return s.TTL
}

type builder func(ctx context.Context) (any, error)

// cached serves one shared payload per panel (FR-DASH: no per-viewer fan-out).
func (s *Service) cached(key string, build builder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := s.getOrBuild(r.Context(), "dash:"+key, build)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}
}

func (s *Service) getOrBuild(ctx context.Context, key string, build builder) ([]byte, error) {
	if s.Redis != nil {
		if raw, err := s.Redis.Get(ctx, key).Bytes(); err == nil {
			return raw, nil
		}
	}
	v, err := build(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := map[string]any{"as_of": time.Now().UTC().Format(time.RFC3339), "data": v}
	raw, err := json.Marshal(wrapped)
	if err != nil {
		return nil, err
	}
	if s.Redis != nil {
		_ = s.Redis.Set(ctx, key, raw, s.ttl()).Err()
	}
	return raw, nil
}

// ---- summary (doc 09 §8) ----

func (s *Service) summary(ctx context.Context) (any, error) {
	out := map[string]any{}
	devices := map[string]int{}
	rows, err := s.Pool.Query(ctx, `
		SELECT status::text, count(*) FROM inventory.devices
		WHERE status != 'retired' GROUP BY status`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "device counts")
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		devices[st] = n
	}
	rows.Close()
	out["devices"] = map[string]int{
		"up": devices["active"], "down": 0, "unreachable": devices["unreachable"],
		"pending": devices["pending"], "disabled": devices["disabled"],
	}

	alerts := map[string]int{"critical": 0, "warning": 0, "info": 0}
	rows, err = s.Pool.Query(ctx, `
		SELECT severity::text, count(*) FROM alerting.alert_instances
		WHERE state IN ('firing','acknowledged','flapping') GROUP BY severity`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "alert counts")
	}
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			rows.Close()
			return nil, err
		}
		alerts[sev] = n
	}
	rows.Close()
	out["alerts"] = alerts

	// Availability: mean ICMP up-ness across the estate, rolling 24h.
	if series, err := s.VM.Query(ctx, `avg(avg_over_time(netinv_icmp_up[24h])) * 100`); err == nil && len(series) > 0 {
		out["availability_24h"] = round2(series[0].Value)
	}
	// Aggregate throughput now (bps, 5m rate).
	tput := map[string]float64{}
	if series, err := s.VM.Query(ctx, `sum(rate(netinv_if_in_octets_total[5m])) * 8`); err == nil && len(series) > 0 {
		tput["in"] = round2(series[0].Value)
	}
	if series, err := s.VM.Query(ctx, `sum(rate(netinv_if_out_octets_total[5m])) * 8`); err == nil && len(series) > 0 {
		tput["out"] = round2(series[0].Value)
	}
	out["throughput_bps"] = tput
	return out, nil
}

// ---- top-N ----

var topQueries = map[string]string{
	// group_left() is load-bearing: a plain `on(...)` division keeps only the
	// listed labels, so device and site were dropped and every row rendered as
	// a bare "if 5" with no way to tell which box it was on.
	"if_utilization": `topk(10, 100 * rate(netinv_if_in_octets_total[5m]) * 8
		/ on(device_id, if_index) group_left() (netinv_if_speed_bps > 0))`,
	"if_traffic": `topk(10, rate(netinv_if_in_octets_total[5m]) * 8)`,
	"if_errors": `topk(10, rate(netinv_if_in_errors_total[15m])
		+ rate(netinv_if_out_errors_total[15m]))`,
	"cpu":    `topk(10, netinv_device_cpu_percent)`,
	"memory": `topk(10, 100 * netinv_device_memory_used_bytes / netinv_device_memory_total_bytes)`,
}

func (s *Service) top(w http.ResponseWriter, r *http.Request) {
	list := r.URL.Query().Get("list")
	expr, ok := topQueries[list]
	if !ok {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid,
			"list must be one of if_utilization, if_traffic, if_errors, cpu, memory"))
		return
	}
	payload, err := s.getOrBuild(r.Context(), "dash:top:"+list, func(ctx context.Context) (any, error) {
		series, err := s.VM.Query(ctx, expr)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(series))
		for i, se := range series {
			out = append(out, map[string]any{
				"rank": i + 1, "value": round2(se.Value),
				"device_id": se.Labels["device_id"], "device": se.Labels["device"],
				"site": se.Labels["site"], "if_index": se.Labels["if_index"],
			})
		}
		s.enrich(ctx, out)
		return out, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

// enrich fills in the names a metric label set cannot carry: interfaces are
// identified in VictoriaMetrics by if_index alone, and the `device` label holds
// the operator's label rather than the device's own sysName. A row reading
// "if 5" tells a reader nothing, so both are resolved from inventory in one
// round trip and the device is named the way the inventory list names it —
// sysName leading, operator label beside it (doc 30 §5).
func (s *Service) enrich(ctx context.Context, rows []map[string]any) {
	ids := map[string]bool{}
	for _, r := range rows {
		if id, _ := r["device_id"].(string); id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}

	type devName struct{ sysName, name string }
	devs := map[string]devName{}
	if drows, err := s.Pool.Query(ctx, `
		SELECT id, coalesce(sys_name,''), name FROM inventory.devices
		WHERE id = any($1)`, list); err == nil {
		for drows.Next() {
			var id, sys, name string
			if drows.Scan(&id, &sys, &name) == nil {
				devs[id] = devName{sys, name}
			}
		}
		drows.Close()
	}

	ifNames := map[string]string{} // device_id|if_index → name
	if irows, err := s.Pool.Query(ctx, `
		SELECT device_id, if_index, coalesce(name,'') FROM inventory.interfaces
		WHERE device_id = any($1) AND state != 'removed'`, list); err == nil {
		for irows.Next() {
			var dev, name string
			var idx int
			if irows.Scan(&dev, &idx, &name) == nil && name != "" {
				ifNames[dev+"|"+strconv.Itoa(idx)] = name
			}
		}
		irows.Close()
	}

	for _, r := range rows {
		id, _ := r["device_id"].(string)
		if d, ok := devs[id]; ok {
			// Prefer what the device calls itself; keep the operator's label
			// when it differs, since that is what a human searched for.
			if d.sysName != "" {
				r["device"] = d.sysName
				if d.name != "" && d.name != d.sysName {
					r["device_label"] = d.name
				}
			} else if d.name != "" {
				r["device"] = d.name
			}
		}
		if idx, _ := r["if_index"].(string); idx != "" {
			if n, ok := ifNames[id+"|"+idx]; ok {
				r["if_name"] = n
			}
		}
	}
}

// ---- heatmap: one cell per device, worst current condition ----

func (s *Service) heatmap(ctx context.Context) (any, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.name, s.name, d.status::text,
			coalesce((SELECT min(CASE ai.severity WHEN 'critical' THEN 1
			                     WHEN 'warning' THEN 2 ELSE 3 END)
			          FROM alerting.alert_instances ai
			          WHERE ai.device_id = d.id
			            AND ai.state IN ('firing','acknowledged','flapping')), 0)
		FROM inventory.devices d
		JOIN platform.sites s ON s.id = d.site_id
		WHERE d.status != 'retired' ORDER BY s.name, d.name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "heatmap")
	}
	defer rows.Close()
	// Non-nil so an empty estate serializes as [] rather than null — clients
	// iterate list payloads directly (FR-API-02).
	out := []map[string]any{}
	for rows.Next() {
		var id, name, site, status string
		var worst int
		if err := rows.Scan(&id, &name, &site, &status, &worst); err != nil {
			return nil, err
		}
		class := "ok"
		switch {
		case status == "unreachable":
			class = "unreachable"
		case status == "disabled" || status == "pending":
			class = "muted"
		case worst == 1:
			class = "critical"
		case worst == 2:
			class = "warning"
		}
		out = append(out, map[string]any{
			"device_id": id, "device": name, "site": site, "class": class,
		})
	}
	return out, rows.Err()
}

// deviceHealth returns the latest CPU / memory / temperature per device in one
// payload, so the inventory list can show live stats with a single request
// instead of one query per row (NFR-12: no per-viewer, per-row fan-out).
func (s *Service) deviceHealth(ctx context.Context) (any, error) {
	out := map[string]map[string]float64{}
	set := func(deviceID, field string, v float64) {
		if deviceID == "" {
			return
		}
		if out[deviceID] == nil {
			out[deviceID] = map[string]float64{}
		}
		out[deviceID][field] = v
	}
	queries := []struct {
		field, expr string
		max         bool // keep the highest value across series (sensors)
	}{
		{"cpu", `netinv_device_cpu_percent`, true},
		{"memory", `netinv_device_memory_percent`, false},
		{"temp", `netinv_sensor_temperature_celsius`, true},
		{"load", `netinv_device_load_average{period="1m"}`, false},
	}
	for _, q := range queries {
		series, err := s.VM.Query(ctx, q.expr)
		if err != nil {
			continue // a missing family must not fail the whole payload
		}
		for _, se := range series {
			id := se.Labels["device_id"]
			if q.max {
				if prev, ok := out[id][q.field]; ok && prev >= se.Value {
					continue
				}
			}
			set(id, q.field, round2(se.Value))
		}
	}
	return out, nil
}

// ---- capacity watchlist: sustained >70% links with trend (doc 30 §2) ----

func (s *Service) watchlist(ctx context.Context) (any, error) {
	series, err := s.VM.Query(ctx, `
		avg_over_time((100 * rate(netinv_if_in_octets_total[5m]) * 8
			/ on(device_id, if_index) group_left() (netinv_if_speed_bps > 0))[24h:10m]) > 70`)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(series))
	for _, se := range series {
		out = append(out, map[string]any{
			"device_id": se.Labels["device_id"], "device": se.Labels["device"],
			"if_index": se.Labels["if_index"], "site": se.Labels["site"],
			"avg_util_24h": round2(se.Value),
		})
	}
	s.enrich(ctx, out)
	return out, nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
