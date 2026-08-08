package postgres

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

type DeviceRepo struct{ Pool *pgxpool.Pool }

const deviceCols = `id, tenant_id, site_id, connector_id, credential_id, profile_id,
	name, host(mgmt_ip), status, coalesce(sys_name,''), coalesce(sys_descr,''),
	coalesce(vendor,''), coalesce(model,''), coalesce(serial_number,''),
	coalesce(os_version,''), tags, coalesce(notes,''), attrs,
	coalesce(wan_capacity_bps,0), created_at, updated_at`

func scanDevice(row pgx.Row) (*domain.Device, error) {
	d := &domain.Device{}
	var status string
	err := row.Scan(&d.ID, &d.TenantID, &d.SiteID, &d.ConnectorID, &d.CredentialID,
		&d.ProfileID, &d.Name, &d.MgmtIP, &status, &d.SysName, &d.SysDescr,
		&d.Vendor, &d.Model, &d.SerialNumber, &d.OSVersion, &d.Tags, &d.Notes,
		&d.Attrs, &d.WANCapacityBPS, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "device not found")
	}
	d.Status = domain.DeviceStatus(status)
	return d, errx.Wrap(errx.KindTransient, err, "scan device")
}

func (r *DeviceRepo) Get(ctx context.Context, did string) (*domain.Device, error) {
	return scanDevice(r.Pool.QueryRow(ctx,
		`SELECT `+deviceCols+` FROM inventory.devices WHERE id = $1`, did))
}

// List uses keyset pagination on id (FR-API-02).
func (r *DeviceRepo) List(ctx context.Context, f app.DeviceFilter) ([]*domain.Device, string, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := `SELECT ` + deviceCols + ` FROM inventory.devices WHERE id > $1`
	args := []any{f.Cursor}
	if f.SiteID != "" {
		args = append(args, f.SiteID)
		q += ` AND site_id = $` + itoa(len(args))
	}
	if len(f.Status) > 0 {
		args = append(args, f.Status)
		q += ` AND status = any($` + itoa(len(args)) + `::inventory.device_status[])`
	} else {
		q += ` AND status != 'retired'`
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		n := itoa(len(args))
		q += ` AND (name ILIKE $` + n + ` OR host(mgmt_ip) LIKE $` + n +
			` OR coalesce(serial_number,'') ILIKE $` + n +
			` OR coalesce(sys_name,'') ILIKE $` + n + `)`
	}
	args = append(args, f.Limit+1)
	q += ` ORDER BY id LIMIT $` + itoa(len(args))

	rows, err := r.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", errx.Wrap(errx.KindTransient, err, "list devices")
	}
	defer rows.Close()
	var out []*domain.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

func (r *DeviceRepo) RefsExist(ctx context.Context, siteID, connectorID, credentialID, profileID string) error {
	var site, conn, cred, prof bool
	err := r.Pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM platform.sites WHERE id=$1),
		EXISTS(SELECT 1 FROM platform.connectors WHERE id=$2 AND enabled),
		EXISTS(SELECT 1 FROM inventory.credentials WHERE id=$3),
		EXISTS(SELECT 1 FROM platform.polling_profiles WHERE id=$4)`,
		siteID, connectorID, credentialID, profileID).Scan(&site, &conn, &cred, &prof)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "check refs")
	}
	switch {
	case !site:
		return errx.New(errx.KindInvalid, "site not found")
	case !conn:
		return errx.New(errx.KindInvalid, "connector not found or disabled")
	case !cred:
		return errx.New(errx.KindInvalid, "credential not found")
	case !prof:
		return errx.New(errx.KindInvalid, "polling profile not found")
	}
	return nil
}

// Create inserts the device plus its polling_schedule rows (one per family,
// jittered so fleets don't poll in lockstep — doc 05 §5) in one transaction.
func (r *DeviceRepo) Create(ctx context.Context, d *domain.Device) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO inventory.devices
				(id, tenant_id, site_id, connector_id, credential_id, profile_id,
				 name, mgmt_ip, status, tags, notes, attrs)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::inet,$9,$10,nullif($11,''),coalesce($12,'{}'::jsonb))`,
			d.ID, d.TenantID, d.SiteID, d.ConnectorID, d.CredentialID, d.ProfileID,
			d.Name, d.MgmtIP, string(d.Status), d.Tags, d.Notes, d.Attrs)
		if isUnique(err) {
			return errx.New(errx.KindConflict, "a device with that management IP already exists")
		}
		if err != nil {
			return errx.Wrap(errx.KindTransient, err, "insert device")
		}
		return insertSchedules(ctx, tx, d.ID, d.ProfileID)
	})
}

func insertSchedules(ctx context.Context, tx pgx.Tx, deviceID, profileID string) error {
	var traffic, health, icmp, sync int
	if err := tx.QueryRow(ctx, `
		SELECT traffic_interval_s, health_interval_s, icmp_interval_s, sync_interval_s
		FROM platform.polling_profiles WHERE id = $1`, profileID).
		Scan(&traffic, &health, &icmp, &sync); err != nil {
		return errx.Wrap(errx.KindInvalid, err, "load profile")
	}
	families := map[string]int{"traffic": traffic, "health": health, "icmp": icmp, "sync": sync}
	for family, interval := range families {
		due := time.Now().UTC()
		if family != "sync" { // sync runs immediately at onboarding (doc 07 §2)
			due = due.Add(time.Duration(rand.IntN(interval)) * time.Second)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.polling_schedule (device_id, family, interval_s, next_due_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (device_id, family)
			DO UPDATE SET interval_s = excluded.interval_s`,
			deviceID, family, interval, due); err != nil {
			return errx.Wrap(errx.KindTransient, err, "insert schedule")
		}
	}
	return nil
}

func (r *DeviceRepo) Update(ctx context.Context, d *domain.Device) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE inventory.devices SET
				site_id=$2, connector_id=$3, credential_id=$4, profile_id=$5,
				name=$6, tags=$7, notes=nullif($8,''),
				wan_capacity_bps=nullif($9::bigint,0), updated_at=now()
			WHERE id=$1`,
			d.ID, d.SiteID, d.ConnectorID, d.CredentialID, d.ProfileID,
			d.Name, d.Tags, d.Notes, d.WANCapacityBPS)
		if err != nil {
			return errx.Wrap(errx.KindTransient, err, "update device")
		}
		if tag.RowsAffected() == 0 {
			return errx.New(errx.KindNotFound, "device not found")
		}
		// Profile changes re-pace schedules on next insert-or-update.
		return insertSchedules(ctx, tx, d.ID, d.ProfileID)
	})
}

func (r *DeviceRepo) SetStatus(ctx context.Context, did string, status domain.DeviceStatus) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		retire := ""
		if status == domain.DeviceRetired {
			retire = ", retired_at = now()"
		}
		tag, err := tx.Exec(ctx,
			`UPDATE inventory.devices SET status=$2, updated_at=now()`+retire+` WHERE id=$1`,
			did, string(status))
		if err != nil {
			return errx.Wrap(errx.KindTransient, err, "set device status")
		}
		if tag.RowsAffected() == 0 {
			return errx.New(errx.KindNotFound, "device not found")
		}
		enabled := status == domain.DeviceActive || status == domain.DevicePending
		_, err = tx.Exec(ctx,
			`UPDATE platform.polling_schedule SET enabled=$2 WHERE device_id=$1`,
			did, enabled)
		return errx.Wrap(errx.KindTransient, err, "toggle schedules")
	})
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// Purge hard-deletes a device. Interfaces, components, schedules, sync runs,
// group memberships and topology links go with it via ON DELETE CASCADE
// (doc 08); alert instances keep their history with device_id nulled. Asset
// history is append-only and deliberately unreferenced, so it is removed
// explicitly here — a purged device should leave nothing behind in inventory.
func (r *DeviceRepo) Purge(ctx context.Context, deviceID string) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM inventory.asset_history WHERE device_id = $1`, deviceID); err != nil {
			return errx.Wrap(errx.KindTransient, err, "purge asset history")
		}
		tag, err := tx.Exec(ctx, `DELETE FROM inventory.devices WHERE id = $1`, deviceID)
		if err != nil {
			return errx.Wrap(errx.KindTransient, err, "purge device")
		}
		if tag.RowsAffected() == 0 {
			return errx.New(errx.KindNotFound, "device not found")
		}
		return nil
	})
}
