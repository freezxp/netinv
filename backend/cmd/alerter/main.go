// netinv-alerter — rule evaluation and alert lifecycle, leader-elected (doc 05 §2).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("alerter", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 9: evaluation loop, lifecycle transitions, event publishing.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
