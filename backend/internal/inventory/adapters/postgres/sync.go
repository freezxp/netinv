package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// SyncRepo implements app.SyncRepo — one transaction per applied diff
// (doc 16 §3: one sync diff = one transaction on one Device aggregate).
type SyncRepo struct{ Pool *pgxpool.Pool }

func (r *SyncRepo) LoadState(ctx context.Context, deviceID string) (*app.DeviceState, error) {
	st := &app.DeviceState{DeviceID: deviceID}
	err := r.Pool.QueryRow(ctx, `
		SELECT coalesce(sys_name,''), coalesce(sys_descr,''), coalesce(sys_object_id,''),
		       coalesce(sys_location,''), coalesce(sys_contact,''),
		       coalesce(vendor,''), coalesce(model,''), coalesce(serial_number,''),
		       coalesce(os_version,''), uptime_basis
		FROM inventory.devices WHERE id = $1 AND status != 'retired'`, deviceID).
		Scan(&st.SysName, &st.SysDescr, &st.SysObjectID, &st.SysLocation,
			&st.SysContact, &st.Vendor, &st.Model, &st.Serial, &st.OSVersion,
			&st.UptimeBasis)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "device not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "load device state")
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, if_index, coalesce(name,''), coalesce(alias,''), coalesce(descr,''),
		       coalesce(if_type,0), coalesce(mtu,0), coalesce(speed_bps,0),
		       coalesce(phys_address::text,''), coalesce(admin_status,0),
		       coalesce(oper_status,0), state, miss_streak
		FROM inventory.interfaces WHERE device_id = $1`, deviceID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "load interfaces")
	}
	defer rows.Close()
	for rows.Next() {
		var i app.IfState
		if err := rows.Scan(&i.ID, &i.IfIndex, &i.Name, &i.Alias, &i.Descr,
			&i.IfType, &i.MTU, &i.SpeedBPS, &i.PhysAddress, &i.AdminStatus,
			&i.OperStatus, &i.State, &i.MissStreak); err != nil {
			return nil, err
		}
		st.Interfaces = append(st.Interfaces, i)
	}
	return st, rows.Err()
}

func (r *SyncRepo) Apply(ctx context.Context, deviceID string, res app.DiffResult,
	adjacencies []wire.SyncAdjacency, run app.SyncRunRecord) (int, error) {
	history := 0
	err := pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		// Device fields + activation (pending/unreachable → active on good sync).
		set := []string{"updated_at = now()", "status = 'active'"}
		args := []any{deviceID}
		for col, val := range res.DeviceFields {
			args = append(args, val)
			set = append(set, col+" = nullif($"+itoa(len(args))+",'')")
		}
		if res.NewUptime != nil {
			args = append(args, *res.NewUptime)
			set = append(set, "uptime_basis = $"+itoa(len(args)))
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.devices SET `+strings.Join(set, ", ")+`
			WHERE id = $1 AND status IN ('pending','active','unreachable')`,
			args...); err != nil {
			return errx.Wrap(errx.KindTransient, err, "update device")
		}

		newIDs := map[int]string{} // ifIndex → interface id (for adjacency binding)
		for _, u := range res.Upserts {
			if u.ExistingID == "" {
				ifID := id.New("if")
				newIDs[u.Rec.IfIndex] = ifID
				if _, err := tx.Exec(ctx, `
					INSERT INTO inventory.interfaces
						(id, device_id, if_index, name, alias, descr, if_type, mtu,
						 speed_bps, phys_address, admin_status, oper_status, ever_up)
					VALUES ($1,$2,$3,nullif($4,''),nullif($5,''),nullif($6,''),$7,$8,$9,
					        nullif($10,'')::macaddr,$11,$12,$12 = 1)
					ON CONFLICT (device_id, if_index) DO UPDATE SET
						name = excluded.name, alias = excluded.alias,
						ever_up = inventory.interfaces.ever_up OR excluded.ever_up,
						state = 'present', miss_streak = 0, updated_at = now()`,
					ifID, deviceID, u.Rec.IfIndex, u.Rec.Name, u.Rec.Alias, u.Rec.Descr,
					u.Rec.IfType, u.Rec.MTU, u.Rec.SpeedBPS, u.Rec.PhysAddress,
					u.Rec.AdminStatus, u.Rec.OperStatus); err != nil {
					return errx.Wrap(errx.KindTransient, err, "insert interface")
				}
				continue
			}
			newIDs[u.Rec.IfIndex] = u.ExistingID
			if _, err := tx.Exec(ctx, `
				UPDATE inventory.interfaces SET
					if_index=$2, name=nullif($3,''), alias=nullif($4,''),
					descr=nullif($5,''), if_type=$6, mtu=$7, speed_bps=$8,
					phys_address=nullif($9,'')::macaddr, admin_status=$10,
					oper_status=$11, ever_up = ever_up OR $11 = 1,
					state='present', miss_streak=0,
					missing_since=NULL, updated_at=now()
				WHERE id=$1`,
				u.ExistingID, u.Rec.IfIndex, u.Rec.Name, u.Rec.Alias, u.Rec.Descr,
				u.Rec.IfType, u.Rec.MTU, u.Rec.SpeedBPS, u.Rec.PhysAddress,
				u.Rec.AdminStatus, u.Rec.OperStatus); err != nil {
				return errx.Wrap(errx.KindTransient, err, "update interface")
			}
		}
		lifecycle := []struct {
			ids []string
			sql string
		}{
			{res.MissingIDs, `UPDATE inventory.interfaces SET state='missing',
				miss_streak=1, missing_since=now(), updated_at=now() WHERE id = any($1)`},
			{res.StreakIDs, `UPDATE inventory.interfaces SET miss_streak=miss_streak+1,
				updated_at=now() WHERE id = any($1)`},
			{res.RemovedIDs, `UPDATE inventory.interfaces SET state='removed',
				updated_at=now() WHERE id = any($1)`},
		}
		for _, l := range lifecycle {
			if len(l.ids) == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, l.sql, l.ids); err != nil {
				return errx.Wrap(errx.KindTransient, err, "interface lifecycle")
			}
		}

		// Topology adjacencies (LLDP/CDP) — refresh last_seen, insert new.
		for _, a := range adjacencies {
			ifID := newIDs[a.LocalIfIndex]
			if _, err := tx.Exec(ctx, `
				INSERT INTO inventory.topology_links
					(id, a_device_id, a_if_id, b_sysname, b_port_descr, b_chassis_id,
					 protocol, state)
				VALUES ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),nullif($6,''),$7,'active')
				ON CONFLICT (a_device_id, a_if_id, b_chassis_id, b_port_descr)
				DO UPDATE SET b_sysname = excluded.b_sysname, state = 'active',
				              last_seen_at = now()`,
				id.New("tl"), deviceID, ifID, a.RemoteSysName, a.RemotePortID,
				a.RemoteChassis, a.Protocol); err != nil {
				return errx.Wrap(errx.KindTransient, err, "upsert adjacency")
			}
		}
		// Adjacencies not seen for 24h go stale (cheap sweep on each sync).
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.topology_links SET state='stale'
			WHERE a_device_id=$1 AND state='active'
			  AND last_seen_at < now() - interval '24 hours'`, deviceID); err != nil {
			return errx.Wrap(errx.KindTransient, err, "stale adjacencies")
		}

		// Asset history (FR-DEV-07).
		for _, c := range res.Changes {
			if _, err := tx.Exec(ctx, `
				INSERT INTO inventory.asset_history
					(device_id, object_kind, object_id, field, old_value, new_value,
					 change_kind, sync_run_id)
				VALUES ($1,$2,nullif($3,''),$4,nullif($5,''),nullif($6,''),$7,$8)`,
				deviceID, c.ObjectKind, c.ObjectID, c.Field, c.Old, c.New,
				c.ChangeKind, run.ID); err != nil {
				return errx.Wrap(errx.KindTransient, err, "asset history")
			}
			history++
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO platform.sync_runs (id, device_id, trigger, started_at,
				finished_at, status, changes_count)
			VALUES ($1,$2,$3,$4,now(),'ok',$5)`,
			run.ID, deviceID, run.Trigger, run.StartedAt, history)
		return errx.Wrap(errx.KindTransient, err, "record sync run")
	})
	return history, err
}

func (r *SyncRepo) RecordFailedRun(ctx context.Context, deviceID string, run app.SyncRunRecord) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO platform.sync_runs (id, device_id, trigger, started_at,
			finished_at, status, error)
		VALUES ($1,$2,$3,$4,now(),'failed',nullif($5,''))
		ON CONFLICT (id) DO NOTHING`,
		run.ID, deviceID, run.Trigger, run.StartedAt, run.Error)
	return errx.Wrap(errx.KindTransient, err, "record failed run")
}

// RedisLocker implements app.DeviceLocker over redisx.
type RedisLocker struct {
	Try func(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error)
}

func (l *RedisLocker) WithLock(ctx context.Context, deviceID string, fn func() error) error {
	release, ok, err := l.Try(ctx, "sync:device:"+deviceID, 2*time.Minute)
	if err != nil {
		return err
	}
	if !ok {
		return errx.New(errx.KindConflict, "device sync already in progress")
	}
	defer release()
	return fn()
}
