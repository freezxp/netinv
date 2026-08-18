package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

func newSiteRepo(t *testing.T) (*SiteRepo, *DeviceRepo, context.Context) {
	t.Helper()
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	return &SiteRepo{Pool: pool}, &DeviceRepo{Pool: pool}, ctx
}

func addSite(t *testing.T, r *SiteRepo, ctx context.Context, name string) *domain.Site {
	t.Helper()
	s := &domain.Site{ID: id.New("s"), TenantID: "t_default", Name: name, Status: "active"}
	if err := r.Create(ctx, s); err != nil {
		t.Fatalf("create site: %v", err)
	}
	return s
}

func addDevice(t *testing.T, dr *DeviceRepo, ctx context.Context,
	siteID, ip string, status domain.DeviceStatus) {
	t.Helper()
	credID := id.New("cr")
	if _, err := dr.Pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`, credID, "c-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	d := &domain.Device{
		ID: id.New("d"), TenantID: "t_default", SiteID: siteID,
		ConnectorID: "generic", CredentialID: credID, ProfileID: "pp_default",
		Name: "dev-" + ip, MgmtIP: ip, Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{},
	}
	if err := dr.Create(ctx, d); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if status != domain.DevicePending {
		if _, err := dr.Pool.Exec(ctx,
			`UPDATE inventory.devices SET status = $2 WHERE id = $1`,
			d.ID, string(status)); err != nil {
			t.Fatalf("set device status: %v", err)
		}
	}
}

func TestDeleteSiteRemovesAnUnreferencedSite(t *testing.T) {
	repo, _, ctx := newSiteRepo(t)
	s := addSite(t, repo, ctx, "empty-site")
	if err := repo.Delete(ctx, s.ID); err != nil {
		t.Fatalf("delete an unreferenced site: %v", err)
	}
	if _, err := repo.Get(ctx, s.ID); errx.KindOf(err) != errx.KindNotFound {
		t.Fatalf("site still readable after delete, err = %v", err)
	}
}

// A retired device does not count as a managed device anywhere else in the
// product, so the old check skipped it — while the foreign key did not. The
// delete failed anyway, reporting "pollers or child sites" for a site whose
// only problem was a device. The count and the constraint have to agree.
func TestDeleteSiteRefusesForRetiredDevices(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	s := addSite(t, repo, ctx, "retired-holder")
	addDevice(t, dr, ctx, s.ID, "10.90.0.1", domain.DeviceRetired)

	err := repo.Delete(ctx, s.ID)
	if errx.KindOf(err) != errx.KindConflict {
		t.Fatalf("kind is %v, want conflict (err = %v)", errx.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "retired device") {
		t.Fatalf("message %q does not mention the retired device", err.Error())
	}
}

// Every blocker at once: an operator emptying a site works through all of
// them, and discovering them one failed request at a time is the slow way.
func TestDeleteSiteNamesEveryBlockerTogether(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	s := addSite(t, repo, ctx, "busy-site")
	addDevice(t, dr, ctx, s.ID, "10.90.1.1", domain.DevicePending)
	addDevice(t, dr, ctx, s.ID, "10.90.1.2", domain.DevicePending)
	addDevice(t, dr, ctx, s.ID, "10.90.1.3", domain.DeviceRetired)

	child := &domain.Site{ID: id.New("s"), TenantID: "t_default",
		Name: "child-of-busy", ParentSiteID: &s.ID, Status: "active"}
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("create child site: %v", err)
	}
	if _, err := repo.Pool.Exec(ctx, `
		INSERT INTO platform.discovery_rules (id, tenant_id, site_id, cidr, credential_ids)
		VALUES ($1,'t_default',$2,'10.90.1.0/24','[]')`,
		id.New("dr"), s.ID); err != nil {
		t.Fatalf("seed discovery rule: %v", err)
	}

	err := repo.Delete(ctx, s.ID)
	if errx.KindOf(err) != errx.KindConflict {
		t.Fatalf("kind is %v, want conflict (err = %v)", errx.KindOf(err), err)
	}
	for _, want := range []string{"2 devices", "1 retired device", "1 child site", "1 discovery rule"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q is missing %q", err.Error(), want)
		}
	}
}

func TestDeleteSiteReportsMissingSite(t *testing.T) {
	repo, _, ctx := newSiteRepo(t)
	if err := repo.Delete(ctx, "s_does_not_exist"); errx.KindOf(err) != errx.KindNotFound {
		t.Fatalf("kind is %v, want not-found (err = %v)", errx.KindOf(err), err)
	}
}
