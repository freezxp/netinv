package generic

import (
	"context"
	"strconv"
	"strings"

	"github.com/freezxp/netinv/connectors/sdk"
)

// Health collection for net-snmp agents via UCD-SNMP-MIB and LM-SENSORS.
// Any Linux-based device running net-snmp exposes these — Ubiquiti UniFi
// gateways (UDM/UDM-Pro), UniFi OS consoles, appliances, and plain servers —
// so it belongs in the generic layer rather than a vendor connector
// (doc 10 §4: generic provides standards-based collection for free, vendors
// extend it). Everything here is best-effort: a device that doesn't expose a
// subtree simply contributes no samples, never an error.
const (
	oidLaLoad       = ".1.3.6.1.4.1.2021.10.1.3"      // laLoad: 1/5/15-min load, DisplayString
	oidMemTotalReal = ".1.3.6.1.4.1.2021.4.5.0"       // KB
	oidMemAvailReal = ".1.3.6.1.4.1.2021.4.6.0"       // KB (free, excl. buffers/cache)
	oidMemBuffer    = ".1.3.6.1.4.1.2021.4.14.0"      // KB
	oidMemCached    = ".1.3.6.1.4.1.2021.4.15.0"      // KB
	oidSsCpuIdle    = ".1.3.6.1.4.1.2021.11.11.0"     // percent idle
	oidLmTempName   = ".1.3.6.1.4.1.2021.13.16.2.1.2" // lmTempSensorsDevice
	oidLmTempValue  = ".1.3.6.1.4.1.2021.13.16.2.1.3" // millidegrees C
)

// CollectHealth reads CPU, load average, memory and temperature sensors.
func (b *Base) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample

	scalars, _ := s.Get(ctx, []string{
		oidMemTotalReal, oidMemAvailReal, oidMemBuffer, oidMemCached, oidSsCpuIdle,
	})
	val := map[string]float64{}
	for _, v := range scalars {
		if f, ok := sdk.Num(v.Value); ok {
			val[strings.TrimPrefix(v.OID, ".")] = f
		}
	}
	get := func(oid string) (float64, bool) {
		f, ok := val[strings.TrimPrefix(oid, ".")]
		return f, ok
	}

	if idle, ok := get(oidSsCpuIdle); ok {
		out = append(out, sdk.GaugeSample("netinv_device_cpu_percent", nil, 100-idle))
	}

	// Linux "used" excludes buffers and page cache — counting them would report
	// a healthy device as ~95% consumed.
	if total, ok := get(oidMemTotalReal); ok && total > 0 {
		free, _ := get(oidMemAvailReal)
		buffers, _ := get(oidMemBuffer)
		cached, _ := get(oidMemCached)
		used := total - free - buffers - cached
		if used < 0 {
			used = total - free
		}
		const kb = 1024
		out = append(out,
			sdk.GaugeSample("netinv_device_memory_total_bytes", nil, total*kb),
			sdk.GaugeSample("netinv_device_memory_used_bytes", nil, used*kb),
			sdk.GaugeSample("netinv_device_memory_percent", nil, 100*used/total),
		)
	}

	// laLoad is a DisplayString ("1.41"); index 1/2/3 = 1/5/15 minutes.
	periods := map[string]string{"1": "1m", "2": "5m", "3": "15m"}
	_ = sdk.WalkColumn(ctx, s, oidLaLoad, func(idx string, v sdk.Var) {
		period, known := periods[idx]
		if !known {
			return
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(toString(v.Value)), 64); err == nil {
			out = append(out, sdk.GaugeSample("netinv_device_load_average",
				map[string]string{"period": period}, f))
		}
	})

	// Sensor names label the readings; values are millidegrees Celsius.
	names := map[string]string{}
	_ = sdk.WalkColumn(ctx, s, oidLmTempName, func(idx string, v sdk.Var) {
		if n := toString(v.Value); n != "" && len(n) <= 40 {
			names[idx] = n
		}
	})
	_ = sdk.WalkColumn(ctx, s, oidLmTempValue, func(idx string, v sdk.Var) {
		f, ok := sdk.Num(v.Value)
		// Unpopulated lm-sensors slots read exactly 0 (seen on UDM-Pro:
		// "Board Temp", temp1, temp3) — publishing them would put phantom
		// 0 °C lines on every chart.
		if !ok || f <= 0 || f > 200_000 {
			return
		}
		sensor := names[idx]
		if sensor == "" {
			sensor = idx
		}
		out = append(out, sdk.GaugeSample("netinv_sensor_temperature_celsius",
			map[string]string{"sensor": sensor}, f/1000))
	})

	return out, nil
}
