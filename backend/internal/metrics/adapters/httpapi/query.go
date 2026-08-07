// Package httpapi — scope-guarded MetricsQL query proxy (doc 09 §7).
package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	})
}

const (
	maxRange = 90 * 24 * time.Hour // server clamps (doc 09 §7)
	minStep  = 15 * time.Second
)

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
	if end.Sub(start) > maxRange {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "range exceeds 90 days"))
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

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errx.New(errx.KindInvalid, "missing time")
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(unix, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}
