package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
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
	// Purging destroys the history the device was retired to keep, and moving
	// it to another site clears the blocker without destroying anything. The
	// message must not name only the destructive way out.
	if !strings.Contains(err.Error(), "move") {
		t.Fatalf("message %q offers no non-destructive option", err.Error())
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

// Importing customer assignments has to resolve interfaces the way an operator
// writes them down — a device by name or address, a port by name or ifIndex —
// because the list being imported came from a billing system or a spreadsheet,
// not from NetInv, and demanding internal ids would mean looking up every row
// by hand first. That lookup is the work the import exists to remove.
func TestApplyTagsResolvesInterfacesTheWayPeopleWriteThem(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "tag-site")
	addDevice(t, dr, ctx, site.ID, "10.94.0.1", domain.DevicePending)

	var deviceID, deviceName string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id, name FROM inventory.devices WHERE site_id = $1`, site.ID).
		Scan(&deviceID, &deviceName); err != nil {
		t.Fatalf("find device: %v", err)
	}
	for _, i := range []struct {
		idx  int
		name string
	}{{10, "ge-0/0/0"}, {11, "ge-0/0/1"}} {
		if _, err := dr.Pool.Exec(ctx, `
			INSERT INTO inventory.interfaces (id, device_id, if_index, name, state)
			VALUES ($1,$2,$3,$4,'present')`,
			id.New("if"), deviceID, i.idx, i.name); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}

	res, err := dr.ApplyTags(ctx, []TagAssignment{
		// By device name + interface name, with case that does not match.
		{Device: deviceName, Interface: "GE-0/0/0", Customer: "Acme Ltd",
			Tags: []string{"gold", "circuit"}, SetTags: true},
		// By management address + ifIndex.
		{Device: "10.94.0.1", Interface: "11", Customer: "Globex"},
		// Nothing to resolve.
		{Device: deviceName, Interface: "ge-9/9/9", Customer: "Nobody"},
	})
	if err != nil {
		t.Fatalf("apply tags: %v", err)
	}
	if res.Matched != 2 || res.Updated != 2 {
		t.Fatalf("matched %d / updated %d, want 2 and 2", res.Matched, res.Updated)
	}
	if len(res.Unmatched) != 1 {
		t.Fatalf("unmatched = %v, want the one bad row reported", res.Unmatched)
	}

	rows, _, err := dr.FindInterfaces(ctx, InterfaceFilter{Customer: "acme ltd"}, 0, 0)
	if err != nil {
		t.Fatalf("find by customer: %v", err)
	}
	// The customer filter is case-insensitive but exact: a billing run must not
	// pick up a different customer whose name merely contains this one.
	if len(rows) != 1 || rows[0].IfIndex != 10 {
		t.Fatalf("customer filter returned %+v", rows)
	}
	if len(rows[0].Tags) != 2 || rows[0].Tags[0] != "gold" {
		t.Fatalf("tags = %v, want [gold circuit]", rows[0].Tags)
	}

	// The free-text search reaches the customer too: typing a customer name
	// into the box means the same thing as choosing it from the filter.
	rows, _, err = dr.FindInterfaces(ctx, InterfaceFilter{Q: "globex"}, 0, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].IfIndex != 11 {
		t.Fatalf("search by customer returned %+v", rows)
	}

	// A customer leaving has to be expressible. A blank cell cannot mean that:
	// in a spreadsheet it overwhelmingly means "I did not fill this in".
	if _, err := dr.ApplyTags(ctx, []TagAssignment{
		{Device: deviceName, Interface: "11", Customer: ClearToken},
	}); err != nil {
		t.Fatalf("clear customer: %v", err)
	}
	rows, _, err = dr.FindInterfaces(ctx, InterfaceFilter{Customer: "Globex"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("customer still assigned after a clear: %+v", rows)
	}

	cust, err := dr.Customers(ctx)
	if err != nil {
		t.Fatalf("customers: %v", err)
	}
	if len(cust) != 1 || cust[0].Customer != "Acme Ltd" || cust[0].Interfaces != 1 {
		t.Fatalf("customers = %+v, want one Acme Ltd with 1 interface", cust)
	}
}

// Sync owns what the device reports and must never touch what an operator
// recorded. A port that gets renamed or re-aliased upstream would otherwise
// lose the customer it belongs to — silently, on the next poll.
func TestSyncDoesNotClearCustomerOrTags(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "sync-tag-site")
	addDevice(t, dr, ctx, site.ID, "10.94.1.1", domain.DevicePending)

	var deviceID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE site_id = $1`, site.ID).
		Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	ifID := id.New("if")
	if _, err := dr.Pool.Exec(ctx, `
		INSERT INTO inventory.interfaces
			(id, device_id, if_index, name, alias, state, customer, tags)
		VALUES ($1,$2,5,'ge-0/0/5','old alias','present','Acme Ltd','["gold"]')`,
		ifID, deviceID); err != nil {
		t.Fatalf("seed interface: %v", err)
	}

	// What a sync does to an interface whose device-reported fields changed.
	sync := &SyncRepo{Pool: dr.Pool}
	if _, err := sync.Apply(ctx, deviceID, app.DiffResult{
		DeviceFields: map[string]string{},
		Upserts: []app.IfUpsert{{ExistingID: ifID, Rec: wire.SyncInterface{
			IfIndex: 5, Name: "ge-0/0/5", Alias: "new alias from the device",
			AdminStatus: 1, OperStatus: 1,
		}}},
	}, nil, app.SyncRunRecord{ID: id.New("sr"), Trigger: "test", Status: "ok"}); err != nil {
		t.Fatalf("apply sync: %v", err)
	}

	var customer string
	var tags []string
	var alias string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT coalesce(customer,''), tags, coalesce(alias,'') FROM inventory.interfaces WHERE id = $1`,
		ifID).Scan(&customer, &tags, &alias); err != nil {
		t.Fatal(err)
	}
	if alias != "new alias from the device" {
		t.Errorf("sync did not update the device-owned alias: %q", alias)
	}
	if customer != "Acme Ltd" {
		t.Errorf("sync cleared the customer: %q", customer)
	}
	if len(tags) != 1 || tags[0] != "gold" {
		t.Errorf("sync cleared the tags: %v", tags)
	}
}

// "Hide down" filters in the database so the total and the paging follow it.
// Dropping down interfaces from the page after it arrives would leave a
// footer reading "100 of 4000" above a list of twelve.
func TestFindInterfacesCanExcludeDownPorts(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "updown-site")
	addDevice(t, dr, ctx, site.ID, "10.95.0.1", domain.DevicePending)

	var deviceID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE site_id = $1`, site.ID).
		Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	for _, i := range []struct {
		idx  int
		name string
		oper int
	}{{1, "up-port", 1}, {2, "down-port", 2}, {3, "unknown-port", 0}} {
		if _, err := dr.Pool.Exec(ctx, `
			INSERT INTO inventory.interfaces (id, device_id, if_index, name, oper_status, state)
			VALUES ($1,$2,$3,$4,$5,'present')`,
			id.New("if"), deviceID, i.idx, i.name, i.oper); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}

	all, total, err := dr.FindInterfaces(ctx, InterfaceFilter{}, 0, 0)
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("unfiltered returned %d rows (total %d), want 3", len(all), total)
	}

	up, upTotal, err := dr.FindInterfaces(ctx, InterfaceFilter{UpOnly: true}, 0, 0)
	if err != nil {
		t.Fatalf("up only: %v", err)
	}
	// The count has to move with the filter, or paging lies.
	if upTotal != 1 || len(up) != 1 || up[0].Name != "up-port" {
		t.Fatalf("up-only returned %d rows (total %d): %+v", len(up), upTotal, up)
	}
	// An interface whose oper status was never recorded is not "up". Treating
	// unknown as up would quietly reintroduce exactly what the filter removes.
	for _, r := range up {
		if r.OperStatus != 1 {
			t.Errorf("row %s has oper %d in an up-only result", r.Name, r.OperStatus)
		}
	}
}

// Sorting is server-side for inventory columns so the order spans the whole
// result rather than the hundred rows on screen — and the key is whitelisted,
// because this string is concatenated into the query and an ORDER BY taken
// from a URL parameter is how a search box becomes an injection.
func TestFindInterfacesSortsAndRejectsUnknownKeys(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "sort-site")
	addDevice(t, dr, ctx, site.ID, "10.96.0.1", domain.DevicePending)

	var deviceID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE site_id = $1`, site.ID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	for _, i := range []struct {
		idx   int
		name  string
		speed int64
	}{{1, "slow", 100_000_000}, {2, "fast", 10_000_000_000}, {3, "unknown", 0}} {
		var sp any = i.speed
		if i.speed == 0 {
			sp = nil // an interface whose speed was never reported
		}
		if _, err := dr.Pool.Exec(ctx, `
			INSERT INTO inventory.interfaces (id, device_id, if_index, name, speed_bps, state)
			VALUES ($1,$2,$3,$4,$5,'present')`,
			id.New("if"), deviceID, i.idx, i.name, sp); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}

	fastest, _, err := dr.FindInterfaces(ctx, InterfaceFilter{Sort: "speed", Desc: true}, 0, 0)
	if err != nil {
		t.Fatalf("sort by speed: %v", err)
	}
	if len(fastest) != 3 || fastest[0].Name != "fast" {
		t.Fatalf("descending speed put %q first: %+v", fastest[0].Name, fastest)
	}
	// Missing information belongs at the end of a list someone reads
	// top-down, not at the top of a descending one.
	if fastest[2].Name != "unknown" {
		t.Errorf("unknown speed sorted to position 2, want last: %+v", fastest)
	}

	asc, _, err := dr.FindInterfaces(ctx, InterfaceFilter{Sort: "speed"}, 0, 0)
	if err != nil {
		t.Fatalf("ascending: %v", err)
	}
	if asc[0].Name != "slow" || asc[2].Name != "unknown" {
		t.Errorf("ascending speed order is %v", []string{asc[0].Name, asc[1].Name, asc[2].Name})
	}

	// An unknown key falls back to the default order rather than erroring or,
	// far worse, reaching the query.
	def, _, err := dr.FindInterfaces(ctx,
		InterfaceFilter{Sort: "speed_bps; DROP TABLE inventory.interfaces --"}, 0, 0)
	if err != nil {
		t.Fatalf("unknown sort key: %v", err)
	}
	if len(def) != 3 || def[0].IfIndex != 1 {
		t.Fatalf("unknown key did not fall back to the default order: %+v", def)
	}
	var still int
	if err := dr.Pool.QueryRow(ctx,
		`SELECT count(*) FROM inventory.interfaces`).Scan(&still); err != nil || still != 3 {
		t.Fatalf("interfaces table is %d rows after an injection attempt (err %v)", still, err)
	}
}

// A device reachable on two addresses is discovered and onboarded twice: the
// addresses differ, so the unique constraint on mgmt_ip never sees it. The
// duplication is in the identity the device reports.
func TestDuplicateDetectionUsesSerialThenSysName(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "dup-site")
	for i, ip := range []string{"10.97.0.1", "10.97.0.2", "10.97.0.3", "10.97.0.4"} {
		addDevice(t, dr, ctx, site.ID, ip, domain.DevicePending)
		_ = i
	}
	set := func(ip, sysName, serial string) {
		if _, err := dr.Pool.Exec(ctx, `
			UPDATE inventory.devices SET sys_name = nullif($2,''), serial_number = nullif($3,'')
			WHERE host(mgmt_ip) = $1`, ip, sysName, serial); err != nil {
			t.Fatalf("set identity: %v", err)
		}
	}
	// One device, two addresses: same serial.
	set("10.97.0.1", "core-a", "SER123")
	set("10.97.0.2", "core-a-mgmt", "SER123")
	// Another, two addresses, no serial reported: hostname is the only signal.
	set("10.97.0.3", "edge-b", "")
	set("10.97.0.4", "EDGE-B", "")

	groups, err := dr.Duplicates(ctx)
	if err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(groups), groups)
	}
	byMatch := map[string]DuplicateGroup{}
	for _, g := range groups {
		byMatch[g.Match] = g
	}
	if g, ok := byMatch["serial"]; !ok || len(g.Devices) != 2 || g.Value != "SER123" {
		t.Fatalf("serial group is %+v", g)
	}
	// Hostname matching is case-insensitive: "EDGE-B" and "edge-b" are one box
	// reported by two agents that disagree about capitalisation.
	if g, ok := byMatch["sys_name"]; !ok || len(g.Devices) != 2 || g.Value != "edge-b" {
		t.Fatalf("sys_name group is %+v", g)
	}

	// A pair already reported by serial must not also appear under sys_name:
	// one problem, listed once.
	set("10.97.0.2", "core-a", "SER123")
	groups, err = dr.Duplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.Match == "sys_name" && g.Value == "core-a" {
			t.Fatalf("serial-matched pair reported again under sys_name: %+v", g)
		}
	}
}

// Approval consults the same evidence before anything is created, which is the
// only moment a duplicate is cheap to avoid.
func TestMatchByIdentityPrefersSerialAndStopsThere(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "match-site")
	addDevice(t, dr, ctx, site.ID, "10.98.0.1", domain.DevicePending)
	if _, err := dr.Pool.Exec(ctx,
		`UPDATE inventory.devices SET sys_name='core-a', serial_number='SER999'
		 WHERE host(mgmt_ip)='10.98.0.1'`); err != nil {
		t.Fatal(err)
	}

	m, match, err := dr.MatchByIdentity(ctx, "", "SER999")
	if err != nil || m == nil || match != "serial" {
		t.Fatalf("serial lookup gave %+v %q %v", m, match, err)
	}
	m, match, err = dr.MatchByIdentity(ctx, "CORE-A", "")
	if err != nil || m == nil || match != "sys_name" {
		t.Fatalf("hostname lookup gave %+v %q %v", m, match, err)
	}
	// A serial that does not match must not fall through to the hostname: the
	// evidence has already separated these two, and pairing them anyway would
	// override the stronger signal with the weaker one.
	if m, _, err := dr.MatchByIdentity(ctx, "core-a", "DIFFERENT"); err != nil || m != nil {
		t.Fatalf("a mismatched serial fell through to the hostname: %+v", m)
	}
}

// Recording the second address is a note, not a second polling target: NetInv
// polls one address, and polling twice would double a device's load for two
// identical sets of graphs.
func TestAddAltAddressIsIdempotent(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "alt-site")
	addDevice(t, dr, ctx, site.ID, "10.99.0.1", domain.DevicePending)
	var id string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE host(mgmt_ip)='10.99.0.1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := dr.AddAltAddress(ctx, id, "10.99.0.2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	addrs, err := dr.AddAltAddress(ctx, id, "10.99.0.2")
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	// Sweeps repeat, so the same address arrives again; recording it twice
	// would grow the list without bound.
	if len(addrs) != 1 || addrs[0] != "10.99.0.2" {
		t.Fatalf("alt addresses = %v after adding the same one twice", addrs)
	}
	if _, err := dr.AddAltAddress(ctx, "d_missing", "10.99.0.3"); err == nil {
		t.Fatal("accepted an address for a device that does not exist")
	}
}

// Merge folds a duplicate into the survivor. What it must not do is invent
// continuity: every series is keyed to the device that collected it, so the
// duplicate is retired rather than purged and its data stays readable.
func TestMergeRetiresTheDuplicateAndKeepsItsAddresses(t *testing.T) {
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "merge-site")
	addDevice(t, dr, ctx, site.ID, "10.100.0.1", domain.DevicePending)
	addDevice(t, dr, ctx, site.ID, "10.100.0.2", domain.DevicePending)

	var keepID, dupID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE host(mgmt_ip)='10.100.0.1'`).Scan(&keepID); err != nil {
		t.Fatal(err)
	}
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE host(mgmt_ip)='10.100.0.2'`).Scan(&dupID); err != nil {
		t.Fatal(err)
	}
	if _, err := dr.Pool.Exec(ctx,
		`UPDATE inventory.devices SET tags='["edge"]' WHERE id=$1`, dupID); err != nil {
		t.Fatal(err)
	}

	res, err := dr.Merge(ctx, keepID, dupID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.MetricsMoved || res.HistoryMoved {
		t.Error("merge claimed to move metrics or history, which it cannot")
	}

	// The duplicate's address moves so discovery stops proposing it and a
	// search for either address finds the device actually being polled.
	var alt []string
	var tags []string
	if err := dr.Pool.QueryRow(ctx, `
		SELECT coalesce(array(SELECT jsonb_array_elements_text(attrs->'alt_addresses')),'{}'),
		       coalesce(array(SELECT jsonb_array_elements_text(tags)),'{}')
		FROM inventory.devices WHERE id=$1`, keepID).Scan(&alt, &tags); err != nil {
		t.Fatal(err)
	}
	if len(alt) != 1 || alt[0] != "10.100.0.2" {
		t.Fatalf("survivor's alt addresses = %v", alt)
	}
	if len(tags) != 1 || tags[0] != "edge" {
		t.Fatalf("tags were not merged: %v", tags)
	}

	// Retired, not deleted: purging would destroy the history and the
	// interfaces its own metrics are keyed to.
	var status string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT status::text FROM inventory.devices WHERE id=$1`, dupID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "retired" {
		t.Fatalf("duplicate is %q, want retired", status)
	}
	// And it stops being polled, or the merge changed nothing operationally.
	var enabled int
	if err := dr.Pool.QueryRow(ctx,
		`SELECT count(*) FROM platform.polling_schedule WHERE device_id=$1 AND enabled`,
		dupID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("%d schedules still enabled on the retired duplicate", enabled)
	}

	// It disappears from the duplicate report, so the list empties as an
	// operator works through it.
	groups, err := dr.Duplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		for _, d := range g.Devices {
			if d.ID == dupID {
				t.Fatalf("retired duplicate still reported: %+v", g)
			}
		}
	}

	// Merging the same pair twice is a mistake, not an idempotent no-op.
	if _, err := dr.Merge(ctx, keepID, dupID); errx.KindOf(err) != errx.KindConflict {
		t.Fatalf("second merge gave %v, want conflict", errx.KindOf(err))
	}
	if _, err := dr.Merge(ctx, keepID, keepID); errx.KindOf(err) != errx.KindInvalid {
		t.Fatal("merging a device into itself was accepted")
	}
}
