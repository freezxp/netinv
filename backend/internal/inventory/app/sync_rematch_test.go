package app

import (
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// A FortiGate-VM added by address starts on generic. Before rematch existed
// nothing ever moved it: only discovery scored sysObjectID, so a hand-added
// device polled through IF-MIB alone for as long as it existed.
func TestRematchMovesGenericDeviceToVendorConnector(t *testing.T) {
	var gotOID string
	s := &SyncService{Match: func(oid, _ string) string {
		gotOID = oid
		return "fortinet-fortios"
	}}
	cur := &DeviceState{DeviceID: "dev_1", ConnectorID: "generic"}
	snap := &wire.SyncSnapshot{SysObjectID: ".1.3.6.1.4.1.12356.101.1.60"}
	diff := DiffResult{DeviceFields: map[string]string{}}

	if got := s.rematch(cur, snap, &diff); got != "fortinet-fortios" {
		t.Fatalf("rematch = %q, want fortinet-fortios", got)
	}
	if gotOID != snap.SysObjectID {
		t.Errorf("matcher saw %q, want the collected sysObjectID", gotOID)
	}
	if diff.DeviceFields["connector_id"] != "fortinet-fortios" {
		t.Errorf("connector_id not written to the diff: %v", diff.DeviceFields)
	}
	if len(diff.Changes) != 1 || diff.Changes[0].Field != "connector_id" ||
		diff.Changes[0].Old != "generic" {
		t.Errorf("asset history should record generic → fortinet-fortios, got %+v", diff.Changes)
	}
}

func TestRematchNeverOverridesAChosenConnector(t *testing.T) {
	s := &SyncService{Match: func(string, string) string { return "cisco-ios" }}
	for _, chosen := range []string{"ubiquiti", "fortinet-fortios"} {
		diff := DiffResult{DeviceFields: map[string]string{}}
		got := s.rematch(&DeviceState{ConnectorID: chosen},
			&wire.SyncSnapshot{SysObjectID: ".1.3.6.1.4.1.9.1.1"}, &diff)
		if got != "" || len(diff.DeviceFields) != 0 {
			t.Errorf("%s was overridden to %q", chosen, got)
		}
	}
}

func TestRematchLeavesGenericWhenNothingBetterMatches(t *testing.T) {
	s := &SyncService{Match: func(string, string) string { return "generic" }}
	diff := DiffResult{DeviceFields: map[string]string{}}
	if got := s.rematch(&DeviceState{ConnectorID: "generic"},
		&wire.SyncSnapshot{SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, &diff); got != "" {
		t.Errorf("rematch = %q, want no change", got)
	}
	if len(diff.Changes) != 0 {
		t.Errorf("no-op match wrote history: %+v", diff.Changes)
	}
}
