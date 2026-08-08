// netinv-poller — site-local SNMP/ICMP collection agent (doc 05 §2, doc 10).
// The only binary that imports connectors/registry (doc 13 rule 5).
//
// Identity comes from enrollment (FR-PLT-02): first boot presents
// NETINV_ENROLL_TOKEN to the core API and persists the returned identity in
// NETINV_STATE_DIR. NETINV_SITE_ID alone still works for dev (no control
// plane, no heartbeats).
package main

import (
	"context"
	"os"
	"strings"

	_ "github.com/freezxp/netinv/connectors/registry"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/collection/pollerrt"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/logx"
	"github.com/freezxp/netinv/backend/internal/platform/service"
)

func main() {
	service.Run("poller", func(ctx context.Context, rt *service.Runtime) error {
		amqpURL := os.Getenv("NETINV_AMQP_URL")
		if amqpURL == "" {
			rt.Log.Warn("NETINV_AMQP_URL not set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}

		stateDir := os.Getenv("NETINV_STATE_DIR")
		if stateDir == "" {
			stateDir = "/var/lib/netinv-poller"
		}

		// Comma-separated: one host often covers several logical sites, and a
		// site with no consumer is never polled at all — its jobs pile up in a
		// queue nobody reads, with nothing else reporting a problem.
		var siteIDs []string
		for _, part := range strings.Split(os.Getenv("NETINV_SITE_ID"), ",") {
			if p := strings.TrimSpace(part); p != "" {
				siteIDs = append(siteIDs, p)
			}
		}
		siteID := ""
		if len(siteIDs) > 0 {
			siteID = siteIDs[0]
		}
		pollerID := os.Getenv("NETINV_POLLER_ID")
		var agent *pollerrt.Agent
		if apiURL := os.Getenv("NETINV_API_URL"); apiURL != "" {
			agent = &pollerrt.Agent{APIURL: apiURL, StateDir: stateDir, Log: rt.Log}
			if err := agent.Enroll(ctx, os.Getenv("NETINV_ENROLL_TOKEN"), logx.Version); err != nil {
				return err
			}
			pollerID, siteID = agent.Identity.PollerID, agent.Identity.SiteID
			siteIDs = []string{siteID} // an enrolled poller serves its own site
		}
		if siteID == "" {
			rt.Log.Warn("no site identity (set NETINV_API_URL+NETINV_ENROLL_TOKEN or NETINV_SITE_ID) — idle")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		if pollerID == "" {
			pollerID = id.New("plr")
		}

		mq, err := amqpx.Connect(ctx, amqpURL)
		if err != nil {
			return err
		}
		defer mq.Close()

		buffer, err := pollerrt.NewDiskBuffer(stateDir + "/buffer")
		if err != nil {
			return err
		}
		runtime := &pollerrt.Runtime{
			PollerID: pollerID, SiteID: siteID, SiteIDs: siteIDs,
			Client: mq, Log: rt.Log,
			Buffer:         buffer,
			ICMPPrivileged: os.Getenv("NETINV_ICMP_PRIVILEGED") == "1",
		}
		if agent != nil {
			go agent.HeartbeatLoop(ctx, logx.Version, func() domain.HeartbeatStats {
				ok, failed, batches, bufDepth, bufBytes := runtime.Stats()
				return domain.HeartbeatStats{
					PollsOK: ok, PollsFailed: failed, Batches: batches,
					BufferDepth: bufDepth, BufferBytes: bufBytes,
					Workers: runtime.Workers,
				}
			})
		}
		rt.Health.SetReady(true)
		rt.Log.Info("poller running", "sites", siteIDs, "poller_id", pollerID)
		return runtime.Run(ctx)
	})
}
