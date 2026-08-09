// Package polling exposes the fleet-wide collection cadence as one setting.
//
// Intervals live per family on a polling profile, and until now the only way
// to change them was SQL. The common case is not that: it is an operator
// deciding the whole fleet should be polled every five minutes instead of
// every minute, usually to cut load on rate-limiting switches or to slow
// storage growth.
//
// Only the traffic interval is offered. The others are deliberately left
// alone, and the reasons matter:
//
//   - ICMP stays fast (30 s). Availability detection is the product's core
//     job and costs almost nothing; slowing it to fifteen minutes would mean
//     an outage goes unnoticed for fifteen minutes.
//   - Health follows traffic only when traffic overtakes it, so a fleet asked
//     to poll every fifteen minutes does not quietly keep reading CPU every
//     five.
//   - Inventory sync is measured in hours and is unrelated to metric cadence.
package polling

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Allowed traffic intervals, in seconds. A fixed set rather than a free
// number: the interval interacts with rate() lookbacks, chart resolution and
// storage growth, and every value here has been checked against all three.
// Arbitrary values invite 7-second polling and 3-hour gaps.
var Allowed = []int{60, 300, 600, 900}

// Settings is the fleet-wide cadence.
type Settings struct {
	TrafficIntervalS int `json:"traffic_interval_s"`
	HealthIntervalS  int `json:"health_interval_s"`
	ICMPIntervalS    int `json:"icmp_interval_s"`
	SyncIntervalS    int `json:"sync_interval_s"`
	// Allowed choices for the traffic interval, so the UI need not hard-code
	// them and drift from what the server accepts.
	Allowed []int `json:"allowed_traffic_interval_s"`
	// Devices that a change would re-schedule.
	Devices int `json:"devices"`
}

type Store struct{ Pool *pgxpool.Pool }

// Get reads the default profile's cadence.
func (s *Store) Get(ctx context.Context) (*Settings, error) {
	out := &Settings{Allowed: Allowed}
	err := s.Pool.QueryRow(ctx, `
		SELECT traffic_interval_s, health_interval_s, icmp_interval_s, sync_interval_s,
		       (SELECT count(*) FROM inventory.devices d WHERE d.profile_id = pp.id)
		FROM platform.polling_profiles pp
		WHERE pp.is_default OR pp.id = 'pp_default'
		ORDER BY pp.is_default DESC
		LIMIT 1`).
		Scan(&out.TrafficIntervalS, &out.HealthIntervalS, &out.ICMPIntervalS,
			&out.SyncIntervalS, &out.Devices)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "load polling profile")
	}
	return out, nil
}

// Set changes the traffic interval for the default profile and re-schedules
// every device using it. Returns the settings as they now stand.
//
// The schedule rows carry their own copy of the interval — the scheduler reads
// them, not the profile — so updating the profile alone would change what the
// UI reports while collection carried on at the old cadence. Both move in one
// transaction.
func (s *Store) Set(ctx context.Context, trafficS int) (*Settings, error) {
	if !allowed(trafficS) {
		return nil, errx.New(errx.KindInvalid,
			"traffic_interval_s must be one of %v", Allowed)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "begin")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var profileID string
	var healthS int
	if err := tx.QueryRow(ctx, `
		SELECT id, health_interval_s FROM platform.polling_profiles
		WHERE is_default OR id = 'pp_default'
		ORDER BY is_default DESC LIMIT 1`).Scan(&profileID, &healthS); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "load polling profile")
	}

	// Health never polls more often than traffic. Asking for a fifteen-minute
	// cadence and still reading CPU every five would not be what anyone meant.
	if healthS < trafficS {
		healthS = trafficS
	}

	if _, err := tx.Exec(ctx, `
		UPDATE platform.polling_profiles
		SET traffic_interval_s = $2, health_interval_s = $3
		WHERE id = $1`, profileID, trafficS, healthS); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "update profile")
	}

	// next_due_at is left alone. A shortened interval simply polls sooner than
	// the row expects, and a lengthened one polls once more at the old time
	// before settling — both self-correct within a cycle, whereas rewriting
	// due times would stampede every device at once.
	if _, err := tx.Exec(ctx, `
		UPDATE platform.polling_schedule ps
		SET interval_s = CASE ps.family
			WHEN 'traffic' THEN $2::int
			WHEN 'health'  THEN $3::int
			ELSE ps.interval_s END
		FROM inventory.devices d
		WHERE d.id = ps.device_id AND d.profile_id = $1
		  AND ps.family IN ('traffic','health')`,
		profileID, trafficS, healthS); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "reschedule devices")
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "commit")
	}
	return s.Get(ctx)
}

func allowed(v int) bool {
	for _, a := range Allowed {
		if a == v {
			return true
		}
	}
	return false
}

// Describe renders an interval the way the UI labels it.
func Describe(seconds int) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%d min", seconds/60)
}
