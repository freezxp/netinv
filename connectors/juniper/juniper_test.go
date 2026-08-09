package juniper

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

func find(ss []sdk.Sample, name, labelK, labelV string) (float64, bool) {
	for _, s := range ss {
		if s.Name == name && (labelK == "" || s.Labels[labelK] == labelV) {
			return s.Value, true
		}
	}
	return 0, false
}

func count(ss []sdk.Sample, name string) int {
	n := 0
	for _, s := range ss {
		if s.Name == name {
			n++
		}
	}
	return n
}

// jnxOperatingTable is indexed per FRU, so the readable name is what makes a
// graph legible — "Routing Engine 0" rather than "9.1.0.0".
func TestCollectHealthLabelsSensorsByFRUName(t *testing.T) {
	got := samples(t, map[string]any{
		oidJnxOperatingDescr + ".9.1.0.0":  []byte("Routing Engine 0"),
		oidJnxOperatingCPU + ".9.1.0.0":    uint64(31),
		oidJnxOperatingTemp + ".9.1.0.0":   uint64(44),
		oidJnxOperatingDescr + ".7.1.0.0":  "FPC: EX4300 @ 0/*/*",
		oidJnxOperatingTemp + ".7.1.0.0":   uint64(52),
		oidJnxOperatingBuffer + ".9.1.0.0": uint64(62),
	})

	if v, ok := find(got, "netinv_device_cpu_percent", "cpu", "Routing Engine 0"); !ok || v != 31 {
		t.Errorf("RE0 cpu = %v (found=%v), want 31", v, ok)
	}
	if v, ok := find(got, "netinv_sensor_temperature_celsius", "sensor", "Routing Engine 0"); !ok || v != 44 {
		t.Errorf("RE0 temperature = %v (found=%v), want 44", v, ok)
	}
	// gosnmp hands octet strings back as []byte, but a fixture or a future
	// transport may present the same value as a string; both are the same FRU.
	if v, ok := find(got, "netinv_sensor_temperature_celsius", "sensor", "FPC: EX4300 @ 0/*/*"); !ok || v != 52 {
		t.Errorf("FPC temperature = %v (found=%v), want 52 — string descr not handled?", v, ok)
	}
	if v, ok := find(got, "netinv_device_memory_percent", "", ""); !ok || v != 62 {
		t.Errorf("memory%% = %v (found=%v), want 62", v, ok)
	}
}

// A chassis whose descr walk is empty or truncated still reports readings, and
// those must stay attributable. An empty label silently merges every FRU's
// series into one, which reads as a single wildly fluctuating CPU.
func TestCollectHealthFallsBackToIndexWhenUnnamed(t *testing.T) {
	got := samples(t, map[string]any{
		oidJnxOperatingCPU + ".9.1.0.0":  uint64(31),
		oidJnxOperatingCPU + ".9.2.0.0":  uint64(77),
		oidJnxOperatingTemp + ".9.1.0.0": uint64(44),
	})

	if _, ok := find(got, "netinv_device_cpu_percent", "cpu", ""); ok {
		t.Error("a CPU sample carries an empty label; unnamed FRUs collapse into one series")
	}
	if v, ok := find(got, "netinv_device_cpu_percent", "cpu", "9.2.0.0"); !ok || v != 77 {
		t.Errorf("unnamed FRU cpu = %v (found=%v), want 77 labelled by index", v, ok)
	}
	if v, ok := find(got, "netinv_sensor_temperature_celsius", "sensor", "9.1.0.0"); !ok || v != 44 {
		t.Errorf("unnamed FRU temperature = %v (found=%v), want 44 labelled by index", v, ok)
	}
}

// Junos answers 0 for FRUs that have no CPU or no thermal probe — a line card
// slot, a fan tray. Zero is not a reading.
func TestCollectHealthDropsZeroReadings(t *testing.T) {
	got := samples(t, map[string]any{
		oidJnxOperatingDescr + ".1.1.0.0": []byte("Power Supply 0"),
		oidJnxOperatingCPU + ".1.1.0.0":   uint64(0),
		oidJnxOperatingTemp + ".1.1.0.0":  uint64(0),
	})
	if n := count(got, "netinv_device_cpu_percent"); n != 0 {
		t.Errorf("cpu samples = %d, want 0 — a PSU has no CPU", n)
	}
	if n := count(got, "netinv_sensor_temperature_celsius"); n != 0 {
		t.Errorf("temperature samples = %d, want 0", n)
	}
	if n := count(got, "netinv_device_memory_percent"); n != 0 {
		t.Errorf("memory samples = %d, want 0 when nothing reported buffer usage", n)
	}
}

// An absurdly long descr is a malformed or hostile agent response; it must not
// become a metric label, where it would land in the time-series index.
func TestCollectHealthRejectsOversizedNames(t *testing.T) {
	long := strings.Repeat("A", 200)
	got := samples(t, map[string]any{
		oidJnxOperatingDescr + ".9.1.0.0": []byte(long),
		oidJnxOperatingCPU + ".9.1.0.0":   uint64(31),
	})
	if _, ok := find(got, "netinv_device_cpu_percent", "cpu", long); ok {
		t.Error("a 200-byte FRU descr was used as a label")
	}
	if v, ok := find(got, "netinv_device_cpu_percent", "cpu", "9.1.0.0"); !ok || v != 31 {
		t.Errorf("cpu = %v (found=%v), want 31 labelled by index instead", v, ok)
	}
}

func TestMatchesJuniperEnterpriseOID(t *testing.T) {
	c := New()
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.2636.1.1.1.2.29"}); s <= 0 {
		t.Errorf("score for a Juniper sysObjectID = %d, want > 0", s)
	}
	// .2636 is Juniper; .2011 is Huawei. One digit apart in the arc, entirely
	// different MIBs behind it.
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.2011.2.240.12"}); s != 0 {
		t.Errorf("score for a Huawei sysObjectID = %d, want 0", s)
	}
}

func TestDeclaresHealthCapability(t *testing.T) {
	var health bool
	for _, c := range New().Capabilities() {
		if c == sdk.CapHealth {
			health = true
		}
	}
	if !health {
		t.Error("connector implements CollectHealth but does not declare CapHealth")
	}
	if id := New().Info().ID; id != "juniper-junos" {
		t.Errorf("connector ID = %q; it is a database key and must not drift", id)
	}
}
