package reports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
)

// Charts for the PDF export.
//
// The summary table answers "how much"; a chart answers "when". Both belong on
// a page handed to a customer, because a p95 of 40 Mbps means one thing on a
// flat line and another on a circuit that saturates every weekday at 09:00 —
// and the second is the one that turns into an upgrade conversation.
//
// The series are fetched only when an export is going to draw them. A report
// read as JSON does not carry them: the browser already draws its own charts
// from the metrics proxy, and shipping a few hundred thousand points to a page
// that ignores them is a cost paid on every run of the report.

// Point is one sample: a unix timestamp and a value in bits per second.
type Point struct {
	T int64
	V float64
}

// Series is one labelled time series across the window.
type Series struct {
	Labels map[string]string
	Points []Point
}

// RangeReader evaluates an expression across the window rather than at an
// instant. Kept separate from SeriesReader so the existing report path — and
// its fakes — are unchanged by the addition.
type RangeReader interface {
	QueryRange(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]Series, error)
}

// Chart is one row's traffic over the window, in the order rep.Rows are in.
type Chart struct {
	// Title names the subject the way the report names it: the alias for an
	// interface, the customer for a group.
	Title string
	// Subtitle is the secondary identity — for a grouped chart, how many
	// circuits it sums.
	Subtitle string
	In       []Point
	Out      []Point
}

// Empty reports a chart with nothing to draw, which is drawn as a labelled
// blank rather than skipped: a missing chart reads as an oversight, while
// "no data in this period" is an answer.
func (c Chart) Empty() bool { return len(c.In) == 0 && len(c.Out) == 0 }

// MaxCharts bounds a chart-bearing export. Each chart is a page-eighth of
// vector drawing plus its own share of a range query, and a 500-interface
// report would be a document nobody opens. The table still carries every row;
// only the charts stop, and the page says so.
const MaxCharts = 60

// chartPoints is how many samples a chart is drawn from. Beyond roughly this
// many, a 135 mm-wide plot is drawing several points per millimetre.
const chartPoints = 140

// BuildCharts fetches the traffic series behind each row of the report.
//
// It is a no-op without a RangeReader, so a deployment whose metrics reader
// predates this still exports — without charts rather than with an error.
func (s *Service) BuildCharts(ctx context.Context, rep *Report) ([]Chart, error) {
	if s.Charts == nil || len(rep.Rows) == 0 {
		return nil, nil
	}
	n := len(rep.Rows)
	if n > MaxCharts {
		n = MaxCharts
	}
	win := rep.To.Sub(rep.From)
	step := chartStep(win, s.rateWindow())
	if rep.GroupedBy == "customer" {
		return s.customerCharts(ctx, rep, n, step)
	}
	return s.interfaceCharts(ctx, rep, n, step)
}

// chartStep sizes the bucket each plotted point averages over. rate() over the
// step rather than over the poll cadence: sampling an instantaneous rate every
// 18 hours across a 90-day report would draw 140 unrelated moments and call it
// a trend, while rate(counter[18h]) is the average across each bucket, which
// is what the eye reads a chart as showing.
func chartStep(win, rateWin time.Duration) time.Duration {
	step := win / chartPoints
	if step < rateWin {
		step = rateWin
	}
	return step.Round(time.Second)
}

// interfaceCharts draws one chart per interface. Selectors pin device_id
// exactly and pattern only if_index, for the same reason the rest of the
// report does: patterns on both sides match the cross product, and a chart
// carrying another customer's port is worse than no chart.
func (s *Service) interfaceCharts(ctx context.Context, rep *Report, n int, step time.Duration) ([]Chart, error) {
	charts := make([]Chart, n)
	at := map[string]int{}
	rows := rep.Rows[:n]
	for i, r := range rows {
		charts[i] = Chart{Title: chartTitle(r), Subtitle: chartSubtitle(r)}
		at[key(r.DeviceID, r.IfIndex)] = i
	}
	for _, expr := range interfaceBatches(rows, ratedAt(step)) {
		series, err := s.Charts.QueryRange(ctx, expr, rep.From, rep.To, step)
		if err != nil {
			return nil, err
		}
		for _, se := range series {
			i, ok := at[key(se.Labels["device_id"], atoi(se.Labels["if_index"]))]
			if !ok {
				continue
			}
			setPoints(&charts[i], se)
		}
	}
	return charts, nil
}

// customerCharts draws the customer's *combined* traffic — the same summed
// series the row's p95 and peak were computed from. Overlaying each circuit
// separately would be a different chart answering a different question, and it
// would not add up to the number printed beside it.
func (s *Service) customerCharts(ctx context.Context, rep *Report, n int, step time.Duration) ([]Chart, error) {
	groups := map[string][]postgres.InterfaceSearchRow{}
	for _, i := range rep.ifaces {
		groups[i.Customer] = append(groups[i.Customer], i)
	}
	charts := make([]Chart, n)
	at := map[string]int{}
	names := make([]string, 0, n)
	for i, r := range rep.Rows[:n] {
		charts[i] = Chart{Title: chartTitle(r), Subtitle: chartSubtitle(r)}
		at[r.Customer] = i
		names = append(names, r.Customer)
	}
	sort.Strings(names)
	for _, expr := range batchGroups(names, groups, ratedAt(step)) {
		series, err := s.Charts.QueryRange(ctx, expr, rep.From, rep.To, step)
		if err != nil {
			return nil, err
		}
		for _, se := range series {
			if i, ok := at[se.Labels[custLabel]]; ok {
				setPoints(&charts[i], se)
			}
		}
	}
	return charts, nil
}

func setPoints(c *Chart, se Series) {
	if se.Labels["dir"] == "out" {
		c.Out = se.Points
		return
	}
	c.In = se.Points
}

func chartTitle(r Row) string {
	if r.Interfaces > 0 {
		if r.Customer == "" {
			return "(untagged)"
		}
		return r.Customer
	}
	// The alias is the identity the report prints, so it is the identity the
	// chart carries. A port nobody has labelled falls back to what the device
	// calls it, because an unnamed chart cannot be matched to its row.
	if r.Alias != "" {
		return r.Alias
	}
	if r.Name != "" {
		return r.Name
	}
	return "ifIndex " + strconv.Itoa(r.IfIndex)
}

func chartSubtitle(r Row) string {
	if r.Interfaces > 0 {
		if r.Interfaces == 1 {
			return "1 circuit"
		}
		return strconv.Itoa(r.Interfaces) + " circuits"
	}
	if r.Customer != "" {
		return r.Customer
	}
	return ""
}

// ratedAt is bits per second averaged across each step bucket, per direction.
// The label_set/or shape is load-bearing here as everywhere else: MetricsQL's
// `or` matches on labels excluding __name__, so in and out would collapse into
// one series and the out direction would vanish without an error.
func ratedAt(step time.Duration) func(sel string) string {
	return func(sel string) string {
		return fmt.Sprintf(
			`label_set(rate(netinv_if_in_octets_total{%s}[%s]) * 8, "dir", "in")`+
				` or label_set(rate(netinv_if_out_octets_total{%s}[%s]) * 8, "dir", "out")`,
			sel, dur(step), sel, dur(step))
	}
}

// interfaceBatches packs per-device selectors into as few expressions as fit
// under the query-length limit. Unlike a customer's sum, an interface series
// is independent of every other, so splitting anywhere changes no number.
func interfaceBatches(rows []Row, series func(sel string) string) []string {
	byDevice := map[string][]string{}
	order := []string{}
	for _, r := range rows {
		if _, seen := byDevice[r.DeviceID]; !seen {
			order = append(order, r.DeviceID)
		}
		byDevice[r.DeviceID] = append(byDevice[r.DeviceID], strconv.Itoa(r.IfIndex))
	}
	var out []string
	var cur []string
	size := 0
	for _, d := range order {
		idx := byDevice[d]
		sort.Strings(idx)
		e := series(fmt.Sprintf(`device_id=%q,if_index=~%q`, d, strings.Join(idx, "|")))
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
