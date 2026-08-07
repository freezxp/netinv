// netinv-api — REST API, authN/Z, RBAC, inventory, query proxy (doc 05 §2).
// Runs embedded migrations at boot when NETINV_PG_DSN is set (doc 19 §4).
package main

import (
	"context"
	"os"

	"github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("api", func(ctx context.Context, rt *service.Runtime) error {
		if dsn := os.Getenv("NETINV_PG_DSN"); dsn != "" {
			if err := pgx.Migrate(ctx, dsn, rt.Log); err != nil {
				return err
			}
			rt.Log.Info("database migrations up to date")
		} else {
			rt.Log.Warn("NETINV_PG_DSN not set — running without database (skeleton mode)")
		}
		// Sprint 3+: mount routers, connect Redis/AMQP/VM, wire contexts.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
