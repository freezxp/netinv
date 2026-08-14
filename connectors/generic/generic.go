// Package generic — the universal SNMP connector (doc 10 §4): IF-MIB traffic,
// SNMPv2-MIB system group. Every vendor connector embeds Base and extends it.
package generic

import (
	"context"
	"fmt"
	"sort"
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
	// Repair a partial walk before moving on. See fillCounterGaps: some agents
	// enumerate every interface in the config columns and then skip almost all
	// of them in the counter columns, even though a GET for the very same OID
	// answers correctly.
	repaired := fillCounterGaps(ctx, s, ifT, pick, hcSeen, now, &samples)
	// Publish the repair count even when it is zero, so the series exists on
	// every device and an alert can be written against it. A device needing
	// repair is a device with a broken SNMP agent: the graphs are correct, but
	// somebody should know, or this silently becomes load nobody accounts for.
	samples = append(samples, sdk.Sample{
		Name:  "netinv_if_counters_repaired",
		Value: float64(repaired),
		At:    now,
	})

	// Interface speed, the denominator every utilisation figure divides by.
	//
	// ifHighSpeed (Mbit/s) is preferred because ifSpeed is a 32-bit gauge that
	// saturates above ~4.29 Gbit/s, but plenty of agents leave ifHighSpeed at
	// zero and populate only ifSpeed — a Ruckus R710 reports 1000000000 in
	// ifSpeed and 0 in ifHighSpeed for every port. Reading ifHighSpeed alone
	// published a speed of 0 for such devices, and a zero denominator means
	// utilisation silently stays at 0% however busy the link is.
	speed := map[string]float64{}
	lowPrefix := oidIfTable + ".5."
	for oid, v := range ifT {
		if strings.HasPrefix(oid, lowPrefix) {
			if val, ok := toFloat(v.Value); ok && val > 0 {
				speed[strings.TrimPrefix(oid, lowPrefix)] = val
			}
		}
	}
	highPrefix := oidIfXTable + ".15."
	for oid, v := range ifX {
		if strings.HasPrefix(oid, highPrefix) {
			if val, ok := toFloat(v.Value); ok && val > 0 {
				speed[strings.TrimPrefix(oid, highPrefix)] = val * 1e6
			}
		}
	}
	for ifIndex, val := range speed {
		samples = append(samples, sdk.Sample{
			Name:   "netinv_if_speed_bps",
			Labels: map[string]string{"if_index": ifIndex},
			Value:  val,
			At:     now,
		})
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

// Bounds on one poll's repair work. A batch of 24 varbinds fits comfortably in
// a default 1472-byte UDP payload, and the total cap stops a pathological agent
// on a 500-interface chassis from turning every poll into a probe storm — a
// device that broken is better reported than exhaustively worked around.
const (
	probeBatch       = 24
	maxProbeVarbinds = 600
)

// fillCounterGaps repairs a partial walk with targeted GETs, appending to
// *samples and returning how many varbinds it recovered.
//
// Some SNMP agents enumerate every interface in the early ifTable columns and
// then return almost nothing for the counter columns, while a GET for those
// exact OIDs answers correctly — the traversal is broken, not the data. A pilot
// UniFi gateway entered this state and stayed there: ifIndex, ifDescr,
// ifAdminStatus and ifOperStatus walked all 47 interfaces, every counter column
// walked 6, and a GET returned real values for 45 of them (doc 10 §7).
//
// Without this the device looks perfectly healthy — it answers, it is up, its
// inventory is complete, its poll succeeds — and simply has no traffic graphs.
//
// The repair is deliberately narrow. A column that returns *nothing* is treated
// as absent rather than broken and is never probed, because that is what a
// 32-bit-only agent's missing ifXTable looks like and probing it every poll
// would buy a PDU of noSuchInstance forever. A column the walk already covered
// in full costs nothing, so healthy devices issue no extra packets at all.
func fillCounterGaps(ctx context.Context, s sdk.Session, ifT map[string]sdk.Var,
	pick func(string) map[string]sdk.Var, hcSeen map[string]bool,
	now time.Time, samples *[]sdk.Sample) int {

	// Every interface the agent named anywhere in ifTable. Taking the union
	// rather than ifIndex alone matters twice over: a broken traversal is
	// exactly the case where one particular column may be short, and not every
	// agent even returns the ifIndex column, since its value duplicates the
	// instance identifier it is keyed by.
	seen := map[string]bool{}
	var idxs []string
	for oid := range ifT {
		_, idx, ok := splitCol(oid, oidIfTable)
		if !ok {
			continue
		}
		key := strconv.Itoa(idx)
		if !seen[key] {
			seen[key] = true
			idxs = append(idxs, key)
		}
	}
	if len(idxs) == 0 {
		return 0
	}
	// Probe in index order so a truncated budget takes a stable prefix rather
	// than a different arbitrary subset each poll, which would make the graphs
	// flicker between interfaces.
	sort.Slice(idxs, func(i, j int) bool {
		a, aerr := strconv.Atoi(idxs[i])
		b, berr := strconv.Atoi(idxs[j])
		if aerr == nil && berr == nil {
			return a < b
		}
		return idxs[i] < idxs[j]
	})

	repaired, budget := 0, maxProbeVarbinds
	// Same order as the main loop, so an HC column still claims an interface
	// before its 32-bit fallback is considered for it.
	for _, cm := range trafficCols {
		if budget <= 0 {
			break
		}
		have := pick(cm.table)
		prefix := cm.table + "." + strconv.Itoa(cm.column) + "."
		present := 0
		for oid := range have {
			if strings.HasPrefix(oid, prefix) {
				present++
			}
		}
		if present == 0 || present >= len(idxs) {
			continue // absent column, or one the walk already covered
		}
		var missing []string
		for _, idx := range idxs {
			if _, ok := have[prefix+idx]; ok {
				continue
			}
			if hcSeen[cm.metric+"/"+idx] && !cm.hc {
				continue
			}
			missing = append(missing, prefix+idx)
		}
		for start := 0; start < len(missing) && budget > 0; start += probeBatch {
			end := min(start+probeBatch, len(missing))
			chunk := missing[start:end]
			if len(chunk) > budget {
				chunk = chunk[:budget]
			}
			budget -= len(chunk)
			vars, err := s.Get(ctx, chunk)
			if err != nil {
				// The agent dislikes the probe; leave the gap rather than
				// hammering it. The walk's results are already collected.
				break
			}
			for _, v := range vars {
				if !strings.HasPrefix(v.OID, prefix) {
					continue // never label a sample with an unexpected OID
				}
				val, ok := toFloat(v.Value)
				if !ok {
					continue // noSuchInstance / noSuchObject arrive unconvertible
				}
				idx := strings.TrimPrefix(v.OID, prefix)
				key := cm.metric + "/" + idx
				if hcSeen[key] && !cm.hc {
					continue
				}
				if cm.hc {
					hcSeen[key] = true
				}
				repaired++
				*samples = append(*samples, sdk.Sample{
					Name:   cm.metric,
					Labels: map[string]string{"if_index": idx},
					Value:  val,
					At:     now,
				})
			}
		}
	}
	return repaired
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
