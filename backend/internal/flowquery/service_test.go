package flowquery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/reports"
)

type staticBook []Attribution

func (b staticBook) Addresses(context.Context) ([]Attribution, error) { return b, nil }

// fakeVM answers by expression shape and records every expression it saw, so
// tests can assert on the MetricsQL a filter produced.
type fakeVM struct {
	exprs  []string
	totals map[string]float64 // metric → sum
	top    []string           // values topk returns, in order
	detail map[string][]reports.Sample
}

func (f *fakeVM) QueryAt(_ context.Context, expr string, _ time.Time) ([]reports.Sample, error) {
	f.exprs = append(f.exprs, expr)
	switch {
	case strings.HasPrefix(expr, "sum(sum_over_time("):
		for m, v := range f.totals {
			if strings.Contains(expr, m+"{") {
				return []reports.Sample{{Labels: map[string]string{}, Value: v}}, nil
			}
		}
		return nil, nil
	case strings.HasPrefix(expr, "topk("):
		out := make([]reports.Sample, len(f.top))
		for i, v := range f.top {
			out[i] = reports.Sample{Labels: map[string]string{"value": v}}
		}
		return out, nil
	case strings.HasPrefix(expr, "sum by (value, exporter, sampled)"):
		for m, rows := range f.detail {
			if strings.Contains(expr, m+"{") {
				return rows, nil
			}
		}
	}
	return nil, nil
}

func (f *fakeVM) QueryRange(_ context.Context, expr string, _, _ time.Time, _ time.Duration) ([]reports.Series, error) {
	f.exprs = append(f.exprs, expr)
	return nil, nil
}

var book = staticBook{
	{Addr: "192.0.2.1", DeviceID: "d_gw1", DeviceName: "gw1", SiteID: "s_a"},
	{Addr: "192.0.2.2", DeviceID: "d_gw2", DeviceName: "gw2", SiteID: "s_b"},
	// gw1 exports from its WAN side (doc 34 §3.1).
	{Addr: "198.51.100.7", DeviceID: "d_gw1", DeviceName: "gw1", SiteID: "s_a", Claimed: true},
	// A claim on another device's management address must not move it.
	{Addr: "192.0.2.2", DeviceID: "d_gw1", DeviceName: "gw1", SiteID: "s_a", Claimed: true},
}

func testWindow() (time.Time, time.Time) {
	to := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	return to.Add(-time.Hour), to
}

func TestDeviceFilterSelectsItsAddressesOnly(t *testing.T) {
	vm := &fakeVM{}
	svc := &Service{Book: book, Metrics: vm}
	from, to := testWindow()
	res, err := svc.Top(context.Background(), Filter{Dimension: flow.DimTalker, From: from, To: to, DeviceID: "d_gw1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Exporters, ","); got != "192.0.2.1,198.51.100.7" {
		t.Errorf("exporters = %s; gw2's management address must stay with gw2", got)
	}
	// Doubled backslashes: MetricsQL unescapes the string before compiling
	// the regex, and a single \. is rejected as an invalid escape.
	want := `exporter=~"192\\.0\\.2\\.1|198\\.51\\.100\\.7",dimension="talker"`
	if !strings.Contains(vm.exprs[0], want) {
		t.Errorf("selector = %s\nwant it to contain %s", vm.exprs[0], want)
	}
}

func TestUnknownDeviceIsNotFoundRatherThanFleetWide(t *testing.T) {
	svc := &Service{Book: book, Metrics: &fakeVM{}}
	from, to := testWindow()
	_, err := svc.Top(context.Background(), Filter{Dimension: flow.DimTalker, From: from, To: to, DeviceID: "d_nope"}, 10)
	if errx.KindOf(err) != errx.KindNotFound {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestFilterValidation(t *testing.T) {
	svc := &Service{Book: book, Metrics: &fakeVM{}, MaxRange: 24 * time.Hour}
	from, to := testWindow()
	for name, f := range map[string]Filter{
		"if_index across the fleet": {Dimension: flow.DimTalker, From: from, To: to, IfIndex: "3"},
		"backwards window":          {Dimension: flow.DimTalker, From: to, To: from},
		"beyond retention":          {Dimension: flow.DimTalker, From: to.Add(-48 * time.Hour), To: to},
		"unknown dimension":         {Dimension: "colour", From: from, To: to},
	} {
		if _, err := svc.Top(context.Background(), f, 10); errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
}

func TestTopRanksSharesAndAttributes(t *testing.T) {
	vm := &fakeVM{
		totals: map[string]float64{flow.MetricBytes: 1000, flow.MetricPackets: 10},
		top:    []string{"10.0.0.9", "10.0.0.1"},
		detail: map[string][]reports.Sample{
			flow.MetricBytes: {
				{Labels: map[string]string{"value": "10.0.0.9", "exporter": "192.0.2.1"}, Value: 300},
				{Labels: map[string]string{"value": "10.0.0.9", "exporter": "203.0.113.5", "sampled": "true"}, Value: 300},
				{Labels: map[string]string{"value": "10.0.0.1", "exporter": "192.0.2.2"}, Value: 100},
			},
			flow.MetricPackets: {
				{Labels: map[string]string{"value": "10.0.0.9", "exporter": "192.0.2.1"}, Value: 6},
			},
		},
	}
	svc := &Service{Book: book, Metrics: vm}
	from, to := testWindow()
	res, err := svc.Top(context.Background(), Filter{Dimension: flow.DimTalker, From: from, To: to}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Data))
	}
	r := res.Data[0]
	if r.Value != "10.0.0.9" || r.Bytes != 600 || r.Packets != 6 || r.Share != 0.6 || !r.Sampled {
		t.Errorf("top row = %+v", r)
	}
	if want := 600 * 8 / 3600.0; r.BitsPerSecond != want {
		t.Errorf("bits/s = %v, want %v", r.BitsPerSecond, want)
	}
	if len(r.Exporters) != 2 || r.Exporters[0].DeviceID != "d_gw1" || r.Exporters[1].DeviceID != "" {
		t.Errorf("exporters = %+v; an unclaimed source must be listed without a device", r.Exporters)
	}
	if res.Data[1].Exporters[0].DeviceID != "d_gw2" {
		t.Errorf("192.0.2.2 attributed to %q, want its own device d_gw2", res.Data[1].Exporters[0].DeviceID)
	}
}

func TestStep(t *testing.T) {
	from, to := testWindow()
	for _, c := range []struct {
		from  time.Time
		asked time.Duration
		want  time.Duration
	}{
		{from, 0, time.Minute},
		{from, 10 * time.Second, time.Minute},
		{from, 90 * time.Second, 2 * time.Minute},
		// 30 days at maxPoints is 30 min a point.
		{to.Add(-30 * 24 * time.Hour), time.Minute, 30 * time.Minute},
	} {
		if got := Step(c.from, to, c.asked); got != c.want {
			t.Errorf("Step(%s, %s) = %s, want %s", to.Sub(c.from), c.asked, got, c.want)
		}
	}
}

func TestQuoteEscapesForMetricsQL(t *testing.T) {
	if got := quote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("quote = %s", got)
	}
}
