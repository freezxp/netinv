package cisco

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

func TestCiscoHealth(t *testing.T) {
	s := &fakeSession{data: map[string]any{
		oidCpmCPU5min + ".1":  uint64(37),
		oidCpmCPU5min + ".2":  uint64(45),
		oidMemPoolUsed + ".1": uint64(300_000_000),
		oidMemPoolFree + ".1": uint64(700_000_000),
		oidEnvTempVal + ".1":  uint64(42),
		oidEnvFanState + ".1": 1,
		oidEnvPSUState + ".1": 3, // critical PSU
	}}
	samples, err := New().CollectHealth(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	get := func(name, labelK, labelV string) (float64, bool) {
		for _, sm := range samples {
			if sm.Name == name && (labelK == "" || sm.Labels[labelK] == labelV) {
				return sm.Value, true
			}
		}
		return 0, false
	}
	if v, ok := get("netinv_device_cpu_percent", "cpu", "2"); !ok || v != 45 {
		t.Errorf("cpu2 = %v", v)
	}
	if v, ok := get("netinv_device_memory_percent", "", ""); !ok || v != 30 {
		t.Errorf("memory%% = %v, want 30", v)
	}
	if v, ok := get("netinv_device_memory_total_bytes", "", ""); !ok || v != 1_000_000_000 {
		t.Errorf("mem total = %v", v)
	}
	if v, ok := get("netinv_sensor_temperature_celsius", "sensor", "1"); !ok || v != 42 {
		t.Errorf("temp = %v", v)
	}
	if v, ok := get("netinv_sensor_psu_status", "sensor", "1"); !ok || v != 3 {
		t.Errorf("psu = %v, want raw state 3", v)
	}
}

func TestMatchPrecedence(t *testing.T) {
	sys := sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.9.1.2137"}
	c, score := sdk.BestMatch(sys)
	if c == nil || c.Info().ID != "cisco-ios" {
		t.Fatalf("best match = %v (score %d), want cisco-ios", c, score)
	}
	// Unknown vendor falls to generic (universal floor).
	c, _ = sdk.BestMatch(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.99999.1"})
	if c == nil || c.Info().ID != "generic" {
		t.Fatalf("fallback = %v, want generic", c)
	}
}
