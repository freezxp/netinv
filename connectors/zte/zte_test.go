package zte

import (
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
)

func TestMatchesZTEEnterpriseOID(t *testing.T) {
	c := New()
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.3902.1015.1"}); s <= 0 {
		t.Errorf("score for a ZTE sysObjectID = %d, want > 0", s)
	}
	if s := c.Match(sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.9.1.1745"}); s != 0 {
		t.Errorf("score for a Cisco sysObjectID = %d, want 0", s)
	}
	if s := c.Match(sdk.SysInfo{}); s != 0 {
		t.Errorf("score for an empty sysObjectID = %d, want 0", s)
	}
}

// This connector is deliberately identification-only until it meets real
// hardware (doc 10 §5, risk R-07): ZXR10 health MIB coverage varies by product
// line, so there is no honest health map to ship yet. What it must not do is
// *claim* health and then return nothing — that shows an operator empty CPU
// graphs and no reason why, which is worse than showing none.
//
// It inherits generic's IF-MIB/system/LLDP collection, and generic declares
// CapHealth for its UCD-SNMP/LM-SENSORS best-effort. So the test pins the real
// contract: no ZTE-specific health override exists.
func TestShipsNoVendorHealthOverride(t *testing.T) {
	var c any = New()
	if _, ok := c.(interface {
		CollectHealth(...any) ([]sdk.Sample, error)
	}); ok {
		t.Fatal("unexpected vendor CollectHealth signature")
	}
	if got, want := New().Info().DisplayName, "ZTE ZXR10 (best-effort)"; got != want {
		t.Errorf("display name = %q, want %q — operators should see the caveat "+
			"in the UI, not discover it in a doc", got, want)
	}
}

func TestInheritsGenericCollection(t *testing.T) {
	caps := map[sdk.Capability]bool{}
	for _, c := range New().Capabilities() {
		caps[c] = true
	}
	// Traffic and inventory are the reason a ZXR10 is worth adding at all: any
	// RFC-compliant agent gives them up, no vendor MIB required (NFR-62).
	for _, want := range []sdk.Capability{sdk.CapInventory, sdk.CapInterfaces} {
		if !caps[want] {
			t.Errorf("capability %q missing; generic.Base is not embedded correctly", want)
		}
	}
}

func TestRegisteredUnderStableID(t *testing.T) {
	if id := New().Info().ID; id != "zte-zxr" {
		t.Errorf("connector ID = %q; it is stored on every device row and must not drift", id)
	}
	if v := New().Info().Vendor; v != "ZTE" {
		t.Errorf("vendor = %q, want ZTE", v)
	}
}
