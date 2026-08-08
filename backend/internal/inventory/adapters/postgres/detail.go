package postgres

import (
	"context"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Read models for the device detail page (doc 09 §6, doc 30 §5).

type InterfaceRow struct {
	ID          string `json:"id"`
	IfIndex     int    `json:"if_index"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	SpeedBPS    int64  `json:"speed_bps"`
	MTU         int    `json:"mtu"`
	AdminStatus int    `json:"admin_status"`
	OperStatus  int    `json:"oper_status"`
	State       string `json:"state"`
	Monitor     bool   `json:"monitor"`
	// EverUp is false for a port never observed in service. Those are excluded
	// from interface-down alerting (FR-ALR-08), so the UI must be able to say
	// why a down port is not alerting.
	EverUp bool `json:"ever_up"`
}

func (r *DeviceRepo) Interfaces(ctx context.Context, deviceID string) ([]InterfaceRow, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, if_index, coalesce(name,''), coalesce(alias,''),
		       coalesce(speed_bps,0), coalesce(mtu,0), coalesce(admin_status,0),
		       coalesce(oper_status,0), state, monitor, ever_up
		FROM inventory.interfaces
		WHERE device_id = $1 AND state != 'removed'
		ORDER BY if_index`, deviceID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "interfaces")
	}
	defer rows.Close()
	out := []InterfaceRow{}
	for rows.Next() {
		var i InterfaceRow
		if err := rows.Scan(&i.ID, &i.IfIndex, &i.Name, &i.Alias, &i.SpeedBPS,
			&i.MTU, &i.AdminStatus, &i.OperStatus, &i.State, &i.Monitor,
			&i.EverUp); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

type HistoryRow struct {
	ObjectKind string `json:"object_kind"`
	ObjectID   string `json:"object_id"`
	Field      string `json:"field"`
	OldValue   string `json:"old_value"`
	NewValue   string `json:"new_value"`
	ChangeKind string `json:"change_kind"`
	SyncRunID  string `json:"sync_run_id,omitempty"`
	DetectedAt string `json:"detected_at"`
}

func (r *DeviceRepo) History(ctx context.Context, deviceID string, limit int) ([]HistoryRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT object_kind, coalesce(object_id,''), field, coalesce(old_value,''),
		       coalesce(new_value,''), change_kind::text, coalesce(sync_run_id,''),
		       detected_at
		FROM inventory.asset_history
		WHERE device_id = $1 ORDER BY detected_at DESC, id DESC LIMIT $2`,
		deviceID, limit)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "history")
	}
	defer rows.Close()
	out := []HistoryRow{}
	for rows.Next() {
		var h HistoryRow
		var at time.Time
		if err := rows.Scan(&h.ObjectKind, &h.ObjectID, &h.Field, &h.OldValue,
			&h.NewValue, &h.ChangeKind, &h.SyncRunID, &at); err != nil {
			return nil, err
		}
		h.DetectedAt = at.UTC().Format(time.RFC3339)
		out = append(out, h)
	}
	return out, rows.Err()
}

type NeighborRow struct {
	LocalIfID    string `json:"local_if_id,omitempty"`
	RemoteSys    string `json:"remote_sysname"`
	RemotePort   string `json:"remote_port"`
	RemoteDevice string `json:"remote_device_id,omitempty"`
	Protocol     string `json:"protocol"`
	State        string `json:"state"`
	LastSeenAt   string `json:"last_seen_at"`
}

func (r *DeviceRepo) Neighbors(ctx context.Context, deviceID string) ([]NeighborRow, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT coalesce(a_if_id,''), coalesce(b_sysname,''), coalesce(b_port_descr,''),
		       coalesce(b_device_id,''), protocol, state, last_seen_at
		FROM inventory.topology_links
		WHERE a_device_id = $1 ORDER BY last_seen_at DESC`, deviceID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "neighbors")
	}
	defer rows.Close()
	out := []NeighborRow{}
	for rows.Next() {
		var n NeighborRow
		var at time.Time
		if err := rows.Scan(&n.LocalIfID, &n.RemoteSys, &n.RemotePort,
			&n.RemoteDevice, &n.Protocol, &n.State, &at); err != nil {
			return nil, err
		}
		n.LastSeenAt = at.UTC().Format(time.RFC3339)
		out = append(out, n)
	}
	return out, rows.Err()
}
