package reports

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// VMReader evaluates an expression at a chosen instant.
//
// A report is asked about a window that has already closed — last month, last
// week — so every query is anchored at the window's end rather than at now.
// The alerting reader cannot serve this: it evaluates at the current instant,
// which is the right thing for a rule and the wrong thing for a report.
type VMReader struct {
	BaseURL string
	HTTP    *http.Client
}

func NewVMReader(baseURL string) *VMReader {
	return &VMReader{
		BaseURL: baseURL,
		// Reports roll up far more series than an alert rule does, over far
		// longer windows. The generous timeout is the difference between a
		// slow month-long report and a failed one.
		HTTP: &http.Client{Timeout: 120 * time.Second},
	}
}

func (r *VMReader) QueryAt(ctx context.Context, expr string, at time.Time) ([]Sample, error) {
	params := url.Values{
		"query": {expr},
		"time":  {strconv.FormatInt(at.Unix(), 10)},
	}
	// POST rather than GET: a grouped report's expression names every
	// interface belonging to every customer, which runs to tens of kilobytes
	// on a real fleet. As a query string that is silently truncated or
	// rejected by whatever proxy sits in the middle; as a form body it is just
	// a body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.BaseURL+"/api/v1/query", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "report query")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		// Carry the store's own words: "missing operand" tells you what is
		// wrong, a status code does not.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if reason := strings.TrimSpace(e.Error); reason != "" {
			return nil, errx.New(errx.KindInvalid, "%s", reason)
		}
	}
	if resp.StatusCode >= 300 {
		return nil, errx.New(errx.KindTransient, "report query status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "decode report query")
	}
	out := make([]Sample, 0, len(body.Data.Result))
	for _, s := range body.Data.Result {
		raw, _ := s.Value[1].(string)
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue // NaN and friends: the interface simply had no data
		}
		out = append(out, Sample{Labels: s.Metric, Value: v})
	}
	return out, nil
}

// QueryRange evaluates an expression across the window, which is what a chart
// needs and what QueryAt cannot give: a rollup at an instant is one number per
// series, and a chart is the shape between them.
func (r *VMReader) QueryRange(ctx context.Context, expr string,
	from, to time.Time, step time.Duration) ([]Series, error) {
	if step <= 0 {
		step = time.Minute
	}
	params := url.Values{
		"query": {expr},
		"start": {strconv.FormatInt(from.Unix(), 10)},
		"end":   {strconv.FormatInt(to.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	// POST for the same reason QueryAt uses it: a grouped report's expression
	// names every interface of every customer and runs to tens of kilobytes.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.BaseURL+"/api/v1/query_range", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "report range query")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if reason := strings.TrimSpace(e.Error); reason != "" {
			return nil, errx.New(errx.KindInvalid, "%s", reason)
		}
	}
	if resp.StatusCode >= 300 {
		return nil, errx.New(errx.KindTransient, "report range query status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "decode report range query")
	}
	out := make([]Series, 0, len(body.Data.Result))
	for _, s := range body.Data.Result {
		se := Series{Labels: s.Metric, Points: make([]Point, 0, len(s.Values))}
		for _, pair := range s.Values {
			ts, _ := pair[0].(float64)
			raw, _ := pair[1].(string)
			v, err := strconv.ParseFloat(raw, 64)
			// ParseFloat *accepts* "NaN" and "+Inf" — it does not error on
			// them — so they have to be rejected by value. A NaN reaching the
			// renderer is not a wrong number on the page: it becomes a NaN
			// coordinate in a vector path, which corrupts the whole chart.
			// A gap in collection is drawn as a gap, never as a drop to zero.
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			se.Points = append(se.Points, Point{T: int64(ts), V: v})
		}
		out = append(out, se)
	}
	return out, nil
}
