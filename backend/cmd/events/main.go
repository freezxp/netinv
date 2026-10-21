// netinv-events — SNMP trap intake (ADR-025, doc 36).
//
// Receives v1/v2c traps and informs, resolves each to a device, stores it in
// events.snmp_traps and publishes a netinv_snmp_trap sample to metrics.raw so
// the alert engine can fire on it. Like netinv-flow it accepts unsolicited
// network input and runs as a single replica: a trap is delivered once, to one
// address, and a second replica behind the same address would split the
// stream without adding anything.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/events"
	eventpg "github.com/freezxp/netinv/backend/internal/events/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

func main() {
	service.Run("events", func(ctx context.Context, rt *service.Runtime) error {
		dsn := os.Getenv("NETINV_PG_DSN")
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		if dsn == "" || amqpURL == "" {
			rt.Log.Warn("NETINV_PG_DSN or NETINV_AMQP_URL not set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		pool, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		mq, err := amqpx.Connect(ctx, amqpURL)
		if err != nil {
			return err
		}
		defer mq.Close()
		if err := mq.EnsureMetricsQueue(); err != nil {
			return err
		}

		store := &eventpg.Store{Pool: pool}
		index := &eventpg.AddrIndex{Pool: pool}
		// The api applies migrations; until it has, the tables this reads do
		// not exist. Exiting lets the restart policy retry, as the ingester does.
		if err := index.Refresh(ctx); err != nil {
			return fmt.Errorf("device address index: %w", err)
		}
		rt.Log.Info("device address index loaded", "addresses", index.Len())

		addr := os.Getenv("NETINV_TRAP_ADDR")
		if addr == "" {
			// 1162, not 162: binding below 1024 needs root or a capability, and
			// the container runs as neither. Compose maps host 162 onto it,
			// so agents still send to the standard port.
			addr = ":1162"
		}
		l := &events.Listener{
			Addr: addr, Resolve: index, Store: store,
			Publish: &publisher{mq: mq}, Log: rt.Log,
		}
		if v := os.Getenv("NETINV_TRAP_ALLOW"); v != "" {
			if l.Allow, err = events.ParseAllow(v); err != nil {
				return err
			}
			rt.Log.Info("trap source allow-list active", "prefixes", len(l.Allow))
		} else {
			rt.Log.Warn("no NETINV_TRAP_ALLOW set — accepting traps from any source")
		}
		if v := os.Getenv("NETINV_TRAP_COMMUNITIES"); v != "" {
			l.Communities = map[string]bool{}
			for _, c := range strings.Split(v, ",") {
				if c = strings.TrimSpace(c); c != "" {
					l.Communities[c] = true
				}
			}
		} else {
			rt.Log.Warn("no NETINV_TRAP_COMMUNITIES set — accepting any community")
		}
		if v := os.Getenv("NETINV_TRAP_RATE"); v != "" {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("NETINV_TRAP_RATE=%q: want a positive number", v)
			}
			l.PerSourceRate = n
		}
		retention := 30 * 24 * time.Hour
		if v := os.Getenv("NETINV_TRAP_RETENTION"); v != "" {
			d, err := parseDays(v)
			if err != nil {
				return err
			}
			retention = d
		}

		go every(ctx, time.Minute, func() {
			if err := index.Refresh(ctx); err != nil {
				rt.Log.Warn("device address index refresh failed", "err", err)
			}
		})
		go every(ctx, time.Hour, func() {
			if n, err := store.Prune(ctx, retention); err != nil {
				rt.Log.Warn("trap prune failed", "err", err)
			} else if n > 0 {
				rt.Log.Info("pruned traps", "rows", n, "retention", retention.String())
			}
		})

		rt.Health.SetReady(true)
		return l.Run(ctx)
	})
}

type publisher struct{ mq *amqpx.Client }

func (p *publisher) PublishSamples(ctx context.Context, s []wire.Sample) error {
	return p.mq.PublishJSON(ctx, "", amqpx.MetricsQueue,
		wire.MetricBatch{PollerID: "events", Samples: s})
}

func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

// parseDays accepts a Go duration or a bare "<n>d", since retention is
// naturally spoken in days and time.ParseDuration has no unit for them.
func parseDays(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("NETINV_TRAP_RETENTION=%q: want e.g. 30d or 720h", v)
	}
	return d, nil
}
