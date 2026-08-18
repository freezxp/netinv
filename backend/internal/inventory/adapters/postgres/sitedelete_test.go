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

// The interface search exists so an operator can find a port by what someone
// wrote on it. Alias is the field people actually curate — circuit ids,
// customer names — and it was previously readable one device at a time.
func TestSearchInterfacesMatchesAliasAndDescription(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "search-site")
	addDevice(t, dr, ctx, site.ID, "10.91.0.1", domain.DevicePending)

	var deviceID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE site_id = $1`, site.ID).
		Scan(&deviceID); err != nil {
		t.Fatalf("find device: %v", err)
	}
	seed := []struct {
		idx                       int
		name, alias, descr, state string
	}{
		{1, "ge-0/0/0", "LONDON-CIRCUIT-4471", "uplink to core", "present"},
		{2, "ge-0/0/1", "", "spare port", "present"},
		{3, "ge-0/0/2", "OLD-LONDON-LINK", "decommissioned", "removed"},
	}
	for _, s := range seed {
		if _, err := dr.Pool.Exec(ctx, `
			INSERT INTO inventory.interfaces
				(id, device_id, if_index, name, alias, descr, speed_bps, state)
			VALUES ($1,$2,$3,$4,$5,$6,1000000000,$7)`,
			id.New("if"), deviceID, s.idx, s.name, s.alias, s.descr, s.state); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}

	// Alias hit.
	rows, total, err := dr.SearchInterfaces(ctx, "london", 0, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Case-insensitive, and the removed interface must not appear: search
	// turns up ports you can act on, not history.
	if total != 1 || len(rows) != 1 || rows[0].Alias != "LONDON-CIRCUIT-4471" {
		t.Fatalf("got %d rows (total %d) %+v, want only the present London circuit",
			len(rows), total, rows)
	}
	if rows[0].DeviceName == "" || rows[0].SpeedBPS != 1000000000 {
		t.Fatalf("row missing device name or speed: %+v", rows[0])
	}

	// Description hit — whoever labelled the port may have used either field.
	rows, _, err = dr.SearchInterfaces(ctx, "spare", 0, 0)
	if err != nil {
		t.Fatalf("search descr: %v", err)
	}
	if len(rows) != 1 || rows[0].IfIndex != 2 {
		t.Fatalf("description search returned %+v", rows)
	}

	// Empty query lists everything present, which is what an empty search box
	// should show rather than nothing.
	rows, total, err = dr.SearchInterfaces(ctx, "", 0, 0)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("empty search returned %d rows (total %d), want the 2 present ones",
			len(rows), total)
	}
}

// A site is a grouping, so moving a device between sites has to be a plain
// update — and the scheduler has to follow it. Jobs are routed by the device's
// current site_id at dispatch (the Due query joins devices), so nothing needs
// rewriting for polling to move with it; this pins that, because a schedule
// that kept pointing at the old site would send the device's jobs to a queue
// its poller no longer reads and collection would stop silently.
func TestDeviceCanMoveBetweenSites(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	from := addSite(t, repo, ctx, "site-from")
	to := addSite(t, repo, ctx, "site-to")
	addDevice(t, dr, ctx, from.ID, "10.93.0.1", domain.DevicePending)

	var dev domain.Device
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id, site_id, connector_id, credential_id, profile_id, name
		   FROM inventory.devices WHERE site_id = $1`, from.ID).
		Scan(&dev.ID, &dev.SiteID, &dev.ConnectorID, &dev.CredentialID,
			&dev.ProfileID, &dev.Name); err != nil {
		t.Fatalf("load device: %v", err)
	}

	dev.SiteID = to.ID
	dev.MgmtIP = "10.93.0.1"
	dev.Tags = []string{}
	dev.Attrs = map[string]any{}
	if err := dr.Update(ctx, &dev); err != nil {
		t.Fatalf("move device: %v", err)
	}

	var got string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT site_id FROM inventory.devices WHERE id = $1`, dev.ID).
		Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != to.ID {
		t.Fatalf("device is in site %s, want %s", got, to.ID)
	}

	// The schedule rows are untouched by design — they carry no site — and the
	// dispatch query resolves the site from the device, so the next poll goes
	// to the new site's queue with no further action.
	var scheduled int
	if err := dr.Pool.QueryRow(ctx,
		`SELECT count(*) FROM platform.polling_schedule WHERE device_id = $1`,
		dev.ID).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled == 0 {
		t.Fatal("moving the device lost its polling schedule")
	}

	// The old site is now empty and must be deletable: a grouping you cannot
	// dissolve after emptying it is not a grouping.
	if err := repo.Delete(ctx, from.ID); err != nil {
		t.Fatalf("deleting the emptied site: %v", err)
	}
}
