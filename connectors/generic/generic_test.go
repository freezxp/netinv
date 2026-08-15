package generic

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
)

// fakeSession replays a recorded walk (doc 24 §2 fixture style, inline here;
// file-based .snmpwalk fixtures arrive with the vendor connectors).
type fakeSession struct{ data map[string]any }

func (f *fakeSession) Get(_ context.Context, oids []string) ([]sdk.Var, error) {
	var out []sdk.Var
	for _, oid := range oids {
		if v, ok := f.data[oid]; ok {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}

func (f *fakeSession) Walk(_ context.Context, root string) ([]sdk.Var, error) {
	var out []sdk.Var
	for oid, v := range f.data {
		if strings.HasPrefix(oid, root+".") {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}

func (f *fakeSession) Target() sdk.TargetMeta {
	return sdk.TargetMeta{Address: "test", Port: 161}
}

func demoDevice() *fakeSession {
	return &fakeSession{data: map[string]any{
		".1.3.6.1.2.1.1.1.0": "NetInv demo device",
		".1.3.6.1.2.1.1.2.0": ".1.3.6.1.4.1.8072.3.2.10",
		".1.3.6.1.2.1.1.3.0": uint64(123456700), // centiseconds
		".1.3.6.1.2.1.1.5.0": "demo-sw-1",
		".1.3.6.1.2.1.1.6.0": "DC-East lab",
		// ifTable: if 1 = lo0 (32-bit only), if 2 = eth0 (has HC)
		".1.3.6.1.2.1.2.2.1.2.1":  "lo0",
		".1.3.6.1.2.1.2.2.1.2.2":  "eth0",
		".1.3.6.1.2.1.2.2.1.5.2":  uint64(1000000000),
		".1.3.6.1.2.1.2.2.1.7.2":  1,
		".1.3.6.1.2.1.2.2.1.8.2":  1,
		".1.3.6.1.2.1.2.2.1.10.1": uint64(11111), // lo0 32-bit in-octets
		".1.3.6.1.2.1.2.2.1.10.2": uint64(999),   // eth0 32-bit (must lose to HC)
		".1.3.6.1.2.1.2.2.1.14.2": uint64(7),     // in errors
		// ifXTable for eth0
		".1.3.6.1.2.1.31.1.1.1.1.2":  "eth0",
		".1.3.6.1.2.1.31.1.1.1.6.2":  uint64(9876543210123), // ifHCInOctets
		".1.3.6.1.2.1.31.1.1.1.10.2": uint64(1234567890123),
		".1.3.6.1.2.1.31.1.1.1.15.2": uint64(1000), // Mbps
		".1.3.6.1.2.1.31.1.1.1.18.2": "uplink-to-core",
	}}
}

func find(samples []sdk.Sample, name, ifIndex string) (float64, bool) {
	for _, s := range samples {
		if s.Name == name && s.Labels["if_index"] == ifIndex {
			return s.Value, true
		}
	}
	return 0, false
}

func TestCollectInterfacesHCPrecedence(t *testing.T) {
	samples, err := New().CollectInterfaces(context.Background(), demoDevice())
	if err != nil {
		t.Fatal(err)
	}
	// eth0 must use the 64-bit counter, not the 32-bit fallback (FR-COLL-03).
	if v, ok := find(samples, "netinv_if_in_octets_total", "2"); !ok || v != 9876543210123 {
		t.Errorf("eth0 in-octets = %v (found=%v), want HC value 9876543210123", v, ok)
	}
	// lo0 has no HC counters — 32-bit fallback must apply.
	if v, ok := find(samples, "netinv_if_in_octets_total", "1"); !ok || v != 11111 {
		t.Errorf("lo0 in-octets = %v (found=%v), want 32-bit 11111", v, ok)
	}
	if v, ok := find(samples, "netinv_if_in_errors_total", "2"); !ok || v != 7 {
		t.Errorf("eth0 in-errors = %v, want 7", v)
	}
	if v, ok := find(samples, "netinv_if_speed_bps", "2"); !ok || v != 1e9 {
		t.Errorf("eth0 speed = %v, want 1e9", v)
	}
	if v, ok := find(samples, "netinv_if_oper_status", "2"); !ok || v != 1 {
		t.Errorf("eth0 oper status = %v, want 1", v)
	}
}

func TestCollectInventory(t *testing.T) {
	snap, err := New().CollectInventory(context.Background(), demoDevice())
	if err != nil {
		t.Fatal(err)
	}
	if snap.SysName != "demo-sw-1" || snap.UptimeS != 1234567 {
		t.Errorf("sysname=%q uptime=%d", snap.SysName, snap.UptimeS)
	}
	if len(snap.Interfaces) != 2 {
		t.Fatalf("interfaces = %d, want 2", len(snap.Interfaces))
	}
	var eth0 *sdk.InterfaceRecord
	for i := range snap.Interfaces {
		if snap.Interfaces[i].IfIndex == 2 {
			eth0 = &snap.Interfaces[i]
		}
	}
	if eth0 == nil || eth0.Name != "eth0" || eth0.Alias != "uplink-to-core" ||
		eth0.SpeedBPS != 1e9 {
		t.Errorf("eth0 = %+v", eth0)
	}
}

func TestRegistryMatch(t *testing.T) {
	c, ok := sdk.ByID("generic")
	if !ok {
		t.Fatal("generic not registered")
	}
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.9.1.1"}); s != 1 {
		t.Errorf("generic score = %d, want universal floor 1", s)
	}
}

// ifHighSpeed is preferred, but an agent that leaves it at zero and populates
// only ifSpeed must still yield a speed. A Ruckus R710 does exactly that on
// every port, and a zero denominator pins utilisation at 0% however busy the
// link is.
func TestSpeedFallsBackToIfSpeedWhenHighSpeedIsZero(t *testing.T) {
	sess := &fakeSession{data: map[string]any{
		".1.3.6.1.2.1.2.2.1.5.14":     uint64(1000000000), // ifSpeed
		".1.3.6.1.2.1.31.1.1.1.15.14": uint64(0),          // ifHighSpeed unset
	}}
	samples, err := (&Base{}).CollectInterfaces(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := find(samples, "netinv_if_speed_bps", "14")
	if !ok {
		t.Fatal("no speed reported at all")
	}
	if v != 1e9 {
		t.Errorf("speed = %v, want 1e9 from ifSpeed", v)
	}
}

// When both are present ifHighSpeed wins: ifSpeed is a 32-bit gauge that
// saturates above ~4.29 Gbit/s, so on a 10G port it reports the ceiling.
func TestSpeedPrefersIfHighSpeedOverSaturatedIfSpeed(t *testing.T) {
	sess := &fakeSession{data: map[string]any{
		".1.3.6.1.2.1.2.2.1.5.7":     uint64(4294967295), // saturated
		".1.3.6.1.2.1.31.1.1.1.15.7": uint64(10000),      // 10 Gbit/s
	}}
	samples, err := (&Base{}).CollectInterfaces(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := find(samples, "netinv_if_speed_bps", "7"); !ok || v != 1e10 {
		t.Errorf("speed = %v (ok=%v), want 1e10 from ifHighSpeed", v, ok)
	}
}

// brokenWalkSession models the SNMP agent seen on a pilot UniFi gateway
// (doc 10 §7): the config columns walk every interface, the counter columns
// walk almost none, and a GET for those very OIDs answers correctly. The bug is
// in the agent's GETNEXT traversal, not in its data.
type brokenWalkSession struct {
	data       map[string]any
	walkableK  func(oid string) bool
	gets       int // GET calls issued, to hold the repair to a packet budget
	maxVarbind int
}

func (f *brokenWalkSession) Get(_ context.Context, oids []string) ([]sdk.Var, error) {
	f.gets++
	if len(oids) > f.maxVarbind {
		f.maxVarbind = len(oids)
	}
	var out []sdk.Var
	for _, oid := range oids {
		if v, ok := f.data[oid]; ok {
			out = append(out, sdk.Var{OID: oid, Value: v})
			continue
		}
		// A real agent answers the whole PDU, using a null value for the
		// instances it does not have. Returning them keeps the test honest
		// about the repair having to filter.
		out = append(out, sdk.Var{OID: oid, Value: nil})
	}
	return out, nil
}

func (f *brokenWalkSession) Walk(_ context.Context, root string) ([]sdk.Var, error) {
	var out []sdk.Var
	for oid, v := range f.data {
		if strings.HasPrefix(oid, root+".") && f.walkableK(oid) {
			out = append(out, sdk.Var{OID: oid, Value: v})
		}
	}
	return out, nil
}

func (f *brokenWalkSession) Target() sdk.TargetMeta {
	return sdk.TargetMeta{Address: "test", Port: 161}
}

// gatewayWithBrokenCounterWalk builds 20 interfaces whose status columns walk
// but whose counters are reachable only by GET — except if_index 9, which walks
// like the handful of stub tunnels the real device still enumerated.
func gatewayWithBrokenCounterWalk() *brokenWalkSession {
	data := map[string]any{}
	// Counters are built in uint64 throughout: an SNMP Counter64 arrives as one,
	// and converting from int here trips gosec's overflow check for no gain.
	for i := uint64(1); i <= 20; i++ {
		idx := strconv.FormatUint(i, 10)
		data[".1.3.6.1.2.1.2.2.1.1."+idx] = i
		data[".1.3.6.1.2.1.2.2.1.2."+idx] = "eth" + idx
		data[".1.3.6.1.2.1.2.2.1.7."+idx] = 1
		data[".1.3.6.1.2.1.2.2.1.8."+idx] = 1
		data[".1.3.6.1.2.1.2.2.1.10."+idx] = 1000 + i
		data[".1.3.6.1.2.1.2.2.1.14."+idx] = i
		data[".1.3.6.1.2.1.31.1.1.1.6."+idx] = 5_000_000_000 + i
	}
	return &brokenWalkSession{
		data: data,
		walkableK: func(oid string) bool {
			// Counter columns walk only for if_index 9.
			for _, col := range []string{
				".1.3.6.1.2.1.2.2.1.10.", ".1.3.6.1.2.1.2.2.1.14.",
				".1.3.6.1.2.1.31.1.1.1.6.",
			} {
				if strings.HasPrefix(oid, col) {
					return strings.TrimPrefix(oid, col) == "9"
				}
			}
			return true
		},
	}
}

func TestPartialCounterWalkIsRepairedByGet(t *testing.T) {
	sess := gatewayWithBrokenCounterWalk()
	samples, err := New().CollectInterfaces(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	// Every interface must carry traffic, not just the one the walk returned.
	for i := uint64(1); i <= 20; i++ {
		idx := strconv.FormatUint(i, 10)
		v, ok := find(samples, "netinv_if_in_octets_total", idx)
		if !ok {
			t.Fatalf("if %s has no in-octets: the partial walk was not repaired", idx)
		}
		// And the repair must keep HC precedence — a GET-recovered 64-bit
		// counter still outranks a 32-bit one for the same interface.
		if want := float64(5_000_000_000 + i); v != want {
			t.Errorf("if %s in-octets = %v, want HC %v", idx, v, want)
		}
	}
	if v, ok := find(samples, "netinv_if_in_errors_total", "20"); !ok || v != 20 {
		t.Errorf("if 20 in-errors = %v (found=%v), want 20", v, ok)
	}
	// The repair is reported, so a broken agent is visible rather than merely
	// worked around.
	var repaired float64
	for _, s := range samples {
		if s.Name == "netinv_if_counters_repaired" {
			repaired = s.Value
		}
	}
	if repaired == 0 {
		t.Error("netinv_if_counters_repaired = 0, want the repaired varbind count")
	}
	if sess.maxVarbind > probeBatch {
		t.Errorf("GET carried %d varbinds, want <= %d", sess.maxVarbind, probeBatch)
	}
}

func TestHealthyWalkIssuesNoProbes(t *testing.T) {
	// The demo device's counter columns are complete for the interfaces that
	// have them, so repair must not add packets to a healthy poll.
	sess := &brokenWalkSession{
		data:      demoDevice().data,
		walkableK: func(string) bool { return true },
	}
	samples, err := New().CollectInterfaces(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		if s.Name == "netinv_if_counters_repaired" && s.Value != 0 {
			t.Errorf("repaired = %v on a healthy device, want 0", s.Value)
		}
	}
}

func TestAbsentColumnIsNotProbedEveryPoll(t *testing.T) {
	// A pure 32-bit agent has no ifXTable at all. That is an absent column, not
	// a broken walk, and probing it would buy a PDU of nulls on every poll.
	data := map[string]any{}
	for i := uint64(1); i <= 8; i++ {
		idx := strconv.FormatUint(i, 10)
		data[".1.3.6.1.2.1.2.2.1.1."+idx] = i
		data[".1.3.6.1.2.1.2.2.1.10."+idx] = 100 + i
	}
	sess := &brokenWalkSession{data: data, walkableK: func(string) bool { return true }}
	if _, err := New().CollectInterfaces(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if sess.gets != 0 {
		t.Errorf("issued %d GETs against an agent with no ifXTable, want 0", sess.gets)
	}
}
