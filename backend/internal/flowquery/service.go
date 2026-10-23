package flowquery

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/reports"
)

// Reader is the metrics store, at an instant and across a window.
type Reader interface {
	QueryAt(ctx context.Context, expr string, at time.Time) ([]reports.Sample, error)
	QueryRange(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]reports.Series, error)
}

type Service struct {
	Book    AddressBook
	Metrics Reader
	// MaxRange is the retention ceiling; longer windows are rejected rather
	// than clamped, as on the metrics proxy (doc 09 §7).
	MaxRange time.Duration
	Now      func() time.Time
}

const (
	// interval is the collector's aggregation interval. A window or step
	// shorter than it can fall between samples and return nothing.
	interval = flow.Interval

	DefaultLimit  = 25
	MaxLimit      = 100
	DefaultSeries = 10
	MaxSeries     = 50
	// maxPoints keeps a range query under VictoriaMetrics' per-series point
	// cap with room to spare; the step widens to fit rather than erroring.
	maxPoints = 1440
)

// Filter selects which aggregates a query covers. Device and Site narrow by
// attribution; Exporter names raw source addresses, which is the only way to
// ask about flow nobody has claimed yet.
type Filter struct {
	Dimension flow.Dimension
	From, To  time.Time
	DeviceID  string
	SiteID    string
	Exporters []string
	IfIndex   string
}

// Exporter is one source address in a result, with the device it resolves to.
// DeviceID is empty for an address no device has claimed.
type Exporter struct {
	Exporter   string `json:"exporter"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type TopRow struct {
	Value   string  `json:"value"`
	Bytes   float64 `json:"bytes"`
	Packets float64 `json:"packets"`
	// BitsPerSecond is the average over the whole window, not a peak.
	BitsPerSecond float64    `json:"bits_per_second"`
	Share         float64    `json:"share"`
	Sampled       bool       `json:"sampled"`
	Exporters     []Exporter `json:"exporters"`
}

type TopResult struct {
	Dimension flow.Dimension `json:"dimension"`
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	Exporters []string       `json:"exporters,omitempty"`
	// TotalBytes is everything kept in this dimension across the window, the
	// denominator of Share. It is the sum of what survived the collector's
	// per-interval top-N cut, not the traffic on the link (doc 34 §1).
	TotalBytes   float64  `json:"total_bytes"`
	TotalPackets float64  `json:"total_packets"`
	Data         []TopRow `json:"data"`
}

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type SeriesLine struct {
	Value  string  `json:"value"`
	Points []Point `json:"points"`
}

type SeriesResult struct {
	Dimension flow.Dimension `json:"dimension"`
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	StepS     int64          `json:"step_s"`
	Unit      string         `json:"unit"`
	Exporters []string       `json:"exporters,omitempty"`
	// Total is every bucket in the dimension, so a client can draw "other" as
	// Total minus the lines it was given.
	Total []Point      `json:"total"`
	Data  []SeriesLine `json:"data"`
}

type ExporterRow struct {
	Exporter
	SiteID  string  `json:"site_id,omitempty"`
	Claimed bool    `json:"claimed"`
	Bytes   float64 `json:"bytes"`
	Sampled bool    `json:"sampled"`
}

type ExportersResult struct {
	From time.Time     `json:"from"`
	To   time.Time     `json:"to"`
	Data []ExporterRow `json:"data"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// errNoAddresses is returned when a device or site filter resolves to no
// address at all, so the answer is a 404 rather than a silently fleet-wide one.
var errNoAddresses = errx.New(errx.KindNotFound, "no device matches the filter")

// resolve turns the filter into a series selector and the address index used
// to attribute results.
func (s *Service) checkWindow(from, to time.Time) error {
	if !to.After(from) {
		return errx.New(errx.KindInvalid, "'from' must be before 'to'")
	}
	if s.MaxRange > 0 && to.Sub(from) > s.MaxRange {
		return errx.New(errx.KindInvalid, "window exceeds retention (%s)", s.MaxRange)
	}
	return nil
}

func (s *Service) resolve(ctx context.Context, f Filter) (string, []string, index, error) {
	if err := s.checkWindow(f.From, f.To); err != nil {
		return "", nil, nil, err
	}
	if f.IfIndex != "" && f.DeviceID == "" && len(f.Exporters) == 0 {
		// ifIndex is only unique within one agent; across the fleet it names
		// a different port on every device.
		return "", nil, nil, errx.New(errx.KindInvalid, "if_index needs a device or exporter")
	}
	list, err := s.Book.Addresses(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	idx := newIndex(list)

	var addrs []string
	scoped := f.DeviceID != "" || f.SiteID != ""
	if scoped {
		seen := map[string]bool{}
		for _, a := range list {
			if f.DeviceID != "" && a.DeviceID != f.DeviceID {
				continue
			}
			if f.SiteID != "" && a.SiteID != f.SiteID {
				continue
			}
			k := normAddr(a.Addr)
			// An address claimed by two devices belongs to whichever the
			// index chose, and must not count for both.
			if idx[k].DeviceID != a.DeviceID || seen[k] {
				continue
			}
			seen[k] = true
			addrs = append(addrs, k)
		}
		if len(addrs) == 0 {
			return "", nil, nil, errNoAddresses
		}
	}
	if len(f.Exporters) > 0 {
		want := map[string]bool{}
		for _, e := range f.Exporters {
			want[normAddr(e)] = true
		}
		if scoped {
			// Both given: the intersection, so exporter narrows a device
			// rather than escaping it.
			kept := addrs[:0]
			for _, a := range addrs {
				if want[a] {
					kept = append(kept, a)
				}
			}
			addrs = kept
			if len(addrs) == 0 {
				return "", nil, nil, errNoAddresses
			}
		} else {
			for e := range want {
				addrs = append(addrs, e)
			}
		}
	}
	sort.Strings(addrs)

	var parts []string
	switch len(addrs) {
	case 0:
	case 1:
		parts = append(parts, `exporter=`+quote(addrs[0]))
	default:
		re := make([]string, len(addrs))
		for i, a := range addrs {
			re[i] = regexp.QuoteMeta(a)
		}
		parts = append(parts, `exporter=~`+quote(strings.Join(re, "|")))
	}
	if f.Dimension != "" {
		parts = append(parts, `dimension=`+quote(string(f.Dimension)))
	}
	if f.IfIndex != "" {
		parts = append(parts, `if_index=`+quote(f.IfIndex))
	}
	return "{" + strings.Join(parts, ",") + "}", addrs, idx, nil
}

// quote renders a MetricsQL string literal. The store unescapes the literal
// before compiling a regex from it, so a regex backslash must arrive doubled —
// a single one is consumed as a string escape and the query is rejected
// (the trap reQuote exists for on the frontend, doc 34 §3.1).
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func withValues(sel string, values []string) string {
	re := make([]string, len(values))
	for i, v := range values {
		re[i] = regexp.QuoteMeta(v)
	}
	return strings.TrimSuffix(sel, "}") + `,value=~` + quote(strings.Join(re, "|")) + "}"
}

func window(f Filter) string {
	w := f.To.Sub(f.From)
	if w < interval {
		w = interval
	}
	return fmt.Sprintf("[%ds]", int64(w.Seconds()))
}

func validDimension(d flow.Dimension) error {
	for _, k := range flow.Dimensions {
		if d == k {
			return nil
		}
	}
	names := make([]string, len(flow.Dimensions))
	for i, k := range flow.Dimensions {
		names[i] = string(k)
	}
	return errx.New(errx.KindInvalid, "dimension must be one of %s", strings.Join(names, ", "))
}

// Top ranks the dimension's values by bytes across the whole window.
//
// Ranking is on the window total, not the latest interval: ranking on one
// minute reorders the table on ordinary jitter. A value's total is a lower
// bound — any interval in which it missed the collector's top-N cut was never
// written — which is the price of ADR-020 and is stated in doc 34 §1.
func (s *Service) Top(ctx context.Context, f Filter, limit int) (*TopResult, error) {
	res, _, err := s.top(ctx, f, limit)
	return res, err
}

// top also returns the selector it resolved, so Series can reuse it rather
// than resolving the filter a second time.
func (s *Service) top(ctx context.Context, f Filter, limit int) (*TopResult, string, error) {
	if err := validDimension(f.Dimension); err != nil {
		return nil, "", err
	}
	sel, addrs, idx, err := s.resolve(ctx, f)
	if err != nil {
		return nil, "", err
	}
	w := window(f)
	res := &TopResult{Dimension: f.Dimension, From: f.From, To: f.To, Exporters: addrs, Data: []TopRow{}}

	total := func(metric string) (float64, error) {
		out, err := s.Metrics.QueryAt(ctx, fmt.Sprintf(`sum(sum_over_time(%s%s%s))`, metric, sel, w), f.To)
		if err != nil || len(out) == 0 {
			return 0, err
		}
		return out[0].Value, nil
	}
	if res.TotalBytes, err = total(flow.MetricBytes); err != nil {
		return nil, "", err
	}
	if res.TotalPackets, err = total(flow.MetricPackets); err != nil {
		return nil, "", err
	}
	if res.TotalBytes == 0 {
		return res, sel, nil
	}

	top, err := s.Metrics.QueryAt(ctx, fmt.Sprintf(
		`topk(%d, sum by (value) (sum_over_time(%s%s%s)))`, limit, flow.MetricBytes, sel, w), f.To)
	if err != nil {
		return nil, "", err
	}
	if len(top) == 0 {
		return res, sel, nil
	}
	values := make([]string, 0, len(top))
	for _, t := range top {
		values = append(values, t.Labels["value"])
	}

	// One detail query per metric, split by exporter and the sampled label,
	// for the chosen values only: asking for every value's breakdown would
	// return every talker the window ever saw.
	detail := func(metric string) ([]reports.Sample, error) {
		return s.Metrics.QueryAt(ctx, fmt.Sprintf(
			`sum by (value, exporter, sampled) (sum_over_time(%s%s%s))`,
			metric, withValues(sel, values), w), f.To)
	}
	bytesBy, err := detail(flow.MetricBytes)
	if err != nil {
		return nil, "", err
	}
	pktsBy, err := detail(flow.MetricPackets)
	if err != nil {
		return nil, "", err
	}

	rows := map[string]*TopRow{}
	exp := map[string]map[string]bool{}
	for _, v := range values {
		rows[v] = &TopRow{Value: v, Exporters: []Exporter{}}
		exp[v] = map[string]bool{}
	}
	for _, smp := range bytesBy {
		r := rows[smp.Labels["value"]]
		if r == nil {
			continue
		}
		r.Bytes += smp.Value
		r.Sampled = r.Sampled || smp.Labels["sampled"] == "true"
		if e := smp.Labels["exporter"]; !exp[r.Value][e] {
			exp[r.Value][e] = true
			r.Exporters = append(r.Exporters, attribute(idx, e))
		}
	}
	for _, smp := range pktsBy {
		if r := rows[smp.Labels["value"]]; r != nil {
			r.Packets += smp.Value
		}
	}
	secs := f.To.Sub(f.From).Seconds()
	for _, v := range values {
		r := rows[v]
		sort.Slice(r.Exporters, func(i, j int) bool { return r.Exporters[i].Exporter < r.Exporters[j].Exporter })
		r.BitsPerSecond = r.Bytes * 8 / secs
		r.Share = r.Bytes / res.TotalBytes
		res.Data = append(res.Data, *r)
	}
	sort.SliceStable(res.Data, func(i, j int) bool {
		if res.Data[i].Bytes != res.Data[j].Bytes {
			return res.Data[i].Bytes > res.Data[j].Bytes
		}
		return res.Data[i].Value < res.Data[j].Value
	})
	return res, sel, nil
}

func attribute(idx index, addr string) Exporter {
	e := Exporter{Exporter: addr}
	if a, ok := idx[normAddr(addr)]; ok {
		e.DeviceID, e.DeviceName = a.DeviceID, a.DeviceName
	}
	return e
}

// Step picks a range-query step: at least the aggregation interval, a whole
// number of intervals, and wide enough to keep the point count bounded.
func Step(from, to time.Time, asked time.Duration) time.Duration {
	step := asked
	if floor := to.Sub(from) / maxPoints; step < floor {
		step = floor
	}
	// Round up to whole intervals: a step of 90 s would put one sample in
	// some buckets and two in others, and the chart would saw-tooth.
	n := (step + interval - 1) / interval
	if n < 1 {
		n = 1
	}
	return n * interval
}

// Series charts the top values over the window in bits per second.
//
// Each point sums the per-interval samples inside one step and divides by the
// step — the same arithmetic as flowRateExpr on the frontend, and the reason
// this endpoint exists rather than leaving clients to write rate(), which
// under-reports a series that is not cumulative.
func (s *Service) Series(ctx context.Context, f Filter, limit int, step time.Duration) (*SeriesResult, error) {
	top, sel, err := s.top(ctx, f, limit)
	if err != nil {
		return nil, err
	}
	step = Step(f.From, f.To, step)
	res := &SeriesResult{
		Dimension: f.Dimension, From: f.From, To: f.To, StepS: int64(step.Seconds()),
		Unit: "bits_per_second", Exporters: top.Exporters, Total: []Point{}, Data: []SeriesLine{},
	}
	if len(top.Data) == 0 {
		return res, nil
	}
	values := make([]string, len(top.Data))
	for i, r := range top.Data {
		values[i] = r.Value
	}
	st := int64(step.Seconds())
	rate := func(by, sel string) string {
		return fmt.Sprintf(`sum %s (sum_over_time(%s%s[%ds])) * 8 / %d`, by, flow.MetricBytes, sel, st, st)
	}
	lines, err := s.Metrics.QueryRange(ctx, rate("by (value)", withValues(sel, values)), f.From, f.To, step)
	if err != nil {
		return nil, err
	}
	total, err := s.Metrics.QueryRange(ctx, rate("", sel), f.From, f.To, step)
	if err != nil {
		return nil, err
	}
	byValue := map[string][]Point{}
	for _, l := range lines {
		byValue[l.Labels["value"]] = points(l.Points)
	}
	// Ordered as Top ranked them, so a legend reads largest first.
	for _, v := range values {
		pts := byValue[v]
		if pts == nil {
			pts = []Point{}
		}
		res.Data = append(res.Data, SeriesLine{Value: v, Points: pts})
	}
	if len(total) > 0 {
		res.Total = points(total[0].Points)
	}
	return res, nil
}

func points(in []reports.Point) []Point {
	out := make([]Point, len(in))
	for i, p := range in {
		out[i] = Point{T: p.T, V: p.V}
	}
	return out
}

// Exporters lists every address that sent flow in the window and who it
// belongs to. An unattributed row is the signal that a device exports from an
// address nobody has claimed (doc 34 §3.1) — its flow is stored but answers to
// no device query.
func (s *Service) Exporters(ctx context.Context, from, to time.Time) (*ExportersResult, error) {
	if err := s.checkWindow(from, to); err != nil {
		return nil, err
	}
	f := Filter{From: from, To: to}
	list, err := s.Book.Addresses(ctx)
	if err != nil {
		return nil, err
	}
	idx := newIndex(list)
	// Bytes in one dimension only: every record is counted in every
	// dimension, and talker counts it twice, so the application dimension is
	// the one whose sum is the exported volume.
	samples, err := s.Metrics.QueryAt(ctx, fmt.Sprintf(
		`sum by (exporter, sampled) (sum_over_time(%s{dimension="application"}%s))`,
		flow.MetricBytes, window(f)), to)
	if err != nil {
		return nil, err
	}
	rows := map[string]*ExporterRow{}
	for _, smp := range samples {
		e := normAddr(smp.Labels["exporter"])
		r := rows[e]
		if r == nil {
			r = &ExporterRow{Exporter: attribute(idx, e)}
			if a, ok := idx[e]; ok {
				r.SiteID, r.Claimed = a.SiteID, a.Claimed
			}
			rows[e] = r
		}
		r.Bytes += smp.Value
		r.Sampled = r.Sampled || smp.Labels["sampled"] == "true"
	}
	res := &ExportersResult{From: from, To: to, Data: make([]ExporterRow, 0, len(rows))}
	for _, r := range rows {
		res.Data = append(res.Data, *r)
	}
	sort.Slice(res.Data, func(i, j int) bool { return res.Data[i].Bytes > res.Data[j].Bytes })
	return res, nil
}
