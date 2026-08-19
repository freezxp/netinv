package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Read models for the device detail page (doc 09 §6, doc 30 §5).

type InterfaceRow struct {
	ID      string `json:"id"`
	IfIndex int    `json:"if_index"`
	Name    string `json:"name"`
	Alias   string `json:"alias"`
	// Descr is ifDescr. Kept alongside Alias because vendors disagree about
	// which one carries the useful text: some put the port's purpose in the
	// alias and a model string in the description, others the reverse. The
	// interface filter searches both for that reason.
	Descr       string `json:"descr"`
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
		       coalesce(descr,''),
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
		if err := rows.Scan(&i.ID, &i.IfIndex, &i.Name, &i.Alias, &i.Descr, &i.SpeedBPS,
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

// SyncRunRow is one entry of the device's sync history. Until this read model
// existed, platform.sync_runs was written on every poll and read by nobody: a
// device that failed to sync sat in 'pending' with the reason recorded in the
// database and no way to see it short of psql. A failing sync is the one thing
// on this page that explains a device which otherwise looks healthy, so the
// error text is part of the read model rather than a status flag.
type SyncRunRow struct {
	ID           string  `json:"id"`
	Trigger      string  `json:"trigger"`
	Status       string  `json:"status"`
	ChangesCount int     `json:"changes_count"`
	Error        string  `json:"error,omitempty"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   string  `json:"finished_at,omitempty"`
	DurationS    float64 `json:"duration_s,omitempty"`
}

func (r *DeviceRepo) SyncRuns(ctx context.Context, deviceID string, limit int) ([]SyncRunRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, trigger, status::text, changes_count, coalesce(error,''),
		       started_at, finished_at
		FROM platform.sync_runs
		WHERE device_id = $1 ORDER BY started_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "sync runs")
	}
	defer rows.Close()
	out := []SyncRunRow{}
	for rows.Next() {
		var s SyncRunRow
		var started time.Time
		var finished *time.Time
		if err := rows.Scan(&s.ID, &s.Trigger, &s.Status, &s.ChangesCount,
			&s.Error, &started, &finished); err != nil {
			return nil, err
		}
		s.StartedAt = started.UTC().Format(time.RFC3339)
		// A run with no finished_at is either in flight or was interrupted;
		// reporting a zero duration for it would read as "instant success".
		if finished != nil {
			s.FinishedAt = finished.UTC().Format(time.RFC3339)
			s.DurationS = finished.Sub(started).Seconds()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SiteCollection reports whether anything is consuming the job queue for a
// device's site. It is the other half of "why is this device pending": a sync
// that failed leaves a reason on the run row, but a site nobody polls produces
// no run at all, no failure and no log line, so the device page would
// otherwise have nothing to show for the commonest silent case.
//
// Absent means the scheduler has not yet dispatched to this site — no rows for
// a site whose devices are all disabled, or a deployment where the scheduler
// has not ticked since the upgrade that added this. Absence is reported as
// unknown rather than as "no poller", because claiming a fault from missing
// data is how a diagnostic loses its credibility.
type SiteCollection struct {
	SiteID          string `json:"site_id"`
	Known           bool   `json:"known"`
	Consumers       int    `json:"consumers"`
	Queued          int    `json:"queued"`
	NoConsumerSince string `json:"no_consumer_since,omitempty"`
	CheckedAt       string `json:"checked_at,omitempty"`
}

func (r *DeviceRepo) SiteCollection(ctx context.Context, deviceID string) (SiteCollection, error) {
	var sc SiteCollection
	var since, checked *time.Time
	err := r.Pool.QueryRow(ctx, `
		SELECT d.site_id, coalesce(h.consumers,0), coalesce(h.queued,0),
		       h.no_consumer_since, h.checked_at
		FROM inventory.devices d
		LEFT JOIN platform.site_collection_health h ON h.site_id = d.site_id
		WHERE d.id = $1`, deviceID).
		Scan(&sc.SiteID, &sc.Consumers, &sc.Queued, &since, &checked)
	if errors.Is(err, pgx.ErrNoRows) {
		return sc, errx.New(errx.KindNotFound, "device not found")
	}
	if err != nil {
		return sc, errx.Wrap(errx.KindTransient, err, "site collection health")
	}
	if checked != nil {
		sc.Known = true
		sc.CheckedAt = checked.UTC().Format(time.RFC3339)
	}
	if since != nil {
		sc.NoConsumerSince = since.UTC().Format(time.RFC3339)
	}
	return sc, nil
}

// InterfaceSearchRow is one hit from the fleet-wide interface search. It
// carries the owning device because the whole point of searching across
// devices is not knowing which one holds the port you are looking for.
type InterfaceSearchRow struct {
	ID          string `json:"id"`
	DeviceID    string `json:"device_id"`
	DeviceName  string `json:"device_name"`
	SiteID      string `json:"site_id"`
	IfIndex     int    `json:"if_index"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Descr       string `json:"descr"`
	SpeedBPS    int64  `json:"speed_bps"`
	AdminStatus int    `json:"admin_status"`
	OperStatus  int    `json:"oper_status"`
	State       string `json:"state"`
	Monitor     bool   `json:"monitor"`
	// Operator-owned, never written by sync: a port can be renamed,
	// re-aliased or renumbered without losing who it belongs to.
	Customer string   `json:"customer,omitempty"`
	Tags     []string `json:"tags"`
}

// SearchInterfaces finds interfaces across every device by alias, description
// or name.
//
// Alias is the field operators actually curate — it is where the circuit id,
// the customer name or the far end goes — and until now it was only readable
// one device at a time. "Which port is the London circuit on" was a question
// the product could not answer without knowing the answer first.
//
// The match is a case-insensitive substring across all three fields: an
// operator typing "uplink" does not know or care whether the previous engineer
// put it in ifAlias or ifDescr. Removed interfaces and retired devices are
// excluded — searching turns up ports you can act on, not history.
// InterfaceFilter narrows a search. Customer is an exact, case-insensitive
// match rather than a substring: a report says what one customer used, and
// "Acme" quietly including "Acme Holdings" is the kind of error that reaches
// an invoice.
type InterfaceFilter struct {
	Q        string
	Customer string
}

func (r *DeviceRepo) SearchInterfaces(ctx context.Context, q string, limit, offset int) ([]InterfaceSearchRow, int, error) {
	return r.FindInterfaces(ctx, InterfaceFilter{Q: q}, limit, offset)
}

func (r *DeviceRepo) FindInterfaces(ctx context.Context, f InterfaceFilter, limit, offset int) ([]InterfaceSearchRow, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	// A blank q lists everything, which is what an empty search box should show.
	// Customer joins the substring match too: someone typing a customer name
	// into the search box means the same thing as choosing it from the filter.
	pattern := "%" + f.Q + "%"
	where := `i.state != 'removed' AND d.status != 'retired'
		  AND ($1 = '' OR i.alias ILIKE $2 OR i.descr ILIKE $2 OR i.name ILIKE $2
		       OR i.customer ILIKE $2)
		  AND ($3 = '' OR lower(i.customer) = lower($3))`
	var total int
	if err := r.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE `+where, f.Q, pattern, f.Customer).Scan(&total); err != nil {
		return nil, 0, errx.Wrap(errx.KindTransient, err, "count interface search")
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT i.id, i.device_id, d.name, d.site_id, i.if_index,
		       coalesce(i.name,''), coalesce(i.alias,''), coalesce(i.descr,''),
		       coalesce(i.speed_bps,0), coalesce(i.admin_status,0),
		       coalesce(i.oper_status,0), i.state, i.monitor,
		       coalesce(i.customer,''), i.tags
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE `+where+`
		ORDER BY d.name, i.if_index
		LIMIT $4 OFFSET $5`, f.Q, pattern, f.Customer, limit, offset)
	if err != nil {
		return nil, 0, errx.Wrap(errx.KindTransient, err, "interface search")
	}
	defer rows.Close()
	out := []InterfaceSearchRow{}
	for rows.Next() {
		var i InterfaceSearchRow
		if err := rows.Scan(&i.ID, &i.DeviceID, &i.DeviceName, &i.SiteID,
			&i.IfIndex, &i.Name, &i.Alias, &i.Descr, &i.SpeedBPS,
			&i.AdminStatus, &i.OperStatus, &i.State, &i.Monitor,
			&i.Customer, &i.Tags); err != nil {
			return nil, 0, err
		}
		if i.Tags == nil {
			i.Tags = []string{}
		}
		out = append(out, i)
	}
	return out, total, rows.Err()
}
