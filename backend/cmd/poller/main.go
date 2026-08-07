// netinv-poller — site-local SNMP collection agent (doc 05 §2, doc 10).
// The only binary that imports connectors/registry (doc 13 rule 5).
package main

import (
	"context"
	"os"

	_ "github.com/freezxp/netinv/connectors/registry"

	"github.com/freezxp/netinv/backend/internal/collection/pollerrt"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("poller", func(ctx context.Context, rt *service.Runtime) error {
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		siteID := os.Getenv("NETINV_SITE_ID")
		if amqpURL == "" || siteID == "" {
			rt.Log.Warn("NETINV_AMQP_URL/NETINV_SITE_ID not set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		mq, err := amqpx.Connect(ctx, amqpURL)
		if err != nil {
			return err
		}
		defer mq.Close()

		pollerID := os.Getenv("NETINV_POLLER_ID")
		if pollerID == "" {
			pollerID = id.New("plr")
		}
		runtime := &pollerrt.Runtime{
			PollerID: pollerID, SiteID: siteID, Client: mq, Log: rt.Log,
		}
		rt.Health.SetReady(true)
		rt.Log.Info("poller running", "site", siteID, "poller_id", pollerID)
		return runtime.Run(ctx)
	})
}
