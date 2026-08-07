// netinv-api — REST API, authN/Z, RBAC, inventory, query proxy (doc 05 §2).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("api", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 3+: mount routers, connect PG/Redis/AMQP/VM, run migrations.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
