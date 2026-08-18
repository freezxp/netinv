package pollerrt

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// A probe that could not run must say so rather than say nothing. Writing no
// sample left `max_over_time(netinv_icmp_up[3m]) == 0` with an empty
// lookbehind, which produces no series, so the availability rule silently
// stopped evaluating instead of firing — the failure mode where the poller
// cannot open an ICMP socket at all looked identical to a healthy quiet fleet.
func TestProbeICMPReportsAFailedProbeWithoutClaimingTheDeviceIsDown(t *testing.T) {
	// A cancelled context fails the run without needing a network, and without
	// depending on whether this machine can reach anything.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// TEST-NET-1 (RFC 5737): never routable, so nothing here depends on the
	// host's connectivity even if the context race were lost.
	samples, err := probeICMP(ctx, wire.PollJob{
		DeviceID: "d_probe", MgmtIP: "192.0.2.1", Family: "icmp",
	}, false)
	if err == nil {
		t.Fatal("probe reported success on a cancelled context")
	}
	byName := map[string]float64{}
	for _, s := range samples {
		byName[s.Name] = s.Value
		if s.DeviceID != "d_probe" {
			t.Errorf("sample %s carries device %q", s.Name, s.DeviceID)
		}
	}
	if got, ok := byName["netinv_icmp_probe_error"]; !ok || got != 1 {
		t.Fatalf("netinv_icmp_probe_error = %v (present=%v), want 1", got, ok)
	}
	// The poller does not know whether the device is up. Reporting it down
	// because we failed to ask would page someone to healthy equipment.
	if _, ok := byName["netinv_icmp_up"]; ok {
		t.Error("a failed probe reported netinv_icmp_up — it cannot know that")
	}
	if _, ok := byName["netinv_icmp_loss_ratio"]; ok {
		t.Error("a failed probe reported a loss ratio it never measured")
	}
}
