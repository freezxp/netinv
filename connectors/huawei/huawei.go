// Package huawei — VRP connector (doc 10 §5): per-entity CPU/memory/temp from
// HUAWEI-ENTITY-EXTENT-MIB hwEntityStateTable.
package huawei

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
		ID: "huawei-vrp", Vendor: "Huawei", DisplayName: "Huawei VRP",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.2011."},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	return sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes)
}

func (c *Connector) Capabilities() []sdk.Capability {
	return sdk.AddCaps(c.Base.Capabilities(), sdk.CapHealth)
}

const (
	oidHwEntityCPU  = ".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.5"  // hwEntityCpuUsage (%)
	oidHwEntityMem  = ".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.7"  // hwEntityMemUsage (%)
	oidHwEntityTemp = ".1.3.6.1.4.1.2011.5.25.31.1.1.1.1.11" // hwEntityTemperature (°C)
)

func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample
	_ = sdk.WalkColumn(ctx, s, oidHwEntityCPU, func(idx string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > 0 {
			out = append(out, sdk.GaugeSample("netinv_device_cpu_percent",
				map[string]string{"cpu": idx}, f))
		}
	})
	var maxMem float64
	_ = sdk.WalkColumn(ctx, s, oidHwEntityMem, func(_ string, v sdk.Var) {
		if f, ok := sdk.Num(v.Value); ok && f > maxMem {
			maxMem = f
		}
	})
	if maxMem > 0 {
		out = append(out, sdk.GaugeSample("netinv_device_memory_percent", nil, maxMem))
	}
	_ = sdk.WalkColumn(ctx, s, oidHwEntityTemp, func(idx string, v sdk.Var) {
		// Huawei reports 0/invalid for entities without sensors.
		if f, ok := sdk.Num(v.Value); ok && f > 0 && f < 200 {
			out = append(out, sdk.GaugeSample("netinv_sensor_temperature_celsius",
				map[string]string{"sensor": idx}, f))
		}
	})
	return out, nil
}
