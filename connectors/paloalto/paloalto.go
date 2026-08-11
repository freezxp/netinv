// Package paloalto — PAN-OS connector for Palo Alto Networks firewalls
// (doc 10 §5, ADR-021): session gauges and identity from PAN-COMMON-MIB, CPU
// and memory from HOST-RESOURCES-MIB. Embeds generic for IF-MIB, the system
// group and LLDP.
//
// NOT VALIDATED AGAINST HARDWARE. Every OID here comes from the published
// PAN-OS MIBs, not from a walk of a real firewall, and there is no fixture
// because there is no PA-series unit to record one from (doc 10 §5.7).
//
// PAN-OS is the one platform in this tree whose CPU and memory come from
// HOST-RESOURCES-MIB rather than a vendor MIB or UCD-SNMP. The generic base
// reads UCD, which PAN-OS does not serve, so without this the device would
// report interfaces and sessions and no health at all.
package paloalto

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
		ID: "paloalto-panos", Vendor: "Palo Alto Networks", DisplayName: "Palo Alto Networks PAN-OS",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.25461."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}

func (c *Connector) Capabilities() []sdk.Capability {
	return sdk.AddCaps(c.Base.Capabilities(), sdk.CapHealth)
}

const (
	// PAN-COMMON-MIB, panSys
	oidPanSwVersion = ".1.3.6.1.4.1.25461.2.1.2.1.1.0" // panSysSwVersion
	oidPanHwVersion = ".1.3.6.1.4.1.25461.2.1.2.1.2.0" // panSysHwVersion
	oidPanSerial    = ".1.3.6.1.4.1.25461.2.1.2.1.3.0" // panSysSerialNumber

	// PAN-COMMON-MIB, panSession
	oidPanSessionMax    = ".1.3.6.1.4.1.25461.2.1.2.3.2.0" // platform ceiling
	oidPanSessionActive = ".1.3.6.1.4.1.25461.2.1.2.3.3.0" // total active

	// HOST-RESOURCES-MIB
	oidHrProcessorLoad = ".1.3.6.1.2.1.25.3.3.1.2" // percent, per processor
	oidHrStorageDescr  = ".1.3.6.1.2.1.25.2.3.1.3"
	oidHrStorageUnits  = ".1.3.6.1.2.1.25.2.3.1.4" // bytes per allocation unit
	oidHrStorageSize   = ".1.3.6.1.2.1.25.2.3.1.5" // in allocation units
	oidHrStorageUsed   = ".1.3.6.1.2.1.25.2.3.1.6" // in allocation units
)

func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample

	// panSessionUtilization (.3.1.0) is not fetched. It is count/max expressed
	// as a percentage, and storing all three invites the derived one to drift
	// from the pair it came from.
	sess, _ := s.Get(ctx, []string{oidPanSessionActive, oidPanSessionMax})
	vals := map[string]float64{}
	for _, v := range sess {
		if f, ok := sdk.Num(v.Value); ok {
			vals[norm(v.OID)] = f
		}
	}

	// Session gauges — the firewall-specific part admitted by ADR-021.
	if n, ok := vals[norm(oidPanSessionActive)]; ok {
		out = append(out, sdk.GaugeSample("netinv_firewall_session_count", nil, n))
	}
	// PAN-OS is the platform that does publish a ceiling, so utilization is
	// answerable. It is stored as count and max rather than as the percentage
	// the device also offers: two numbers that cannot disagree with each other,
	// where a stored percentage could drift from the pair it was derived from.
	if m, ok := vals[norm(oidPanSessionMax)]; ok && m > 0 {
		out = append(out, sdk.GaugeSample("netinv_firewall_session_max", nil, m))
	}
	// The per-protocol counts PAN-OS also publishes are deliberately not
	// emitted. Adding them under netinv_firewall_session_count with a protocol
	// label would put the same metric name in the store both with and without
	// that label, so any sum() over it would count every session twice — the
	// same shape of trap as the MetricsQL `or` collapse this codebase has hit
	// three times. They need their own metric name and an ADR line admitting
	// them; ADR-021 admits the totals only.

	// CPU: hrProcessorLoad is a column, one row per core.
	_ = sdk.WalkColumn(ctx, s, oidHrProcessorLoad, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok {
			out = append(out, sdk.GaugeSample("netinv_device_cpu_percent",
				map[string]string{"cpu": idx}, f))
		}
	})

	// Memory: hrStorage carries RAM, swap, and every mounted filesystem in one
	// table, told apart by hrStorageType. Summing the lot would report a
	// firewall's disk as its memory, so only the RAM rows count.
	out = append(out, memoryFromHostResources(ctx, s)...)

	return out, nil
}

func memoryFromHostResources(ctx context.Context, s sdk.Session) []sdk.Sample {
	const oidHrStorageType = ".1.3.6.1.2.1.25.2.3.1.2"

	col := func(oid string) map[string]string {
		m := map[string]string{}
		_ = sdk.WalkColumn(ctx, s, oid, func(idx string, v sdk.Var) {
			m[idx] = toStr(v.Value)
		})
		return m
	}
	numCol := func(oid string) map[string]float64 {
		m := map[string]float64{}
		_ = sdk.WalkColumn(ctx, s, oid, func(idx string, v sdk.Var) {
			if f, ok := sdk.Num(v.Value); ok {
				m[idx] = f
			}
		})
		return m
	}

	// Every column is walked exactly once. Fetching `used` inside the row loop
	// re-walked the whole table per candidate row, which on a firewall with
	// dozens of mounted filesystems is a real cost for no benefit.
	types, descr := col(oidHrStorageType), col(oidHrStorageDescr)
	units, size, used := numCol(oidHrStorageUnits), numCol(oidHrStorageSize), numCol(oidHrStorageUsed)

	for idx, total := range size {
		// hrStorage carries RAM, swap and every mounted filesystem in one
		// table. Summing the lot would report a firewall's disk as its memory,
		// so only the row whose hrStorageType is hrStorageRam counts. Some
		// agents render that OID as a string and some abbreviate it, so the
		// description is accepted as a fallback — it is "Physical memory" on
		// every agent seen in the wild.
		isRAM := strings.HasSuffix(norm(types[idx]), "25.2.1.2") ||
			strings.EqualFold(descr[idx], "Physical memory")
		if !isRAM || total <= 0 {
			continue
		}
		unit := units[idx]
		if unit <= 0 {
			unit = 1
		}
		totalBytes, usedBytes := total*unit, used[idx]*unit
		return []sdk.Sample{
			sdk.GaugeSample("netinv_device_memory_total_bytes", nil, totalBytes),
			sdk.GaugeSample("netinv_device_memory_used_bytes", nil, usedBytes),
			sdk.GaugeSample("netinv_device_memory_percent", nil, 100*usedBytes/totalBytes),
		}
	}
	return nil
}

// CollectInventory adds Palo Alto identity to the generic system-group snapshot.
func (c *Connector) CollectInventory(ctx context.Context, s sdk.Session) (*sdk.InventorySnapshot, error) {
	snap, err := c.Base.CollectInventory(ctx, s)
	if err != nil {
		return nil, err
	}
	snap.Vendor = "Palo Alto Networks"
	ident, _ := s.Get(ctx, []string{oidPanSerial, oidPanSwVersion, oidPanHwVersion})
	for _, v := range ident {
		val := toStr(v.Value)
		if val == "" {
			continue
		}
		switch norm(v.OID) {
		case norm(oidPanSerial):
			snap.Serial = val
		case norm(oidPanSwVersion):
			snap.OSVersion = val
		case norm(oidPanHwVersion):
			// panSysHwVersion is the chassis revision, not the product name.
			// The model proper lives in sysDescr, which the generic base has
			// already captured, so this only fills a gap rather than
			// overwriting a better answer.
			if snap.Model == "" {
				snap.Model = val
			}
		}
	}
	return snap, nil
}

func norm(oid string) string { return strings.TrimPrefix(oid, ".") }

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
