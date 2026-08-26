package reports

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
)

type fakeRange struct {
	exprs  []string
	steps  []time.Duration
	series []Series
}

func (f *fakeRange) QueryRange(_ context.Context, expr string,
	_, _ time.Time, step time.Duration) ([]Series, error) {
	f.exprs = append(f.exprs, expr)
	f.steps = append(f.steps, step)
	return f.series, nil
}

func window() (time.Time, time.Time) {
	to := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	return to.Add(-7 * 24 * time.Hour), to
}

// A chart is matched to its row by device and ifIndex, and the two directions
// must not collapse into one — the `or` between two metrics that differ only by
// name is the bug this codebase has now hit three times.
func TestInterfaceChartsMapBothDirectionsToTheirRow(t *testing.T) {
	from, to := window()
	rd := &fakeRange{series: []Series{
		{Labels: map[string]string{"device_id": "d1", "if_index": "3", "dir": "in"},
			Points: []Point{{T: from.Unix(), V: 10}, {T: to.Unix(), V: 20}}},
		{Labels: map[string]string{"device_id": "d1", "if_index": "3", "dir": "out"},
			Points: []Point{{T: from.Unix(), V: 1}, {T: to.Unix(), V: 2}}},
		// A series for an interface outside the report is ignored rather than
		// charted onto whichever row happens to be first.
		{Labels: map[string]string{"device_id": "d9", "if_index": "1", "dir": "in"},
			Points: []Point{{T: from.Unix(), V: 999}}},
	}}
	svc := &Service{Charts: rd}
	rep := &Report{From: from, To: to, Rows: []Row{
		{DeviceID: "d1", IfIndex: 3, Alias: "ACME-WAN-01", Customer: "Acme"},
	}}
	charts, err := svc.BuildCharts(context.Background(), rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != 1 {
		t.Fatalf("got %d charts, want 1", len(charts))
	}
	if len(charts[0].In) != 2 || len(charts[0].Out) != 2 {
		t.Fatalf("in=%d out=%d — a direction was lost",
			len(charts[0].In), len(charts[0].Out))
	}
	if charts[0].Title != "ACME-WAN-01" {
		t.Errorf("title = %q, want the alias", charts[0].Title)
	}
	if !strings.Contains(rd.exprs[0], `device_id="d1"`) ||
		!strings.Contains(rd.exprs[0], `if_index=~"3"`) {
		t.Errorf("selector should pin the device and pattern only the index: %s", rd.exprs[0])
	}
}

// A grouped chart is the customer's summed traffic — the same series the row's
// p95 came from. Charting each circuit separately would answer a different
// question from the number printed beside it.
func TestCustomerChartsUseTheSummedSeries(t *testing.T) {
	from, to := window()
	rd := &fakeRange{series: []Series{
		{Labels: map[string]string{custLabel: "Acme", "dir": "in"},
			Points: []Point{{T: from.Unix(), V: 5}}},
	}}
	svc := &Service{Charts: rd}
	rep := &Report{From: from, To: to, GroupedBy: "customer",
		Rows: []Row{{Customer: "Acme", Interfaces: 2}},
		ifaces: []postgres.InterfaceSearchRow{
			{DeviceID: "d1", IfIndex: 3, Customer: "Acme"},
			{DeviceID: "d2", IfIndex: 4, Customer: "Acme"},
		}}
	charts, err := svc.BuildCharts(context.Background(), rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != 1 || len(charts[0].In) != 1 {
		t.Fatalf("the grouped series did not reach its row: %+v", charts)
	}
	if charts[0].Subtitle != "2 circuits" {
		t.Errorf("subtitle = %q", charts[0].Subtitle)
	}
	if !strings.Contains(rd.exprs[0], "sum by (dir)") {
		t.Errorf("a grouped chart must sum before it rolls up: %s", rd.exprs[0])
	}
}

// The row cap is enforced where the cost is, not left to the renderer.
func TestChartsStopAtTheCap(t *testing.T) {
	from, to := window()
	rows := make([]Row, MaxCharts+25)
	for i := range rows {
		rows[i] = Row{DeviceID: "d1", IfIndex: i}
	}
	svc := &Service{Charts: &fakeRange{}}
	charts, err := svc.BuildCharts(context.Background(),
		&Report{From: from, To: to, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != MaxCharts {
		t.Fatalf("got %d charts, want the cap of %d", len(charts), MaxCharts)
	}
}

// Without a range reader an export is a table, which is what every export was
// before charts existed — not an error.
func TestChartsAreSkippedWithoutARangeReader(t *testing.T) {
	from, to := window()
	charts, err := (&Service{}).BuildCharts(context.Background(),
		&Report{From: from, To: to, Rows: []Row{{DeviceID: "d1"}}})
	if err != nil || charts != nil {
		t.Fatalf("charts=%v err=%v, want both empty", charts, err)
	}
}

// Every selector batch has to fit under VictoriaMetrics' query-length limit,
// which a fleet-wide report has already exceeded once in this codebase.
func TestInterfaceBatchesStayUnderTheQueryLimit(t *testing.T) {
	rows := make([]Row, 500)
	for i := range rows {
		// Distinct devices are the expensive shape: each one is its own
		// selector with the full metric names repeated.
		rows[i] = Row{DeviceID: "device-uuid-that-is-a-realistic-length-0000" +
			string(rune('a'+i%26)) + strings.Repeat("x", i%7), IfIndex: i}
	}
	batches := interfaceBatches(rows, ratedAt(5*time.Minute))
	if len(batches) < 2 {
		t.Fatalf("500 devices packed into %d batch(es) — the limit cannot hold", len(batches))
	}
	for i, b := range batches {
		if len(b) > maxQueryLen {
			t.Errorf("batch %d is %d bytes, over the %d limit", i, len(b), maxQueryLen)
		}
	}
}

// A gap in collection is drawn as a gap. Joining across it draws a straight
// line through an outage, which reads as steady traffic over exactly the
// period there was none.
func TestSegmentsSplitWhereCollectionStopped(t *testing.T) {
	pts := []Point{{T: 0}, {T: 60}, {T: 120}, {T: 900}, {T: 960}}
	segs := segments(pts)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if len(segs[0]) != 3 || len(segs[1]) != 2 {
		t.Fatalf("split in the wrong place: %+v", segs)
	}
	if segs := segments(nil); segs != nil {
		t.Errorf("an empty series should yield no segments, got %+v", segs)
	}
}

// The axis top lands on a number a reader can halve.
func TestNiceCeilRoundsToAReadableTop(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0, 1}, {0.4, 0.5}, {7e6, 10e6}, {12e6, 20e6}, {44e6, 50e6}, {620e6, 1e9},
	} {
		if got := niceCeil(c.in); got != c.want {
			t.Errorf("niceCeil(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The plotted point is the average across its bucket, not an instantaneous
// rate sampled every eighteen hours and joined up.
func TestChartStepNeverFallsBelowTheRateWindow(t *testing.T) {
	if got := chartStep(time.Hour, 5*time.Minute); got != 5*time.Minute {
		t.Errorf("short window: got %v, want the rate window", got)
	}
	if got := chartStep(90*24*time.Hour, 5*time.Minute); got <= 5*time.Minute {
		t.Errorf("long window: got %v, want a step sized to the window", got)
	}
}
