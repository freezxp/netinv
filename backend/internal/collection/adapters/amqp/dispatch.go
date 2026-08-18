package amqp

import (
	"context"
	"time"

	"github.com/freezxp/netinv/backend/internal/collection/app"
	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// SyncTarget is the minimal device info an on-demand dispatch needs; callers
// (cmd wiring) map their context's device type onto it.
type SyncTarget struct {
	DeviceID     string
	SiteID       string
	MgmtIP       string
	Port         int
	ConnectorID  string
	CredentialID string
}

// SyncDispatcher publishes on-demand sync jobs (FR-SYNC-01 manual trigger).
type SyncDispatcher struct {
	Client  *amqpx.Client
	Secrets app.SecretResolver
}

func (d *SyncDispatcher) DispatchSync(ctx context.Context, t SyncTarget) (string, error) {
	cred, err := d.Secrets.Resolve(ctx, t.CredentialID)
	if err != nil {
		return "", err
	}
	if _, err := d.Client.EnsureSiteQueue(t.SiteID); err != nil {
		return "", err
	}
	port := t.Port
	if port == 0 {
		port = 161
	}
	job := wire.PollJob{
		JobID: id.New("job"), DeviceID: t.DeviceID, SiteID: t.SiteID,
		Family: "sync", Trigger: "manual", MgmtIP: t.MgmtIP, Port: port,
		ConnectorID: t.ConnectorID, Cred: cred,
		ScheduledAt: time.Now().UTC(), TimeoutMS: 5000, Retries: 2,
	}
	if err := d.Client.PublishJSON(ctx, amqpx.JobsExchange,
		amqpx.SiteRouting(t.SiteID), job); err != nil {
		return "", err
	}
	return job.JobID, nil
}

// DiscoveryDispatcher publishes a subnet sweep to the site's poller queue
// (FR-SYNC-04). Sweeps share the site job queue with polls.
type DiscoveryDispatcher struct{ Client *amqpx.Client }

func (d *DiscoveryDispatcher) Dispatch(ctx context.Context, job wire.DiscoveryJob) error {
	if _, err := d.Client.EnsureSiteQueue(job.SiteID); err != nil {
		return err
	}
	return d.Client.PublishJSON(ctx, amqpx.JobsExchange,
		amqpx.SiteRouting(job.SiteID), job)
}
