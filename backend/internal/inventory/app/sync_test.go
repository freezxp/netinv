package app

import (
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

var now = time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

func baseState() DeviceState {
	basis := now.Add(-1000 * time.Second)
	return DeviceState{
		DeviceID: "d_1", SysName: "sw1", UptimeBasis: &basis,
		Interfaces: []IfState{
			{ID: "if_a", IfIndex: 1, Name: "lo0", State: "present"},
			{ID: "if_b", IfIndex: 2, Name: "eth0", Alias: "uplink",
				SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6, State: "present"},
		},
	}
}

func snap(ifs ...wire.SyncInterface) wire.SyncSnapshot {
	return wire.SyncSnapshot{SysName: "sw1", UptimeS: 1000, Interfaces: ifs}
}

func hasChange(res DiffResult, kind, field, newVal string) bool {
	for _, c := range res.Changes {
		if c.ChangeKind == kind && c.Field == field && c.New == newVal {
			return true
		}
	}
	return false
}

func TestDiffNoChanges(t *testing.T) {
	res := Diff(baseState(), snap(
		wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "eth0", Alias: "uplink",
			SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6},
	), now)
	if len(res.Changes) != 0 || len(res.MissingIDs) != 0 || res.Rebooted {
		t.Fatalf("expected clean diff, got %+v", res)
	}
	if len(res.Upserts) != 2 {
		t.Fatalf("upserts = %d, want 2 (refresh both)", len(res.Upserts))
	}
}

func TestDiffAliasChange(t *testing.T) {
	res := Diff(baseState(), snap(
		wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "eth0", Alias: "uplink-to-west",
			SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6},
	), now)
	if !hasChange(res, "updated", "alias", "uplink-to-west") {
		t.Fatalf("alias change not detected: %+v", res.Changes)
	}
}

func TestDiffRebootReindex(t *testing.T) {
	// After reboot, eth0 comes back with a shifted ifIndex — identity must
	// survive via name match (FR-DEV-09), and the reboot must be flagged.
	res := Diff(baseState(), wire.SyncSnapshot{
		SysName: "sw1", UptimeS: 60, // rebooted 1 minute ago
		Interfaces: []wire.SyncInterface{
			{IfIndex: 1, Name: "lo0"},
			{IfIndex: 9, Name: "eth0", Alias: "uplink", SpeedBPS: 1e9,
				PhysAddress: "aa:bb:cc:00:00:01", IfType: 6},
		},
	}, now)
	if !res.Rebooted {
		t.Fatal("reboot not detected")
	}
	var matched bool
	for _, u := range res.Upserts {
		if u.ExistingID == "if_b" && u.Rec.IfIndex == 9 {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("eth0 identity lost on reindex: %+v", res.Upserts)
	}
	if !hasChange(res, "updated", "if_index", "9") {
		t.Fatal("reindex not recorded in history")
	}
	if len(res.MissingIDs) != 0 {
		t.Fatalf("reindexed interface wrongly marked missing")
	}
}

func TestDiffRenameSurvivesViaPhysAddr(t *testing.T) {
	// Name changed but MAC+type identical → linecard identity match (rule 3).
	res := Diff(baseState(), snap(
		wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "Ethernet0", Alias: "uplink",
			SpeedBPS: 1e9, PhysAddress: "AA:BB:CC:00:00:01", IfType: 6},
	), now)
	var matched bool
	for _, u := range res.Upserts {
		if u.ExistingID == "if_b" {
			matched = true
		}
	}
	if !matched {
		t.Fatal("physAddress identity match failed")
	}
	if !hasChange(res, "updated", "name", "Ethernet0") {
		t.Fatal("rename not recorded")
	}
}

func TestDiffMissingLifecycle(t *testing.T) {
	// Sync 1: eth0 absent → missing.
	res := Diff(baseState(), snap(wire.SyncInterface{IfIndex: 1, Name: "lo0"}), now)
	if len(res.MissingIDs) != 1 || res.MissingIDs[0] != "if_b" {
		t.Fatalf("missing = %v, want [if_b]", res.MissingIDs)
	}

	// Sync 2 (streak 1 → 2): still absent, below threshold.
	st := baseState()
	st.Interfaces[1].State, st.Interfaces[1].MissStreak = "missing", 1
	res = Diff(st, snap(wire.SyncInterface{IfIndex: 1, Name: "lo0"}), now)
	if len(res.StreakIDs) != 1 || len(res.RemovedIDs) != 0 {
		t.Fatalf("streak=%v removed=%v, want streak only", res.StreakIDs, res.RemovedIDs)
	}

	// Sync 3 (streak 2 → threshold): removed.
	st.Interfaces[1].MissStreak = 2
	res = Diff(st, snap(wire.SyncInterface{IfIndex: 1, Name: "lo0"}), now)
	if len(res.RemovedIDs) != 1 {
		t.Fatalf("removed = %v, want [if_b]", res.RemovedIDs)
	}

	// Reappearance from missing → restored.
	st.Interfaces[1].State, st.Interfaces[1].MissStreak = "missing", 2
	res = Diff(st, snap(
		wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "eth0", Alias: "uplink",
			SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6},
	), now)
	if len(res.RestoredIDs) != 1 || res.RestoredIDs[0] != "if_b" {
		t.Fatalf("restored = %v, want [if_b]", res.RestoredIDs)
	}
}

func TestDiffNewInterface(t *testing.T) {
	res := Diff(baseState(), snap(
		wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "eth0", Alias: "uplink",
			SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6},
		wire.SyncInterface{IfIndex: 3, Name: "eth1"},
	), now)
	var created int
	for _, u := range res.Upserts {
		if u.ExistingID == "" {
			created++
		}
	}
	if created != 1 || !hasChange(res, "created", "interface", "eth1") {
		t.Fatalf("new interface not detected: %+v", res.Upserts)
	}
}

func TestDiffDeviceFieldChange(t *testing.T) {
	st := baseState()
	st.SysLocation = "old rack"
	s := snap(wire.SyncInterface{IfIndex: 1, Name: "lo0"},
		wire.SyncInterface{IfIndex: 2, Name: "eth0", Alias: "uplink",
			SpeedBPS: 1e9, PhysAddress: "aa:bb:cc:00:00:01", IfType: 6})
	s.SysLocation = "new rack"
	res := Diff(st, s, now)
	if res.DeviceFields["sys_location"] != "new rack" ||
		!hasChange(res, "updated", "sys_location", "new rack") {
		t.Fatalf("device field change missed: %+v", res)
	}
}
