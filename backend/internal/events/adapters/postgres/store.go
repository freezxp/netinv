// Package postgres — storage for the events context (doc 36 §5). Reads
// inventory.devices directly to resolve source addresses, the same pragmatic
// v1 exception the ingester's label snapshot makes (doc 05 §8).
package postgres

import (
	"context"
	"encoding/json"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/events"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) InsertTraps(ctx context.Context, traps []*events.Trap) error {
	rows := make([][]any, 0, len(traps))
	for _, t := range traps {
		vb, err := json.Marshal(t.Varbinds)
		if err != nil {
			return err
		}
		var dev, name, ifIndex, agent any
		if t.DeviceID != "" {
			dev = t.DeviceID
		}
		if t.Name != "" {
			name = t.Name
		}
		if n, err := strconv.ParseInt(t.IfIndex, 10, 32); err == nil {
			ifIndex = int32(n)
		}
		if t.Agent.IsValid() {
			agent = t.Agent
		}
		rows = append(rows, []any{t.At, t.Source, agent, dev, t.Version,
			t.Inform, t.TrapOID, name, ifIndex, vb})
	}
	_, err := s.Pool.CopyFrom(ctx, pgx.Identifier{"events", "snmp_traps"},
		[]string{"received_at", "source", "agent_addr", "device_id", "version",
			"inform", "trap_oid", "trap_name", "if_index", "varbinds"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "insert traps")
	}
	return nil
}

// Prune deletes traps older than the retention window and reports how many.
func (s *Store) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM events.snmp_traps WHERE received_at < now() - make_interval(secs => $1)`,
		keep.Seconds())
	if err != nil {
		return 0, errx.Wrap(errx.KindTransient, err, "prune traps")
	}
	return tag.RowsAffected(), nil
}

// TrapRow is one stored trap as the API returns it.
type TrapRow struct {
	ID         int64           `json:"id"`
	ReceivedAt time.Time       `json:"received_at"`
	Source     string          `json:"source"`
	AgentAddr  *string         `json:"agent_addr,omitempty"`
	DeviceID   *string         `json:"device_id,omitempty"`
	DeviceName *string         `json:"device_name,omitempty"`
	Version    string          `json:"version"`
	Inform     bool            `json:"inform"`
	TrapOID    string          `json:"trap_oid"`
	TrapName   *string         `json:"trap_name,omitempty"`
	IfIndex    *int            `json:"if_index,omitempty"`
	IfName     *string         `json:"if_name,omitempty"`
	Varbinds   json.RawMessage `json:"varbinds"`
}

type TrapFilter struct {
	DeviceID    string
	UnknownOnly bool   // traps from no inventoried device
	Q           string // matches trap name or OID
	Before      int64  // keyset cursor: rows with id < Before
	Limit       int
}

func (s *Store) ListTraps(ctx context.Context, f TrapFilter) ([]TrapRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.received_at, host(t.source), host(t.agent_addr), t.device_id,
		       d.name, t.version, t.inform, t.trap_oid, t.trap_name, t.if_index,
		       i.name, t.varbinds
		FROM events.snmp_traps t
		LEFT JOIN inventory.devices d ON d.id = t.device_id
		LEFT JOIN inventory.interfaces i
		       ON i.device_id = t.device_id AND i.if_index = t.if_index AND i.state = 'present'
		WHERE ($1 = '' OR t.device_id = $1)
		  AND (NOT $2 OR t.device_id IS NULL)
		  AND ($3 = '' OR t.trap_oid LIKE $3 || '%' OR t.trap_name ILIKE '%' || $3 || '%')
		  AND ($4 = 0 OR t.id < $4)
		ORDER BY t.id DESC
		LIMIT $5`, f.DeviceID, f.UnknownOnly, f.Q, f.Before, f.Limit)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list traps")
	}
	defer rows.Close()
	out := []TrapRow{}
	for rows.Next() {
		var r TrapRow
		if err := rows.Scan(&r.ID, &r.ReceivedAt, &r.Source, &r.AgentAddr, &r.DeviceID,
			&r.DeviceName, &r.Version, &r.Inform, &r.TrapOID, &r.TrapName, &r.IfIndex,
			&r.IfName, &r.Varbinds); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddrIndex resolves a source address to a device. It is rebuilt from
// inventory on an interval rather than queried per trap: a trap storm is
// exactly when a per-datagram query would hurt the database most.
type AddrIndex struct {
	Pool *pgxpool.Pool

	mu sync.RWMutex
	m  map[netip.Addr]string
}

func (x *AddrIndex) DeviceByAddr(a netip.Addr) string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.m[a.Unmap()]
}

// Refresh reloads the index. A device is reachable by its management address
// and by every address claimed in attrs.flow_exporters: gateways commonly send
// from their WAN side rather than the address they are polled on, and the
// operator has already said which addresses belong to which device (doc 34 §3.1).
func (x *AddrIndex) Refresh(ctx context.Context) error {
	rows, err := x.Pool.Query(ctx, `
		SELECT id, host(mgmt_ip), 0 AS pri FROM inventory.devices WHERE status != 'retired'
		UNION ALL
		SELECT d.id, e, 1
		FROM inventory.devices d,
		     jsonb_array_elements_text(
		       CASE WHEN jsonb_typeof(d.attrs->'flow_exporters') = 'array'
		            THEN d.attrs->'flow_exporters' ELSE '[]'::jsonb END) e
		WHERE d.status != 'retired'
		ORDER BY 3`)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "load device addresses")
	}
	defer rows.Close()
	m := map[netip.Addr]string{}
	for rows.Next() {
		var id, addr string
		var pri int
		if err := rows.Scan(&id, &addr, &pri); err != nil {
			return err
		}
		a, err := netip.ParseAddr(addr)
		if err != nil {
			continue
		}
		a = a.Unmap()
		// The management address wins (pri 0 sorts first): a claimed exporter
		// address must not move a device's own address to another device.
		if _, taken := m[a]; !taken {
			m[a] = id
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	x.mu.Lock()
	x.m = m
	x.mu.Unlock()
	return nil
}

func (x *AddrIndex) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.m)
}
