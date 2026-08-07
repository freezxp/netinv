// netinv-notifier — email/webhook/Slack delivery with retries (doc 05 §2).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("notifier", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 9: notify.dispatch consumers, channel senders, delivery log.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
