// Package reports answers questions about a period rather than a moment.
//
// The graphs answer "what is it doing"; a report answers "what did it do" —
// which interface carried how much last month, which ones ran hot, what to
// bill or to upgrade. That question is asked of a *set* of interfaces chosen
// by what an operator wrote on them (FR-DEV-04), not of one device at a time,
// which is why selection here is the same alias/description search the
// Interfaces page uses.
package reports

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Row is one interface's traffic over the report window. Bit rates are per
// second; totals are bytes, matching how counters are collected and how an
// operator states a transfer allowance.
type Row struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	IfIndex    int    `json:"if_index"`
	Name       string `json:"name"`
	Alias      string `json:"alias"`
	Descr      string `json:"descr"`
	SpeedBPS   int64  `json:"speed_bps"`

	AvgInBPS  float64 `json:"avg_in_bps"`
	AvgOutBPS float64 `json:"avg_out_bps"`
	P95InBPS  float64 `json:"p95_in_bps"`
	P95OutBPS float64 `json:"p95_out_bps"`
	MaxInBPS  float64 `json:"max_in_bps"`
	MaxOutBPS float64 `json:"max_out_bps"`

	TotalInBytes  float64 `json:"total_in_bytes"`
	TotalOutBytes float64 `json:"total_out_bytes"`

	// Utilization of the busier direction, as a percentage of speed. Negative
	// means unknown: ifSpeed is absent or zero on plenty of real ports — a
	// PPPoE session has none — and 0% would read as "idle" rather than
	// "unmeasurable".
	AvgUtilPct float64 `json:"avg_util_pct"`
	P95UtilPct float64 `json:"p95_util_pct"`
	MaxUtilPct float64 `json:"max_util_pct"`
}

// Report is the whole answer, including what was asked. The window is echoed
// back because a bandwidth figure without its period is meaningless, and a CSV
// that has been emailed twice needs to say which is which.
type Report struct {
	Query     string    `json:"query"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Truncated bool      `json:"truncated"`
	Rows      []Row     `json:"rows"`
}

// InterfaceFinder is the inventory side: which interfaces the operator means.
type InterfaceFinder interface {
	SearchInterfaces(ctx context.Context, q string, limit, offset int) ([]postgres.InterfaceSearchRow, int, error)
}

// SeriesReader runs one MetricsQL expression at an instant and returns the
// labelled results.
type SeriesReader interface {
	QueryAt(ctx context.Context, expr string, at time.Time) ([]Sample, error)
}

type Sample struct {
	Labels map[string]string
	Value  float64
}

type Service struct {
	Interfaces InterfaceFinder
	Metrics    SeriesReader
	// PollInterval sizes the rate window. A lookback shorter than the poll
	// cadence spans a single sample and returns nothing, which would empty
	// every column without erroring.
	//
	// A function rather than a value, and read per report: the cadence is
	// changeable from the UI, so one captured at boot goes stale and takes the
	// report's columns with it. nil falls back to 60s.
	PollInterval func() time.Duration
}

// MaxRows caps a report. The cost of a report is dominated by how many series
// the metrics store has to roll up, and an operator who searches for "" wants
// a sanity check, not a fleet-wide export that times out.
const MaxRows = 500

// Bandwidth builds the report for interfaces matching q over [from, to].
func (s *Service) Bandwidth(ctx context.Context, q string, from, to time.Time, limit int) (*Report, error) {
	if !to.After(from) {
		return nil, errx.New(errx.KindInvalid, "the report window ends before it starts")
	}
	if limit <= 0 || limit > MaxRows {
		limit = MaxRows
	}
	ifaces, total, err := s.Interfaces.SearchInterfaces(ctx, q, limit, 0)
	if err != nil {
		return nil, err
	}
	rep := &Report{Query: q, From: from, To: to, Truncated: total > len(ifaces), Rows: []Row{}}
	if len(ifaces) == 0 {
		return rep, nil
	}

	byKey := make(map[string]*Row, len(ifaces))
	for _, i := range ifaces {
		r := Row{
			DeviceID: i.DeviceID, DeviceName: i.DeviceName, IfIndex: i.IfIndex,
			Name: i.Name, Alias: i.Alias, Descr: i.Descr, SpeedBPS: i.SpeedBPS,
			AvgUtilPct: -1, P95UtilPct: -1, MaxUtilPct: -1,
		}
		rep.Rows = append(rep.Rows, r)
	}
	for i := range rep.Rows {
		byKey[key(rep.Rows[i].DeviceID, rep.Rows[i].IfIndex)] = &rep.Rows[i]
	}

	win := to.Sub(from)
	rateWin := s.rateWindow()
	step := subStep(win, rateWin)

	// Four queries rather than one per interface: the store is far better at
	// rolling up a selector than the caller is at fanning out, and a report
	// over 500 interfaces would otherwise be 2000 round trips.
	rated := ratedSeries(rateWin)
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
		{totalsExpr(win),
			func(r *Row, dir string, v float64) { setDir(&r.TotalInBytes, &r.TotalOutBytes, dir, v) }},
	}
	for _, qy := range queries {
		samples, err := s.Metrics.QueryAt(ctx, qy.expr, to)
		if err != nil {
			return nil, err
		}
		for _, sm := range samples {
			row := byKey[key(sm.Labels["device_id"], atoi(sm.Labels["if_index"]))]
			// Series for interfaces outside the selection are ignored rather
			// than filtered in the query: a selector listing 500 device/index
			// pairs is a regex the store has to compile on every evaluation.
			if row == nil || math.IsNaN(sm.Value) {
				continue
			}
			qy.assign(row, sm.Labels["dir"], sm.Value)
		}
	}

	for i := range rep.Rows {
		r := &rep.Rows[i]
		if r.SpeedBPS > 0 {
			sp := float64(r.SpeedBPS)
			r.AvgUtilPct = 100 * math.Max(r.AvgInBPS, r.AvgOutBPS) / sp
			r.P95UtilPct = 100 * math.Max(r.P95InBPS, r.P95OutBPS) / sp
			r.MaxUtilPct = 100 * math.Max(r.MaxInBPS, r.MaxOutBPS) / sp
		}
	}
	// Busiest first: a report is read from the top, and the interface that
	// carried the most is the one being asked about.
	sort.SliceStable(rep.Rows, func(a, b int) bool {
		ra, rb := rep.Rows[a], rep.Rows[b]
		return ra.TotalInBytes+ra.TotalOutBytes > rb.TotalInBytes+rb.TotalOutBytes
	})
	return rep, nil
}

// ratedSeries is bits per second per direction, with the direction kept as a
// label. The label_set/or shape is load-bearing: MetricsQL's `or` matches on
// labels excluding __name__, so in and out — identical in every other label —
// would collapse into one series and the out direction would vanish silently.
func ratedSeries(rateWin time.Duration) string {
	return fmt.Sprintf(
		`label_set(rate(netinv_if_in_octets_total[%s]) * 8, "dir", "in")`+
			` or label_set(rate(netinv_if_out_octets_total[%s]) * 8, "dir", "out")`,
		dur(rateWin), dur(rateWin))
}

// totalsExpr is bytes transferred across the whole window. increase() over the
// window rather than a sum of rates: it handles counter resets, which a device
// reboot mid-report guarantees.
func totalsExpr(win time.Duration) string {
	return fmt.Sprintf(
		`label_set(increase(netinv_if_in_octets_total[%s]), "dir", "in")`+
			` or label_set(increase(netinv_if_out_octets_total[%s]), "dir", "out")`,
		dur(win), dur(win))
}

func (s *Service) rateWindow() time.Duration {
	var poll time.Duration
	if s.PollInterval != nil {
		poll = s.PollInterval()
	}
	if poll <= 0 {
		poll = 60 * time.Second
	}
	// Four cadences: enough samples for rate() to survive one missed poll,
	// which is common enough that a report full of gaps would otherwise be the
	// normal result.
	w := 4 * poll
	if w < 5*time.Minute {
		w = 5 * time.Minute
	}
	return w
}

// subStep bounds how many points a subquery evaluates. A month at the rate
// window would be tens of thousands of evaluations per series; capping the
// count keeps a long report answerable, at the cost of resolution that a
// month-long average does not have anyway.
func subStep(win, rateWin time.Duration) time.Duration {
	const maxPoints = 720
	step := win / maxPoints
	if step < rateWin {
		step = rateWin
	}
	return step.Round(time.Second)
}

func setDir(in, out *float64, dir string, v float64) {
	if dir == "out" {
		*out = v
		return
	}
	*in = v
}

func key(deviceID string, ifIndex int) string {
	return deviceID + "|" + strconv.Itoa(ifIndex)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// dur renders a duration the way MetricsQL wants it. time.Duration.String()
// produces "1h0m0s", which the parser rejects.
func dur(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
}
