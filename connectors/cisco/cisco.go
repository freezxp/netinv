// Package cisco — IOS/IOS-XE connector (doc 10 §5): CPU (CISCO-PROCESS-MIB),
// memory pools (CISCO-MEMORY-POOL-MIB), temperature/fan/PSU (CISCO-ENVMON).
// Embeds generic for IF-MIB, system, and LLDP.
package cisco

import (
	"context"
	"strconv"
	"strings"

	"github.com/freezxp/netinv/connectors/generic"
	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Connector { return &Connector{} }

type Connector struct{ generic.Base }

func (c *Connector) Info() sdk.Info {
	return sdk.Info{
		ID: "cisco-ios", Vendor: "Cisco", DisplayName: "Cisco IOS / IOS-XE",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.9."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}

func (c *Connector) Capabilities() []sdk.Capability {
	return sdk.AddCaps(c.Base.Capabilities(), sdk.CapHealth)
}

const (
	oidCpmCPU5min  = ".1.3.6.1.4.1.9.9.109.1.1.1.1.8" // cpmCPUTotal5minRev (%)
	oidCpmCPUPhys  = ".1.3.6.1.4.1.9.9.109.1.1.1.1.2" // cpmCPUTotalPhysicalIndex
	oidEntPhysName = ".1.3.6.1.2.1.47.1.1.1.1.7"      // entPhysicalName
	oidMemPoolUsed = ".1.3.6.1.4.1.9.9.48.1.1.1.5"    // ciscoMemoryPoolUsed (bytes)
	oidMemPoolFree = ".1.3.6.1.4.1.9.9.48.1.1.1.6"    // ciscoMemoryPoolFree (bytes)
	oidEnvTempVal  = ".1.3.6.1.4.1.9.9.13.1.3.1.3"    // ciscoEnvMonTemperatureValue (°C)
	oidEnvFanState = ".1.3.6.1.4.1.9.9.13.1.4.1.3"    // ciscoEnvMonFanState (1=normal)
	oidEnvPSUState = ".1.3.6.1.4.1.9.9.13.1.5.1.3"    // ciscoEnvMonSupplyState (1=normal)
)

func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample

	// cpmCPUTotalTable has a row per processor, and on a chassis platform that
	// is several: route processor, forwarding engine, line cards. They are
	// emitted as separate series rather than collapsed, because a busy
	// forwarding engine and a busy control plane are different faults — but a
	// row identified only by table index is unreadable. "cpu=7 is at 99%" tells
	// an operator nothing they can check against `show processes cpu`, and the
	// dashboard's topk will surface whichever row is worst without saying what
	// it is.
	//
	// So each row is named by the physical entity it belongs to, via
	// cpmCPUTotalPhysicalIndex into ENTITY-MIB. The index remains the label
	// when the device does not populate either, which is common on older IOS
	// and on agents with a restricted view.
	physIdx := map[string]string{}
	_ = sdk.WalkColumn(ctx, s, oidCpmCPUPhys, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > 0 {
			physIdx[idx] = strconv.FormatInt(int64(f), 10)
		}
	})
	entName := map[string]string{}
	if len(physIdx) > 0 {
		_ = sdk.WalkColumn(ctx, s, oidEntPhysName, func(idx string, v sdk.Var) {
			if name, ok := sdk.Str(v.Value); ok && strings.TrimSpace(name) != "" {
				entName[idx] = strings.TrimSpace(name)
			}
		})
	}
	_ = sdk.WalkColumn(ctx, s, oidCpmCPU5min, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok {
			label := idx
			if name := entName[physIdx[idx]]; name != "" {
				label = name
			}
			out = append(out, sdk.GaugeSample("netinv_device_cpu_percent",
				map[string]string{"cpu": label}, f))
		}
	})
	used := map[string]float64{}
	free := map[string]float64{}
	_ = sdk.WalkColumn(ctx, s, oidMemPoolUsed, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok {
			used[idx] = f
		}
	})
	_ = sdk.WalkColumn(ctx, s, oidMemPoolFree, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok {
			free[idx] = f
		}
	})
	var totalUsed, totalBytes float64
	for idx, u := range used {
		totalUsed += u
		totalBytes += u + free[idx]
	}
	if totalBytes > 0 {
		out = append(out,
			sdk.GaugeSample("netinv_device_memory_used_bytes", nil, totalUsed),
			sdk.GaugeSample("netinv_device_memory_total_bytes", nil, totalBytes),
			sdk.GaugeSample("netinv_device_memory_percent", nil, 100*totalUsed/totalBytes),
		)
	}
	_ = sdk.WalkColumn(ctx, s, oidEnvTempVal, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok {
			out = append(out, sdk.GaugeSample("netinv_sensor_temperature_celsius",
				map[string]string{"sensor": idx}, f))
		}
	})
	statusCols := map[string]string{
		oidEnvFanState: "netinv_sensor_fan_status",
		oidEnvPSUState: "netinv_sensor_psu_status",
	}
	for oid, metric := range statusCols {
		_ = sdk.WalkColumn(ctx, s, oid, func(idx string, v sdk.Var) {
			if f, ok := sdk.Num(v.Value); ok {
				// envmon states: 1 normal, 2 warning, 3 critical, 5 not present.
				out = append(out, sdk.GaugeSample(metric,
					map[string]string{"sensor": idx}, f))
			}
		})
	}
	return out, nil
}
