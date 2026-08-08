package ubiquiti

import (
	"testing"

	"github.com/freezxp/netinv/connectors/sdk"
	_ "github.com/freezxp/netinv/connectors/generic" // register the floor
)

// UniFi OS consoles report net-snmp's sysObjectID, so identification has to
// fall back to sysDescr (real UDM-Pro strings from pilot validation).
func TestMatchUDMPro(t *testing.T) {
	cases := []struct {
		name  string
		sys   sdk.SysInfo
		match bool
	}{
		{"UDM-Pro descr", sdk.SysInfo{
			SysObjectID: ".1.3.6.1.4.1.8072.3.2.10",
			SysDescr:    "Ubiquiti UniFi UDM-Pro 5.1.26 Linux 4.19.152 al324"}, true},
		{"airOS by enterprise OID", sdk.SysInfo{
			SysObjectID: ".1.3.6.1.4.1.10002.1"}, true},
		{"UniFi switch by enterprise OID", sdk.SysInfo{
			SysObjectID: ".1.3.6.1.4.1.41112.1.6"}, true},
		{"unrelated linux host", sdk.SysInfo{
			SysObjectID: ".1.3.6.1.4.1.8072.3.2.10",
			SysDescr:    "Linux db-01 5.15.0 x86_64"}, false},
	}
	c := New()
	for _, tc := range cases {
		got := c.Match(tc.sys) > 0
		if got != tc.match {
			t.Errorf("%s: matched=%v, want %v", tc.name, got, tc.match)
		}
	}
}

// A vendor match must outrank the generic universal floor.
func TestBeatsGenericFloor(t *testing.T) {
	sys := sdk.SysInfo{SysObjectID: ".1.3.6.1.4.1.8072.3.2.10",
		SysDescr: "Ubiquiti UniFi UDM-Pro 5.1.26"}
	best, score := sdk.BestMatch(sys)
	if best == nil || best.Info().ID != "ubiquiti" {
		t.Fatalf("best match = %v (score %d), want ubiquiti", best, score)
	}
}
