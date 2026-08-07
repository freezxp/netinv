// netinv-scheduler — poll/sync job fan-out, leader-elected (doc 05 §2, §5).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("scheduler", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 5: schedule scan loop, leader election, job publishing.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
