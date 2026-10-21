package fortinet

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// Recorded from a FortiGate-VM on FortiOS v6.0.4 build0231 (sysObjectID
// .1.3.6.1.4.1.12356.101.1.60), redacted at capture time and by hand after it
// — see the footer of the walk. These tests pin what that device actually
// answered, which is the thing the unit tests in fortinet_test.go cannot do:
// they key their fake data by this package's own OID constants, so a constant
// naming the wrong column passes them regardless.

func fixture(t *testing.T) *sdktest.Session {
	t.Helper()
	return sdktest.Load(t, "testdata/fortinet.snmpwalk")
}

func health(t *testing.T) []sdk.Sample {
	t.Helper()
	out, err := New().CollectHealth(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectHealth: %v", err)
	}
	return out
}

func TestFortiGateVMMatchesOnSysObjectID(t *testing.T) {
	vars, err := fixture(t).Get(context.Background(), []string{".1.3.6.1.2.1.1.2.0"})
	if err != nil || len(vars) != 1 {
		t.Fatalf("fixture has no sysObjectID: %v", err)
	}
	sysOID, _ := vars[0].Value.(string)
	if s := New().Match(sdk.SysInfo{SysObjectID: sysOID}); s <= 0 {
		t.Errorf("sysObjectID %q did not match fortinet-fortios", sysOID)
	}
}

// The regression this fixture exists for. fgSysInfo.10 is fgSysLowMemCapacity,
// and on this device it equals the memory capacity in KB; the connector once
// read it as the 1-minute session setup rate and published four million new
// sessions per second. The rate is .11.
func TestSessionSetupRateIsNotLowMemoryCapacity(t *testing.T) {
	out := health(t)
	s, ok := find(out, "netinv_firewall_session_setup_rate", "", "")
	if !ok {
		t.Fatal("no session setup rate from a device that publishes fgSysSesRate1")
	}
	if s.Value != 693 {
		t.Errorf("session setup rate = %v, want 693 (fgSysSesRate1, .101.4.1.11.0)", s.Value)
	}
	if s.Value == 4063936 {
		t.Error("session setup rate is fgSysLowMemCapacity (.101.4.1.10.0) — the wrong column")
	}
}

func TestSystemGaugesFromRealWalk(t *testing.T) {
	out := health(t)
	for _, c := range []struct {
		name string
		want float64
	}{
		{"netinv_device_memory_percent", 64},
		{"netinv_device_memory_total_bytes", 4063936 * 1024},
		{"netinv_firewall_session_count", 72300},
		// fgSysCPUUsage reads 0 on this idle VM while fgProcessorTable reports
		// 2% per core. Both are what the device said; the system figure is the
		// one collected (doc 10 §5).
		{"netinv_device_cpu_percent", 0},
	} {
		s, ok := find(out, c.name, "", "")
		if !ok {
			t.Errorf("%s missing", c.name)
			continue
		}
		if s.Value != c.want {
			t.Errorf("%s = %v, want %v", c.name, s.Value, c.want)
		}
	}
	// FortiOS publishes no session ceiling (ADR-021), so none may appear.
	if _, ok := find(out, "netinv_firewall_session_max", "", ""); ok {
		t.Error("session max published by a platform that has no such object")
	}
}

// A FortiGate-VM reports fgHwSensorCount = 0 and has no sensor rows. That must
// produce no sensor samples and no error — it is the biggest difference between
// a VM and an appliance, not a fault.
func TestVMHasNoHardwareSensors(t *testing.T) {
	f := fixture(t)
	if f.Has(oidFgHwSensorName) || f.Has(oidFgHwSensorValue) {
		t.Fatal("fixture unexpectedly carries fgHwSensorEntTable rows; is this still the VM walk?")
	}
	for _, s := range health(t) {
		if s.Name == "netinv_sensor_temperature_celsius" || s.Name == "netinv_sensor_fan_rpm" {
			t.Errorf("sensor sample %s %v from a device with no sensors", s.Name, s.Labels)
		}
	}
}

func TestInventoryIdentityFromRealWalk(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectInventory: %v", err)
	}
	if snap.Vendor != "Fortinet" {
		t.Errorf("vendor = %q", snap.Vendor)
	}
	if snap.OSVersion != "v6.0.4,build0231,190107 (GA)" {
		t.Errorf("os version = %q, want fgSysVersion verbatim", snap.OSVersion)
	}
	if snap.Serial == "" {
		t.Error("serial empty; fnSysSerial is present in the walk")
	}
	if len(snap.Interfaces) != 8 {
		t.Errorf("interfaces = %d, want 8 (port1-7 plus ssl.root)", len(snap.Interfaces))
	}
}

// Every port on this device answers the 64-bit ifXTable counters, so no repair
// should be needed — a non-zero repair count here would mean the generic walk
// regressed, not that the device changed.
func TestInterfaceCountersNeedNoRepair(t *testing.T) {
	out, err := New().CollectInterfaces(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectInterfaces: %v", err)
	}
	octets := 0
	for _, s := range out {
		if s.Name == "netinv_if_in_octets_total" {
			octets++
		}
		if s.Name == "netinv_if_counters_repaired" && s.Value != 0 {
			t.Errorf("counters repaired = %v, want 0", s.Value)
		}
	}
	if octets != 8 {
		t.Errorf("in-octet series = %d, want 8", octets)
	}
}
