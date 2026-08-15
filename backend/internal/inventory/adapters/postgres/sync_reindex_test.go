package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedSyncDevice creates the minimum a sync can be applied against.
func seedSyncDevice(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	profID := id.New("pp")
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.polling_profiles (id, tenant_id, name, icmp_interval_s)
		VALUES ($1,'t_default',$2,30)`, profID, "reindex-"+profID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	credID := id.New("cr")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`,
		credID, "reindex-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	devID := id.New("d")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.devices
			(id, tenant_id, site_id, connector_id, credential_id, profile_id,
			 name, mgmt_ip, status)
		VALUES ($1,'t_default','s_default','generic',$2,$3,$4,'10.255.255.253','active')`,
		devID, credID, profID, "reindex-gw-"+devID); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return devID
}

func ifaceIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ifID string) (int, string) {
	t.Helper()
	var idx int
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT if_index, state::text FROM inventory.interfaces WHERE id=$1`,
		ifID).Scan(&idx, &state); err != nil {
		t.Fatalf("read interface %s: %v", ifID, err)
	}
	return idx, state
}

// A gateway that reboots and renumbers must not deadlock the sync.
//
// A pilot UniFi gateway moved ppp2 from ifIndex 76 to 41 across a restart,
// where a long-retired row still held 41. Diff correctly matched ppp2 by name
// and asked for the move; Apply then aborted the transaction on the
// (device_id, if_index) unique constraint, and because a failed sync result is
// requeued it retried every second indefinitely. Inventory froze at the
// pre-reboot topology and every weathermap link bound to a renumbered interface
// went flat, resolving to an ifIndex the device no longer had.
func TestSyncSurvivesInterfaceRenumbering(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	devID := seedSyncDevice(t, ctx, pool)
	repo := &SyncRepo{Pool: pool}

	// First sync: ppp2 at 76, plus an interface at 41 that will later vanish.
	first := wire.SyncSnapshot{
		Interfaces: []wire.SyncInterface{
			{IfIndex: 76, Name: "ppp2", OperStatus: 1, AdminStatus: 1},
			{IfIndex: 41, Name: "tlprt0", OperStatus: 1, AdminStatus: 1},
			{IfIndex: 16, Name: "eth0", OperStatus: 1, AdminStatus: 1},
		},
	}
	state, err := repo.LoadState(ctx, devID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Apply(ctx, devID,
		app.Diff(*state, first, time.Now()), nil, app.SyncRunRecord{ID: id.New("sr")}); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	state, err = repo.LoadState(ctx, devID)
	if err != nil {
		t.Fatal(err)
	}
	var ppp2ID string
	for _, ifc := range state.Interfaces {
		if ifc.Name == "ppp2" {
			ppp2ID = ifc.ID
		}
	}
	if ppp2ID == "" {
		t.Fatal("ppp2 not stored by the first sync")
	}

	// The reboot: ppp2 is now 41, tlprt0 is gone, and eth0 and eth1 have
	// swapped — a permutation among live interfaces, which retiring rows alone
	// cannot resolve.
	second := wire.SyncSnapshot{
		Interfaces: []wire.SyncInterface{
			{IfIndex: 41, Name: "ppp2", OperStatus: 1, AdminStatus: 1},
			{IfIndex: 17, Name: "eth0", OperStatus: 1, AdminStatus: 1},
			{IfIndex: 16, Name: "eth1", OperStatus: 1, AdminStatus: 1},
		},
	}
	if _, err := repo.Apply(ctx, devID,
		app.Diff(*state, second, time.Now()), nil, app.SyncRunRecord{ID: id.New("sr")}); err != nil {
		t.Fatalf("sync after renumbering: %v", err)
	}

	// The map link's whole value is that it follows the interface, not the
	// number: the same row must now carry ifIndex 41.
	if idx, st := ifaceIndex(t, ctx, pool, ppp2ID); idx != 41 || st != "present" {
		t.Errorf("ppp2 row is if_index=%d state=%s, want 41/present", idx, st)
	}

	// No negative index may escape the transaction — parking is an internal
	// step, and a leaked negative would be published as a metric label.
	var negatives int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM inventory.interfaces WHERE device_id=$1 AND if_index < 0`,
		devID).Scan(&negatives); err != nil {
		t.Fatal(err)
	}
	if negatives != 0 {
		t.Errorf("%d interfaces left parked at a negative if_index", negatives)
	}

	// And the swap landed both ways round.
	var eth0Idx, eth1Idx int
	if err := pool.QueryRow(ctx,
		`SELECT if_index FROM inventory.interfaces
		 WHERE device_id=$1 AND name='eth0' AND state='present'`, devID).Scan(&eth0Idx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT if_index FROM inventory.interfaces
		 WHERE device_id=$1 AND name='eth1' AND state='present'`, devID).Scan(&eth1Idx); err != nil {
		t.Fatal(err)
	}
	if eth0Idx != 17 || eth1Idx != 16 {
		t.Errorf("eth0=%d eth1=%d, want the swap 17/16", eth0Idx, eth1Idx)
	}
}
