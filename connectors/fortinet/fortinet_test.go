package fortinet

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

func find(out []sdk.Sample, name string, label, want string) (sdk.Sample, bool) {
	for _, s := range out {
		if s.Name != name {
			continue
		}
		if label == "" {
			return s, true
		}
		if s.Labels[label] == want {
			return s, true
		}
	}
	return sdk.Sample{}, false
}

func TestMatchesFortinetEnterpriseTree(t *testing.T) {
	c := New()
	if got := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.12356.101.1.1000"}); got == 0 {
		t.Error("a FortiGate sysObjectID must match")
	}
	if got := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.9.1.1"}); got != 0 {
		t.Errorf("a Cisco sysObjectID matched fortinet with score %d", got)
	}
}

func TestCollectHealthReadsSystemAndSessionGauges(t *testing.T) {
	out := samples(t, map[string]any{
		oidFgSysCPUUsage:    int64(37),
		oidFgSysMemUsage:    int64(64),
		oidFgSysMemCapacity: int64(2_000_000), // KB
		oidFgSysSesCount:    int64(18342),
		oidFgSysSesRate1:    int64(220),
	})

	if s, ok := find(out, "netinv_device_cpu_percent", "", ""); !ok || s.Value != 37 {
		t.Errorf("cpu = %v (found %v), want 37", s.Value, ok)
	}
	// FortiOS reports one system-wide CPU figure. Attaching a cpu label would
	// imply a per-core breakdown the device does not provide.
	if s, _ := find(out, "netinv_device_cpu_percent", "", ""); len(s.Labels) != 0 {
		t.Errorf("cpu sample carries labels %v, want none", s.Labels)
	}

	if s, ok := find(out, "netinv_device_memory_total_bytes", "", ""); !ok || s.Value != 2_000_000*1024 {
		t.Errorf("memory total = %v, want capacity in KB converted to bytes", s.Value)
	}
	// Used is derived from the percentage, since FortiOS publishes no used figure.
	if s, ok := find(out, "netinv_device_memory_used_bytes", "", ""); !ok ||
		s.Value != 2_000_000*1024*0.64 {
		t.Errorf("memory used = %v, want 64%% of capacity", s.Value)
	}

	if s, ok := find(out, "netinv_firewall_session_count", "", ""); !ok || s.Value != 18342 {
		t.Errorf("session count = %v, want 18342", s.Value)
	}
	if s, ok := find(out, "netinv_firewall_session_setup_rate", "", ""); !ok || s.Value != 220 {
		t.Errorf("session setup rate = %v, want 220", s.Value)
	}
	// FortiOS publishes no session ceiling, so none may be invented.
	if _, ok := find(out, "netinv_firewall_session_max", "", ""); ok {
		t.Error("a session ceiling was published; FortiOS does not report one")
	}
}

// A FortiGate VM answers the system group and nothing in the sensor table. It
// must produce the samples it can and no error — a connector that failed here
// would take a whole poll cycle down over a missing optional subtree.
func TestMissingSubtreesProduceNoSamplesNotAnError(t *testing.T) {
	out := samples(t, map[string]any{oidFgSysCPUUsage: int64(12)})
	if len(out) != 1 {
		t.Fatalf("got %d samples from a VM-shaped device, want just the CPU one", len(out))
	}
	if out[0].Name != "netinv_device_cpu_percent" {
		t.Errorf("unexpected sample %q", out[0].Name)
	}
	if len(samples(t, map[string]any{})) != 0 {
		t.Error("a device answering nothing must yield no samples")
	}
}

// One table carries temperatures, fans and voltages, told apart only by name.
// A fan at 8000 RPM charted as a temperature would read as a fire.
func TestSensorTableIsSplitByKind(t *testing.T) {
	out := samples(t, map[string]any{
		oidFgHwSensorName + ".1":  "CPU Temp",
		oidFgHwSensorValue + ".1": int64(54),
		oidFgHwSensorName + ".2":  "FAN1 Speed",
		oidFgHwSensorValue + ".2": int64(8100),
		oidFgHwSensorName + ".3":  "VCC3V3",
		oidFgHwSensorValue + ".3": int64(3),
	})

	if s, ok := find(out, "netinv_sensor_temperature_celsius", "sensor", "CPU Temp"); !ok || s.Value != 54 {
		t.Errorf("CPU Temp = %v (found %v), want 54 °C", s.Value, ok)
	}
	if s, ok := find(out, "netinv_sensor_fan_rpm", "sensor", "FAN1 Speed"); !ok || s.Value != 8100 {
		t.Errorf("FAN1 = %v (found %v), want 8100 rpm", s.Value, ok)
	}
	if _, ok := find(out, "netinv_sensor_temperature_celsius", "sensor", "FAN1 Speed"); ok {
		t.Error("a fan speed was published as a temperature")
	}
	// Voltages have no metric in the catalogue and must be dropped, not guessed at.
	for _, s := range out {
		if s.Labels["sensor"] == "VCC3V3" {
			t.Errorf("voltage sensor published as %q", s.Name)
		}
	}
}

// Values a sensor reports when it is absent must not become chart lines.
func TestImplausibleTemperaturesAreDropped(t *testing.T) {
	out := samples(t, map[string]any{
		oidFgHwSensorName + ".1":  "Ghost Temp",
		oidFgHwSensorValue + ".1": int64(3000),
	})
	if len(out) != 0 {
		t.Errorf("got %d samples for an out-of-range reading, want none", len(out))
	}
}

func TestCollectInventoryAddsFortinetIdentity(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), &fakeSession{data: map[string]any{
		oidFnSysSerial:  "FGT60FTK00000000",
		oidFgSysVersion: "v7.4.4,build2662,240902 (GA.F)",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Vendor != "Fortinet" {
		t.Errorf("vendor = %q", snap.Vendor)
	}
	if snap.Serial != "FGT60FTK00000000" {
		t.Errorf("serial = %q", snap.Serial)
	}
	if !strings.HasPrefix(snap.OSVersion, "v7.4.4") {
		t.Errorf("os version = %q", snap.OSVersion)
	}
	// The model is not derived from the serial. Fortinet serials are entirely
	// uppercase alphanumeric, so every attempt to split off a platform code
	// ends up returning the whole serial — putting a serial number in the
	// model column, which is both wrong and identifying.
	if snap.Model == snap.Serial && snap.Model != "" {
		t.Error("the serial leaked into the model column")
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
