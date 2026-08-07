// Package postgres — Collection repositories.
package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type ScheduleRepo struct{ Pool *pgxpool.Pool }

// Due claims due schedules with a single UPDATE … RETURNING: advancing
// next_due_at atomically means a crashed or raced leader cannot double-book
// (publishes are idempotent anyway — doc 05 §5). Jitter is ±5% of interval.
func (r *ScheduleRepo) Due(ctx context.Context, now time.Time, limit int) ([]domain.DueSchedule, error) {
	rows, err := r.Pool.Query(ctx, `
		WITH due AS (
			SELECT ps.id
			FROM platform.polling_schedule ps
			JOIN inventory.devices d ON d.id = ps.device_id
			WHERE ps.enabled
			  AND ps.next_due_at <= $1
			  AND d.status IN ('pending','active','unreachable')
			ORDER BY ps.next_due_at
			LIMIT $2
			FOR UPDATE OF ps SKIP LOCKED
		)
		UPDATE platform.polling_schedule ps SET
			next_due_at = $1
				+ make_interval(secs => ps.interval_s)
				+ make_interval(secs => (random() - 0.5) * ps.interval_s * 0.1),
			last_run_at = $1
		FROM due, inventory.devices d
		WHERE ps.id = due.id AND d.id = ps.device_id
		RETURNING ps.id, d.id, d.site_id, ps.family::text, host(d.mgmt_ip),
		          d.connector_id, d.credential_id, ps.interval_s`,
		now, limit)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "claim due schedules")
	}
	defer rows.Close()
	var out []domain.DueSchedule
	for rows.Next() {
		var d domain.DueSchedule
		var family string
		if err := rows.Scan(&d.ScheduleID, &d.DeviceID, &d.SiteID, &family,
			&d.MgmtIP, &d.ConnectorID, &d.CredentialID, &d.IntervalS); err != nil {
			return nil, err
		}
		d.Family = domain.PollFamily(family)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *ScheduleRepo) ActiveSites(ctx context.Context) ([]string, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT DISTINCT site_id FROM inventory.devices WHERE status != 'retired'`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "active sites")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
