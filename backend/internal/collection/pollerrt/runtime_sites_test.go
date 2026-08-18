package pollerrt

import (
	"context"
	"testing"
)

func TestAllSitesTokenIsExactAndAlone(t *testing.T) {
	// Fields rather than a Runtime value: Runtime carries atomic counters, and
	// ranging over a table of them copies a lock.
	cases := []struct {
		name    string
		siteID  string
		siteIDs []string
		want    bool
	}{
		{"wildcard alone", "", []string{"*"}, true},
		{"wildcard as single SiteID", "*", nil, true},
		// A list containing the token is a configuration mistake, not a
		// request to serve everything: treating it as the wildcard would
		// silently widen what the operator asked for.
		{"wildcard among real sites", "", []string{"s_a", "*"}, false},
		{"explicit sites", "", []string{"s_a", "s_b"}, false},
		{"nothing configured", "", nil, false},
	}
	for _, c := range cases {
		rt := Runtime{SiteID: c.siteID, SiteIDs: c.siteIDs}
		if got := rt.allSites(); got != c.want {
			t.Errorf("%s: allSites() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReconcileSitesStartsAndStops(t *testing.T) {
	noop := func() {}
	running := map[string]context.CancelFunc{"s_a": noop, "s_b": noop}

	start, stop := reconcileSites(running, []string{"s_b", "s_c"})
	if len(start) != 1 || start[0] != "s_c" {
		t.Fatalf("start = %v, want [s_c]", start)
	}
	// Stopping matters as much as starting: a deleted site takes its queue
	// with it, and a consumer left pointing at a queue that no longer exists
	// reconnects forever, logging a failure that is not one.
	if len(stop) != 1 || stop[0] != "s_a" {
		t.Fatalf("stop = %v, want [s_a]", stop)
	}

	// Steady state must be a no-op, or every announcement churns consumers.
	start, stop = reconcileSites(running, []string{"s_a", "s_b"})
	if len(start) != 0 || len(stop) != 0 {
		t.Fatalf("steady state churned: start=%v stop=%v", start, stop)
	}
}

// An announcement carrying nothing must not tear the fleet down. It is far
// more likely to mean the scheduler could not read the site list than that
// every site was deleted at once, and a poller that stops consuming
// everything on one bad message is worse than one that keeps going.
func TestReconcileSitesIgnoresBlankEntries(t *testing.T) {
	noop := func() {}
	running := map[string]context.CancelFunc{"s_a": noop}
	start, stop := reconcileSites(running, []string{"", "s_a"})
	if len(start) != 0 {
		t.Fatalf("start = %v, want none — a blank entry is not a site", start)
	}
	if len(stop) != 0 {
		t.Fatalf("stop = %v, want none", stop)
	}
}
