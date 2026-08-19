// Package victoriametrics — SeriesWriter over VM's JSON-line import API
// (doc 05 §2: ingester is the sole VM writer).
package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"log/slog"

	"github.com/freezxp/netinv/backend/internal/metrics/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/vmwrite"
)

type Writer struct {
	BaseURL string // e.g. http://victoriametrics:8428
	HTTP    *http.Client
	// Target carries the primary plus any backup instances. When set it owns
	// the POST, so mirroring covers every sample the ingester writes rather
	// than a subset someone has to reason about.
	Target *vmwrite.Target
}

func New(baseURL string) *Writer {
	return &Writer{BaseURL: baseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// NewMirrored writes to baseURL and copies every batch to each mirror,
// best-effort — see package vmwrite for what that does and does not promise.
func NewMirrored(baseURL string, mirrors []string, log *slog.Logger) *Writer {
	w := New(baseURL)
	if len(mirrors) > 0 {
		w.Target = vmwrite.New(baseURL, mirrors, log)
	}
	return w
}

// NewMirroredDynamic takes the destination list as a function, so a change
// made in the UI takes effect on the next batch rather than the next restart.
// The target is always installed: the list is allowed to be empty now and
// non-empty a minute later.
func NewMirroredDynamic(baseURL string, mirrors func() []string, log *slog.Logger) *Writer {
	w := New(baseURL)
	w.Target = vmwrite.NewDynamic(baseURL, mirrors, log)
	return w
}

// importLine is VM's /api/v1/import format: one JSON object per line.
type importLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

func (w *Writer) Write(ctx context.Context, samples []app.EnrichedSample) error {
	if len(samples) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, s := range samples {
		metric := make(map[string]string, len(s.Labels)+1)
		metric["__name__"] = s.Name
		for k, v := range s.Labels {
			metric[k] = v
		}
		if err := enc.Encode(importLine{
			Metric: metric, Values: []float64{s.Value}, Timestamps: []int64{s.TSMillis},
		}); err != nil {
			return errx.Wrap(errx.KindInternal, err, "vm: encode")
		}
	}
	if w.Target != nil {
		return w.Target.Import(ctx, buf.Bytes())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		w.BaseURL+"/api/v1/import", &buf)
	if err != nil {
		return errx.Wrap(errx.KindInternal, err, "vm: request")
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "vm: post")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		kind := errx.KindTransient
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			kind = errx.KindInvalid
		}
		return errx.New(kind, "vm: import status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
