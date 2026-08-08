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
	jobs := make(chan amqp.Delivery)
	var wg sync.WaitGroup
	for range r.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-jobs:
					if !ok {
						return
					}
					r.handle(ctx, d)
				}
			}
		}()
	}
	// Job-stream supervisor: (re)establishes consumption across broker
	// restarts — the delivery channel closes on connection loss (doc 07 §6).
	go func() {
		defer close(jobs)
		for ctx.Err() == nil {
			err := func() error {
				if err := r.Client.DeclareJobTopology(); err != nil {
					return err
				}
				if err := r.Client.EnsureSiteQueue(r.SiteID); err != nil {
					return err
				}
				return r.Client.EnsureMetricsQueue()
			}()
			if err == nil {
				var deliveries <-chan amqp.Delivery
				deliveries, err = r.Client.Consume(amqpx.SiteQueue(r.SiteID), r.Workers*2)
				if err == nil {
					r.Log.Info("job stream established",
						"queue", amqpx.SiteQueue(r.SiteID), "workers", r.Workers)
					for d := range deliveries {
						select {
						case <-ctx.Done():
							return
						case jobs <- d:
						}
					}
					r.Log.Warn("job stream closed — reconnecting")
				}
			}
			if err != nil {
				r.Log.Warn("job stream unavailable — retrying", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
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
	case "sync":
		return nil, r.executeSync(jctx, conn, job)
	case "health":
		hc, ok := conn.(sdk.HealthCollector)
		if !ok {
			return nil, nil // connector without health capability
		}
		sess, err := NewSNMPSession(job)
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		sdkSamples, err := hc.CollectHealth(jctx, sess)
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
	default:
		return nil, nil
	}
}

// executeSync collects the inventory snapshot (+ topology when the connector
// is capable) and publishes a SyncResult for the core's sync consumer (doc 11).
func (r *Runtime) executeSync(ctx context.Context, conn sdk.Connector, job wire.PollJob) error {
	res := wire.SyncResult{
		JobID: job.JobID, DeviceID: job.DeviceID, PollerID: r.PollerID,
		Trigger: job.Trigger, CollectedAt: time.Now().UTC(),
	}
	inv, ok := conn.(sdk.InventoryCollector)
	if !ok {
		return nil // connector without inventory capability: nothing to sync
	}
	sess, err := NewSNMPSession(job)
	if err == nil {
		defer sess.Close()
		var snap *sdk.InventorySnapshot
		snap, err = inv.CollectInventory(ctx, sess)
		if err == nil {
			ws := &wire.SyncSnapshot{
				SysName: snap.SysName, SysDescr: snap.SysDescr,
				SysObjectID: snap.SysObjectID, SysLocation: snap.SysLocation,
				SysContact: snap.SysContact, UptimeS: snap.UptimeS,
			}
			for _, i := range snap.Interfaces {
				ws.Interfaces = append(ws.Interfaces, wire.SyncInterface{
					IfIndex: i.IfIndex, Name: i.Name, Alias: i.Alias, Descr: i.Descr,
					IfType: i.IfType, MTU: i.MTU, SpeedBPS: i.SpeedBPS,
					PhysAddress: i.PhysAddress, AdminStatus: i.AdminStatus,
					OperStatus: i.OperStatus,
				})
			}
			if topo, tok := conn.(sdk.TopologyCollector); tok {
				if adjs, terr := topo.CollectTopology(ctx, sess); terr == nil {
					for _, a := range adjs {
						ws.Adjacencies = append(ws.Adjacencies, wire.SyncAdjacency{
							LocalIfIndex: a.LocalIfIndex, RemoteSysName: a.RemoteSysName,
							RemotePortID: a.RemotePortID, RemoteChassis: a.RemoteChassis,
							Protocol: a.Protocol,
						})
					}
				}
			}
			res.Snapshot = ws
		}
	}
	if err != nil {
		res.Error = err.Error()
	}
	if perr := r.Client.PublishJSON(ctx, "", amqpx.SyncResultsQueue, res); perr != nil {
		return perr
	}
	return err // original collect error drives poll_success
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
