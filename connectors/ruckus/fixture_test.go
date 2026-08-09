package ruckus

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// These run against a walk recorded from the pilot's R710 (Unleashed
// 200.15.6.212), redacted at capture time. The inline fixtures in
// ruckus_test.go encode what the connector is meant to do; this one encodes
// what the device actually said.

func fixture(t *testing.T) *sdktest.Session {
	t.Helper()
	return sdktest.Load(t, "testdata/ruckus.snmpwalk")
}

func TestInventoryFromRecordedWalk(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectInventory: %v", err)
	}
	if snap.Vendor != "Ruckus" {
		t.Errorf("vendor = %q, want Ruckus", snap.Vendor)
	}
	if snap.Model != "R710" {
		t.Errorf("model = %q, want R710 — read from the vendor MIB, not sysDescr", snap.Model)
	}
	// The recorded firmware is "200.15.6.212 build 27". Matching on a prefix
	// keeps the assertion about parsing rather than about this exact build.
	if len(snap.OSVersion) < 5 || snap.OSVersion[:3] != "200" {
		t.Errorf("firmware = %q, want the Unleashed version string", snap.OSVersion)
	}
	// Redacted in the fixture, but it must still be *read* — a connector that
	// silently stopped populating serial would pass a test that only checked
	// for a specific value.
	if snap.Serial == "" {
		t.Error("serial is empty; the connector is not reading the serial column")
	}
}

func TestWirelessCountsFromRecordedWalk(t *testing.T) {
	samples, err := New().CollectHealth(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectHealth: %v", err)
	}
	get := func(name string) (float64, bool) {
		for _, s := range samples {
			if s.Name == name {
				return s.Value, true
			}
		}
		return 0, false
	}
	// Two APs: the root plus one mesh-joined member, counted from the per-AP
	// table. That is structural — it is how the pilot estate is built — so it
	// is safe to pin.
	if v, ok := get("netinv_wireless_ap_total"); !ok || v != 2 {
		t.Errorf("ap_total = %v (found=%v), want 2 (root + one mesh member)", v, ok)
	}
	if v, ok := get("netinv_wireless_ap_up_count"); !ok || v != 2 {
		t.Errorf("ap_up_count = %v (found=%v), want 2 — both were up when recorded", v, ok)
	}

	// The client count is whatever the estate happened to be carrying at
	// capture time, and it moved between two recordings taken minutes apart.
	// Pinning the number would make the test a record of that moment; what is
	// worth asserting is that the connector reports the scalar the agent gave
	// it, so the expectation comes from the fixture itself.
	want, err := fixture(t).Get(context.Background(), []string{oidStatsNumClient})
	if err != nil || len(want) != 1 {
		t.Fatalf("fixture has no client-count scalar: %v", err)
	}
	expect, _ := sdkNum(want[0].Value)
	if v, ok := get("netinv_wireless_client_count"); !ok || v != expect {
		t.Errorf("client_count = %v (found=%v), want %v as recorded", v, ok, expect)
	}
}

// sdkNum mirrors sdk.Num for the one conversion this test needs, keeping the
// assertion independent of how the fixture loader chose to type the value.
func sdkNum(v any) (float64, bool) {
	switch x := v.(type) {
	case uint:
		return float64(x), true
	case uint64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// Doc 10 records that an R710 exposes no CPU, memory or temperature anywhere —
// not its own MIB, not UCD-SNMP, not HOST-RESOURCES — and that the connector
// therefore reports none rather than inventing them. That claim is only worth
// making if something checks it, and a recorded walk is the only thing that
// can: the absence is a property of the device, not of the code.
func TestR710ReportsNoHealthMetrics(t *testing.T) {
	f := fixture(t)
	for _, probe := range []struct{ name, root string }{
		{"UCD-SNMP", ".1.3.6.1.4.1.2021"},
		{"HOST-RESOURCES", ".1.3.6.1.2.1.25"},
		{"ENTITY-SENSOR", ".1.3.6.1.2.1.99"},
	} {
		if f.Has(probe.root) {
			t.Errorf("%s (%s) is present in the recorded walk — doc 10 says an "+
				"R710 exposes none, so either the device changed or the doc is wrong",
				probe.name, probe.root)
		}
	}

	samples, err := New().CollectHealth(context.Background(), f)
	if err != nil {
		t.Fatalf("CollectHealth: %v", err)
	}
	for _, s := range samples {
		switch s.Name {
		case "netinv_device_cpu_percent",
			"netinv_device_memory_percent",
			"netinv_sensor_temperature_celsius":
			t.Errorf("reported %s = %v; the device exposes no such reading, so "+
				"this value was invented somewhere", s.Name, s.Value)
		}
	}
}
