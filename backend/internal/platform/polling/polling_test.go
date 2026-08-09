package polling

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

func TestSetRejectsIntervalsOutsideTheAllowedSet(t *testing.T) {
	s := &Store{}
	for _, bad := range []int{0, -60, 7, 30, 120, 3600} {
		_, err := s.Set(context.Background(), bad)
		if err == nil {
			t.Errorf("interval %d was accepted", bad)
			continue
		}
		// Must be a client error, not a 500: the operator picked a bad value,
		// the server is fine.
		if errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("interval %d: kind = %v, want KindInvalid", bad, errx.KindOf(err))
		}
	}
}

// The scheduler reads polling_schedule, not the profile. Updating only the
// profile would change what the UI reports while collection carried on at the
// old cadence — the settings page would be confidently wrong.
func TestSetReschedulesEveryDeviceNotJustTheProfile(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO platform.connectors
			(id, vendor, display_name, version, capabilities, sys_object_id_prefixes, enabled)
		VALUES ('generic','Generic','Generic SNMP','test','[]','[]',true)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory.credentials (id, tenant_id, name, kind, enc_payload, enc_dek, key_version)
		VALUES ('cr_p','t_default','poll-test','snmp_v2c','\x00','\x00',1)`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	for i, id := range []string{"d_a", "d_b"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.devices
				(id, tenant_id, site_id, connector_id, credential_id, profile_id,
				 name, mgmt_ip, status, tags, attrs)
			VALUES ($1,'t_default','s_default','generic','cr_p','pp_default',
			        $2,$3,'pending','[]','{}')`,
			id, "dev-"+id, "198.51.100."+string(rune('1'+i))); err != nil {
			t.Fatalf("seed device %s: %v", id, err)
		}
		for _, fam := range []string{"traffic", "health", "icmp"} {
			if _, err := pool.Exec(ctx, `
				INSERT INTO platform.polling_schedule (device_id, family, interval_s, next_due_at)
				VALUES ($1,$2,$3, now())`, id, fam, 60); err != nil {
				t.Fatalf("seed schedule: %v", err)
			}
		}
	}

	store := &Store{Pool: pool}
	got, err := store.Set(ctx, 600)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.TrafficIntervalS != 600 {
		t.Errorf("traffic interval = %d, want 600", got.TrafficIntervalS)
	}

	var traffic, health, icmp int
	if err := pool.QueryRow(ctx, `
		SELECT
			max(interval_s) FILTER (WHERE family='traffic'),
			max(interval_s) FILTER (WHERE family='health'),
			max(interval_s) FILTER (WHERE family='icmp')
		FROM platform.polling_schedule
		WHERE device_id IN ('d_a','d_b')`).Scan(&traffic, &health, &icmp); err != nil {
		t.Fatalf("read schedules: %v", err)
	}
	if traffic != 600 {
		t.Errorf("schedule traffic interval = %d, want 600 — the scheduler reads "+
			"this table, so collection would have stayed at the old cadence", traffic)
	}
	// Health must not out-poll traffic once traffic is slowed.
	if health != 600 {
		t.Errorf("schedule health interval = %d, want 600 (raised to match traffic)", health)
	}
	// ICMP is the fastest signal that a device has gone down; slowing it would
	// delay every outage alert by the same amount.
	if icmp != 60 {
		t.Errorf("schedule icmp interval = %d, want it left alone at 60", icmp)
	}
}

// Speeding collection back up must lower the schedule too, or the fleet stays
// slow while the UI claims otherwise.
func TestSetCanSpeedCollectionBackUp(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	store := &Store{Pool: pool}

	if _, err := store.Set(ctx, 900); err != nil {
		t.Fatalf("Set(900): %v", err)
	}
	got, err := store.Set(ctx, 60)
	if err != nil {
		t.Fatalf("Set(60): %v", err)
	}
	if got.TrafficIntervalS != 60 {
		t.Errorf("traffic interval = %d, want 60", got.TrafficIntervalS)
	}
	// Health was raised to 900 on the way up and is not lowered again: an
	// operator who widened health deliberately should not have it silently
	// narrowed by a traffic change.
	if got.HealthIntervalS < 60 {
		t.Errorf("health interval = %d, want at least the traffic interval",
			got.HealthIntervalS)
	}
}

func TestGetReportsTheAllowedChoices(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	s, err := (&Store{Pool: pool}).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(s.Allowed) == 0 {
		t.Fatal("no allowed intervals reported; the UI would have to hard-code them")
	}
	for _, want := range Allowed {
		var found bool
		for _, got := range s.Allowed {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("allowed set is missing %d", want)
		}
	}
}

func TestDescribeReadsAsAnInterval(t *testing.T) {
	cases := map[int]string{30: "30s", 60: "1 min", 300: "5 min", 900: "15 min"}
	for in, want := range cases {
		if got := Describe(in); got != want {
			t.Errorf("Describe(%d) = %q, want %q", in, got, want)
		}
	}
}

// The audit column is `inet`, and audit failures are logged rather than
// returned — so a RemoteAddr passed through with its port made the change
// succeed while its record silently did not appear.
func TestSourceIPHasNoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/platform/polling", nil)
	r.RemoteAddr = "172.18.0.13:56146"
	if got, want := sourceIP(r), "172.18.0.13"; got != want {
		t.Errorf("sourceIP = %q, want %q", got, want)
	}

	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got, want := sourceIP(r), "203.0.113.9"; got != want {
		t.Errorf("with XFF: sourceIP = %q, want the original client %q", got, want)
	}
}
