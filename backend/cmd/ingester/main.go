// netinv-ingester — metric batch validation, enrichment, VM writes (doc 05 §5).
package main

import (
	"context"
	"os"
	"time"

	"github.com/freezxp/netinv/backend/internal/metrics/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/metrics/adapters/victoriametrics"
	"github.com/freezxp/netinv/backend/internal/metrics/app"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
	"github.com/freezxp/netinv/backend/internal/platform/settings"
	"github.com/freezxp/netinv/backend/internal/platform/vmwrite"
)

func main() {
	service.Run("ingester", func(ctx context.Context, rt *service.Runtime) error {
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		dsn := os.Getenv("NETINV_PG_DSN")
		vmURL := os.Getenv("NETINV_VM_URL")
		if amqpURL == "" || dsn == "" || vmURL == "" {
			rt.Log.Warn("NETINV_AMQP_URL/NETINV_PG_DSN/NETINV_VM_URL not all set — idle skeleton mode")
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
		// Destinations come from the UI setting, with NETINV_VM_MIRROR_URL as a
		// floor that cannot be switched off from a browser — for a copy that is
		// a requirement rather than an operator's convenience. Read per batch
		// through a cache, so a change made in the UI applies without a restart.
		mirrors := settings.NewCache(&settings.Store{Pool: pool}, 30*time.Second)
		fixed := vmwrite.ParseMirrors(os.Getenv("NETINV_VM_MIRROR_URL"))
		ing := &app.Ingester{
			Labels: &postgres.LabelSource{Pool: pool},
			Writer: victoriametrics.NewMirroredDynamic(vmURL, func() []string {
				return append(append([]string{}, fixed...), mirrors.Targets(ctx)...)
			}, rt.Log),
			Log: rt.Log,
		}
		rt.Health.SetReady(true)
		// Consume with reconnect: the delivery stream closes on broker
		// restarts; amqpx redials underneath (doc 23 §7).
		for ctx.Err() == nil {
			if err := mq.EnsureMetricsQueue(); err == nil {
				if deliveries, stop, err := mq.Consume(amqpx.MetricsQueue, 32); err == nil {
					rt.Log.Info("ingester consuming", "queue", amqpx.MetricsQueue, "vm", vmURL)
					err := ing.Run(ctx, deliveries)
					// Cancel before reconnecting, or the retry leaves a consumer
					// behind that the broker keeps feeding and nobody acks.
					stop()
					if err != nil {
						return err
					}
					rt.Log.Warn("metric stream closed — reconnecting")
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
		return nil
	})
}
