package postgres

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// families_enabled is the per-family switch FR-COLL-04 describes. It has been
// in the schema since the first migration and was never read, so a profile
// that turned a family off was polled anyway. Integration-level because the
// behaviour lives entirely in the scheduling query.
func TestProfileFamiliesEnabledGatesScheduling(t *testing.T) {
	// Its own database: this creates a profile, credential and device, which
	// have no business appearing in an operator's inventory.
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()

	// An ICMP-only profile: the shape wanted for a device that answers ping
	// but runs no SNMP agent, such as a Ruckus Unleashed member AP. The
	// intervals stay valid — only the family list narrows.
	// The connector catalog is seeded by the API from the compiled-in registry
	// at startup, not by a migration, so a fresh database has none. Relying on
	// a live deployment's row is exactly what this test just stopped doing.
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}

	profID := id.New("pp")
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.polling_profiles
			(id, tenant_id, name, icmp_interval_s, families_enabled)
		VALUES ($1,'t_default',$2,30,'["icmp"]')`, profID, "icmp-only-"+profID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM platform.polling_profiles WHERE id=$1`, profID)

	credID := id.New("cr")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`,
		credID, "sched-test-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM inventory.credentials WHERE id=$1`, credID)

	repo := &DeviceRepo{Pool: pool}
	dev := &domain.Device{
		ID: id.New("d"), TenantID: "t_default", SiteID: "s_default",
		ConnectorID: "generic", CredentialID: credID, ProfileID: profID,
		Name: "mesh-ap", MgmtIP: "10.255.255.254", Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{},
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create device on an icmp-only profile: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM inventory.devices WHERE id=$1`, dev.ID)

	rows, err := pool.Query(ctx,
		`SELECT family, interval_s FROM platform.polling_schedule WHERE device_id=$1`, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var f string
		var iv int
		if err := rows.Scan(&f, &iv); err != nil {
			t.Fatal(err)
		}
		got[f] = iv
	}
	if len(got) != 1 || got["icmp"] != 30 {
		t.Fatalf("scheduled %v, want icmp only at 30s", got)
	}
}
