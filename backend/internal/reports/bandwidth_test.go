package reports

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
)

type stubFinder struct {
	rows  []postgres.InterfaceSearchRow
	total int
	gotQ  string
}

func (f *stubFinder) FindInterfaces(_ context.Context, flt postgres.InterfaceFilter, limit, _ int) ([]postgres.InterfaceSearchRow, int, error) {
	f.gotQ = flt.Q
	rows := f.rows
	if limit < len(rows) {
		rows = rows[:limit]
	}
	total := f.total
	if total == 0 {
		total = len(f.rows)
	}
	return rows, total, nil
}

type stubReader struct {
	exprs   []string
	samples map[string][]Sample // keyed by a substring of the expression
	at      time.Time
}

func (r *stubReader) QueryAt(_ context.Context, expr string, at time.Time) ([]Sample, error) {
	r.exprs = append(r.exprs, expr)
	r.at = at
	for frag, s := range r.samples {
		if strings.Contains(expr, frag) {
			return s, nil
		}
	}
	return nil, nil
}

func sample(device string, ifIndex, dir string, v float64) Sample {
	return Sample{Labels: map[string]string{
		"device_id": device, "if_index": ifIndex, "dir": dir}, Value: v}
}

func svc(rows []postgres.InterfaceSearchRow, samples map[string][]Sample) (*Service, *stubReader) {
	r := &stubReader{samples: samples}
	return &Service{Interfaces: &stubFinder{rows: rows}, Metrics: r}, r
}

func TestBandwidthMergesDirectionsAndComputesUtilization(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{{
		DeviceID: "d_a", DeviceName: "core-a", IfIndex: 1, Name: "ge-0/0/0",
		Alias: "LONDON", SpeedBPS: 1_000_000_000,
	}}
	s, reader := svc(rows, map[string][]Sample{
		"avg_over_time":      {sample("d_a", "1", "in", 100e6), sample("d_a", "1", "out", 200e6)},
		"quantile_over_time": {sample("d_a", "1", "in", 300e6), sample("d_a", "1", "out", 250e6)},
		"max_over_time":      {sample("d_a", "1", "in", 400e6), sample("d_a", "1", "out", 900e6)},
		"increase":           {sample("d_a", "1", "in", 5e9), sample("d_a", "1", "out", 7e9)},
	})
	to := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	rep, err := s.Bandwidth(context.Background(), postgres.InterfaceFilter{Q: "london"}, to.Add(-24*time.Hour), to, 0)
	if err != nil {
		t.Fatalf("bandwidth: %v", err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rep.Rows))
	}
	r := rep.Rows[0]
	if r.AvgInBPS != 100e6 || r.AvgOutBPS != 200e6 {
		t.Errorf("avg in/out = %v/%v", r.AvgInBPS, r.AvgOutBPS)
	}
	if r.P95InBPS != 300e6 || r.MaxOutBPS != 900e6 {
		t.Errorf("p95 in = %v, max out = %v", r.P95InBPS, r.MaxOutBPS)
	}
	if r.TotalInBytes != 5e9 || r.TotalOutBytes != 7e9 {
		t.Errorf("totals = %v/%v", r.TotalInBytes, r.TotalOutBytes)
	}
	// Utilization follows the busier direction: a link is congested when
	// either direction is, and averaging the two would hide a saturated
	// upload behind an idle download.
	if r.AvgUtilPct != 20 || r.P95UtilPct != 30 || r.MaxUtilPct != 90 {
		t.Errorf("util avg/p95/max = %v/%v/%v, want 20/30/90",
			r.AvgUtilPct, r.P95UtilPct, r.MaxUtilPct)
	}
	// Every query is anchored at the end of the window, not at now: a report
	// about last month must not be evaluated against today.
	if !reader.at.Equal(to) {
		t.Errorf("queried at %v, want the window end %v", reader.at, to)
	}
}

// ifSpeed is missing or zero on plenty of real ports — a PPPoE session has
// none. Reporting 0% would read as an idle interface rather than an
// unmeasurable one, and dividing by it would produce +Inf.
func TestBandwidthLeavesUtilizationUnknownWithoutSpeed(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{{
		DeviceID: "d_a", DeviceName: "core-a", IfIndex: 2, Name: "ppp0", SpeedBPS: 0,
	}}
	s, _ := svc(rows, map[string][]Sample{
		"avg_over_time": {sample("d_a", "2", "in", 50e6)},
	})
	to := time.Now().UTC()
	rep, err := s.Bandwidth(context.Background(), postgres.InterfaceFilter{Q: ""}, to.Add(-time.Hour), to, 0)
	if err != nil {
		t.Fatalf("bandwidth: %v", err)
	}
	r := rep.Rows[0]
	if r.AvgInBPS != 50e6 {
		t.Errorf("rate lost: %v", r.AvgInBPS)
	}
	for _, v := range []float64{r.AvgUtilPct, r.P95UtilPct, r.MaxUtilPct} {
		if v >= 0 {
			t.Errorf("utilization reported as %v with no speed — must be unknown", v)
		}
	}
}

// Series arrive for every interface in the store, not just the selected ones,
// because filtering by 500 device/index pairs would mean compiling a vast
// regex on every evaluation. Rows for interfaces nobody asked about must be
// dropped rather than invented.
func TestBandwidthIgnoresSeriesOutsideTheSelection(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{{
		DeviceID: "d_a", DeviceName: "core-a", IfIndex: 1, Name: "ge-0/0/0",
	}}
	s, _ := svc(rows, map[string][]Sample{
		"avg_over_time": {
			sample("d_a", "1", "in", 10),
			sample("d_zzz", "9", "in", 999), // not in the selection
		},
	})
	to := time.Now().UTC()
	rep, err := s.Bandwidth(context.Background(), postgres.InterfaceFilter{Q: ""}, to.Add(-time.Hour), to, 0)
	if err != nil {
		t.Fatalf("bandwidth: %v", err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].AvgInBPS != 10 {
		t.Fatalf("got %+v, want only the selected interface", rep.Rows)
	}
}

func TestBandwidthRejectsABackwardsWindow(t *testing.T) {
	s, _ := svc(nil, nil)
	now := time.Now().UTC()
	if _, err := s.Bandwidth(context.Background(), postgres.InterfaceFilter{Q: ""}, now, now.Add(-time.Hour), 0); err == nil {
		t.Fatal("accepted a window that ends before it starts")
	}
}

// The rate window has to span the poll cadence: a lookback shorter than it
// covers a single sample, rate() returns nothing, and every column comes back
// empty with no error to explain it.
func TestRateWindowTracksThePollCadence(t *testing.T) {
	s := &Service{PollInterval: func() time.Duration { return 15 * time.Minute }}
	if got := s.rateWindow(); got != time.Hour {
		t.Errorf("rate window = %v for a 15m cadence, want 1h", got)
	}
	// A fast cadence still gets a floor: rate() over two samples is noise.
	s = &Service{PollInterval: func() time.Duration { return 10 * time.Second }}
	if got := s.rateWindow(); got != 5*time.Minute {
		t.Errorf("rate window = %v for a 10s cadence, want the 5m floor", got)
	}
	// Unset must not collapse to zero, which would produce `rate(m[0s])`.
	s = &Service{}
	if got := s.rateWindow(); got != 5*time.Minute {
		t.Errorf("rate window = %v with no cadence configured", got)
	}
}

// A month-long report at the rate window would evaluate tens of thousands of
// points per series. The step grows with the window so a long report stays
// answerable, and never drops below the rate window, which would resample the
// same data.
func TestSubStepBoundsThePointCount(t *testing.T) {
	rateWin := 5 * time.Minute
	day := subStep(24*time.Hour, rateWin)
	if day != rateWin {
		t.Errorf("a day stepped at %v, want the rate window %v", day, rateWin)
	}
	month := subStep(30*24*time.Hour, rateWin)
	if points := (30 * 24 * time.Hour) / month; points > 720 {
		t.Errorf("a month evaluates %d points, want at most 720", points)
	}
	if month < rateWin {
		t.Errorf("step %v is shorter than the rate window", month)
	}
}

// MetricsQL rejects Go's duration format: "1h0m0s" is not a valid range.
func TestDurRendersMetricsQLDurations(t *testing.T) {
	if got := dur(90 * time.Minute); got != "5400s" {
		t.Errorf("dur(90m) = %q", got)
	}
	if got := dur(0); got != "1s" {
		t.Errorf("dur(0) = %q — a zero range is a parse error", got)
	}
}

// Grouping is not "add up the per-interface numbers", and this is the test
// that says so. A customer's peak is the peak of their *combined* traffic:
// summing per-interface peaks assumes every circuit peaked at the same
// instant, which overstates. So the sum has to happen inside the query, and
// the query has to arrive labelled per group.
func TestByCustomerSumsInsideTheQuery(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{
		{DeviceID: "d_a", IfIndex: 1, Customer: "Acme Ltd", SpeedBPS: 100_000_000},
		{DeviceID: "d_b", IfIndex: 7, Customer: "Acme Ltd", SpeedBPS: 100_000_000},
		{DeviceID: "d_c", IfIndex: 3, Customer: "Globex", SpeedBPS: 1_000_000_000},
	}
	grouped := func(cust, dir string, v float64) Sample {
		return Sample{Labels: map[string]string{custLabel: cust, "dir": dir}, Value: v}
	}
	s, reader := svc(rows, map[string][]Sample{
		"avg_over_time":      {grouped("Acme Ltd", "in", 30e6), grouped("Globex", "in", 5e6)},
		"quantile_over_time": {grouped("Acme Ltd", "in", 60e6)},
		"max_over_time":      {grouped("Acme Ltd", "in", 150e6)},
		"increase":           {grouped("Acme Ltd", "in", 9e9), grouped("Globex", "in", 1e9)},
	})
	to := time.Now().UTC()
	rep, err := s.ByCustomer(context.Background(), postgres.InterfaceFilter{},
		to.Add(-24*time.Hour), to, 0)
	if err != nil {
		t.Fatalf("by customer: %v", err)
	}
	if rep.GroupedBy != "customer" {
		t.Fatalf("report is not marked as grouped: %q", rep.GroupedBy)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(rep.Rows), rep.Rows)
	}
	acme := rep.Rows[0]
	if acme.Customer != "Acme Ltd" || acme.Interfaces != 2 {
		t.Fatalf("first row is %+v, want Acme with 2 interfaces (busiest first)", acme)
	}
	// Speed is the sum of the group's members, so utilization is against what
	// the customer actually bought.
	if acme.SpeedBPS != 200_000_000 {
		t.Errorf("group speed = %d, want the sum of both circuits", acme.SpeedBPS)
	}
	if acme.MaxUtilPct != 75 {
		t.Errorf("max util = %v, want 75 (150M of 200M)", acme.MaxUtilPct)
	}

	// The aggregation must be in the expression, not in Go: `sum by (dir)`
	// per group, each labelled, and every member's selector present.
	expr := reader.exprs[0]
	if !strings.Contains(expr, "sum by (dir)") {
		t.Errorf("expression does not sum inside the query:\n%s", expr)
	}
	for _, want := range []string{`device_id="d_a"`, `device_id="d_b"`, `if_index=~"7"`} {
		if !strings.Contains(expr, want) {
			t.Errorf("expression is missing %s:\n%s", want, expr)
		}
	}
	// device_id stays an exact match while if_index is a regex. That asymmetry
	// is the correctness property: pinning the device makes the index pattern
	// safe, whereas patterns on both sides match the cross product and would
	// sweep one customer's port on another customer's device into the total —
	// invisible in the output, and it lands on an invoice.
	if strings.Contains(expr, `device_id=~`) {
		t.Errorf("expression uses a regex across device_id, which matches the cross product:\n%s", expr)
	}
}

// A group's speed is only meaningful when every member reports one. A partial
// sum understates the denominator and reports a customer as more congested
// than they are, on the strength of whichever circuits happen to have ifSpeed.
func TestByCustomerLeavesUtilizationUnknownWhenAnyMemberHasNoSpeed(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{
		{DeviceID: "d_a", IfIndex: 1, Customer: "Acme Ltd", SpeedBPS: 100_000_000},
		{DeviceID: "d_a", IfIndex: 2, Customer: "Acme Ltd", SpeedBPS: 0}, // PPPoE
	}
	s, _ := svc(rows, map[string][]Sample{
		"avg_over_time": {{Labels: map[string]string{custLabel: "Acme Ltd", "dir": "in"}, Value: 50e6}},
	})
	to := time.Now().UTC()
	rep, err := s.ByCustomer(context.Background(), postgres.InterfaceFilter{},
		to.Add(-time.Hour), to, 0)
	if err != nil {
		t.Fatalf("by customer: %v", err)
	}
	r := rep.Rows[0]
	if r.SpeedBPS != 0 || r.AvgUtilPct >= 0 {
		t.Fatalf("reported speed %d / util %v with an unmeasurable member",
			r.SpeedBPS, r.AvgUtilPct)
	}
	if r.AvgInBPS != 50e6 {
		t.Errorf("lost the rate: %v", r.AvgInBPS)
	}
}

// Untagged interfaces are grouped, not dropped. A report shaped like an
// invoice that silently omits whatever nobody has tagged yet is how a circuit
// goes unbilled for a year.
func TestByCustomerKeepsUntaggedInterfaces(t *testing.T) {
	rows := []postgres.InterfaceSearchRow{
		{DeviceID: "d_a", IfIndex: 1, Customer: "Acme Ltd"},
		{DeviceID: "d_b", IfIndex: 2, Customer: ""},
	}
	s, _ := svc(rows, nil)
	to := time.Now().UTC()
	rep, err := s.ByCustomer(context.Background(), postgres.InterfaceFilter{},
		to.Add(-time.Hour), to, 0)
	if err != nil {
		t.Fatalf("by customer: %v", err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("got %d groups, want the untagged one kept: %+v", len(rep.Rows), rep.Rows)
	}
	var found bool
	for _, r := range rep.Rows {
		if r.Customer == UntaggedCustomer && r.Interfaces == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("untagged group missing from %+v", rep.Rows)
	}
}

// VictoriaMetrics refuses queries over -search.maxQueryLen (16 KiB by
// default), and a fleet-wide grouped report first hit that at 49 KiB. Groups
// are independent, so batching them across queries changes no number —
// unlike splitting one group, which would break the sum the aggregate depends
// on, so a group is never split.
func TestBatchGroupsStaysUnderTheQueryLimit(t *testing.T) {
	groups := map[string][]postgres.InterfaceSearchRow{}
	var names []string
	// Spread across devices on purpose. Collapsing a customer's ports on one
	// device into a single indexed selector is what keeps these expressions
	// small, so a fixture with everything on one device would never reach the
	// limit — 1200 such interfaces fit in one query.
	for c := range 120 {
		name := "Customer " + strconv.Itoa(c)
		names = append(names, name)
		for d := range 4 {
			for i := range 5 {
				groups[name] = append(groups[name], postgres.InterfaceSearchRow{
					DeviceID: "d_01KZFQPJKD9GWDB3ESXNN9RE" + strconv.Itoa(d),
					IfIndex:  i + 1, Customer: name,
				})
			}
		}
	}
	series := func(sel string) string {
		return "label_set(rate(netinv_if_in_octets_total{" + sel + "}[300s]) * 8, \"dir\", \"in\")"
	}
	batches := batchGroups(names, groups, series)
	if len(batches) < 2 {
		t.Fatalf("1200 interfaces packed into %d batch(es) — the limit is not being applied", len(batches))
	}
	seen := map[string]bool{}
	for _, b := range batches {
		if len(b) > maxQueryLen {
			t.Errorf("batch is %d bytes, over the %d limit", len(b), maxQueryLen)
		}
		for _, n := range names {
			if strings.Contains(b, `"`+n+`"`) {
				if seen[n] {
					t.Errorf("group %q appears in more than one batch — its sum is split", n)
				}
				seen[n] = true
			}
		}
	}
	if len(seen) != len(names) {
		t.Errorf("%d of %d groups made it into a batch", len(seen), len(names))
	}
}
