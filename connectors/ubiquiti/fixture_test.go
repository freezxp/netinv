package ubiquiti

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// Recorded from the pilot's UDM-Pro (UniFi OS 5.1.26), redacted at capture
// time. Doc 10 makes two specific claims about this hardware that no MIB
// document would support; both are pinned here against the real walk.

func fixture(t *testing.T) *sdktest.Session {
	t.Helper()
	return sdktest.Load(t, "testdata/ubiquiti.snmpwalk")
}

// Claim 1: a UniFi console runs stock net-snmp and reports the net-snmp
// sysObjectID, not a Ubiquiti one — so prefix matching alone never sees it,
// and the connector needs the sysDescr fallback. If this ever stops being
// true the connector is over-matching and should be simplified.
func TestUniFiConsoleReportsNetSNMPObjectID(t *testing.T) {
	vars, err := fixture(t).Get(context.Background(), []string{".1.3.6.1.2.1.1.2.0"})
	if err != nil || len(vars) != 1 {
		t.Fatalf("fixture has no sysObjectID: %v", err)
	}
	sysOID, _ := vars[0].Value.(string)
	if !strings.HasPrefix(sysOID, ".1.3.6.1.4.1.8072") {
		t.Fatalf("sysObjectID = %q, expected the net-snmp arc .1.3.6.1.4.1.8072", sysOID)
	}

	c := New()
	// The Ubiquiti enterprise prefixes must NOT match this device...
	if s := c.Match(sdk.SysInfo{SysObjectID: sysOID}); s > 0 {
		t.Errorf("matched on sysObjectID alone (score %d); that is not what "+
			"this device reports and the score is misleading", s)
	}
	// ...and the sysDescr fallback is what has to carry it.
	descr := descrFrom(t, fixture(t))
	if s := c.Match(sdk.SysInfo{SysObjectID: sysOID, SysDescr: descr}); s <= 0 {
		t.Errorf("sysDescr %q did not match; a real UDM-Pro would fall through "+
			"to the generic connector and lose vendor identity", descr)
	}
}

// Claim 2: UniFi OS consoles expose UCD-SNMP and LM-SENSORS but not the UniFi
// MIB, not HOST-RESOURCES and not LLDP. The absences are the interesting part
// — they are why this connector inherits generic's health set instead of
// implementing its own, and only a recorded walk can evidence them.
func TestUDMProExposesUCDButNotUniFiMIBOrLLDP(t *testing.T) {
	f := fixture(t)
	for _, present := range []struct{ name, root string }{
		{"UCD-SNMP (memory/load)", ".1.3.6.1.4.1.2021"},
		{"IF-MIB ifTable", ".1.3.6.1.2.1.2.2.1"},
		{"IF-MIB ifXTable", ".1.3.6.1.2.1.31.1.1.1"},
	} {
		if !f.Has(present.root) {
			t.Errorf("%s (%s) is missing from the walk; the connector depends on it",
				present.name, present.root)
		}
	}
	for _, absent := range []struct{ name, root string }{
		{"UniFi enterprise MIB", ".1.3.6.1.4.1.41112"},
		{"HOST-RESOURCES", ".1.3.6.1.2.1.25"},
		{"LLDP-MIB", ".1.0.8802.1.1.2"},
	} {
		if f.Has(absent.root) {
			t.Errorf("%s (%s) IS present — doc 10 says a UDM-Pro exposes none, "+
				"so either firmware changed or the doc is stale",
				absent.name, absent.root)
		}
	}
}

// Site-to-site tunnels appear as wgstsNNNN interfaces, and the tunnel number is
// identical on both endpoints — that is what pairs them into weathermap links
// without a controller API (FR-MAP-02). They also report ifSpeed 0, which is
// why a link over one needs a capacity set by hand or it never colours.
func TestSDWANTunnelsAppearAsWgstsInterfacesWithNoSpeed(t *testing.T) {
	samples, err := New().CollectInterfaces(context.Background(), fixture(t))
	if err != nil {
		t.Fatalf("CollectInterfaces: %v", err)
	}
	_ = samples

	f := fixture(t)
	names, _ := f.Walk(context.Background(), ".1.3.6.1.2.1.31.1.1.1.1") // ifName
	var tunnels []string
	for _, v := range names {
		if n := string(toBytes(v.Value)); strings.HasPrefix(n, "wgsts") {
			tunnels = append(tunnels, n)
		}
	}
	if len(tunnels) == 0 {
		t.Fatal("no wgsts interfaces in the walk; SD-WAN link pairing has no input")
	}
	for _, n := range tunnels {
		if len(n) <= len("wgsts") {
			t.Errorf("tunnel %q carries no number, so the two endpoints cannot be paired", n)
		}
	}
}

func descrFrom(t *testing.T, f *sdktest.Session) string {
	t.Helper()
	vars, err := f.Get(context.Background(), []string{".1.3.6.1.2.1.1.1.0"})
	if err != nil || len(vars) != 1 {
		t.Fatalf("fixture has no sysDescr: %v", err)
	}
	return string(toBytes(vars[0].Value))
}

func toBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	}
	return nil
}
