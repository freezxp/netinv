// SyncDiffer + SyncService — structural inventory reconciliation (doc 11).
// The differ is a pure function: current state × snapshot → change set.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// RemoveAfterMisses: absent 1 sync → missing; absent this many → removed
// (FR-SYNC-03).
const RemoveAfterMisses = 3

// ---- differ input: current state ----

type IfState struct {
	ID          string
	IfIndex     int
	Name        string
	Alias       string
	Descr       string
	IfType      int
	MTU         int
	SpeedBPS    int64
	PhysAddress string
	AdminStatus int
	OperStatus  int
	State       string // present | missing | removed
	MissStreak  int
}

type DeviceState struct {
	DeviceID    string
	SysName     string
	SysDescr    string
	SysObjectID string
	SysLocation string
	SysContact  string
	UptimeBasis *time.Time
	Interfaces  []IfState
}

// ---- differ output ----

type FieldChange struct {
	ObjectKind string // device | interface
	ObjectID   string // device id or interface id ("" = new interface)
	Field      string
	Old, New   string
	ChangeKind string // created | updated | removed | status
}

type IfUpsert struct {
	ExistingID string // "" = insert
	Rec        wire.SyncInterface
}

type DiffResult struct {
	DeviceFields map[string]string // column → new value (device-owned by network)
	NewUptime    *time.Time        // new uptime basis when changed
	Rebooted     bool
	Upserts      []IfUpsert
	// Reindexed: existing interface matched by identity but ifIndex moved.
	MissingIDs  []string // newly missing this sync
	StreakIDs   []string // still missing (streak++)
	RemovedIDs  []string // streak crossed threshold
	RestoredIDs []string
	Changes     []FieldChange // asset-history rows
}

// Diff computes the reconciliation (doc 11 §3). Identity resolution order:
// (1) same ifIndex + same name, (2) same name, (3) same physAddress+ifType.
func Diff(cur DeviceState, snap wire.SyncSnapshot, collectedAt time.Time) DiffResult {
	res := DiffResult{DeviceFields: map[string]string{}}

	// Device-owned-by-network fields.
	devFields := []struct{ col, old, new string }{
		{"sys_name", cur.SysName, snap.SysName},
		{"sys_descr", cur.SysDescr, snap.SysDescr},
		{"sys_object_id", cur.SysObjectID, snap.SysObjectID},
		{"sys_location", cur.SysLocation, snap.SysLocation},
		{"sys_contact", cur.SysContact, snap.SysContact},
	}
	for _, f := range devFields {
		if f.new != f.old {
			res.DeviceFields[f.col] = f.new
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "device", ObjectID: cur.DeviceID, Field: f.col,
				Old: f.old, New: f.new, ChangeKind: changeKind(f.old),
			})
		}
	}

	// Reboot detection: boot time shifted by more than 5 minutes.
	boot := collectedAt.Add(-time.Duration(snap.UptimeS) * time.Second).UTC()
	if cur.UptimeBasis == nil || absDur(boot.Sub(*cur.UptimeBasis)) > 5*time.Minute {
		res.NewUptime = &boot
		if cur.UptimeBasis != nil && boot.After(*cur.UptimeBasis) {
			res.Rebooted = true
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "device", ObjectID: cur.DeviceID, Field: "uptime",
				Old: cur.UptimeBasis.Format(time.RFC3339), New: boot.Format(time.RFC3339),
				ChangeKind: "status",
			})
		}
	}

	// ---- interface identity resolution ----
	unmatched := map[string]*IfState{} // by ID
	byIdxName := map[string]*IfState{}
	byName := map[string]*IfState{}
	byPhys := map[string]*IfState{}
	for i := range cur.Interfaces {
		ifc := &cur.Interfaces[i]
		if ifc.State == "removed" {
			continue // removed interfaces only come back as brand-new records
		}
		unmatched[ifc.ID] = ifc
		byIdxName[fmt.Sprintf("%d/%s", ifc.IfIndex, ifc.Name)] = ifc
		if ifc.Name != "" {
			byName[ifc.Name] = ifc
		}
		if ifc.PhysAddress != "" {
			byPhys[fmt.Sprintf("%s/%d", strings.ToLower(ifc.PhysAddress), ifc.IfType)] = ifc
		}
	}
	match := func(rec wire.SyncInterface) *IfState {
		if m := byIdxName[fmt.Sprintf("%d/%s", rec.IfIndex, rec.Name)]; m != nil && unmatched[m.ID] != nil {
			return m
		}
		if rec.Name != "" {
			if m := byName[rec.Name]; m != nil && unmatched[m.ID] != nil {
				return m
			}
		}
		if rec.PhysAddress != "" {
			if m := byPhys[fmt.Sprintf("%s/%d", strings.ToLower(rec.PhysAddress), rec.IfType)]; m != nil && unmatched[m.ID] != nil {
				return m
			}
		}
		return nil
	}

	for _, rec := range snap.Interfaces {
		m := match(rec)
		if m == nil {
			res.Upserts = append(res.Upserts, IfUpsert{Rec: rec})
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "interface",
				// The row ID doesn't exist yet at diff time; ifIndex is the
				// stable-enough reference for the history record.
				ObjectID: fmt.Sprintf("ifindex:%d", rec.IfIndex),
				Field:    "interface", New: rec.Name, ChangeKind: "created",
			})
			continue
		}
		delete(unmatched, m.ID)
		res.Upserts = append(res.Upserts, IfUpsert{ExistingID: m.ID, Rec: rec})
		if m.State == "missing" {
			res.RestoredIDs = append(res.RestoredIDs, m.ID)
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "interface", ObjectID: m.ID, Field: "state",
				Old: "missing", New: "present", ChangeKind: "status",
			})
		}
		if rec.IfIndex != m.IfIndex {
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "interface", ObjectID: m.ID, Field: "if_index",
				Old: itoa(m.IfIndex), New: itoa(rec.IfIndex), ChangeKind: "updated",
			})
		}
		ifFields := []struct {
			f        string
			old, new string
		}{
			{"name", m.Name, rec.Name},
			{"alias", m.Alias, rec.Alias},
			{"speed_bps", i64(m.SpeedBPS), i64(rec.SpeedBPS)},
			{"mtu", itoa(m.MTU), itoa(rec.MTU)},
		}
		for _, f := range ifFields {
			if f.old != f.new {
				res.Changes = append(res.Changes, FieldChange{
					ObjectKind: "interface", ObjectID: m.ID, Field: f.f,
					Old: f.old, New: f.new, ChangeKind: "updated",
				})
			}
		}
	}

	// Absent interfaces → missing / streak / removed (doc 11 §4).
	for id, m := range unmatched {
		switch {
		case m.State == "present":
			res.MissingIDs = append(res.MissingIDs, id)
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "interface", ObjectID: id, Field: "state",
				Old: "present", New: "missing", ChangeKind: "status",
			})
		case m.MissStreak+1 >= RemoveAfterMisses:
			res.RemovedIDs = append(res.RemovedIDs, id)
			res.Changes = append(res.Changes, FieldChange{
				ObjectKind: "interface", ObjectID: id, Field: "state",
				Old: "missing", New: "removed", ChangeKind: "removed",
			})
		default:
			res.StreakIDs = append(res.StreakIDs, id)
		}
	}
	return res
}

func changeKind(old string) string {
	if old == "" {
		return "created"
	}
	return "updated"
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func itoa(n int) string   { return fmt.Sprintf("%d", n) }
func i64(n int64) string  { return fmt.Sprintf("%d", n) }

// ---- service ----

// SyncRepo is the persistence port for applying a diff transactionally.
type SyncRepo interface {
	LoadState(ctx context.Context, deviceID string) (*DeviceState, error)
	// Apply performs the whole write set in one transaction (doc 16 §3) and
	// records the sync run. Returns the number of history rows written.
	Apply(ctx context.Context, deviceID string, res DiffResult,
		adjacencies []wire.SyncAdjacency, run SyncRunRecord) (int, error)
	RecordFailedRun(ctx context.Context, deviceID string, run SyncRunRecord) error
}

type SyncRunRecord struct {
	ID        string
	Trigger   string
	StartedAt time.Time
	Status    string // ok | failed
	Error     string
}

// DeviceLocker prevents concurrent sync of one device (FR-SYNC-06).
type DeviceLocker interface {
	WithLock(ctx context.Context, deviceID string, fn func() error) error
}

type SyncService struct {
	Repo   SyncRepo
	Locks  DeviceLocker
	Audit  audit.Writer
	Log    *slog.Logger
}

// HandleResult processes one poller SyncResult (consumed from the queue).
func (s *SyncService) HandleResult(ctx context.Context, res wire.SyncResult, runID string) error {
	run := SyncRunRecord{ID: runID, Trigger: orDefault(res.Trigger, "scheduled"),
		StartedAt: res.CollectedAt, Status: "ok"}
	if res.Error != "" || res.Snapshot == nil {
		run.Status, run.Error = "failed", res.Error
		return s.Repo.RecordFailedRun(ctx, res.DeviceID, run)
	}
	return s.Locks.WithLock(ctx, res.DeviceID, func() error {
		cur, err := s.Repo.LoadState(ctx, res.DeviceID)
		if err != nil {
			if errx.KindOf(err) == errx.KindNotFound {
				s.Log.Warn("sync result for unknown device dropped", "device", res.DeviceID)
				return nil
			}
			return err
		}
		diff := Diff(*cur, *res.Snapshot, res.CollectedAt)
		n, err := s.Repo.Apply(ctx, res.DeviceID, diff, res.Snapshot.Adjacencies, run)
		if err != nil {
			return err
		}
		if n > 0 {
			s.Audit.Write(ctx, audit.Event{
				ActorKind: "system", Action: "sync.completed",
				ResourceKind: "device", ResourceID: res.DeviceID,
				Detail: map[string]any{"changes": n, "trigger": run.Trigger,
					"rebooted": diff.Rebooted},
			})
		}
		return nil
	})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
