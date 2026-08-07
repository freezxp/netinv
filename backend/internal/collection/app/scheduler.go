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
	EnsureSiteQueue(siteID string) error
	Publish(ctx context.Context, siteID string, job wire.PollJob) error
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
	Log       *slog.Logger
	Tick      time.Duration // default 10s
	Batch     int           // max jobs per tick, default 5000
}

func (s *Scheduler) Run(ctx context.Context) error {
	if s.Tick == 0 {
		s.Tick = 10 * time.Second
	}
	if s.Batch == 0 {
		s.Batch = 5000
	}
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	knownQueues := map[string]bool{}
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
			if !knownQueues[d.SiteID] {
				if err := s.Publisher.EnsureSiteQueue(d.SiteID); err != nil {
					s.Log.Error("queue declare failed", "site", d.SiteID, "err", err)
					failed++
					continue
				}
				knownQueues[d.SiteID] = true
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
