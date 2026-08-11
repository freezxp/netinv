// Package fortinet — FortiOS connector for FortiGate appliances (doc 10 §5,
// ADR-021): CPU, memory and session gauges from FORTINET-FORTIGATE-MIB,
// hardware sensors from fgHwSensorEntTable, identity from FORTINET-CORE-MIB.
// Embeds generic for IF-MIB, the system group and LLDP.
//
// NOT VALIDATED AGAINST HARDWARE. Every OID here comes from the published
// Fortinet MIBs, not from a walk of a real appliance, and there is no fixture
// because there is no FortiGate to record one from. The shape most likely to
// be wrong is what a given FortiOS build actually populates — several of these
// objects exist on all models but return nothing on some, which this code
// treats as "no sample" rather than an error (doc 10 §5.7).
package fortinet

import (
	"context"
	"strings"

	"github.com/freezxp/netinv/connectors/generic"
	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Connector { return &Connector{} }

type Connector struct{ generic.Base }

func (c *Connector) Info() sdk.Info {
	return sdk.Info{
		ID: "fortinet-fortios", Vendor: "Fortinet", DisplayName: "Fortinet FortiGate (FortiOS)",
		Version: "0.1.0",
		// 12356 is Fortinet's enterprise number; .101 is the FortiGate branch.
		// Matching the enterprise root rather than the product branch means a
		// FortiSwitch or FortiAP lands here too — better than falling back to
		// generic, since the core identity OIDs are shared across the range.
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.12356."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}

func (c *Connector) Capabilities() []sdk.Capability {
	return sdk.AddCaps(c.Base.Capabilities(), sdk.CapHealth)
}

const (
	// FORTINET-CORE-MIB
	oidFnSysSerial = ".1.3.6.1.4.1.12356.100.1.1.1.0" // fnSysSerial

	// FORTINET-FORTIGATE-MIB, fgSystemInfo
	oidFgSysVersion     = ".1.3.6.1.4.1.12356.101.4.1.1.0"  // fgSysVersion (firmware)
	oidFgSysCPUUsage    = ".1.3.6.1.4.1.12356.101.4.1.3.0"  // percent
	oidFgSysMemUsage    = ".1.3.6.1.4.1.12356.101.4.1.4.0"  // percent
	oidFgSysMemCapacity = ".1.3.6.1.4.1.12356.101.4.1.5.0"  // KB
	oidFgSysSesCount    = ".1.3.6.1.4.1.12356.101.4.1.8.0"  // active sessions
	oidFgSysSesRate1    = ".1.3.6.1.4.1.12356.101.4.1.10.0" // sessions/sec, 1-min average

	// fgHwSensorEntTable — present on appliances with physical sensors, absent
	// on VMs, which is the single biggest difference between two FortiGates
	// that otherwise look identical over SNMP.
	oidFgHwSensorName  = ".1.3.6.1.4.1.12356.101.4.3.2.1.2" // fgHwSensorEntName
	oidFgHwSensorValue = ".1.3.6.1.4.1.12356.101.4.3.2.1.3" // fgHwSensorEntValue
	// fgHwSensorEntAlarmStatus (.4) exists and is not read: its polarity is
	// the inverse of the catalogue's status convention and unverifiable here,
	// so mapping it would be a guess published as a fact. The first person
	// with hardware should wire it up.
)

func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample

	scalars, _ := s.Get(ctx, []string{
		oidFgSysCPUUsage, oidFgSysMemUsage, oidFgSysMemCapacity,
		oidFgSysSesCount, oidFgSysSesRate1,
	})
	vals := map[string]float64{}
	for _, v := range scalars {
		if f, ok := sdk.Num(v.Value); ok {
			vals[norm(v.OID)] = f
		}
	}

	if cpu, ok := vals[norm(oidFgSysCPUUsage)]; ok {
		// A single system-wide figure, not per-core: FortiOS publishes one
		// number here, so there is no cpu label to attach and inventing an
		// index of "0" would imply a per-core breakdown that does not exist.
		out = append(out, sdk.GaugeSample("netinv_device_cpu_percent", nil, cpu))
	}
	memPct, hasPct := vals[norm(oidFgSysMemUsage)]
	if hasPct {
		out = append(out, sdk.GaugeSample("netinv_device_memory_percent", nil, memPct))
	}
	// Capacity is in KB. Publishing used/total alongside the percentage keeps
	// this device comparable with the ones that report bytes and no percentage.
	if capKB, ok := vals[norm(oidFgSysMemCapacity)]; ok && capKB > 0 {
		total := capKB * 1024
		out = append(out, sdk.GaugeSample("netinv_device_memory_total_bytes", nil, total))
		if hasPct {
			out = append(out, sdk.GaugeSample("netinv_device_memory_used_bytes", nil,
				total*memPct/100))
		}
	}

	// Session gauges — the firewall-specific part admitted by ADR-021. There is
	// no session ceiling in this MIB: FortiOS publishes the count and the setup
	// rate but not the platform maximum, so a utilization percentage cannot be
	// derived here and is deliberately not invented.
	if n, ok := vals[norm(oidFgSysSesCount)]; ok {
		out = append(out, sdk.GaugeSample("netinv_firewall_session_count", nil, n))
	}
	if r, ok := vals[norm(oidFgSysSesRate1)]; ok {
		out = append(out, sdk.GaugeSample("netinv_firewall_session_setup_rate", nil, r))
	}

	// Sensors are named in one column and valued in another, joined by index.
	names := map[string]string{}
	_ = sdk.WalkColumn(ctx, s, oidFgHwSensorName, func(idx string, v sdk.Var) {
		if n := toStr(v.Value); n != "" {
			names[idx] = n
		}
	})
	_ = sdk.WalkColumn(ctx, s, oidFgHwSensorValue, func(idx string, v sdk.Var) {
		f, ok := sdk.Num(v.Value)
		if !ok {
			return
		}
		name := names[idx]
		if name == "" {
			name = idx
		}
		// One table carries temperatures, fan speeds and voltages, told apart
		// only by the sensor's name. Splitting them by metric matters: a fan at
		// 8000 RPM charted as a temperature would read as a fire.
		switch {
		case looksLikeFan(name):
			out = append(out, sdk.GaugeSample("netinv_sensor_fan_rpm",
				map[string]string{"sensor": name}, f))
		case looksLikeVoltage(name):
			// Dropped on purpose. No voltage metric exists in the catalogue,
			// and adding one for a single unvalidated connector would be
			// inventing a schema nobody can check.
		default:
			// Temperatures are the residue. Values outside a plausible range are
			// dropped rather than charted, the same guard the Huawei connector
			// needs for entities without sensors.
			if f > -50 && f < 200 {
				out = append(out, sdk.GaugeSample("netinv_sensor_temperature_celsius",
					map[string]string{"sensor": name}, f))
			}
		}
	})
	return out, nil
}

// CollectInventory adds Fortinet identity to the generic system-group snapshot.
func (c *Connector) CollectInventory(ctx context.Context, s sdk.Session) (*sdk.InventorySnapshot, error) {
	snap, err := c.Base.CollectInventory(ctx, s)
	if err != nil {
		return nil, err
	}
	snap.Vendor = "Fortinet"
	ident, _ := s.Get(ctx, []string{oidFnSysSerial, oidFgSysVersion})
	for _, v := range ident {
		val := toStr(v.Value)
		if val == "" {
			continue
		}
		switch norm(v.OID) {
		case norm(oidFnSysSerial):
			snap.Serial = val
		case norm(oidFgSysVersion):
			snap.OSVersion = val
		}
	}
	return snap, nil
}

func norm(oid string) string { return strings.TrimPrefix(oid, ".") }

func looksLikeFan(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "fan")
}

func looksLikeVoltage(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "vcc") || strings.Contains(n, "volt") ||
		strings.HasPrefix(n, "+") || strings.Contains(n, "vin")
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []byte:
		return strings.TrimSpace(string(x))
	default:
		return ""
	}
}
