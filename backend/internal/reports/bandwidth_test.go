package reports

import (
	"context"
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
