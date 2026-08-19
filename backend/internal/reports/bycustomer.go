package reports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// UntaggedCustomer is the group interfaces with no customer land in. They are
// grouped rather than dropped: an invoice-shaped report that silently omits
// whatever nobody has tagged yet is how a circuit goes unbilled for a year.
const UntaggedCustomer = ""

// MaxGroups caps a grouped report. Each customer contributes its interfaces to
// one expression, so the cost is the interface count, not the customer count —
// but a fleet-wide grouping still has to stop somewhere.
const MaxGroups = 200

// ByCustomer aggregates traffic per customer rather than per interface.
//
// The aggregate is summed *before* it is rolled up, and that is the whole
// point of doing this server-side. Adding up per-interface peaks would
// overstate a customer's peak by assuming every circuit peaked at the same
// instant, and adding up per-interface 95th percentiles is not a 95th
// percentile of anything — a customer's p95 is the 95th percentile of their
// combined traffic, which can only be computed from the combined series. Only
// the byte totals are safe to add after the fact, and they are added the same
// way for consistency.
func (s *Service) ByCustomer(ctx context.Context, f postgres.InterfaceFilter,
	from, to time.Time, limit int) (*Report, error) {
	if !to.After(from) {
		return nil, errx.New(errx.KindInvalid, "the report window ends before it starts")
	}
	if limit <= 0 || limit > MaxRows {
		limit = MaxRows
	}
	ifaces, total, err := s.Interfaces.FindInterfaces(ctx, f, limit, 0)
	if err != nil {
		return nil, err
	}
	rep := &Report{Query: f.Q, Customer: f.Customer, From: from, To: to,
		GroupedBy: "customer", Truncated: total > len(ifaces), Rows: []Row{}}
	if len(ifaces) == 0 {
		return rep, nil
	}

	groups := map[string][]postgres.InterfaceSearchRow{}
	for _, i := range ifaces {
		groups[i.Customer] = append(groups[i.Customer], i)
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > MaxGroups {
		names = names[:MaxGroups]
		rep.Truncated = true
	}

	byName := make(map[string]*Row, len(names))
	for _, n := range names {
		g := groups[n]
		row := Row{Customer: n, Interfaces: len(g),
			AvgUtilPct: -1, P95UtilPct: -1, MaxUtilPct: -1}
		// A group's speed is the sum of its members', but only when every
		// member reports one. A partial sum would understate the denominator
		// and report a customer as more congested than they are — on the
		// strength of the circuits whose speed happens to be known.
		known := true
		for _, i := range g {
			if i.SpeedBPS <= 0 {
				known = false
				break
			}
			row.SpeedBPS += i.SpeedBPS
		}
		if !known {
			row.SpeedBPS = 0
		}
		rep.Rows = append(rep.Rows, row)
	}
	for i := range rep.Rows {
		byName[rep.Rows[i].Customer] = &rep.Rows[i]
	}

	rateWin := s.rateWindow()
	win := to.Sub(from)
	step := subStep(win, rateWin)

	ratedOf := func(sel string) string {
		return fmt.Sprintf(
			`label_set(rate(netinv_if_in_octets_total{%s}[%s]) * 8, "dir", "in")`+
				` or label_set(rate(netinv_if_out_octets_total{%s}[%s]) * 8, "dir", "out")`,
			sel, dur(rateWin), sel, dur(rateWin))
	}
	totalOf := func(sel string) string {
		return fmt.Sprintf(
			`label_set(increase(netinv_if_in_octets_total{%s}[%s]), "dir", "in")`+
				` or label_set(increase(netinv_if_out_octets_total{%s}[%s]), "dir", "out")`,
			sel, dur(win), sel, dur(win))
	}

	stats := []struct {
		build  func(string) string // group expression -> full query
		series func(string) string // selector -> series expression
		assign func(r *Row, dir string, v float64)
	}{
		{func(u string) string { return fmt.Sprintf("avg_over_time((%s)[%s:%s])", u, dur(win), dur(step)) },
			ratedOf, func(r *Row, dir string, v float64) { setDir(&r.AvgInBPS, &r.AvgOutBPS, dir, v) }},
		{func(u string) string {
			return fmt.Sprintf("quantile_over_time(0.95, (%s)[%s:%s])", u, dur(win), dur(step))
		}, ratedOf, func(r *Row, dir string, v float64) { setDir(&r.P95InBPS, &r.P95OutBPS, dir, v) }},
		{func(u string) string { return fmt.Sprintf("max_over_time((%s)[%s:%s])", u, dur(win), dur(step)) },
			ratedOf, func(r *Row, dir string, v float64) { setDir(&r.MaxInBPS, &r.MaxOutBPS, dir, v) }},
		{func(u string) string { return u }, totalOf,
			func(r *Row, dir string, v float64) { setDir(&r.TotalInBytes, &r.TotalOutBytes, dir, v) }},
	}
	for _, st := range stats {
		for _, batch := range batchGroups(names, groups, st.series) {
			samples, err := s.Metrics.QueryAt(ctx, st.build(batch), to)
			if err != nil {
				return nil, err
			}
			for _, sm := range samples {
				if row := byName[sm.Labels[custLabel]]; row != nil {
					st.assign(row, sm.Labels["dir"], sm.Value)
				}
			}
		}
	}

	for i := range rep.Rows {
		r := &rep.Rows[i]
		if r.SpeedBPS > 0 {
			sp := float64(r.SpeedBPS)
			r.AvgUtilPct = 100 * maxf(r.AvgInBPS, r.AvgOutBPS) / sp
			r.P95UtilPct = 100 * maxf(r.P95InBPS, r.P95OutBPS) / sp
			r.MaxUtilPct = 100 * maxf(r.MaxInBPS, r.MaxOutBPS) / sp
		}
	}
	sort.SliceStable(rep.Rows, func(a, b int) bool {
		ra, rb := rep.Rows[a], rep.Rows[b]
		return ra.TotalInBytes+ra.TotalOutBytes > rb.TotalInBytes+rb.TotalOutBytes
	})
	return rep, nil
}

// custLabel carries the group name through the query. It is not a label any
// collector writes — it is attached by label_set on the way out, because the
// customer↔interface mapping lives in the database and the metrics store has
// never heard of it.
const custLabel = "netinv_group"

// maxQueryLen mirrors VictoriaMetrics' -search.maxQueryLen, whose default is
// 16 KiB. A fleet-wide grouped report first tried to ask in one expression and
// was refused at 49 KiB — the limit is on the query, so POSTing it does not
// help. Batching is the fix, with headroom for the rollup wrapper.
const maxQueryLen = 14000

// groupExpr builds one customer's `sum by (dir)`, labelled with the group name.
//
// Selectors are emitted per *device*, with that device's ifIndexes as a regex.
// Pinning the device makes the regex exact — the cross-product hazard only
// exists when both sides are patterns — and it shortens the expression by an
// order of magnitude on a real fleet, which is what keeps a grouped report
// inside the query-length limit at all.
func groupExpr(name string, ifaces []postgres.InterfaceSearchRow,
	series func(sel string) string) string {
	byDevice := map[string][]string{}
	order := []string{}
	for _, i := range ifaces {
		if _, seen := byDevice[i.DeviceID]; !seen {
			order = append(order, i.DeviceID)
		}
		byDevice[i.DeviceID] = append(byDevice[i.DeviceID], strconv.Itoa(i.IfIndex))
	}
	sels := make([]string, 0, len(order))
	for _, d := range order {
		idx := byDevice[d]
		sort.Strings(idx)
		sels = append(sels, series(fmt.Sprintf(`device_id=%q,if_index=~%q`,
			d, strings.Join(idx, "|"))))
	}
	return fmt.Sprintf(`label_set(sum by (dir) (%s), %q, %q)`,
		strings.Join(sels, " or "), custLabel, name)
}

// batchGroups packs group expressions into as few queries as fit under the
// length limit. Groups are independent, so splitting them across queries
// changes no number — unlike splitting a single group, which would break the
// sum that makes the aggregate correct in the first place.
//
// A single group too large to fit alone is still sent alone: it will fail
// loudly against the store's own limit rather than be silently split into an
// answer that looks plausible and is wrong.
func batchGroups(names []string, groups map[string][]postgres.InterfaceSearchRow,
	series func(sel string) string) []string {
	var out []string
	var cur []string
	size := 0
	for _, n := range names {
		e := groupExpr(n, groups[n], series)
		if len(cur) > 0 && size+len(e)+4 > maxQueryLen {
			out = append(out, strings.Join(cur, " or "))
			cur, size = nil, 0
		}
		cur = append(cur, e)
		size += len(e) + 4
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " or "))
	}
	return out
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
