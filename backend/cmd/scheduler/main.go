// netinv-scheduler — poll/sync job fan-out, leader-elected (doc 05 §2, §5).
package main

import (
	"context"
	"os"

	amqpadapter "github.com/freezxp/netinv/backend/internal/collection/adapters/amqp"
	"github.com/freezxp/netinv/backend/internal/collection/adapters/leader"
	colpg "github.com/freezxp/netinv/backend/internal/collection/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/collection/adapters/secrets"
	"github.com/freezxp/netinv/backend/internal/collection/app"
	invpg "github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/redisx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("scheduler", func(ctx context.Context, rt *service.Runtime) error {
		dsn := os.Getenv("NETINV_PG_DSN")
		redisAddr := os.Getenv("NETINV_REDIS_ADDR")
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		if dsn == "" || redisAddr == "" || amqpURL == "" {
			rt.Log.Warn("NETINV_PG_DSN/NETINV_REDIS_ADDR/NETINV_AMQP_URL not all set — idle skeleton mode")
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
		if err := mq.DeclareJobTopology(); err != nil {
			return err
		}

		keys, err := cryptox.LoadEnvMasterKey()
		if err != nil {
			return err // scheduler must decrypt credentials for dispatch (doc 20 §6)
		}

		lease := &leader.Lease{Client: rc, Key: "leader:scheduler", Log: rt.Log}
		defer lease.Release(context.WithoutCancel(ctx))

		sched := &app.Scheduler{
			Repo:      &colpg.ScheduleRepo{Pool: pool},
			Publisher: &amqpadapter.JobPublisher{Client: mq},
			Secrets:   &secrets.Resolver{Vault: &invpg.EnvelopeVault{Pool: pool, Keys: keys}},
			Leader:    lease,
			Log:       rt.Log,
		}
		rt.Health.SetReady(true)
		rt.Log.Info("scheduler running")
		return sched.Run(ctx)
	})
}
