package postgres

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/events"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

func seedDevice(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mgmt, attrs string) string {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	profID, credID, devID := id.New("pp"), id.New("cr"), id.New("d")
	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.polling_profiles (id, tenant_id, name, icmp_interval_s)
		VALUES ($1,'t_default',$1,30)`, profID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ($1,'t_default',$1,'snmp_v2c','\x00','\x00',1)`, credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.devices
			(id, tenant_id, site_id, connector_id, credential_id, profile_id,
			 name, mgmt_ip, status, attrs)
		VALUES ($1,'t_default','s_default','generic',$2,$3,$1,$4,'active',$5::jsonb)`,
		devID, credID, profID, mgmt, attrs); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return devID
}

// The listener resolves sources from this index, so what it maps is what gets
// a device and a series. A gateway sending from its WAN side must resolve
// through flow_exporters; a claimed address must never steal another device's
// management address.
func TestAddrIndexResolvesMgmtAndClaimedAddresses(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	gw := seedDevice(t, ctx, pool, "192.0.2.1", `{"flow_exporters":["203.0.113.5","192.0.2.2"]}`)
	fw := seedDevice(t, ctx, pool, "192.0.2.2", `{}`)
	_ = seedDevice(t, ctx, pool, "192.0.2.3", `{"flow_exporters":"not-a-list"}`)

	x := &AddrIndex{Pool: pool}
	if err := x.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]string{
		"192.0.2.1":    gw,
		"203.0.113.5":  gw, // claimed WAN address
		"192.0.2.2":    fw, // fw's own mgmt address beats gw's claim on it
		"198.51.100.1": "",
	} {
		if got := x.DeviceByAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s → %q, want %q", addr, got, want)
		}
	}
}

func TestInsertListAndPruneTraps(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	dev := seedDevice(t, ctx, pool, "192.0.2.10", `{}`)
	s := &Store{Pool: pool}

	now := time.Now().UTC().Truncate(time.Millisecond)
	traps := []*events.Trap{
		{At: now.Add(-40 * 24 * time.Hour), Source: netip.MustParseAddr("192.0.2.10"),
			Version: "2c", TrapOID: "1.3.6.1.6.3.1.1.5.1", Name: "coldStart", DeviceID: dev},
		{At: now, Source: netip.MustParseAddr("192.0.2.10"), Version: "2c",
			TrapOID: "1.3.6.1.6.3.1.1.5.3", Name: "linkDown", IfIndex: "7", DeviceID: dev,
			Varbinds: []events.Varbind{{OID: "1.3.6.1.2.1.2.2.1.8.7", Type: "Integer", Value: "2"}}},
		{At: now, Source: netip.MustParseAddr("203.0.113.9"),
			Agent: netip.MustParseAddr("198.51.100.4"), Version: "1",
			TrapOID: "1.3.6.1.4.1.9.0.1", Inform: false},
	}
	if err := s.InsertTraps(ctx, traps); err != nil {
		t.Fatalf("insert: %v", err)
	}

	all, err := s.ListTraps(ctx, TrapFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: %d rows, %v", len(all), err)
	}
	byDev, _ := s.ListTraps(ctx, TrapFilter{DeviceID: dev})
	if len(byDev) != 2 || byDev[0].TrapName == nil || *byDev[0].TrapName != "linkDown" {
		t.Fatalf("by device = %+v", byDev)
	}
	if byDev[0].IfIndex == nil || *byDev[0].IfIndex != 7 {
		t.Errorf("if_index = %v", byDev[0].IfIndex)
	}
	var vb []events.Varbind
	if err := json.Unmarshal(byDev[0].Varbinds, &vb); err != nil || len(vb) != 1 || vb[0].Value != "2" {
		t.Errorf("varbinds = %s (%v)", byDev[0].Varbinds, err)
	}
	unknown, _ := s.ListTraps(ctx, TrapFilter{UnknownOnly: true})
	if len(unknown) != 1 || unknown[0].Source != "203.0.113.9" ||
		unknown[0].AgentAddr == nil || *unknown[0].AgentAddr != "198.51.100.4" {
		t.Errorf("unknown = %+v", unknown)
	}
	named, _ := s.ListTraps(ctx, TrapFilter{Q: "link"})
	byOID, _ := s.ListTraps(ctx, TrapFilter{Q: "1.3.6.1.4.1.9"})
	if len(named) != 1 || len(byOID) != 1 {
		t.Errorf("q=link → %d, q=oid prefix → %d; want 1 and 1", len(named), len(byOID))
	}
	page2, _ := s.ListTraps(ctx, TrapFilter{Before: all[0].ID, Limit: 1})
	if len(page2) != 1 || page2[0].ID != all[1].ID {
		t.Errorf("keyset page = %+v", page2)
	}

	n, err := s.Prune(ctx, 30*24*time.Hour)
	if err != nil || n != 1 {
		t.Errorf("prune removed %d (%v), want the 40-day-old row", n, err)
	}
}
