// Package pollerrt — the poller runtime (doc 05 §5, doc 13): consumes the
// site's job queue, executes jobs through connectors, batches samples home.
// Sprint 6 scope: traffic + inventory-capable dispatch, batching, poll-success
// metrics. ICMP (S7), disk overflow buffer (S7), sync results (S8).
package pollerrt

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/freezxp/netinv/connectors/sdk"

	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type Runtime struct {
	PollerID       string
	SiteID         string
	Client         *amqpx.Client
	Log            *slog.Logger
	Workers        int // default 50
	Buffer         *DiskBuffer
	ICMPPrivileged bool
	Counters       Counters

	batchMu sync.Mutex
	batch   []wire.Sample
}

const (
	batchFlushSize = 500
	batchFlushAge  = 5 * time.Second
)

func (r *Runtime) Run(ctx context.Context) error {
	if r.Workers == 0 {
		r.Workers = 50
	}
	if err := r.Client.DeclareJobTopology(); err != nil {
		return err
	}
	if err := r.Client.EnsureSiteQueue(r.SiteID); err != nil {
		return err
	}
	if err := r.Client.EnsureMetricsQueue(); err != nil {
		return err
	}
	deliveries, err := r.Client.Consume(amqpx.SiteQueue(r.SiteID), r.Workers*2)
	if err != nil {
		return err
	}
	r.Log.Info("poller consuming", "queue", amqpx.SiteQueue(r.SiteID), "workers", r.Workers)

	var wg sync.WaitGroup
	for range r.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok {
						return
					}
					r.handle(ctx, d)
				}
			}
		}()
	}
	// Periodic flusher (age-based) + buffer drain loop (FR-COLL-08).
	flushT := time.NewTicker(batchFlushAge)
	defer flushT.Stop()
	drainT := time.NewTicker(15 * time.Second)
	defer drainT.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			r.flush(context.WithoutCancel(ctx)) // drain on shutdown
			return nil
		case <-flushT.C:
			r.flush(ctx)
		case <-drainT.C:
			if r.Buffer != nil {
				if n := r.Buffer.Drain(ctx, func(b wire.MetricBatch) error {
					return r.Client.PublishJSON(ctx, "", amqpx.MetricsQueue, b)
				}); n > 0 {
					r.Log.Info("buffer drained", "batches", n)
				}
			}
		}
	}
}

// Stats snapshots runtime counters for the heartbeat (FR-PLT-02).
func (r *Runtime) Stats() (ok, failed, batches int64, bufDepth int, bufBytes int64) {
	ok = r.Counters.PollsOK.Load()
	failed = r.Counters.PollsFailed.Load()
	batches = r.Counters.Batches.Load()
	if r.Buffer != nil {
		bufDepth, bufBytes = r.Buffer.Depth()
	}
	return
}

func (r *Runtime) handle(ctx context.Context, d amqp.Delivery) {
	var job wire.PollJob
	if err := json.Unmarshal(d.Body, &job); err != nil {
		r.Log.Warn("malformed job dropped", "err", err)
		_ = d.Reject(false) // poison → DLQ policy (doc 23 §3)
		return
	}
	start := time.Now()
	samples, err := r.execute(ctx, job)
	dur := time.Since(start)

	success := 1.0
	if err != nil {
		success = 0
		r.Counters.PollsFailed.Add(1)
		// Rate-limited logging happens at slog handler level later; keep warn.
		r.Log.Warn("poll failed", "device", job.DeviceID, "family", job.Family,
			"dur_ms", dur.Milliseconds(), "err", err)
	} else {
		r.Counters.PollsOK.Add(1)
	}
	now := time.Now().UTC().UnixMilli()
	samples = append(samples,
		wire.Sample{DeviceID: job.DeviceID, Name: "netinv_poll_success",
			Labels: map[string]string{"family": job.Family}, TSMillis: now, Value: success},
		wire.Sample{DeviceID: job.DeviceID, Name: "netinv_poll_duration_seconds",
			Labels: map[string]string{"family": job.Family}, TSMillis: now, Value: dur.Seconds()},
	)
	r.append(ctx, samples)
	// Ack regardless of poll outcome: the retry is the next scheduled cycle,
	// never an immediate redelivery hammering a sick device (NFR-19).
	_ = d.Ack(false)
}

func (r *Runtime) execute(ctx context.Context, job wire.PollJob) ([]wire.Sample, error) {
	conn, ok := sdk.ByID(job.ConnectorID)
	if !ok {
		conn, _ = sdk.ByID("generic")
	}
	jctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	switch job.Family {
	case "traffic":
		ic, ok := conn.(sdk.InterfaceCollector)
		if !ok {
			return nil, nil // connector lacks capability — success, no data
		}
		sess, err := NewSNMPSession(job)
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		sdkSamples, err := ic.CollectInterfaces(jctx, sess)
		if err != nil {
			return nil, err
		}
		out := make([]wire.Sample, 0, len(sdkSamples))
		for _, s := range sdkSamples {
			out = append(out, wire.Sample{
				DeviceID: job.DeviceID, Name: s.Name, Labels: s.Labels,
				TSMillis: s.At.UnixMilli(), Value: s.Value,
			})
		}
		return out, nil
	case "icmp":
		return probeICMP(jctx, job, r.ICMPPrivileged)
	case "health", "sync":
		// Implemented in Sprints 8 (sync) and 17 (vendor health).
		return nil, nil
	default:
		return nil, nil
	}
}

func (r *Runtime) append(ctx context.Context, samples []wire.Sample) {
	if len(samples) == 0 {
		return
	}
	r.batchMu.Lock()
	r.batch = append(r.batch, samples...)
	full := len(r.batch) >= batchFlushSize
	r.batchMu.Unlock()
	if full {
		r.flush(ctx)
	}
}

func (r *Runtime) flush(ctx context.Context) {
	r.batchMu.Lock()
	if len(r.batch) == 0 {
		r.batchMu.Unlock()
		return
	}
	out := r.batch
	r.batch = nil
	r.batchMu.Unlock()

	batch := wire.MetricBatch{PollerID: r.PollerID, SiteID: r.SiteID, Samples: out}
	if err := r.Client.PublishJSON(ctx, "", amqpx.MetricsQueue, batch); err != nil {
		if r.Buffer != nil {
			if berr := r.Buffer.Put(batch); berr == nil {
				r.Log.Warn("batch publish failed — spilled to disk buffer",
					"samples", len(out), "err", err)
				return
			}
		}
		r.Log.Error("batch publish failed — requeued in memory",
			"samples", len(out), "err", err)
		r.batchMu.Lock()
		r.batch = append(out, r.batch...)
		r.batchMu.Unlock()
		return
	}
	r.Counters.Batches.Add(1)
}
