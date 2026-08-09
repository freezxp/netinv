package capacity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const gb = 1 << 30

func TestGrowthProjectsFromMeasuredRate(t *testing.T) {
	r := &Report{
		Disk:    Disk{UsedBytes: 2 * gb, FreeBytes: 8 * gb},
		Devices: 10,
		Metrics: Metrics{
			Series:         2000,
			SamplesPerDay:  3_000_000,
			BytesPerSample: 1, // keeps the arithmetic checkable by hand
		},
	}
	g := growth(r)

	if g.BytesPerDay != 3_000_000 {
		t.Errorf("bytes/day = %v, want 3000000", g.BytesPerDay)
	}
	// 8 GiB free at 3 MB/day.
	if want := float64(8*gb) / 3_000_000; !approx(g.DaysUntilFull, want, 0.01) {
		t.Errorf("days until full = %v, want %v", g.DaysUntilFull, want)
	}
	// The volume's whole capacity, not just what is unused: "how long can this
	// keep data" includes the space the current data already occupies.
	wantMax := float64(10*gb) / 3_000_000 * 86400
	if !approx(g.MaxRetentionS, wantMax, 0.01) {
		t.Errorf("max retention = %v s, want %v s", g.MaxRetentionS, wantMax)
	}
	if want := 3_000_000.0 * 365 / 10; !approx(g.BytesPerDevicePerYear, want, 0.01) {
		t.Errorf("bytes/device/year = %v, want %v", g.BytesPerDevicePerYear, want)
	}
}

// Projecting from zero data would divide by zero or, worse, report a
// confident "0 days until full" on a healthy system that simply has not been
// running long.
func TestGrowthIsUnknownRatherThanZero(t *testing.T) {
	for _, name := range []string{"no samples", "no compression figure"} {
		r := &Report{Disk: Disk{FreeBytes: 8 * gb}}
		if name == "no compression figure" {
			r.Metrics.SamplesPerDay = 1000
		} else {
			r.Metrics.BytesPerSample = 0.8
		}
		g := growth(r)
		if g.DaysUntilFull != -1 || g.MaxRetentionS != -1 {
			t.Errorf("%s: got days=%v maxRetention=%v, want -1 for unknown",
				name, g.DaysUntilFull, g.MaxRetentionS)
		}
	}
}

// The whole point of the page: say so when retention is set to more than the
// disk can hold, because otherwise data is dropped by disk pressure and looks
// like data loss.
func TestWarnsWhenRetentionExceedsWhatTheDiskHolds(t *testing.T) {
	r := &Report{
		Disk:    Disk{UsedBytes: 1 * gb, FreeBytes: 1 * gb},
		Metrics: Metrics{SamplesPerDay: 100_000_000, BytesPerSample: 1}, // 100 MB/day
	}
	r.Growth = growth(r)

	got := warnings(r, 2*365*24*time.Hour) // 2 years wanted, ~20 days available
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "Retention is set to") {
		t.Fatalf("no retention warning in %q", joined)
	}
	if !strings.Contains(joined, "2.0 years") {
		t.Errorf("warning does not state the configured retention: %q", joined)
	}
	if !strings.Contains(joined, "days") {
		t.Errorf("warning does not state what the disk actually sustains: %q", joined)
	}
}

func TestWarnsWhenDiskFillsSoon(t *testing.T) {
	r := &Report{
		Disk:    Disk{UsedBytes: 1 * gb, FreeBytes: 1 * gb},
		Metrics: Metrics{SamplesPerDay: 200_000_000, BytesPerSample: 1}, // ~5 days left
	}
	r.Growth = growth(r)
	joined := strings.Join(warnings(r, 24*time.Hour), " | ")
	if !strings.Contains(joined, "Disk fills in about") {
		t.Errorf("no disk-full warning in %q", joined)
	}
	if !strings.Contains(joined, "stops collection") {
		t.Errorf("warning does not say what actually breaks: %q", joined)
	}
}

// Silence when there is nothing to say: a page that always warns is a page
// nobody reads.
func TestNoWarningsWhenComfortable(t *testing.T) {
	r := &Report{
		Disk:    Disk{UsedBytes: 2 * gb, FreeBytes: 500 * gb},
		Metrics: Metrics{Series: 2000, SamplesPerDay: 3_000_000, BytesPerSample: 0.8},
	}
	r.Growth = growth(r)
	if got := warnings(r, 730*24*time.Hour); len(got) != 0 {
		t.Errorf("warnings on a healthy deployment: %q", got)
	}
}

// Collection stopping is the failure this project has actually hit twice, and
// it presents as everything looking healthy.
func TestWarnsWhenNothingIsBeingWritten(t *testing.T) {
	r := &Report{Disk: Disk{FreeBytes: 500 * gb}, Metrics: Metrics{Series: 2000}}
	r.Growth = growth(r)
	joined := strings.Join(warnings(r, 730*24*time.Hour), " | ")
	if !strings.Contains(joined, "Collection may have stopped") {
		t.Errorf("no stalled-collection warning in %q", joined)
	}
}

func TestParseMetricLineHandlesLabelsAndComments(t *testing.T) {
	cases := map[string]struct {
		name string
		val  float64
		ok   bool
	}{
		`vm_rows{type="storage/small"} 3926799`: {`vm_rows{type="storage/small"}`, 3926799, true},
		`vm_free_disk_space_bytes{path="/storage"} 1.6156393472e+10`: {
			`vm_free_disk_space_bytes{path="/storage"}`, 1.6156393472e10, true},
		`# HELP vm_rows total rows`: {"", 0, false},
		``:                          {"", 0, false},
		`malformed_no_value`:        {"", 0, false},
	}
	for line, want := range cases {
		name, val, ok := parseMetricLine(line)
		if ok != want.ok {
			t.Errorf("%q: ok = %v, want %v", line, ok, want.ok)
			continue
		}
		if ok && (name != want.name || val != want.val) {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", line, name, val, want.name, want.val)
		}
	}
}

// The disk figures must survive a metrics store that answers /metrics but not
// queries — otherwise a partial outage hides the capacity numbers, which is
// exactly when they matter.
func TestCollectDegradesWhenQueriesFail(t *testing.T) {
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/query") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(
			"# HELP whatever\n" +
				`vm_data_size_bytes{type="storage/small"} 1000000` + "\n" +
				`vm_indexdb_size_bytes{type="file"} 500000` + "\n" +
				`vm_free_disk_space_bytes{path="/storage"} 8000000000` + "\n" +
				`vm_rows{type="storage/small"} 2000000` + "\n"))
	}))
	defer vm.Close()

	c := &Collector{VMURL: vm.URL, Retention: 730 * 24 * time.Hour}
	rep, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if rep.Disk.FreeBytes != 8_000_000_000 {
		t.Errorf("free = %d, want the value from /metrics", rep.Disk.FreeBytes)
	}
	if rep.Disk.UsedBytes != 1_500_000 {
		t.Errorf("used = %d, want data+index summed", rep.Disk.UsedBytes)
	}
	if rep.Metrics.BytesPerSample != 0.75 {
		t.Errorf("bytes/sample = %v, want 1500000/2000000", rep.Metrics.BytesPerSample)
	}
	if rep.Growth.DaysUntilFull != -1 {
		t.Errorf("days until full = %v; with no sample rate it must read as unknown",
			rep.Growth.DaysUntilFull)
	}
}

func TestCollectFailsWhenStoreUnreachable(t *testing.T) {
	c := &Collector{VMURL: "http://127.0.0.1:1", Retention: time.Hour}
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("expected an error when the metrics store is unreachable")
	}
}

func TestHumanDurationReadsLikeAnOperatorWouldSayIt(t *testing.T) {
	cases := map[time.Duration]string{
		730 * 24 * time.Hour: "2.0 years",
		90 * 24 * time.Hour:  "3 months",
		10 * 24 * time.Hour:  "10 days",
		5 * time.Hour:        "5 hours",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func approx(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol*b || d <= tol
}
