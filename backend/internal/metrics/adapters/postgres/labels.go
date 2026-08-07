// Package postgres — enrichment label snapshot source. NOTE: reads the
// inventory schema directly as a pragmatic v1 exception to strict context
// isolation; the extraction seam is the LabelSource interface — replace with
// an event-fed cache when netinv-ingester leaves the monolith (doc 05 §8).
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/metrics/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type LabelSource struct{ Pool *pgxpool.Pool }

func (l *LabelSource) Snapshot(ctx context.Context) (map[string]app.DeviceLabels, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT d.id, d.name, s.name, coalesce(d.vendor,'')
		FROM inventory.devices d
		JOIN platform.sites s ON s.id = d.site_id
		WHERE d.status != 'retired'`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "label snapshot")
	}
	defer rows.Close()
	out := map[string]app.DeviceLabels{}
	for rows.Next() {
		var id string
		var dl app.DeviceLabels
		if err := rows.Scan(&id, &dl.Device, &dl.Site, &dl.Vendor); err != nil {
			return nil, err
		}
		out[id] = dl
	}
	return out, rows.Err()
}
