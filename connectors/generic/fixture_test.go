package generic

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	"github.com/freezxp/netinv/connectors/sdk/sdktest"
)

// Recorded from the bundled SNMP simulator, which is the reproducible case:
// any contributor can regenerate this fixture with `make dev` and
// scripts/record-fixture.sh, without hardware and without redaction.
func TestGenericReadsTheSystemGroup(t *testing.T) {
	f := sdktest.Load(t, "testdata/generic.snmpwalk")
	snap, err := New().CollectInventory(context.Background(), f)
	if err != nil {
		t.Fatalf("CollectInventory: %v", err)
	}
	if snap.SysDescr == "" {
		t.Error("sysDescr is empty; the device would appear unidentified in inventory")
	}
	if snap.SysObjectID == "" {
		t.Error("sysObjectID is empty; connector auto-matching has nothing to key on")
	}
}

// generic is the universal floor: it must match anything, including the
// net-snmp sysObjectID that this fixture carries and that real UniFi consoles
// also report. A zero score here would leave unknown devices unmonitorable,
// which is the opposite of NFR-62.
func TestGenericMatchesAnythingAsTheFloor(t *testing.T) {
	f := sdktest.Load(t, "testdata/generic.snmpwalk")
	vars, err := f.Get(context.Background(), []string{".1.3.6.1.2.1.1.2.0"})
	if err != nil || len(vars) != 1 {
		t.Fatalf("fixture has no sysObjectID: %v", err)
	}
	sysOID, _ := vars[0].Value.(string)

	c := New()
	for _, sys := range []sdk.SysInfo{
		{SysObjectID: sysOID},
		{SysObjectID: ".1.3.6.1.4.1.9.1.1745"}, // a Cisco
		{},                                     // an agent that says nothing
	} {
		if s := c.Match(sys); s <= 0 {
			t.Errorf("generic scored %d for %+v; it is the fallback and must "+
				"always match", s, sys)
		}
	}
}
