package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// The service half of re-addressing is unit-tested; this covers the SQL, which
// omitted mgmt_ip and attrs entirely. Both layers dropped the change, so a
// fix to either alone would still have looked like it worked.
func TestUpdatePersistsAddressAndPort(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	repo := &DeviceRepo{Pool: pool}
	seedRefs(t, pool)

	d := &domain.Device{
		ID: "d_move", TenantID: "t_default", SiteID: "s_default",
		ConnectorID: "generic", CredentialID: "cr_x", ProfileID: "pp_default",
		Name: "mover", MgmtIP: "198.51.100.10", Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{"snmp_port": 1161},
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.MgmtIP = "198.51.100.20"
	d.Attrs = map[string]any{"snmp_port": 2161}
	if err := repo.Update(ctx, d); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.Get(ctx, "d_move")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MgmtIP != "198.51.100.20" {
		t.Errorf("mgmt_ip = %q, want the new address — the UPDATE dropped it", got.MgmtIP)
	}
	// The poller reads the port from attrs at job time, so a stale value here
	// means it keeps polling the old port with no indication why.
	if p, ok := got.Attrs["snmp_port"]; !ok || int(p.(float64)) != 2161 {
		t.Errorf("snmp_port = %v, want 2161", got.Attrs["snmp_port"])
	}
}

// Two devices on one address would make the fleet ambiguous, and Create
// already refuses it. Update must refuse it the same way rather than surface a
// raw constraint violation as a 500.
func TestUpdateRefusesAnAddressAlreadyInUse(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	repo := &DeviceRepo{Pool: pool}
	seedRefs(t, pool)

	for i, id := range []string{"d_a", "d_b"} {
		d := &domain.Device{
			ID: id, TenantID: "t_default", SiteID: "s_default",
			ConnectorID: "generic", CredentialID: "cr_x", ProfileID: "pp_default",
			Name: id, MgmtIP: []string{"198.51.100.31", "198.51.100.32"}[i],
			Status: domain.DevicePending, Tags: []string{}, Attrs: map[string]any{},
		}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	b, _ := repo.Get(ctx, "d_b")
	b.MgmtIP = "198.51.100.31" // already d_a's
	err := repo.Update(ctx, b)
	if err == nil {
		t.Fatal("two devices were allowed onto one address")
	}
	if errx.KindOf(err) != errx.KindConflict {
		t.Errorf("kind = %v, want KindConflict so the API answers 409", errx.KindOf(err))
	}
}

// seedRefs supplies the rows a device points at. The connector catalog is
// seeded by the API from the compiled-in registry at startup rather than by a
// migration, so a freshly migrated database has none.
func seedRefs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials
			(id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ('cr_x','t_default','readdress-test','snmp_v2c','\x00','\x00',1)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}
