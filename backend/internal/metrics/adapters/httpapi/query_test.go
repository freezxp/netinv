package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The proxy's range ceiling was a hard-coded 90 days while the UI offered
// Cacti's presets up to two years, so "Last Year" returned 422 instead of a
// chart — and the mistake survived a round of manual checking because those
// were run against VictoriaMetrics directly, bypassing this handler. These
// tests exercise the ceiling itself.

func rangeReq(t *testing.T, hours float64, step time.Duration) *http.Request {
	t.Helper()
	end := time.Now()
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	q := "query=up" +
		"&start=" + strconv.FormatInt(start.Unix(), 10) +
		"&end=" + strconv.FormatInt(end.Unix(), 10) +
		"&step=" + strconv.Itoa(int(step.Seconds()))
	return httptest.NewRequest(http.MethodGet, "/metrics/query_range?"+q, nil)
}

func TestRangeCeilingFollowsConfiguredRetention(t *testing.T) {
	// An upstream that records whether the request was forwarded at all.
	var forwarded bool
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded = true
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer vm.Close()

	twoYears := 730 * 24 * time.Hour
	p := &QueryProxy{VMURL: vm.URL, MaxRange: twoYears}

	for _, tc := range []struct {
		name    string
		hours   float64
		wantFwd bool
	}{
		{"a year is within a two-year retention", 365 * 24, true},
		{"two years exactly", 730 * 24, true},
		{"three years exceeds it", 3 * 365 * 24, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forwarded = false
			w := httptest.NewRecorder()
			p.rangeQuery(w, rangeReq(t, tc.hours, time.Hour))

			if tc.wantFwd {
				if !forwarded {
					t.Fatalf("range was rejected; with retention at 2y it should reach the store (status %d)",
						w.Code)
				}
				return
			}
			if forwarded {
				t.Fatal("range beyond retention was forwarded; the store would scan data it does not have")
			}
			if w.Code < 400 {
				t.Errorf("status = %d, want a client error", w.Code)
			}
		})
	}
}

// A ceiling that misreports itself is worse than a low one: the operator
// changes retention and the error keeps naming the old number.
func TestRejectionNamesTheActualLimit(t *testing.T) {
	p := &QueryProxy{VMURL: "http://127.0.0.1:0", MaxRange: 730 * 24 * time.Hour}
	w := httptest.NewRecorder()
	p.rangeQuery(w, rangeReq(t, 3*365*24, time.Hour))

	body := w.Body.String()
	if want := "730 day"; !strings.Contains(body, want) {
		t.Errorf("error %q does not mention the configured limit (%s)", body, want)
	}
	if strings.Contains(body, "90 day") {
		t.Errorf("error %q still names the old hard-coded 90-day limit", body)
	}
}

func TestDefaultCeilingIsNinetyDaysWhenUnset(t *testing.T) {
	p := &QueryProxy{VMURL: "http://127.0.0.1:0"} // MaxRange zero
	if got, want := p.maxRange(), 90*24*time.Hour; got != want {
		t.Errorf("maxRange() = %v, want %v — zero must not mean unlimited", got, want)
	}
}

// The UI needs this to decide which presets to offer; a wrong or missing value
// puts it back to guessing, which is how the 422s shipped.
func TestLimitsReportsTheCeilingInSeconds(t *testing.T) {
	p := &QueryProxy{MaxRange: 730 * 24 * time.Hour}
	w := httptest.NewRecorder()
	p.limits(w, httptest.NewRequest(http.MethodGet, "/metrics/limits", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got struct {
		MaxRangeS int64 `json:"max_range_s"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if want := int64(730 * 24 * 3600); got.MaxRangeS != want {
		t.Errorf("max_range_s = %d, want %d", got.MaxRangeS, want)
	}
}
