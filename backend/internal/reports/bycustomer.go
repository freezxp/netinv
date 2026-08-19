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

	rated := groupUnion(names, groups, func(sel string) string {
		return fmt.Sprintf(
			`label_set(rate(netinv_if_in_octets_total{%s}[%s]) * 8, "dir", "in")`+
				` or label_set(rate(netinv_if_out_octets_total{%s}[%s]) * 8, "dir", "out")`,
			sel, dur(rateWin), sel, dur(rateWin))
	})
	totals := groupUnion(names, groups, func(sel string) string {
		return fmt.Sprintf(
			`label_set(increase(netinv_if_in_octets_total{%s}[%s]), "dir", "in")`+
				` or label_set(increase(netinv_if_out_octets_total{%s}[%s]), "dir", "out")`,
			sel, dur(win), sel, dur(win))
	})

	queries := []struct {
		expr   string
		assign func(r *Row, dir string, v float64)
	}{
		{fmt.Sprintf("avg_over_time((%s)[%s:%s])", rated, dur(win), dur(step)),
			func(r *Row, dir string, v float64) { setDir(&r.AvgInBPS, &r.AvgOutBPS, dir, v) }},
		{fmt.Sprintf("quantile_over_time(0.95, (%s)[%s:%s])", rated, dur(win), dur(step)),
			func(r *Row, dir string, v float64) { setDir(&r.P95InBPS, &r.P95OutBPS, dir, v) }},
		{fmt.Sprintf("max_over_time((%s)[%s:%s])", rated, dur(win), dur(step)),
			func(r *Row, dir string, v float64) { setDir(&r.MaxInBPS, &r.MaxOutBPS, dir, v) }},
		{totals,
			func(r *Row, dir string, v float64) { setDir(&r.TotalInBytes, &r.TotalOutBytes, dir, v) }},
	}
	for _, qy := range queries {
		samples, err := s.Metrics.QueryAt(ctx, qy.expr, to)
		if err != nil {
			return nil, err
		}
		for _, sm := range samples {
			if row := byName[sm.Labels[custLabel]]; row != nil {
				qy.assign(row, sm.Labels["dir"], sm.Value)
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

// groupUnion builds `sum by (dir)` per customer, labelled with the group name,
// unioned across every group — one expression that answers for all of them.
//
// Per-interface selectors are unioned rather than combined into a regex over
// device_id and if_index: `device_id=~"a|b",if_index=~"1|7"` matches the cross
// product, so one customer's port on another customer's device would be swept
// into the total. That error is invisible in the output and lands on an
// invoice.
func groupUnion(names []string, groups map[string][]postgres.InterfaceSearchRow,
	series func(sel string) string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		sels := make([]string, 0, len(groups[n]))
		for _, i := range groups[n] {
			sels = append(sels, series(fmt.Sprintf(`device_id=%q,if_index=%q`,
				i.DeviceID, strconv.Itoa(i.IfIndex))))
		}
		parts = append(parts, fmt.Sprintf(`label_set(sum by (dir) (%s), %q, %q)`,
			strings.Join(sels, " or "), custLabel, n))
	}
	return strings.Join(parts, " or ")
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
