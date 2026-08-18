package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type stubRepo struct {
	due   []domain.DueSchedule
	sites []string
}

func (r *stubRepo) Due(context.Context, time.Time, int) ([]domain.DueSchedule, error) {
	return r.due, nil
}
func (r *stubRepo) ActiveSites(context.Context) ([]string, error) { return r.sites, nil }

type stubPublisher struct {
	mu        sync.Mutex
	consumers int
	declares  int
	published int
}

func (p *stubPublisher) EnsureSiteQueue(string) (domain.SiteQueueState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.declares++
	return domain.SiteQueueState{Consumers: p.consumers, Queued: 7}, nil
}

func (p *stubPublisher) Publish(context.Context, string, wire.PollJob) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published++
	return nil
}

func (p *stubPublisher) setConsumers(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consumers = n
}

func (p *stubPublisher) counts() (declares, published int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.declares, p.published
}

type stubSites struct {
	mu        sync.Mutex
	published [][]string
}

func (p *stubSites) PublishSites(_ context.Context, sites []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, sites)
	return nil
}

func (p *stubSites) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

type stubSecrets struct{}

func (stubSecrets) Resolve(context.Context, string) (wire.SNMPCred, error) {
	return wire.SNMPCred{Version: "v2c", Community: "public"}, nil
}

type stubLeader struct{}

func (stubLeader) TryAcquire(context.Context) bool { return true }

type recordedSite struct {
	siteID string
	state  domain.SiteQueueState
}

type stubHealth struct {
	mu   sync.Mutex
	rows []recordedSite
}

func (h *stubHealth) RecordSiteQueue(_ context.Context, siteID string,
	st domain.SiteQueueState) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rows = append(h.rows, recordedSite{siteID, st})
	return nil
}

func (h *stubHealth) snapshot() []recordedSite {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedSite(nil), h.rows...)
}

func runScheduler(t *testing.T, s *Scheduler, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("scheduler run: %v", err)
	}
}

// A site whose queue nobody consumes is the quietest fault in the system: the
// scheduler declares the queue itself, so the publish succeeds and the jobs
// accumulate unread. Nothing fails, so nothing is logged by any other path —
// which is why the consumer count from the declare has to be both recorded and
// said out loud.
func TestSchedulerReportsSiteWithNoConsumer(t *testing.T) {
	var logBuf bytes.Buffer
	pub := &stubPublisher{consumers: 0}
	health := &stubHealth{}
	s := &Scheduler{
		Repo: &stubRepo{due: []domain.DueSchedule{{
			DeviceID: "d_1", SiteID: "s_lonely", Family: domain.FamilySync,
			MgmtIP: "10.0.0.1", Port: 161, ConnectorID: "juniper-junos",
			CredentialID: "cr_1",
		}}},
		Publisher:  pub,
		Secrets:    stubSecrets{},
		Leader:     stubLeader{},
		SiteHealth: health,
		Log:        slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Tick:       5 * time.Millisecond,
	}
	runScheduler(t, s, 120*time.Millisecond)

	rows := health.snapshot()
	if len(rows) == 0 {
		t.Fatal("no site queue state recorded")
	}
	if rows[0].siteID != "s_lonely" || rows[0].state.Consumers != 0 || rows[0].state.Queued != 7 {
		t.Fatalf("recorded %+v, want s_lonely with 0 consumers and 7 queued", rows[0])
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "no poller consuming") {
		t.Fatalf("nothing warned about the unserved site; log was:\n%s", logs)
	}
	// The line names the state on transition, not once per tick — an unserved
	// site must not produce a log line every minute forever.
	if n := strings.Count(logs, "no poller consuming"); n != 1 {
		t.Fatalf("warned %d times, want exactly 1 (on the transition)", n)
	}
	// Jobs still go out. A site with no consumer today may have one in a
	// minute, and the 15-minute queue TTL is what bounds the backlog.
	if _, published := pub.counts(); published == 0 {
		t.Fatal("no jobs published — the scheduler stopped dispatching")
	}
}

// The declare is cheap but not free: re-checking every site on every 10s tick
// would multiply broker round-trips by the fleet's site count for a signal
// that changes on the timescale of a poller restart.
func TestSchedulerThrottlesQueueRechecks(t *testing.T) {
	pub := &stubPublisher{consumers: 1}
	s := &Scheduler{
		Repo: &stubRepo{due: []domain.DueSchedule{
			{DeviceID: "d_1", SiteID: "s_a", Family: domain.FamilyICMP, Port: 161},
			{DeviceID: "d_2", SiteID: "s_a", Family: domain.FamilyTraffic, Port: 161},
		}},
		Publisher: pub,
		Secrets:   stubSecrets{},
		Leader:    stubLeader{},
		Log:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Tick:      2 * time.Millisecond,
	}
	runScheduler(t, s, 150*time.Millisecond)

	declares, published := pub.counts()
	if declares != 1 {
		t.Fatalf("declared the site queue %d times over ~75 ticks, want 1 "+
			"(the recheck interval is a minute)", declares)
	}
	if published < 4 {
		t.Fatalf("published %d jobs, expected the tick loop to keep dispatching", published)
	}
}

// A scheduler without a database must still dispatch: SiteHealth is optional
// so the loop is runnable in isolation.
func TestSchedulerRunsWithoutSiteHealth(t *testing.T) {
	pub := &stubPublisher{consumers: 0}
	s := &Scheduler{
		Repo: &stubRepo{due: []domain.DueSchedule{
			{DeviceID: "d_1", SiteID: "s_a", Family: domain.FamilyICMP, Port: 161},
		}},
		Publisher: pub,
		Secrets:   stubSecrets{},
		Leader:    stubLeader{},
		Log:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Tick:      5 * time.Millisecond,
	}
	runScheduler(t, s, 60*time.Millisecond)
	if _, published := pub.counts(); published == 0 {
		t.Fatal("no jobs published without a SiteHealth repo")
	}
}

// The site list is what lets a poller serve a site nobody configured it with.
// It is republished on a timer rather than on change: a poller that missed an
// announcement, or started after the last one, has to converge without anyone
// creating a site to trigger it.
func TestSchedulerAnnouncesTheActiveSiteList(t *testing.T) {
	sites := &stubSites{}
	s := &Scheduler{
		Repo:      &stubRepo{due: nil, sites: []string{"s_a", "s_b"}},
		Publisher: &stubPublisher{consumers: 1},
		Secrets:   stubSecrets{},
		Leader:    stubLeader{},
		Sites:     sites,
		Log:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Tick:      5 * time.Millisecond,
	}
	runScheduler(t, s, 80*time.Millisecond)

	if sites.count() == 0 {
		t.Fatal("scheduler never announced the site list")
	}
	got := sites.published[0]
	if len(got) != 2 || got[0] != "s_a" || got[1] != "s_b" {
		t.Fatalf("announced %v, want the active sites", got)
	}
	// Announcing every tick would be needless traffic; the interval throttles
	// it well below the 5ms tick this test runs at.
	if sites.count() > 2 {
		t.Fatalf("announced %d times in 80ms — the interval is not being honoured",
			sites.count())
	}
}
