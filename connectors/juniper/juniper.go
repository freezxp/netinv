// Package juniper — JunOS connector (doc 10 §5): per-FRU CPU/temp/memory from
// the JUNIPER-MIB jnxOperatingTable.
package juniper

import (
	"context"

	"github.com/freezxp/netinv/connectors/generic"
	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Connector { return &Connector{} }

type Connector struct{ generic.Base }

func (c *Connector) Info() sdk.Info {
	return sdk.Info{
		ID: "juniper-junos", Vendor: "Juniper", DisplayName: "Juniper JunOS",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.2636."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}

func (c *Connector) Capabilities() []sdk.Capability {
	return sdk.AddCaps(c.Base.Capabilities(), sdk.CapHealth)
}

const (
	oidJnxOperatingDescr  = ".1.3.6.1.4.1.2636.3.1.13.1.5"  // FRU name
	oidJnxOperatingTemp   = ".1.3.6.1.4.1.2636.3.1.13.1.7"  // °C (0 = n/a)
	oidJnxOperatingCPU    = ".1.3.6.1.4.1.2636.3.1.13.1.8"  // %
	oidJnxOperatingBuffer = ".1.3.6.1.4.1.2636.3.1.13.1.11" // memory %
)

func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample
	// FRU names give operators readable sensor labels (bounded set).
	names := map[string]string{}
	_ = sdk.WalkColumn(ctx, s, oidJnxOperatingDescr, func(idx string, v sdk.Var) {
		if b, ok := v.Value.([]byte); ok && len(b) < 64 {
			names[idx] = string(b)
		} else if str, ok := v.Value.(string); ok && len(str) < 64 {
			names[idx] = str
		}
	})
	// Falling back to the index matters: a chassis whose descr walk came back
	// empty or truncated still reports CPU and temperature, and an empty label
	// merges every unnamed FRU into a single series that reads as one CPU
	// swinging wildly between cores.
	label := func(key, idx string) map[string]string {
		if n := names[idx]; n != "" {
			return map[string]string{key: n}
		}
		return map[string]string{key: idx}
	}
	_ = sdk.WalkColumn(ctx, s, oidJnxOperatingCPU, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > 0 {
			out = append(out, sdk.GaugeSample("netinv_device_cpu_percent",
				label("cpu", idx), f))
		}
	})
	_ = sdk.WalkColumn(ctx, s, oidJnxOperatingTemp, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > 0 {
			out = append(out, sdk.GaugeSample("netinv_sensor_temperature_celsius",
				label("sensor", idx), f))
		}
	})
	// Highest buffer utilization across FRUs is the device memory pressure.
	var maxMem float64
	_ = sdk.WalkColumn(ctx, s, oidJnxOperatingBuffer, func(_ string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > maxMem {
			maxMem = f
		}
	})
	if maxMem > 0 {
		out = append(out, sdk.GaugeSample("netinv_device_memory_percent", nil, maxMem))
	}
	return out, nil
}
