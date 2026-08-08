// Package generic — the universal SNMP connector (doc 10 §4): IF-MIB traffic,
// SNMPv2-MIB system group. Every vendor connector embeds Base and extends it.
package generic

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/connectors/sdk"
)

func init() { sdk.Register(New()) }

func New() *Base { return &Base{} }

type Base struct{}

func (b *Base) Info() sdk.Info {
	return sdk.Info{
		ID: "generic", Vendor: "Generic",
		DisplayName: "Generic SNMP (IF-MIB)", Version: "0.2.0",
	}
}

// Match: universal floor — any SNMP agent scores 1.
func (b *Base) Match(sdk.SysInfo) sdk.MatchScore { return 1 }

func (b *Base) Capabilities() []sdk.Capability {
	return []sdk.Capability{sdk.CapInventory, sdk.CapInterfaces,
		sdk.CapTopology, sdk.CapHealth}
}

// OIDs (IF-MIB, SNMPv2-MIB).
const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = ".1.3.6.1.2.1.1.3.0"
	oidSysContact  = ".1.3.6.1.2.1.1.4.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"
	oidSysLocation = ".1.3.6.1.2.1.1.6.0"

	oidIfTable  = ".1.3.6.1.2.1.2.2.1"
	oidIfXTable = ".1.3.6.1.2.1.31.1.1.1"
)

// ifTable / ifXTable column ids → metric mapping for traffic collection.
// HC (64-bit) columns take precedence over 32-bit ones (FR-COLL-03).
type colMap struct {
	table  string
	column int
	metric string
	hc     bool
}

var trafficCols = []colMap{
	{oidIfXTable, 6, "netinv_if_in_octets_total", true},   // ifHCInOctets
	{oidIfXTable, 10, "netinv_if_out_octets_total", true}, // ifHCOutOctets
	{oidIfTable, 10, "netinv_if_in_octets_total", false},  // ifInOctets (fallback)
	{oidIfTable, 16, "netinv_if_out_octets_total", false}, // ifOutOctets (fallback)
	{oidIfXTable, 7, "netinv_if_in_ucast_pkts_total", true},
	{oidIfXTable, 11, "netinv_if_out_ucast_pkts_total", true},
	{oidIfTable, 14, "netinv_if_in_errors_total", false},
	{oidIfTable, 20, "netinv_if_out_errors_total", false},
	{oidIfTable, 13, "netinv_if_in_discards_total", false},
	{oidIfTable, 19, "netinv_if_out_discards_total", false},
	{oidIfTable, 7, "netinv_if_admin_status", false},
	{oidIfTable, 8, "netinv_if_oper_status", false},
}

// CollectInterfaces walks traffic counters and status (family=traffic).
func (b *Base) CollectInterfaces(ctx context.Context, s sdk.Session) ([]sdk.Sample, error) {
	now := time.Now().UTC()
	var samples []sdk.Sample
	// hcSeen tracks (metric, ifIndex) already provided by an HC column so the
	// 32-bit fallback doesn't overwrite it.
	hcSeen := map[string]bool{}

	walk := func(root string) (map[string]sdk.Var, error) {
		vars, err := s.Walk(ctx, root)
		if err != nil {
			return nil, err
		}
		out := make(map[string]sdk.Var, len(vars))
		for _, v := range vars {
			out[v.OID] = v
		}
		return out, nil
	}
	ifT, err := walk(oidIfTable)
	if err != nil {
		return nil, fmt.Errorf("generic: ifTable walk: %w", err)
	}
	ifX, _ := walk(oidIfXTable) // optional: pure 32-bit agents lack it

	pick := func(table string) map[string]sdk.Var {
		if table == oidIfXTable {
			return ifX
		}
		return ifT
	}
	for _, cm := range trafficCols {
		prefix := cm.table + "." + strconv.Itoa(cm.column) + "."
		for oid, v := range pick(cm.table) {
			if !strings.HasPrefix(oid, prefix) {
				continue
			}
			ifIndex := strings.TrimPrefix(oid, prefix)
			key := cm.metric + "/" + ifIndex
			if hcSeen[key] && !cm.hc {
				continue
			}
			val, ok := toFloat(v.Value)
			if !ok {
				continue
			}
			if cm.hc {
				hcSeen[key] = true
			}
			samples = append(samples, sdk.Sample{
				Name:   cm.metric,
				Labels: map[string]string{"if_index": ifIndex},
				Value:  val,
				At:     now,
			})
		}
	}
	// ifHighSpeed (Mbps) → speed label metric for utilization math.
	speedPrefix := oidIfXTable + ".15."
	for oid, v := range ifX {
		if strings.HasPrefix(oid, speedPrefix) {
			if val, ok := toFloat(v.Value); ok {
				samples = append(samples, sdk.Sample{
					Name:   "netinv_if_speed_bps",
					Labels: map[string]string{"if_index": strings.TrimPrefix(oid, speedPrefix)},
					Value:  val * 1e6,
					At:     now,
				})
			}
		}
	}
	return samples, nil
}

// CollectInventory reads the system group and interface identity (family=sync).
func (b *Base) CollectInventory(ctx context.Context, s sdk.Session) (*sdk.InventorySnapshot, error) {
	vars, err := s.Get(ctx, []string{oidSysName, oidSysDescr, oidSysObjectID,
		oidSysLocation, oidSysContact, oidSysUpTime})
	if err != nil {
		return nil, fmt.Errorf("generic: system group: %w", err)
	}
	snap := &sdk.InventorySnapshot{}
	for _, v := range vars {
		sv := toString(v.Value)
		switch v.OID {
		case oidSysName:
			snap.SysName = sv
		case oidSysDescr:
			snap.SysDescr = sv
		case oidSysObjectID:
			snap.SysObjectID = sv
		case oidSysLocation:
			snap.SysLocation = sv
		case oidSysContact:
			snap.SysContact = sv
		case oidSysUpTime:
			if f, ok := toFloat(v.Value); ok {
				snap.UptimeS = int64(f / 100) // TimeTicks are centiseconds
			}
		}
	}
	byIndex := map[int]*sdk.InterfaceRecord{}
	rec := func(idx int) *sdk.InterfaceRecord {
		if r, ok := byIndex[idx]; ok {
			return r
		}
		r := &sdk.InterfaceRecord{IfIndex: idx}
		byIndex[idx] = r
		return r
	}
	ifT, err := s.Walk(ctx, oidIfTable)
	if err != nil {
		return nil, fmt.Errorf("generic: ifTable walk: %w", err)
	}
	for _, v := range ifT {
		col, idx, ok := splitCol(v.OID, oidIfTable)
		if !ok {
			continue
		}
		r := rec(idx)
		switch col {
		case 2:
			r.Descr = toString(v.Value)
		case 3:
			if f, ok := toFloat(v.Value); ok {
				r.IfType = int(f)
			}
		case 4:
			if f, ok := toFloat(v.Value); ok {
				r.MTU = int(f)
			}
		case 5:
			if f, ok := toFloat(v.Value); ok {
				r.SpeedBPS = int64(f)
			}
		case 7:
			if f, ok := toFloat(v.Value); ok {
				r.AdminStatus = int(f)
			}
		case 8:
			if f, ok := toFloat(v.Value); ok {
				r.OperStatus = int(f)
			}
		}
	}
	ifX, _ := s.Walk(ctx, oidIfXTable)
	for _, v := range ifX {
		col, idx, ok := splitCol(v.OID, oidIfXTable)
		if !ok {
			continue
		}
		r := rec(idx)
		switch col {
		case 1:
			r.Name = toString(v.Value)
		case 15:
			if f, ok := toFloat(v.Value); ok && f > 0 {
				r.SpeedBPS = int64(f) * 1e6
			}
		case 18:
			r.Alias = toString(v.Value)
		}
	}
	for _, r := range byIndex {
		if r.Name == "" {
			r.Name = r.Descr
		}
		snap.Interfaces = append(snap.Interfaces, *r)
	}
	return snap, nil
}

// LLDP-MIB remote table (IEEE 802.1AB): index is timeMark.localPortNum.remIndex.
const oidLldpRemTable = ".1.0.8802.1.1.2.1.4.1.1"

// CollectTopology walks lldpRemTable (best-effort — many devices ship without
// LLDP enabled; empty result is success).
func (b *Base) CollectTopology(ctx context.Context, s sdk.Session) ([]sdk.Adjacency, error) {
	vars, err := s.Walk(ctx, oidLldpRemTable)
	if err != nil || len(vars) == 0 {
		return nil, nil // absent LLDP support is not a failure
	}
	type key struct{ port, rem int }
	adj := map[key]*sdk.Adjacency{}
	get := func(port, rem int) *sdk.Adjacency {
		k := key{port, rem}
		if a, ok := adj[k]; ok {
			return a
		}
		a := &sdk.Adjacency{LocalIfIndex: port, Protocol: "lldp"}
		adj[k] = a
		return a
	}
	for _, v := range vars {
		rest, found := strings.CutPrefix(v.OID, oidLldpRemTable+".")
		if !found {
			continue
		}
		parts := strings.Split(rest, ".")
		if len(parts) != 4 { // col.timeMark.localPort.remIndex
			continue
		}
		col, _ := strconv.Atoi(parts[0])
		port, _ := strconv.Atoi(parts[2])
		rem, _ := strconv.Atoi(parts[3])
		switch col {
		case 5: // lldpRemChassisId
			get(port, rem).RemoteChassis = toString(v.Value)
		case 7: // lldpRemPortId
			get(port, rem).RemotePortID = toString(v.Value)
		case 9: // lldpRemSysName
			get(port, rem).RemoteSysName = toString(v.Value)
		}
	}
	out := make([]sdk.Adjacency, 0, len(adj))
	for _, a := range adj {
		out = append(out, *a)
	}
	return out, nil
}

func splitCol(oid, table string) (col, idx int, ok bool) {
	rest, found := strings.CutPrefix(oid, table+".")
	if !found {
		return 0, 0, false
	}
	parts := strings.SplitN(rest, ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	c, err1 := strconv.Atoi(parts[0])
	i, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return c, i, true
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprintf("%v", v)
}
