// Package app — Metrics context: the ingest pipeline (doc 05 §5).
// Consumes metric batches, enriches with inventory labels, writes VM.
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/retryx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// DeviceLabels is the enrichment snapshot entry (doc 05 §5).
type DeviceLabels struct {
	Device string
	Site   string
	Vendor string
}

// LabelSource provides the id→labels snapshot. Event-driven refresh replaces
// polling when the event bus consumer lands (doc 05 §8).
type LabelSource interface {
	Snapshot(ctx context.Context) (map[string]DeviceLabels, error)
}

// SeriesWriter persists enriched samples (VictoriaMetrics adapter).
type SeriesWriter interface {
	Write(ctx context.Context, samples []EnrichedSample) error
}

type EnrichedSample struct {
	Name     string
	Labels   map[string]string
	TSMillis int64
	Value    float64
}

type Ingester struct {
	Labels  LabelSource
	Writer  SeriesWriter
	Log     *slog.Logger
	Refresh time.Duration // snapshot refresh, default 60s

	mu   sync.RWMutex
	snap map[string]DeviceLabels
}

func (i *Ingester) Run(ctx context.Context, deliveries <-chan amqp.Delivery) error {
	if i.Refresh == 0 {
		i.Refresh = time.Minute
	}
	if err := i.refresh(ctx); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(i.Refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := i.refresh(ctx); err != nil {
					i.Log.Warn("label snapshot refresh failed", "err", err)
				}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			i.handle(ctx, d)
		}
	}
}

func (i *Ingester) refresh(ctx context.Context) error {
	snap, err := i.Labels.Snapshot(ctx)
	if err != nil {
		return err
	}
	i.mu.Lock()
	i.snap = snap
	i.mu.Unlock()
	return nil
}

func (i *Ingester) handle(ctx context.Context, d amqp.Delivery) {
	var batch wire.MetricBatch
	if err := json.Unmarshal(d.Body, &batch); err != nil {
		i.Log.Warn("malformed batch dropped", "err", err)
		_ = d.Reject(false)
		return
	}
	enriched := make([]EnrichedSample, 0, len(batch.Samples))
	i.mu.RLock()
	for _, s := range batch.Samples {
		labels := map[string]string{"device_id": s.DeviceID}
		for k, v := range s.Labels {
			labels[k] = sanitizeLabel(v)
		}
		if dl, ok := i.snap[s.DeviceID]; ok {
			labels["device"] = dl.Device
			labels["site"] = dl.Site
			if dl.Vendor != "" {
				labels["vendor"] = dl.Vendor
			}
		}
		enriched = append(enriched, EnrichedSample{
			Name: s.Name, Labels: labels, TSMillis: s.TSMillis, Value: s.Value,
		})
	}
	i.mu.RUnlock()

	// At-least-once: ack only after a confirmed VM write; VM dedupes
	// identical samples (doc 05 §4).
	err := retryx.Do(ctx, retryx.Policy{
		MaxAttempts: 10, BaseDelay: 250 * time.Millisecond, MaxDelay: 30 * time.Second,
	}, func() error { return i.Writer.Write(ctx, enriched) })
	if err != nil {
		i.Log.Error("VM write failed after retries — requeueing batch", "err", err)
		_ = d.Nack(false, errx.KindOf(err) == errx.KindTransient)
		return
	}
	_ = d.Ack(false)
}

// sanitizeLabel bounds label values (doc 05 §6 cardinality rules).
func sanitizeLabel(v string) string {
	if len(v) > 120 {
		return v[:120]
	}
	return v
}
