package huawei

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

func TestCollectHealthPerEntity(t *testing.T) {
	got := samples(t, map[string]any{
		oidHwEntityCPU + ".9":   uint64(23),
		oidHwEntityCPU + ".17":  uint64(64),
		oidHwEntityMem + ".9":   uint64(41),
		oidHwEntityMem + ".17":  uint64(58),
		oidHwEntityTemp + ".9":  uint64(37),
		oidHwEntityTemp + ".17": uint64(45),
	})

	if v, ok := find(got, "netinv_device_cpu_percent", "cpu", "17"); !ok || v != 64 {
		t.Errorf("cpu of entity 17 = %v (found=%v), want 64", v, ok)
	}
	if v, ok := find(got, "netinv_sensor_temperature_celsius", "sensor", "9"); !ok || v != 37 {
		t.Errorf("temperature of entity 9 = %v (found=%v), want 37", v, ok)
	}
	// VRP reports memory per entity; the device's memory pressure is the worst
	// of them, not the last one walked. Map iteration is unordered, so a
	// last-wins bug would only show up intermittently in production.
	if v, ok := find(got, "netinv_device_memory_percent", "", ""); !ok || v != 58 {
		t.Errorf("memory%% = %v (found=%v), want 58 (the maximum)", v, ok)
	}
	if n := count(got, "netinv_device_memory_percent"); n != 1 {
		t.Errorf("memory samples = %d, want exactly 1 device-level sample", n)
	}
}

// Entities without a given sensor answer 0 rather than declining to answer, and
// a 0 published as a reading is indistinguishable from a genuinely idle CPU or
// a chassis at freezing point.
func TestCollectHealthDropsAbsentSensors(t *testing.T) {
	got := samples(t, map[string]any{
		oidHwEntityCPU + ".1":  uint64(0), // slot with no CPU
		oidHwEntityCPU + ".2":  uint64(12),
		oidHwEntityTemp + ".1": uint64(0),   // no sensor
		oidHwEntityTemp + ".2": uint64(250), // out of range, not a temperature
		oidHwEntityTemp + ".3": uint64(41),
	})

	if n := count(got, "netinv_device_cpu_percent"); n != 1 {
		t.Errorf("cpu samples = %d, want 1 (the zero-usage entity is not a CPU)", n)
	}
	if n := count(got, "netinv_sensor_temperature_celsius"); n != 1 {
		t.Errorf("temperature samples = %d, want 1 (0 and 250 are not readings)", n)
	}
	if v, _ := find(got, "netinv_sensor_temperature_celsius", "sensor", "3"); v != 41 {
		t.Errorf("surviving temperature = %v, want the 41 from entity 3", v)
	}
}

func TestCollectHealthEmptyAgent(t *testing.T) {
	got := samples(t, map[string]any{})
	if len(got) != 0 {
		t.Errorf("samples from a silent agent = %d, want none invented", len(got))
	}
}

func TestMatchesHuaweiEnterpriseOID(t *testing.T) {
	c := New()
	vrp := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.2011.2.240.12"})
	if vrp <= 0 {
		t.Errorf("score for a Huawei sysObjectID = %d, want > 0", vrp)
	}
	// .2011 is Huawei's enterprise arc; .9 is Cisco's. Matching a Cisco device
	// would hand it a connector that walks OIDs it does not implement.
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.9.1.1745"}); s != 0 {
		t.Errorf("score for a Cisco sysObjectID = %d, want 0", s)
	}
	if s := c.Match(sdk.SysInfo{}); s != 0 {
		t.Errorf("score for an empty sysObjectID = %d, want 0", s)
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
	if id := New().Info().ID; id != "huawei-vrp" {
		t.Errorf("connector ID = %q; it is a database key and must not drift", id)
	}
}
