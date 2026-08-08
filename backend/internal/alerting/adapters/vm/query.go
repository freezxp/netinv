// Package vm — MetricsReader over VictoriaMetrics' Prometheus-compatible
// instant query API (doc 05 §7 read path).
package vm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/alerting/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type Reader struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Reader {
	return &Reader{BaseURL: baseURL, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (r *Reader) Query(ctx context.Context, expr string) ([]app.Series, error) {
	u := r.BaseURL + "/api/v1/query?query=" + url.QueryEscape(expr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "vm query")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnprocessableEntity ||
		resp.StatusCode == http.StatusBadRequest {
		// Carry VictoriaMetrics' own words: "missing operand" tells the author
		// what to fix, a status code does not.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if reason := strings.TrimSpace(e.Error); reason != "" {
			return nil, errx.New(errx.KindInvalid, "%s", reason)
		}
		return nil, errx.New(errx.KindInvalid, "vm rejected expression (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, errx.New(errx.KindTransient, "vm query status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "vm decode")
	}
	out := make([]app.Series, 0, len(body.Data.Result))
	for _, res := range body.Data.Result {
		val := 0.0
		if s, ok := res.Value[1].(string); ok {
			val, _ = strconv.ParseFloat(s, 64)
		}
		out = append(out, app.Series{Labels: res.Metric, Value: val})
	}
	return out, nil
}
