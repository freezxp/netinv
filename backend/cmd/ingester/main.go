// netinv-ingester — metric batch validation, enrichment, VM writes (doc 05 §5).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("ingester", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 6: metrics.raw consumer, enrichment snapshot, VM writer.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
