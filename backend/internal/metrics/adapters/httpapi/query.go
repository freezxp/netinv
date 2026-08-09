// Package httpapi — scope-guarded MetricsQL query proxy (doc 09 §7).
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type QueryProxy struct {
	VMURL   string
	Checker authz.Checker
	HTTP    *http.Client
	// PollInterval is how often devices are polled. The UI needs it to size
	// rate() lookbacks: a lookback shorter than the interval spans at most one
	// sample and rate() returns nothing, so every traffic graph would go blank
	// the moment an operator slowed collection down. Zero means 60s.
	PollInterval func() time.Duration
	// MaxRange caps how far back a range query may reach. Zero means the
	// 90-day default. It should equal the metrics store's retention: a lower
	// ceiling hides data the operator is paying to keep, and a higher one just
	// returns long stretches of nothing.
	MaxRange time.Duration
}

func (q *QueryProxy) client() *http.Client {
	if q.HTTP == nil {
		q.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return q.HTTP
}

func (q *QueryProxy) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(q.Checker, authz.MetricsRead))
		g.Get("/metrics/query", q.instant)
		g.Get("/metrics/query_range", q.rangeQuery)
		g.Get("/metrics/limits", q.limits)
		g.Get("/metrics/names", q.names)
	})
}

const (
	defaultMaxRange = 730 * 24 * time.Hour // matches config.DefaultRetention
	minStep         = 15 * time.Second
)

func (q *QueryProxy) maxRange() time.Duration {
	if q.MaxRange > 0 {
		return q.MaxRange
	}
	return defaultMaxRange
}

// limits reports the query ceiling so the UI can offer time ranges that will
// actually resolve. Without it the range selector has to hard-code a guess,
// and any operator who changes retention gets either presets that fail or
// presets that are missing — the browser has no other way to learn what the
// deployment keeps.
func (q *QueryProxy) limits(w http.ResponseWriter, r *http.Request) {
	poll := 60 * time.Second
	if q.PollInterval != nil {
		if d := q.PollInterval(); d > 0 {
			poll = d
		}
	}
	_ = r
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"max_range_s":     int64(q.maxRange().Seconds()),
		"poll_interval_s": int64(poll.Seconds()),
	})
}

// names lists the metric names available to build a panel from. Without it a
// dashboard builder has to hard-code a list, which goes stale the moment a
// connector starts publishing something new — exactly the metrics an operator
// would most want to chart.
func (q *QueryProxy) names(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		q.VMURL+"/api/v1/label/__name__/values", nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := q.client().Do(req)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "metrics store"))
		return
	}
	defer resp.Body.Close()

	var out struct {
		Data []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "decode metric names"))
		return
	}
	// Only NetInv's own series. VictoriaMetrics also reports its internal vm_*
	// metrics, which are not what anyone is trying to graph.
	names := []string{}
	for _, n := range out.Data {
		if strings.HasPrefix(n, "netinv_") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": names})
}

func (q *QueryProxy) instant(w http.ResponseWriter, r *http.Request) {
	expr := r.URL.Query().Get("query")
	if expr == "" {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "query parameter is required"))
		return
	}
	params := url.Values{"query": {expr}}
	if t := r.URL.Query().Get("time"); t != "" {
		params.Set("time", t)
	}
	q.forward(w, r, "/api/v1/query", params)
}

func (q *QueryProxy) rangeQuery(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	expr := qs.Get("query")
	start, err1 := parseTime(qs.Get("start"))
	end, err2 := parseTime(qs.Get("end"))
	if expr == "" || err1 != nil || err2 != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid,
			"query, start and end (RFC3339 or unix) are required"))
		return
	}
	// The message names the actual ceiling. Hard-coding "90 days" outlived the
	// constant it described the moment retention became configurable, and a
	// limit that misreports itself is worse than one that is merely low.
	if limit := q.maxRange(); end.Sub(start) > limit {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid,
			"range exceeds the %s query limit", roundDays(limit)))
		return
	}
	step := 60 * time.Second
	if s := qs.Get("step"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			step = d
		} else if secs, err := strconv.Atoi(s); err == nil {
			step = time.Duration(secs) * time.Second
		}
	}
	if step < minStep {
		step = minStep
	}
	// Cap the datapoint count per series (NFR-13 guardrail).
	if points := end.Sub(start) / step; points > 11000 {
		step = end.Sub(start) / 11000
	}
	params := url.Values{
		"query": {expr},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	q.forward(w, r, "/api/v1/query_range", params)
}

func (q *QueryProxy) forward(w http.ResponseWriter, r *http.Request, path string, params url.Values) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		q.VMURL+path+"?"+params.Encode(), nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := q.client().Do(req)
	if err != nil {
		httpx.WriteError(w, r, errx.Wrap(errx.KindTransient, err, "metrics store"))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// roundDays renders a limit the way an operator set it, so the error is
// actionable: "730 days" rather than "17520h0m0s".
func roundDays(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days <= 0 {
		return d.String()
	}
	return strconv.Itoa(days) + " day" + plural(days)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errx.New(errx.KindInvalid, "missing time")
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(unix, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}
