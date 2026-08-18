package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// A failed sync is the reason a device sits in 'pending' forever while ICMP
// and traffic polls keep succeeding, and the reason text exists only on the
// run row. This covers the two properties the device page depends on: the
// newest run comes first, and a failed run carries its error rather than
// collapsing to a status word.
func TestSyncRunsReturnsNewestFirstWithErrorText(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('juniper-junos','Juniper','Juniper JunOS','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	credID := id.New("cr")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v3','\x00','\x00',1)`,
		credID, "syncruns-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	repo := &DeviceRepo{Pool: pool}
	dev := &domain.Device{
		ID: id.New("d"), TenantID: "t_default", SiteID: "s_default",
		ConnectorID: "juniper-junos", CredentialID: credID, ProfileID: "pp_default",
		Name: "junos-edge", MgmtIP: "10.255.255.253", Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{},
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create device: %v", err)
	}

	const wantErr = "generic: ifTable walk: request timeout (after 2 retries)"
	older := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.sync_runs
			(id, device_id, trigger, started_at, finished_at, status, changes_count, error)
		VALUES ($1,$2,'scheduled',$3::timestamptz,$3::timestamptz + interval '4 seconds','ok',7,NULL),
		       ($4,$2,'manual',$5::timestamptz,$5::timestamptz + interval '11 seconds','failed',0,$6)`,
		id.New("sr"), dev.ID, older, id.New("sr"), older.Add(time.Hour), wantErr); err != nil {
		t.Fatalf("seed sync runs: %v", err)
	}

	runs, err := repo.SyncRuns(ctx, dev.ID, 0)
	if err != nil {
		t.Fatalf("sync runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Status != "failed" || runs[1].Status != "ok" {
		t.Fatalf("got %q then %q, want the newest (failed) run first",
			runs[0].Status, runs[1].Status)
	}
	if runs[0].Error != wantErr {
		t.Fatalf("error text is %q, want %q — the reason is the whole point", runs[0].Error, wantErr)
	}
	if runs[0].DurationS != 11 {
		t.Fatalf("duration %v, want 11s", runs[0].DurationS)
	}
	// A successful run must not carry a phantom error string: the UI colours
	// the column red whenever it is non-empty.
	if runs[1].Error != "" {
		t.Fatalf("ok run carries error %q", runs[1].Error)
	}
}

// An in-flight run has no finished_at. Reporting 0 s for it would render as an
// instant sync rather than one still running.
func TestSyncRunsLeavesRunningDurationUnset(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	credID := id.New("cr")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`,
		credID, "syncruns-running-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	repo := &DeviceRepo{Pool: pool}
	dev := &domain.Device{
		ID: id.New("d"), TenantID: "t_default", SiteID: "s_default",
		ConnectorID: "generic", CredentialID: credID, ProfileID: "pp_default",
		Name: "still-running", MgmtIP: "10.255.255.252", Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{},
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.sync_runs (id, device_id, trigger, status)
		VALUES ($1,$2,'scheduled','running')`, id.New("sr"), dev.ID); err != nil {
		t.Fatalf("seed running sync: %v", err)
	}

	runs, err := repo.SyncRuns(ctx, dev.ID, 0)
	if err != nil {
		t.Fatalf("sync runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].FinishedAt != "" || runs[0].DurationS != 0 {
		t.Fatalf("running sync reported finished at %q after %vs",
			runs[0].FinishedAt, runs[0].DurationS)
	}
}

// The read model must not claim a fault it cannot see. A site the scheduler
// has not dispatched to yet has no row at all, and reporting that as "no
// poller" would put a red banner on every device of a freshly upgraded
// deployment until the first tick.
func TestSiteCollectionSeparatesUnknownFromUnserved(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	credID := id.New("cr")
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`,
		credID, "sitecoll-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	repo := &DeviceRepo{Pool: pool}
	dev := &domain.Device{
		ID: id.New("d"), TenantID: "t_default", SiteID: "s_default",
		ConnectorID: "generic", CredentialID: credID, ProfileID: "pp_default",
		Name: "unpolled", MgmtIP: "10.255.255.251", Status: domain.DevicePending,
		Tags: []string{}, Attrs: map[string]any{},
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create device: %v", err)
	}

	// Nothing dispatched yet: unknown, not a fault.
	sc, err := repo.SiteCollection(ctx, dev.ID)
	if err != nil {
		t.Fatalf("site collection: %v", err)
	}
	if sc.Known {
		t.Fatal("reported known before the scheduler ever declared the queue")
	}
	if sc.SiteID != "s_default" {
		t.Fatalf("site is %q, want s_default", sc.SiteID)
	}

	// Now the scheduler has looked and found nobody reading.
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.site_collection_health
			(site_id, consumers, queued, no_consumer_since, checked_at)
		VALUES ('s_default', 0, 42, now(), now())`); err != nil {
		t.Fatalf("seed health: %v", err)
	}
	sc, err = repo.SiteCollection(ctx, dev.ID)
	if err != nil {
		t.Fatalf("site collection: %v", err)
	}
	if !sc.Known || sc.Consumers != 0 || sc.Queued != 42 || sc.NoConsumerSince == "" {
		t.Fatalf("got %+v, want a known site with 0 consumers, 42 queued and a start instant", sc)
	}
}
