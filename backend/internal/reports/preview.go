package reports

import (
	"math"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/settings"
)

// A sample report, for the settings page's preview.
//
// Synthetic rather than live, for two reasons. A preview has to render for a
// deployment that has collected nothing yet — the sitting where someone
// configures branding is usually the first day — and a preview built from real
// traffic is a real report that happens to be unsaved, which is exactly the
// document that ends up attached to an email.
//
// The numbers are therefore obviously invented and the page says so.

// SamplePDF builds the report and charts behind the branding preview.
func SamplePDF(brand settings.Branding) (*Report, []Chart) {
	// A fixed window rather than one anchored to now: the preview is a picture
	// of the layout, and a stable one is easier to compare against the last
	// change to the form.
	to := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)

	samples := []struct {
		alias    string
		customer string
		device   string
		name     string
		speed    int64
		peak     float64
		shape    float64
	}{
		{"ACME-WAN-01 / primary", "Acme Logistics", "edge-gw-01", "ge-0/0/1", 1_000_000_000, 612e6, 0.62},
		{"ACME-WAN-02 / standby", "Acme Logistics", "edge-gw-02", "ge-0/0/1", 1_000_000_000, 84e6, 0.15},
		{"Globex FTTB 500M", "Globex", "agg-sw-04", "Gi1/0/12", 500_000_000, 388e6, 0.71},
		{"Initech metro E-Line", "Initech", "agg-sw-04", "Gi1/0/18", 200_000_000, 141e6, 0.55},
		{"Umbrella DIA 100M", "Umbrella", "edge-gw-01", "ge-0/0/7", 100_000_000, 31e6, 0.29},
		{"Spare — unlabelled port", "", "agg-sw-04", "Gi1/0/44", 1_000_000_000, 0, 0},
	}

	rep := &Report{
		Sample: true, From: from, To: to,
		Rows: make([]Row, 0, len(samples)),
	}
	charts := make([]Chart, 0, len(samples))
	for i, s := range samples {
		in, out := samplePoints(from, to, s.peak, s.shape, i)
		row := Row{
			DeviceID: "sample", DeviceName: s.device, Name: s.name,
			Alias: s.alias, Customer: s.customer, IfIndex: 100 + i,
			Descr:    s.name,
			SpeedBPS: s.speed,
			AvgInBPS: s.peak * 0.41, AvgOutBPS: s.peak * 0.18,
			P95InBPS: s.peak * 0.82, P95OutBPS: s.peak * 0.34,
			MaxInBPS: s.peak, MaxOutBPS: s.peak * 0.44,
			TotalInBytes:  s.peak * 0.41 / 8 * 7 * 86400,
			TotalOutBytes: s.peak * 0.18 / 8 * 7 * 86400,
			AvgUtilPct:    -1, P95UtilPct: -1, MaxUtilPct: -1,
		}
		if row.SpeedBPS > 0 {
			sp := float64(row.SpeedBPS)
			row.AvgUtilPct = 100 * row.AvgInBPS / sp
			row.P95UtilPct = 100 * row.P95InBPS / sp
			row.MaxUtilPct = 100 * row.MaxInBPS / sp
		}
		rep.Rows = append(rep.Rows, row)
		charts = append(charts, Chart{
			Title:    chartTitle(row),
			Subtitle: chartSubtitle(row),
			In:       in, Out: out,
		})
	}
	if !brand.ChartsEnabled() {
		charts = nil
	}
	return rep, charts
}

// samplePoints draws a week with a working-day rhythm: a daily peak, a lighter
// weekend, and one gap where collection stopped — because a report of a real
// week usually has one, and a preview that never shows one hides how it looks.
func samplePoints(from, to time.Time, peak, shape float64, seed int) (in, out []Point) {
	if peak <= 0 {
		return nil, nil
	}
	const n = 140
	step := to.Sub(from) / n
	for i := 0; i < n; i++ {
		t := from.Add(time.Duration(i) * step)
		if i > 62 && i < 70 {
			continue // the gap
		}
		hour := float64(t.Hour()) + float64(t.Minute())/60
		day := 1.0
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			day = 0.42
		}
		// A working-day curve: quiet overnight, a plateau across the day.
		daily := 0.25 + 0.75*math.Max(0, math.Sin((hour-4)/20*math.Pi))
		wobble := 1 + 0.08*math.Sin(float64(i*7+seed*13))
		v := peak * shape * daily * day * wobble
		in = append(in, Point{T: t.Unix(), V: v})
		out = append(out, Point{T: t.Unix(), V: v * 0.42 * (1 + 0.1*math.Cos(float64(i*5+seed)))})
	}
	return in, out
}
