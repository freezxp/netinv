// Package vmwrite sends a VictoriaMetrics import body to a primary store and,
// optionally, to mirrors.
//
// The mirror exists so a second VictoriaMetrics can hold a copy of everything
// collected — a warm standby, an off-box archive, somewhere to point a
// long-retention instance. It is deliberately *best effort*, and that word is
// load-bearing: the primary write is what collection depends on, so a mirror
// that is slow, full or simply switched off must never delay it, fail it, or
// cause a batch to be redelivered. A backup target that can stop production
// ingest is a liability, not a backup.
//
// The honest consequence: what the mirror holds is complete only for the time
// it was reachable. Nothing backfills a window it missed. That gap is counted
// and exported (netinv_vm_mirror_failed_total, netinv_vm_mirror_samples_total)
// rather than left to be discovered later, and doc 05 says plainly that a
// guaranteed-complete copy is vmagent's job — it has a persistent queue and
// replays what it could not deliver.
package vmwrite

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	mirrorSamples = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "netinv_vm_mirror_samples_total",
		Help: "Sample batches successfully mirrored, by target.",
	}, []string{"target"})
	mirrorFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "netinv_vm_mirror_failed_total",
		Help: "Sample batches a mirror did not accept, by target. Each one is a hole in the copy that nothing backfills.",
	}, []string{"target"})
)

// Target is a primary VictoriaMetrics plus zero or more mirrors.
type Target struct {
	Primary string
	Mirrors []string
	Log     *slog.Logger
	HTTP    *http.Client
	// MirrorHTTP is separate on purpose: a mirror gets a short timeout of its
	// own so a wedged backup cannot hold a goroutine open for the primary's
	// much longer one.
	MirrorHTTP *http.Client

	warnOnce sync.Map // target -> *time.Time, to rate-limit the failure log
}

// ParseMirrors splits the configured mirror list. Comma-separated so one
// environment variable can name several, which is the shape every other list
// in this deployment already uses.
func ParseMirrors(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func New(primary string, mirrors []string, log *slog.Logger) *Target {
	return &Target{
		Primary: primary, Mirrors: mirrors, Log: log,
		HTTP:       &http.Client{Timeout: 15 * time.Second},
		MirrorHTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

// Import posts one import body to the primary and every mirror.
//
// Only the primary's error is returned. The body is buffered because it has to
// be replayed per target — an io.Reader is consumed by the first POST, and
// mirroring by re-reading a spent reader is the kind of bug that silently
// writes nothing.
func (t *Target) Import(ctx context.Context, body []byte) error {
	if err := post(ctx, t.HTTP, t.Primary, body); err != nil {
		return err
	}
	for _, m := range t.Mirrors {
		t.mirror(ctx, m, body)
	}
	return nil
}

// mirror writes to one backup target, swallowing every failure.
//
// context.WithoutCancel matters: the caller's context is often tied to the
// message being processed, and a primary write that finishes and acks would
// otherwise cancel the mirror mid-flight, turning a healthy backup into a
// stream of truncated requests.
func (t *Target) mirror(ctx context.Context, url string, body []byte) {
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := post(mctx, t.MirrorHTTP, url, body); err != nil {
		mirrorFailed.WithLabelValues(url).Inc()
		t.warn(url, err)
		return
	}
	mirrorSamples.WithLabelValues(url).Inc()
}

// warn logs at most once a minute per target. A mirror that has been down for
// an hour would otherwise produce a line per batch — thousands of them — and
// bury the one thing worth reading in the log of a service that is otherwise
// working perfectly.
func (t *Target) warn(url string, err error) {
	now := time.Now()
	if last, ok := t.warnOnce.Load(url); ok {
		if now.Sub(last.(time.Time)) < time.Minute {
			return
		}
	}
	t.warnOnce.Store(url, now)
	if t.Log != nil {
		t.Log.Warn("metrics mirror rejected a batch — the copy has a hole "+
			"nothing will backfill", "target", url, "err", err)
	}
}

func post(ctx context.Context, c *http.Client, base string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v1/import", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: string(b)}
	}
	// Drain so the connection can be reused rather than torn down per batch.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return "import status " + http.StatusText(e.Code) + ": " + e.Body
}
