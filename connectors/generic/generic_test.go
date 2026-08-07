package generic

import (
	"context"
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
