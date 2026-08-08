// Package ruckus — Ruckus Wireless (CommScope) connector.
//
// Validated against a real R710 running Unleashed 200.15.6.212. Findings that
// shaped this connector, from walking the device:
//
//   - It answers on the Ruckus enterprise OID (.1.3.6.1.4.1.25053) and
//     implements the Unleashed MIB at .1.3.6.1.4.1.25053.1.15.
//   - It exposes NO CPU, memory or temperature — not in its own MIB, not
//     UCD-SNMP, not HOST-RESOURCES. So this connector deliberately reports no
//     host-health metrics rather than inventing them.
//   - What it does expose is wireless state: managed-AP count, client count,
//     and a per-AP table — plus model/serial/firmware for inventory.
//
// Interfaces, system group and LLDP come from the embedded generic layer.
package ruckus

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
		ID: "ruckus", Vendor: "Ruckus", DisplayName: "Ruckus Wireless (Unleashed / ZoneFlex)",
		Version:             "0.1.0",
		SysObjectIDPrefixes: []string{".1.3.6.1.4.1.25053"},
	}
}

func (c *Connector) Match(sys sdk.SysInfo) sdk.MatchScore {
	if s := sdk.PrefixScore(sys, c.Info().SysObjectIDPrefixes); s > 0 {
		return s
	}
	if strings.Contains(strings.ToLower(sys.SysDescr), "ruckus") {
		return 5 // above the generic floor, below an enterprise-OID match
	}
	return 0
}

func (c *Connector) Capabilities() []sdk.Capability {
	return append(c.Base.Capabilities(), sdk.CapHealth)
}

// RUCKUS-UNLEASHED-MIB. Scalars are .0-suffixed.
const (
	oidSystemName     = ".1.3.6.1.4.1.25053.1.15.1.1.1.1.1.0"
	oidSystemModel    = ".1.3.6.1.4.1.25053.1.15.1.1.1.1.9.0"
	oidSystemSerial   = ".1.3.6.1.4.1.25053.1.15.1.1.1.1.15.0"
	oidSystemVersion  = ".1.3.6.1.4.1.25053.1.15.1.1.1.1.18.0"
	oidStatsNumAP     = ".1.3.6.1.4.1.25053.1.15.1.1.1.15.1.0"
	oidStatsNumClient = ".1.3.6.1.4.1.25053.1.15.1.1.1.15.2.0"

	// Per-AP table, indexed by AP MAC: .<column>.<macIndex>
	apTable       = ".1.3.6.1.4.1.25053.1.15.2.1.1.2.1.1"
	apColModel    = 4
	apColStatus   = 3
	apColUptime   = 6
)

// CollectHealth reports wireless state. This device family has no host-health
// counters (see package doc), so nothing is fabricated for CPU/memory/temp.
func (c *Connector) CollectHealth(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	var out []sdk.Sample

	scalars, _ := s.Get(ctx, []string{oidStatsNumAP, oidStatsNumClient})
	for _, v := range scalars {
		f, ok := sdk.Num(v.Value)
		if !ok {
			continue
		}
		switch strings.TrimPrefix(v.OID, ".") {
		case strings.TrimPrefix(oidStatsNumAP, "."):
			out = append(out, sdk.GaugeSample("netinv_wireless_ap_count", nil, f))
		case strings.TrimPrefix(oidStatsNumClient, "."):
			out = append(out, sdk.GaugeSample("netinv_wireless_client_count", nil, f))
		}
	}

	// Per-AP reachability, labelled by model. The table index is the AP's MAC
	// in dotted-decimal form, which is stable but not human-friendly, so the
	// model is used as the label and the count carries the detail.
	models := map[string]string{}
	_ = sdk.WalkColumn(ctx, s, apTable+"."+itoa(apColModel), func(idx string, v sdk.Var) {
		models[idx] = toStr(v.Value)
	})
	up := 0.0
	total := 0.0
	_ = sdk.WalkColumn(ctx, s, apTable+"."+itoa(apColStatus), func(idx string, v sdk.Var) {
		f, ok := sdk.Num(v.Value)
		if !ok {
			return
		}
		total++
		if f == 1 {
			up++
		}
	})
	if total > 0 {
		out = append(out,
			sdk.GaugeSample("netinv_wireless_ap_up_count", nil, up),
			sdk.GaugeSample("netinv_wireless_ap_total", nil, total),
		)
	}
	return out, nil
}

// CollectInventory adds Ruckus identity to the generic system-group snapshot,
// so the device shows a real vendor/model/serial/firmware in inventory.
func (c *Connector) CollectInventory(ctx context.Context, s sdk.Session) (*sdk.InventorySnapshot, error) {
	snap, err := c.Base.CollectInventory(ctx, s)
	if err != nil {
		return nil, err
	}
	ident, _ := s.Get(ctx, []string{
		oidSystemModel, oidSystemSerial, oidSystemVersion, oidSystemName,
	})
	snap.Vendor = "Ruckus"
	for _, v := range ident {
		val := toStr(v.Value)
		if val == "" {
			continue
		}
		switch strings.TrimPrefix(v.OID, ".") {
		case strings.TrimPrefix(oidSystemModel, "."):
			snap.Model = val
		case strings.TrimPrefix(oidSystemSerial, "."):
			snap.Serial = val
		case strings.TrimPrefix(oidSystemVersion, "."):
			snap.OSVersion = val
		case strings.TrimPrefix(oidSystemName, "."):
			if snap.SysName == "" {
				snap.SysName = val
			}
		}
	}
	return snap, nil
}

func toStr(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	}
	return ""
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
