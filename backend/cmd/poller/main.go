// netinv-poller — site-local SNMP/ICMP collection agent (doc 05 §2, doc 10).
// The only binary that imports connectors/registry (doc 13 rule 5).
package main

import (
	"context"

	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("poller", func(ctx context.Context, rt *service.Runtime) error {
		// Sprint 6: worker pool, SNMP sessions, batcher, disk buffer.
		rt.Health.SetReady(true)
		<-ctx.Done()
		return nil
	})
}
