package maps

import (
	"context"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// End to end against a real database: the builder is unit-tested, but the map
// it produces still has to pass the same validation a hand-drawn one does and
// land as a draft with its link bindings written. The pilot could not prove
// this — its gateways are joined by WireGuard tunnels and report no LLDP at
// all, so the only path exercised there is the refusal.
func TestGenerateFromTopologyWritesADraftWithBoundLinks(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	store := &Store{Pool: pool}

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
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`, credID, "gen-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	// Two managed devices, one interface each, adjacent over LLDP in both
	// directions — which is how a real pair reports itself.
	type dev struct {
		id, ifID, name string
		ifIndex        int
	}
	devs := []dev{
		{id.New("d"), id.New("if"), "core-a", 11},
		{id.New("d"), id.New("if"), "core-b", 22},
	}
	for i, d := range devs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.devices
				(id, tenant_id, site_id, connector_id, credential_id, profile_id,
				 name, mgmt_ip, status)
			VALUES ($1,'t_default','s_default','generic',$2,'pp_default',$3,$4::inet,'active')`,
			d.id, credID, d.name, "10.92.0."+string(rune('1'+i))); err != nil {
			t.Fatalf("seed device: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.interfaces (id, device_id, if_index, name, state)
			VALUES ($1,$2,$3,$4,'present')`,
			d.ifID, d.id, d.ifIndex, "uplink"); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}
	for _, l := range []struct{ a, aIf, b string }{
		{devs[0].id, devs[0].ifID, devs[1].id},
		{devs[1].id, devs[1].ifID, devs[0].id},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.topology_links
				(id, a_device_id, a_if_id, b_device_id, b_sysname, b_port_descr,
				 b_chassis_id, protocol, state)
			VALUES ($1,$2,$3,$4,'peer','uplink',$5,'lldp','active')`,
			id.New("tl"), l.a, l.aIf, l.b, id.New("ch")); err != nil {
			t.Fatalf("seed topology link: %v", err)
		}
	}

	meta, nodes, links, err := store.GenerateFromTopology(ctx, "Generated", "")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if nodes != 2 || links != 1 {
		t.Fatalf("drew %d nodes / %d links, want 2 and 1 (both directions are one link)",
			nodes, links)
	}

	def, rev, err := store.Load(ctx, meta.ID, "draft")
	if err != nil {
		t.Fatalf("load draft: %v", err)
	}
	if rev < 1 {
		t.Fatalf("draft rev is %d", rev)
	}
	if len(def.Nodes) != 2 || len(def.Links) != 1 {
		t.Fatalf("stored document has %d nodes / %d links", len(def.Nodes), len(def.Links))
	}
	l := def.Links[0]
	if l.AEndpoint == nil || l.BEndpoint == nil {
		t.Fatalf("link is half-bound after a round trip: %+v", l)
	}

	// Not published: what LLDP saw is a starting point, and publishing is the
	// operator's decision after they have moved things where they belong.
	var published int
	if err := pool.QueryRow(ctx,
		`SELECT published_rev FROM maps.maps WHERE id = $1`, meta.ID).
		Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("generated map is published at rev %d", published)
	}

	// Publishing is where endpoints are resolved to stable interface row ids —
	// the binding that survives an ifIndex renumber — and where a link whose
	// endpoint does not resolve is rejected outright. Generated endpoints have
	// to pass that gate exactly like hand-drawn ones, so publish here rather
	// than trust that they would.
	if _, err := store.Publish(ctx, meta.ID, ""); err != nil {
		t.Fatalf("publishing the generated map: %v", err)
	}
	var bound int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM maps.map_links
		WHERE map_id = $1 AND a_if_id IS NOT NULL AND b_if_id IS NOT NULL`,
		meta.ID).Scan(&bound); err != nil {
		t.Fatalf("read map_links: %v", err)
	}
	if bound != 1 {
		t.Fatalf("%d fully-bound rows in maps.map_links, want 1", bound)
	}
}

// Nothing drawable must not leave a half-made artefact behind. An empty map in
// the list is worse than an error: it looks like the feature worked.
func TestGenerateFromTopologyCreatesNothingWhenThereIsNoTopology(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	store := &Store{Pool: pool}

	if _, _, _, err := store.GenerateFromTopology(ctx, "Empty", ""); err == nil {
		t.Fatal("generated a map from no adjacencies")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM maps.maps`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d maps created despite the refusal", n)
	}
}

// ifIndex is a snapshot of the moment a link was drawn, and agents renumber:
// a pilot gateway moved ppp2 from 76 to 41 across a reboot. The live view has
// resolved through maps.map_links' stable row id since that bug, but publish
// still checked the document's index — so a correct map became unpublishable
// and the operator was told an endpoint "does not resolve" while the link was
// drawing traffic perfectly.
func TestPublishSurvivesAnIfIndexRenumber(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	store := &Store{Pool: pool}

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
		VALUES ($1,'t_default',$2,'snmp_v2c','\x00','\x00',1)`, credID, "renum-"+credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	devA, devB := id.New("d"), id.New("d")
	ifA, ifB := id.New("if"), id.New("if")
	for i, d := range []struct{ dev, iface, ip string }{{devA, ifA, "10.210.0.1"}, {devB, ifB, "10.210.0.2"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.devices
				(id, tenant_id, site_id, connector_id, credential_id, profile_id, name, mgmt_ip, status)
			VALUES ($1,'t_default','s_default','generic',$2,'pp_default',$3,$4::inet,'active')`,
			d.dev, credID, "renum-dev-"+d.dev, d.ip); err != nil {
			t.Fatalf("seed device: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.interfaces (id, device_id, if_index, name, state)
			VALUES ($1,$2,$3,'ppp2','present')`, d.iface, d.dev, 76+i); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}

	meta, err := store.Create(ctx, "renumber map", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	def := Definition{Schema: "netinv.map/1",
		Nodes: []Node{{ID: "n1", Kind: "device", DeviceID: devA},
			{ID: "n2", Kind: "device", DeviceID: devB}},
		Links: []Link{{ID: "l1", From: "n1", To: "n2",
			AEndpoint: &Endpoint{DeviceID: devA, IfIndex: 76},
			BEndpoint: &Endpoint{DeviceID: devB, IfIndex: 77}}},
	}
	if err := store.SaveDraft(ctx, meta.ID, &def, ""); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	// The first publish binds the link to the interfaces' own row ids.
	if _, err := store.Publish(ctx, meta.ID, ""); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// The device reboots and renumbers: same interface row, new ifIndex.
	if _, err := pool.Exec(ctx,
		`UPDATE inventory.interfaces SET if_index = 41 WHERE id = $1`, ifA); err != nil {
		t.Fatalf("renumber: %v", err)
	}

	// Publishing again must succeed, resolving through the stable binding
	// rather than the index the document remembers.
	if _, err := store.Publish(ctx, meta.ID, ""); err != nil {
		t.Fatalf("publish after renumber: %v", err)
	}

	// And the document is healed, so the next reader is not left correcting
	// the same stale value.
	got, _, err := store.Load(ctx, meta.ID, "draft")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Links[0].AEndpoint.IfIndex != 41 {
		t.Fatalf("document still says ifIndex %d after publish",
			got.Links[0].AEndpoint.IfIndex)
	}

	// An endpoint whose interface is genuinely gone still fails: a stale
	// binding is no better evidence than a stale index.
	if _, err := pool.Exec(ctx,
		`UPDATE inventory.interfaces SET state='removed' WHERE id=$1`, ifB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(ctx, meta.ID, ""); err == nil {
		t.Fatal("published with an endpoint whose interface no longer exists")
	}
}
