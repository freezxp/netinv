package postgres

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// no_consumer_since answers "since when", not "is it bad now". The instant is
// what makes the state actionable — an operator needs to know whether the site
// lost its poller a minute ago or two days ago — so it must survive every
// later zero observation and reset the moment a consumer appears.
func TestRecordSiteQueueTracksWhenTheConsumerWentAway(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	repo := &PollerRepo{Pool: pool}

	since := func() *string {
		var s *string
		if err := pool.QueryRow(ctx,
			`SELECT no_consumer_since::text FROM platform.site_collection_health
			 WHERE site_id = 's_default'`).Scan(&s); err != nil {
			t.Fatalf("read row: %v", err)
		}
		return s
	}

	// Healthy: a consumer is present, so there is no "since".
	if err := repo.RecordSiteQueue(ctx, "s_default",
		domain.SiteQueueState{Consumers: 1, Queued: 0}); err != nil {
		t.Fatalf("record served: %v", err)
	}
	if s := since(); s != nil {
		t.Fatalf("no_consumer_since is %q on a served site", *s)
	}

	// The poller goes away.
	if err := repo.RecordSiteQueue(ctx, "s_default",
		domain.SiteQueueState{Consumers: 0, Queued: 12}); err != nil {
		t.Fatalf("record unserved: %v", err)
	}
	first := since()
	if first == nil {
		t.Fatal("no_consumer_since not set when the consumer count hit zero")
	}

	// Still gone a while later: the queue keeps growing but the instant the
	// fault began must not move, or "since" becomes "as of the last check".
	if err := repo.RecordSiteQueue(ctx, "s_default",
		domain.SiteQueueState{Consumers: 0, Queued: 480}); err != nil {
		t.Fatalf("record still unserved: %v", err)
	}
	if again := since(); again == nil || *again != *first {
		got := "nil"
		if again != nil {
			got = *again
		}
		t.Fatalf("no_consumer_since moved to %s, want it pinned at %s", got, *first)
	}
	var queued int
	if err := pool.QueryRow(ctx,
		`SELECT queued FROM platform.site_collection_health WHERE site_id='s_default'`).
		Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 480 {
		t.Fatalf("queued is %d, want the latest observation 480", queued)
	}

	// A poller comes back.
	if err := repo.RecordSiteQueue(ctx, "s_default",
		domain.SiteQueueState{Consumers: 2, Queued: 0}); err != nil {
		t.Fatalf("record recovered: %v", err)
	}
	if s := since(); s != nil {
		t.Fatalf("no_consumer_since is still %q after a consumer returned", *s)
	}
}
