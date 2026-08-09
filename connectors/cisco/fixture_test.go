package cisco

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// Recorded from the bundled SNMP simulator's cisco profile rather than from
// hardware — this connector has never met a real IOS device (doc 10 §5, risk
// R-07), and a fixture that pretends otherwise would be worse than none.
//
// What it pins is therefore modest but real: that a Cisco enterprise
// sysObjectID routes to this connector rather than falling through to generic.
// That is the exact failure the ubiquiti connector hit in the field, where a
// device reported an unexpected sysObjectID and silently lost vendor identity.
func TestCiscoSysObjectIDRoutesHere(t *testing.T) {
	f := sdktest.Load(t, "testdata/cisco.snmpwalk")
	vars, err := f.Get(context.Background(), []string{".1.3.6.1.2.1.1.2.0"})
	if err != nil || len(vars) != 1 {
		t.Fatalf("fixture has no sysObjectID: %v", err)
	}
	sysOID, _ := vars[0].Value.(string)

	score := New().Match(sdk.SysInfo{SysObjectID: sysOID})
	if score <= 0 {
		t.Fatalf("sysObjectID %q scored %d; an IOS device would be handled by "+
			"the generic connector and report no vendor health", sysOID, score)
	}

	// The simulator is a stand-in, so assert the shape of what it returns
	// rather than a specific model string that a real device would differ on.
	descr, err := f.Get(context.Background(), []string{".1.3.6.1.2.1.1.1.0"})
	if err != nil || len(descr) != 1 {
		t.Fatalf("fixture has no sysDescr: %v", err)
	}
	if len(descr[0].Value.([]byte)) == 0 {
		t.Error("sysDescr is empty; inventory would show an unidentified device")
	}
}

// Health collection must not invent readings when the agent has none. The
// simulator serves only the system group, so every vendor health OID is absent
// — which is the same situation as a locked-down real device.
func TestNoHealthInventedFromASystemOnlyAgent(t *testing.T) {
	f := sdktest.Load(t, "testdata/cisco.snmpwalk")
	samples, err := New().CollectHealth(context.Background(), f)
	if err != nil {
		t.Fatalf("CollectHealth: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("produced %d samples from an agent exposing no health OIDs: %+v",
			len(samples), samples)
	}
}
