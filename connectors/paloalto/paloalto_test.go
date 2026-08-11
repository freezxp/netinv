package paloalto

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
)

type fakeSession struct{ data map[string]any }

func (f *fakeSession) Get(_ context.Context, oids []string) ([]sdk.Var, error) {
	var out []sdk.Var
	for _, oid := range oids {
		if v, ok := f.data[oid]; ok {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}

func (f *fakeSession) Walk(_ context.Context, root string) ([]sdk.Var, error) {
	var out []sdk.Var
	for oid, v := range f.data {
		if strings.HasPrefix(oid, root+".") {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}

func (f *fakeSession) Target() sdk.TargetMeta { return sdk.TargetMeta{} }

func samples(t *testing.T, data map[string]any) []sdk.Sample {
	t.Helper()
	out, err := New().CollectHealth(context.Background(), &fakeSession{data: data})
	if err != nil {
		t.Fatalf("CollectHealth: %v", err)
	}
	return out
}

func find(out []sdk.Sample, name string) (sdk.Sample, bool) {
	for _, s := range out {
		if s.Name == name {
			return s, true
		}
	}
	return sdk.Sample{}, false
}

func count(out []sdk.Sample, name string) int {
	n := 0
	for _, s := range out {
		if s.Name == name {
			n++
		}
	}
	return n
}

func TestMatchesPaloAltoEnterpriseTree(t *testing.T) {
	c := New()
	if c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.25461.2.3.19"}) == 0 {
		t.Error("a PA-series sysObjectID must match")
	}
	if got := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.12356.101.1.1"}); got != 0 {
		t.Errorf("a Fortinet sysObjectID matched paloalto with score %d", got)
	}
}

func TestSessionGaugesIncludeTheCeiling(t *testing.T) {
	out := samples(t, map[string]any{
		oidPanSessionActive: int64(120_000),
		oidPanSessionMax:    int64(250_000),
	})
	if s, ok := find(out, "netinv_firewall_session_count"); !ok || s.Value != 120_000 {
		t.Errorf("session count = %v (found %v)", s.Value, ok)
	}
	// Unlike FortiOS, PAN-OS publishes a ceiling, which is what makes session
	// utilization answerable as a query rather than a stored number.
	if s, ok := find(out, "netinv_firewall_session_max"); !ok || s.Value != 250_000 {
		t.Errorf("session max = %v (found %v)", s.Value, ok)
	}
}

// Emitting the same metric name both with and without a protocol label would
// double every session under any sum(). The per-protocol counts PAN-OS offers
// are deliberately not collected; this pins that.
func TestSessionCountIsPublishedExactlyOnceAndUnlabelled(t *testing.T) {
	out := samples(t, map[string]any{
		oidPanSessionActive: int64(120_000),
		oidPanSessionMax:    int64(250_000),
		// Present on a real device, and must be ignored:
		".1.3.6.1.4.1.25461.2.1.2.3.4.0": int64(90_000), // tcp
		".1.3.6.1.4.1.25461.2.1.2.3.5.0": int64(25_000), // udp
		".1.3.6.1.4.1.25461.2.1.2.3.6.0": int64(5_000),  // icmp
	})
	if n := count(out, "netinv_firewall_session_count"); n != 1 {
		t.Errorf("session count published %d times, want exactly 1", n)
	}
	s, _ := find(out, "netinv_firewall_session_count")
	if len(s.Labels) != 0 {
		t.Errorf("session count carries labels %v; a labelled and an unlabelled "+
			"series under one name double-count under sum()", s.Labels)
	}
}

func TestCPUComesFromHostResourcesPerCore(t *testing.T) {
	out := samples(t, map[string]any{
		oidHrProcessorLoad + ".1": int64(12),
		oidHrProcessorLoad + ".2": int64(44),
	})
	if n := count(out, "netinv_device_cpu_percent"); n != 2 {
		t.Errorf("got %d cpu samples, want one per processor", n)
	}
	for _, s := range out {
		if s.Name == "netinv_device_cpu_percent" && s.Labels["cpu"] == "" {
			t.Error("a per-core CPU sample has no cpu label to tell it apart")
		}
	}
}

// hrStorage carries RAM, swap and every mounted filesystem in one table.
// Taking the wrong row reports a firewall's disk as its memory.
func TestMemoryTakesTheRAMRowNotTheDisk(t *testing.T) {
	const typeCol = ".1.3.6.1.2.1.25.2.3.1.2"
	out := samples(t, map[string]any{
		// Row 1: a large disk that must be ignored.
		typeCol + ".1":           ".1.3.6.1.2.1.25.2.1.4",
		oidHrStorageDescr + ".1": "/dev/root",
		oidHrStorageUnits + ".1": int64(4096),
		oidHrStorageSize + ".1":  int64(20_000_000),
		oidHrStorageUsed + ".1":  int64(10_000_000),
		// Row 2: physical memory.
		typeCol + ".2":           ".1.3.6.1.2.1.25.2.1.2",
		oidHrStorageDescr + ".2": "Physical memory",
		oidHrStorageUnits + ".2": int64(1024),
		oidHrStorageSize + ".2":  int64(8_000_000),
		oidHrStorageUsed + ".2":  int64(6_000_000),
	})

	s, ok := find(out, "netinv_device_memory_total_bytes")
	if !ok || s.Value != 8_000_000*1024 {
		t.Errorf("memory total = %v, want the RAM row (8 GB), not the disk", s.Value)
	}
	if s, _ := find(out, "netinv_device_memory_used_bytes"); s.Value != 6_000_000*1024 {
		t.Errorf("memory used = %v", s.Value)
	}
	if s, _ := find(out, "netinv_device_memory_percent"); s.Value != 75 {
		t.Errorf("memory percent = %v, want 75", s.Value)
	}
	if n := count(out, "netinv_device_memory_total_bytes"); n != 1 {
		t.Errorf("memory total published %d times, want 1", n)
	}
}

// Agents differ in how they render hrStorageType; the description is the
// fallback that keeps memory working when the type is unusable.
func TestMemoryFallsBackToTheStorageDescription(t *testing.T) {
	out := samples(t, map[string]any{
		oidHrStorageDescr + ".1": "Physical memory",
		oidHrStorageUnits + ".1": int64(1024),
		oidHrStorageSize + ".1":  int64(4_000_000),
		oidHrStorageUsed + ".1":  int64(1_000_000),
	})
	if s, ok := find(out, "netinv_device_memory_total_bytes"); !ok || s.Value != 4_000_000*1024 {
		t.Errorf("memory total = %v (found %v)", s.Value, ok)
	}
}

func TestMissingSubtreesProduceNoSamplesNotAnError(t *testing.T) {
	if got := samples(t, map[string]any{}); len(got) != 0 {
		t.Errorf("got %d samples from a device answering nothing", len(got))
	}
}

func TestCollectInventoryAddsPaloAltoIdentity(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), &fakeSession{data: map[string]any{
		oidPanSerial:    "001901000000",
		oidPanSwVersion: "10.2.9-h1",
		oidPanHwVersion: "PA-3220",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Vendor != "Palo Alto Networks" {
		t.Errorf("vendor = %q", snap.Vendor)
	}
	if snap.Serial != "001901000000" || snap.OSVersion != "10.2.9-h1" {
		t.Errorf("identity = %q / %q", snap.Serial, snap.OSVersion)
	}
}

func TestDeclaresHealthCapability(t *testing.T) {
	var found bool
	for _, c := range New().Capabilities() {
		if c == sdk.CapHealth {
			found = true
		}
	}
	if !found {
		t.Error("the connector collects health but does not declare the capability")
	}
}
