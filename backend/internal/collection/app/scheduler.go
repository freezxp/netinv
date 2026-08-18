// Package app — Collection use cases. The scheduler owns time (ADR-013):
// leader-elected tick loop publishing due jobs to per-site queues (doc 05 §5).
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type ScheduleRepo interface {
	// Due claims up to limit due schedules and advances their next_due_at
	// (jittered) in the same statement, so a crashed leader never double-books.
	Due(ctx context.Context, now time.Time, limit int) ([]domain.DueSchedule, error)
	ActiveSites(ctx context.Context) ([]string, error)
}

type JobPublisher interface {
	// EnsureSiteQueue declares the site's queue and reports what the broker
	// says about it. The declare is the only place the consumer count is
	// available on the publish path.
	EnsureSiteQueue(siteID string) (domain.SiteQueueState, error)
	Publish(ctx context.Context, siteID string, job wire.PollJob) error
}

// SiteHealthRepo records what the broker reported about each site's queue, so
// the API can answer "is anything collecting for this site" without an AMQP
// connection of its own — see migration 0013 for why this is not a metric.
type SiteHealthRepo interface {
	RecordSiteQueue(ctx context.Context, siteID string, st domain.SiteQueueState) error
}

// SecretResolver turns a credential reference into the wire credential the
// poller needs — decrypted at dispatch time (doc 20 §6). Implemented by the
// inventory vault, wired in cmd (cross-context via interface, doc 13 rule 3).
type SecretResolver interface {
	Resolve(ctx context.Context, credentialID string) (wire.SNMPCred, error)
}

// Leader gates the loop to a single active scheduler (doc 05 §9).
type Leader interface {
	// TryAcquire returns true while this instance holds/renews the lease.
	TryAcquire(ctx context.Context) bool
}

type Scheduler struct {
	Repo      ScheduleRepo
	Publisher JobPublisher
	Secrets   SecretResolver
	Leader    Leader
	// SiteHealth persists the queue state observed at dispatch. Optional: nil
	// disables recording, which keeps the scheduler runnable without a database
	// in tests.
	SiteHealth SiteHealthRepo
	Log        *slog.Logger
	Tick       time.Duration // default 10s
	Batch      int           // max jobs per tick, default 5000
}

// siteQueueRecheck is how often a site's queue is re-declared to refresh its
// consumer count. The declare is cheap but not free, and a poller appearing or
// vanishing does not need to be noticed within one 10 s tick.
const siteQueueRecheck = time.Minute

func (s *Scheduler) Run(ctx context.Context) error {
	if s.Tick == 0 {
		s.Tick = 10 * time.Second
	}
	if s.Batch == 0 {
		s.Batch = 5000
	}
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	lastQueueCheck := map[string]time.Time{}
	// Previous consumer state per site, so the log fires on the transition
	// rather than once a minute forever.
	hadConsumers := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if !s.Leader.TryAcquire(ctx) {
			continue // standby replica
		}
		start := time.Now()
		due, err := s.Repo.Due(ctx, start.UTC(), s.Batch)
		if err != nil {
			s.Log.Error("schedule scan failed", "err", err)
			continue
		}
		published, failed := 0, 0
		for _, d := range due {
			// The queue was declared once and never looked at again, which is
			// exactly how a site with no poller stayed invisible: the declare
			// succeeds, the publish succeeds, and the jobs pile up unread.
			if time.Since(lastQueueCheck[d.SiteID]) >= siteQueueRecheck {
				st, err := s.Publisher.EnsureSiteQueue(d.SiteID)
				if err != nil {
					s.Log.Error("queue declare failed", "site", d.SiteID, "err", err)
					failed++
					continue
				}
				lastQueueCheck[d.SiteID] = time.Now()
				s.noteSiteQueue(ctx, d.SiteID, st, hadConsumers)
			}
			cred, err := s.Secrets.Resolve(ctx, d.CredentialID)
			if err != nil {
				s.Log.Warn("credential resolve failed", "device", d.DeviceID, "err", err)
				failed++
				continue
			}
			job := wire.PollJob{
				JobID: id.New("job"), DeviceID: d.DeviceID, SiteID: d.SiteID,
				Family: string(d.Family), MgmtIP: d.MgmtIP, Port: d.Port,
				ConnectorID: d.ConnectorID, Cred: cred, ScheduledAt: start.UTC(),
				IntervalS: d.IntervalS, TimeoutMS: d.TimeoutMS, Retries: d.Retries,
			}
			if err := s.Publisher.Publish(ctx, d.SiteID, job); err != nil {
				// next_due_at already advanced; the missed poll is skipped, not
				// queued unboundedly (NFR-19) — counted and logged instead.
				s.Log.Warn("job publish failed", "device", d.DeviceID,
					"family", d.Family, "err", err)
				failed++
				continue
			}
			published++
		}
		if published > 0 || failed > 0 {
			s.Log.Info("tick complete", "due", len(due), "published", published,
				"failed", failed, "dur_ms", time.Since(start).Milliseconds())
		}
	}
}

// noteSiteQueue records the queue state and logs the transition into and out of
// "no poller is reading this site". It logs on change only: a site that has been
// unserved for a week does not need a line a minute, and the one line that
// matters is the one naming when it started.
func (s *Scheduler) noteSiteQueue(ctx context.Context, siteID string,
	st domain.SiteQueueState, had map[string]bool) {
	if s.SiteHealth != nil {
		if err := s.SiteHealth.RecordSiteQueue(ctx, siteID, st); err != nil {
			s.Log.Warn("site queue health not recorded", "site", siteID, "err", err)
		}
	}
	served := st.Consumers > 0
	prev, seen := had[siteID]
	had[siteID] = served
	if seen && prev == served {
		return
	}
	if !served {
		// Not an error: nothing has failed, which is the whole problem with it.
		s.Log.Warn("site has no poller consuming its queue — jobs are being "+
			"queued and never executed; devices in this site will stay pending",
			"site", siteID, "queued", st.Queued)
		return
	}
	if seen {
		s.Log.Info("site queue has a consumer again", "site", siteID)
	}
}
