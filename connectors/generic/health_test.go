package generic

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
)

// Values recorded from a real Ubiquiti UDM-Pro (UniFi OS 5.1.26, net-snmp)
// during pilot validation — doc 31 §6.
func udmProSession() *fakeSession {
	return &fakeSession{data: map[string]any{
		oidMemTotalReal:       uint64(4040828), // KB
		oidMemAvailReal:       uint64(244044),
		oidMemBuffer:          uint64(378528),
		oidMemCached:          uint64(1307192),
		oidSsCpuIdle:          uint64(80),
		oidLaLoad + ".1":      "1.41",
		oidLaLoad + ".2":      "1.24",
		oidLaLoad + ".3":      "1.02",
		oidLmTempName + ".1":  "temp-CPU",
		oidLmTempName + ".2":  "temp-Local",
		oidLmTempName + ".3":  "Board Temp",  // unpopulated slot, reads 0
		oidLmTempValue + ".1": uint64(50000), // millidegrees
		oidLmTempValue + ".2": uint64(68000),
		oidLmTempValue + ".3": uint64(0),
	}}
}

func sample(t *testing.T, samples []sdk.Sample, name, labelKey, labelVal string) float64 {
	t.Helper()
	for _, s := range samples {
		if s.Name == name && (labelKey == "" || s.Labels[labelKey] == labelVal) {
			return s.Value
		}
	}
	t.Fatalf("sample %s{%s=%s} not collected", name, labelKey, labelVal)
	return 0
}

func TestHealthFromNetSNMPAgent(t *testing.T) {
	samples, err := New().CollectHealth(context.Background(), udmProSession())
	if err != nil {
		t.Fatal(err)
	}

	if v := sample(t, samples, "netinv_device_cpu_percent", "", ""); v != 20 {
		t.Errorf("cpu = %v, want 20 (100 - 80%% idle)", v)
	}

	// Linux used memory excludes buffers and page cache: 4040828 - 244044 -
	// 378528 - 1307192 = 2111064 KB. Counting cache would report ~94% on a
	// healthy box and fire the memory alert constantly.
	if v := sample(t, samples, "netinv_device_memory_used_bytes", "", ""); v != 2111064*1024 {
		t.Errorf("memory used = %v bytes, want %v", v, 2111064*1024)
	}
	if v := sample(t, samples, "netinv_device_memory_total_bytes", "", ""); v != 4040828*1024 {
		t.Errorf("memory total = %v", v)
	}
	if pct := sample(t, samples, "netinv_device_memory_percent", "", ""); pct < 52 || pct > 53 {
		t.Errorf("memory percent = %v, want ~52.2", pct)
	}

	if v := sample(t, samples, "netinv_device_load_average", "period", "1m"); v != 1.41 {
		t.Errorf("load 1m = %v, want 1.41", v)
	}
	if v := sample(t, samples, "netinv_device_load_average", "period", "15m"); v != 1.02 {
		t.Errorf("load 15m = %v, want 1.02", v)
	}

	// Millidegrees → Celsius, labelled with the sensor's own name.
	if v := sample(t, samples, "netinv_sensor_temperature_celsius", "sensor", "temp-CPU"); v != 50 {
		t.Errorf("temp-CPU = %v, want 50", v)
	}
	if v := sample(t, samples, "netinv_sensor_temperature_celsius", "sensor", "temp-Local"); v != 68 {
		t.Errorf("temp-Local = %v, want 68", v)
	}
	// Unpopulated sensors must not become series.
	for _, s := range samples {
		if s.Name == "netinv_sensor_temperature_celsius" && s.Labels["sensor"] == "Board Temp" {
			t.Error("unpopulated 0 °C sensor was published")
		}
	}
}

// A device exposing none of these subtrees must yield no samples and no error
// (best-effort contract, doc 10 §4).
func TestHealthAbsentIsNotAnError(t *testing.T) {
	samples, err := New().CollectHealth(context.Background(),
		&fakeSession{data: map[string]any{".1.3.6.1.2.1.1.5.0": "bare-switch"}})
	if err != nil {
		t.Fatalf("absent health must not error: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected no samples, got %d", len(samples))
	}
}
