// netinv-alerter — rule evaluation and alert lifecycle, leader-elected (doc 05 §2).
package main

import (
	"context"
	"os"
	"time"

	alertamqp "github.com/freezxp/netinv/backend/internal/alerting/adapters/amqp"
	alertpg "github.com/freezxp/netinv/backend/internal/alerting/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/alerting/adapters/vm"
	"github.com/freezxp/netinv/backend/internal/alerting/app"
	"github.com/freezxp/netinv/backend/internal/collection/adapters/leader"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/redisx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("alerter", func(ctx context.Context, rt *service.Runtime) error {
		dsn := os.Getenv("NETINV_PG_DSN")
		redisAddr := os.Getenv("NETINV_REDIS_ADDR")
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		vmURL := os.Getenv("NETINV_VM_URL")
		if dsn == "" || redisAddr == "" || amqpURL == "" || vmURL == "" {
			rt.Log.Warn("PG/Redis/AMQP/VM env not all set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		pool, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()
		rc, err := redisx.Connect(ctx, redisAddr)
		if err != nil {
			return err
		}
		defer rc.Close()
		mq, err := amqpx.Connect(ctx, amqpURL)
		if err != nil {
			return err
		}
		defer mq.Close()
		if err := mq.EnsureEventsTopology(); err != nil {
			return err
		}

		store := &alertpg.Store{Pool: pool}
		// TTL must exceed the 30s evaluation tick or the lease expires between
		// renewals and leadership bounces every cycle.
		lease := &leader.Lease{Client: rc, Key: "leader:alerter", TTL: 90 * time.Second, Log: rt.Log}
		defer lease.Release(context.WithoutCancel(ctx))

		eval := &app.Evaluator{
			Rules: store, Instances: store, Silences: store, Ifaces: store,
			Metrics: vm.New(vmURL),
			Publish: &alertamqp.Publisher{Client: mq, BaseURL: os.Getenv("NETINV_UI_URL")},
			Log:     rt.Log,
		}
		rt.Health.SetReady(true)
		rt.Log.Info("alerter running", "vm", vmURL)
		return eval.Run(ctx, lease, 0)
	})
}
