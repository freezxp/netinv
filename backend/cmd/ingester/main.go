// netinv-ingester — metric batch validation, enrichment, VM writes (doc 05 §5).
package main

import (
	"context"
	"os"

	"github.com/freezxp/netinv/backend/internal/metrics/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/metrics/adapters/victoriametrics"
	"github.com/freezxp/netinv/backend/internal/metrics/app"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
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
		if err := mq.EnsureMetricsQueue(); err != nil {
			return err
		}
		deliveries, err := mq.Consume(amqpx.MetricsQueue, 32)
		if err != nil {
			return err
		}

		ing := &app.Ingester{
			Labels: &postgres.LabelSource{Pool: pool},
			Writer: victoriametrics.New(vmURL),
			Log:    rt.Log,
		}
		rt.Health.SetReady(true)
		rt.Log.Info("ingester consuming", "queue", amqpx.MetricsQueue, "vm", vmURL)
		return ing.Run(ctx, deliveries)
	})
}
