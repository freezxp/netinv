// Package capacity answers the question a retention setting raises but does
// not answer: will the disk actually hold it?
//
// NetInv defaults to keeping two years of raw samples. That is a few gigabytes
// for a small fleet and tens of gigabytes for a large one, and nothing in the
// product told an operator which case they were in. The failure mode is slow
// and quiet — VictoriaMetrics stops accepting writes when the disk fills, and
// collection stops with it — so it needs to be visible long before it happens.
//
// Everything here is measured from the running system rather than estimated
// from configuration: bytes-per-sample from VictoriaMetrics' own totals, and
// the sample rate by counting what was written in the last hour.
package capacity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Report is the capacity picture at one moment.
type Report struct {
	// Configured retention, in seconds.
	RetentionS int64 `json:"retention_s"`

	Disk    Disk    `json:"disk"`
	Metrics Metrics `json:"metrics"`
	Growth  Growth  `json:"growth"`

	// Devices currently being polled.
	Devices int `json:"devices"`

	// Warnings are plain-language problems worth acting on, most serious
	// first. Always a list, never null: a null array crashed the weathermap
	// viewer earlier in this project's life, and there is no reason to make
	// the next consumer guard against the same thing.
	Warnings []string `json:"warnings"`
}

type Disk struct {
	// UsedBytes is what the metrics store occupies (data + index).
	UsedBytes int64 `json:"used_bytes"`
	// FreeBytes is what remains on its volume. VictoriaMetrics reports this
	// itself, so it reflects the volume the data is actually on rather than
	// whichever filesystem the API happens to run from.
	FreeBytes int64 `json:"free_bytes"`
}

type Metrics struct {
	// Series being written right now.
	Series int64 `json:"series"`
	// Samples stored in total.
	Samples int64 `json:"samples"`
	// BytesPerSample after compression, measured rather than assumed. Around
	// 0.8 is typical; it varies with how much the data actually changes.
	BytesPerSample float64 `json:"bytes_per_sample"`
	// SamplesPerDay actually being written, counted from the last hour of
	// stored data.
	//
	// Deliberately measured rather than derived from the polling schedule.
	// Schedules mix families with wildly different periods — 30 s ICMP against
	// 6-hourly inventory sync — so their mean is meaningless: on the pilot it
	// averages 5498 s against an effective 65 s, an 85-fold error that would
	// have made every projection here nonsense.
	SamplesPerDay float64 `json:"samples_per_day"`
	// EffectiveIntervalS is SamplesPerDay expressed per series, which is the
	// number an operator recognises.
	EffectiveIntervalS float64 `json:"effective_interval_s"`
}

type Growth struct {
	BytesPerDay float64 `json:"bytes_per_day"`
	// BytesPerDevicePerYear is the number to plan a fleet with: capacity
	// scales with how many devices are polled, not with how long you wait.
	BytesPerDevicePerYear float64 `json:"bytes_per_device_per_year"`
	// DaysUntilFull at the current rate; -1 when growth is not yet
	// measurable.
	DaysUntilFull float64 `json:"days_until_full"`
	// MaxRetentionS the current volume could sustain — what retention could
	// be set to, as opposed to what it is set to.
	MaxRetentionS float64 `json:"max_retention_s"`
}

// Collector builds a Report. VMURL points at VictoriaMetrics; Pool at the
// inventory database.
type Collector struct {
	VMURL     string
	Pool      *pgxpool.Pool
	Retention time.Duration
	HTTP      *http.Client
}

func (c *Collector) client() *http.Client {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return c.HTTP
}

const day = 24 * time.Hour

// Collect gathers the report. A failure to reach VictoriaMetrics is an error;
// a database that cannot answer the schedule query is not, because the disk
// figures are still worth showing without it.
func (c *Collector) Collect(ctx context.Context) (*Report, error) {
	vm, err := c.vmVitals(ctx)
	if err != nil {
		return nil, err
	}

	r := &Report{
		RetentionS: int64(c.Retention.Seconds()),
		Disk: Disk{
			UsedBytes: vm.dataBytes,
			FreeBytes: vm.freeBytes,
		},
		Metrics: Metrics{
			Samples: vm.rows,
		},
	}
	if vm.rows > 0 {
		r.Metrics.BytesPerSample = float64(vm.dataBytes) / float64(vm.rows)
	}

	r.Metrics.Series = c.seriesCount(ctx)
	r.Metrics.SamplesPerDay = c.samplesPerDay(ctx)
	if r.Metrics.Series > 0 && r.Metrics.SamplesPerDay > 0 {
		perSeriesPerDay := r.Metrics.SamplesPerDay / float64(r.Metrics.Series)
		r.Metrics.EffectiveIntervalS = day.Seconds() / perSeriesPerDay
	}
	r.Devices = c.deviceCount(ctx)

	r.Growth = growth(r)
	r.Warnings = warnings(r, c.Retention)
	return r, nil
}

// growth projects forward from what is measured. Kept separate from
// collection so it can be tested without a VictoriaMetrics or a database.
func growth(r *Report) Growth {
	g := Growth{DaysUntilFull: -1, MaxRetentionS: -1}
	if r.Metrics.SamplesPerDay <= 0 || r.Metrics.BytesPerSample <= 0 {
		return g
	}
	g.BytesPerDay = r.Metrics.SamplesPerDay * r.Metrics.BytesPerSample
	if g.BytesPerDay <= 0 {
		return g
	}
	if r.Devices > 0 {
		g.BytesPerDevicePerYear = g.BytesPerDay * 365 / float64(r.Devices)
	}
	g.DaysUntilFull = float64(r.Disk.FreeBytes) / g.BytesPerDay
	// What the volume could hold in total, not merely what is left: the
	// question is "how long can this deployment keep data", and the space the
	// current data occupies is part of the answer.
	total := float64(r.Disk.FreeBytes + r.Disk.UsedBytes)
	g.MaxRetentionS = total / g.BytesPerDay * day.Seconds()
	return g
}

// warnings turns the numbers into the two or three sentences an operator
// actually needs. Ordered most urgent first.
func warnings(r *Report, retention time.Duration) []string {
	out := []string{}
	g := r.Growth
	if g.DaysUntilFull >= 0 && g.DaysUntilFull < 30 {
		out = append(out, fmt.Sprintf(
			"Disk fills in about %.0f days at the current rate. VictoriaMetrics stops "+
				"accepting writes when it does, which stops collection.", g.DaysUntilFull))
	}
	if g.MaxRetentionS > 0 && retention > 0 {
		if want := retention.Seconds(); g.MaxRetentionS < want {
			out = append(out, fmt.Sprintf(
				"Retention is set to %s but this volume sustains about %s at the current "+
					"rate. The oldest data will be dropped early, by disk pressure rather "+
					"than by policy.",
				humanDuration(retention), humanDuration(time.Duration(g.MaxRetentionS)*time.Second)))
		}
	}
	if r.Metrics.SamplesPerDay <= 0 {
		out = append(out, "No samples were written in the last hour, so growth cannot "+
			"be projected. Collection may have stopped — check that every queue has a "+
			"consumer.")
	}
	return out
}

// humanDuration renders a span the way an operator would say it.
func humanDuration(d time.Duration) string {
	days := d.Hours() / 24
	switch {
	case days >= 365:
		return fmt.Sprintf("%.1f years", days/365)
	case days >= 60:
		return fmt.Sprintf("%.0f months", days/30)
	case days >= 1:
		return fmt.Sprintf("%.0f days", days)
	default:
		return fmt.Sprintf("%.0f hours", d.Hours())
	}
}

type vmVitals struct {
	dataBytes int64
	freeBytes int64
	rows      int64
}

// vmVitals reads VictoriaMetrics' own /metrics. Those numbers are not in the
// time series database — VictoriaMetrics does not scrape itself by default —
// so they have to be read from the endpoint directly.
func (c *Collector) vmVitals(ctx context.Context) (vmVitals, error) {
	var v vmVitals
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.VMURL+"/metrics", nil)
	if err != nil {
		return v, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return v, fmt.Errorf("metrics store unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, fmt.Errorf("metrics store returned %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		name, val, ok := parseMetricLine(sc.Text())
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(name, "vm_data_size_bytes"),
			strings.HasPrefix(name, "vm_indexdb_size_bytes"):
			v.dataBytes += int64(val)
		case strings.HasPrefix(name, "vm_free_disk_space_bytes"):
			// One volume; take the reading rather than summing, in case the
			// same path is reported more than once.
			v.freeBytes = int64(val)
		case strings.HasPrefix(name, "vm_rows{"):
			v.rows += int64(val)
		}
	}
	return v, sc.Err()
}

// parseMetricLine splits one Prometheus exposition line. Labels stay attached
// to the name so callers can match on a prefix.
func parseMetricLine(line string) (name string, value float64, ok bool) {
	if line == "" || line[0] == '#' {
		return "", 0, false
	}
	i := strings.LastIndexByte(line, ' ')
	if i < 0 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[i+1:]), 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(line[:i]), v, true
}

// seriesCount asks how many NetInv series are being written.
func (c *Collector) seriesCount(ctx context.Context) int64 {
	v, _ := c.instant(ctx, `count%28%7B__name__%3D~%22netinv_.%2A%22%7D%29`)
	return int64(v)
}

// instant runs a URL-encoded instant query. A failure returns ok=false rather
// than an error: every caller degrades the report instead of failing it, since
// the disk figures remain worth showing on their own.
func (c *Collector) instant(ctx context.Context, encodedQuery string) (float64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.VMURL+"/api/v1/query?query="+encodedQuery, nil)
	if err != nil {
		return 0, false
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()

	var out struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Data.Result) == 0 {
		return 0, false
	}
	if len(out.Data.Result[0].Value) < 2 {
		return 0, false
	}
	str, _ := out.Data.Result[0].Value[1].(string)
	n, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// samplesPerDay counts what was actually written in the last hour and scales
// it to a day. Costs one instant query over a one-hour window.
func (c *Collector) samplesPerDay(ctx context.Context) float64 {
	const q = `sum%28count_over_time%28%7B__name__%3D~%22netinv_.%2A%22%7D%5B1h%5D%29%29`
	v, ok := c.instant(ctx, q)
	if !ok {
		return 0
	}
	return v * 24
}

// deviceCount reports how many devices are scheduled for collection. Used to
// express growth per device, which is how a fleet is actually planned.
func (c *Collector) deviceCount(ctx context.Context) int {
	if c.Pool == nil {
		return 0
	}
	var n int
	row := c.Pool.QueryRow(ctx, `
		SELECT count(DISTINCT device_id) FROM platform.polling_schedule
		WHERE interval_s > 0`)
	if row.Scan(&n) != nil {
		return 0
	}
	return n
}
