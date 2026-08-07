// Package maps — weathermap definitions, revisions, and live data (the v1
// flagship, ADR-008; schema doc 08, API doc 09 §10).
package maps

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// Definition is the netinv.map/1 document (doc 09 §10).
type Definition struct {
	Schema  string         `json:"schema"`
	Name    string         `json:"name,omitempty"`
	Options map[string]any `json:"options,omitempty"`
	Nodes   []Node         `json:"nodes"`
	Links   []Link         `json:"links"`
}

type Node struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"` // device | site | cloud | label
	DeviceID string  `json:"device_id,omitempty"`
	SiteID   string  `json:"site_id,omitempty"`
	Text     string  `json:"text,omitempty"`
	Label    string  `json:"label,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

type Endpoint struct {
	DeviceID string `json:"device_id"`
	IfIndex  int    `json:"if_index"`
}

type Link struct {
	ID           string    `json:"id"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	AEndpoint    *Endpoint `json:"a_endpoint,omitempty"`
	BEndpoint    *Endpoint `json:"b_endpoint,omitempty"`
	BandwidthBPS int64     `json:"bandwidth_bps,omitempty"`
}

type MapMeta struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	PublishedRev int    `json:"published_rev"`
	DraftRev     int    `json:"draft_rev"`
	UpdatedAt    string `json:"updated_at"`
}

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) List(ctx context.Context) ([]MapMeta, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT m.id, m.name, coalesce(m.description,''), m.published_rev,
		       coalesce((SELECT max(rev) FROM maps.map_revisions r
		                 WHERE r.map_id = m.id AND r.state = 'draft'), 0),
		       m.updated_at
		FROM maps.maps m ORDER BY m.name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list maps")
	}
	defer rows.Close()
	out := []MapMeta{}
	for rows.Next() {
		var m MapMeta
		var at time.Time
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.PublishedRev,
			&m.DraftRev, &at); err != nil {
			return nil, err
		}
		m.UpdatedAt = at.UTC().Format(time.RFC3339)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, name, createdBy string) (*MapMeta, error) {
	mid := id.New("map")
	def := Definition{Schema: "netinv.map/1", Nodes: []Node{}, Links: []Link{}}
	raw, _ := json.Marshal(def)
	err := pgxp.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO maps.maps (id, tenant_id, name, created_by)
			VALUES ($1,'t_default',$2,nullif($3,''))`, mid, name, createdBy); err != nil {
			return errx.Wrap(errx.KindConflict, err, "a map with that name may already exist")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO maps.map_revisions (map_id, rev, state, definition, saved_by)
			VALUES ($1,1,'draft',$2,nullif($3,''))`, mid, raw, createdBy)
		return errx.Wrap(errx.KindTransient, err, "initial revision")
	})
	if err != nil {
		return nil, err
	}
	return &MapMeta{ID: mid, Name: name, DraftRev: 1}, nil
}

// Load returns the requested revision ("draft" = newest draft, else published).
func (s *Store) Load(ctx context.Context, mapID, which string) (*Definition, int, error) {
	var raw []byte
	var rev int
	var err error
	if which == "draft" {
		err = s.Pool.QueryRow(ctx, `
			SELECT definition, rev FROM maps.map_revisions
			WHERE map_id = $1 AND state = 'draft'
			ORDER BY rev DESC LIMIT 1`, mapID).Scan(&raw, &rev)
	} else {
		err = s.Pool.QueryRow(ctx, `
			SELECT r.definition, r.rev FROM maps.map_revisions r
			JOIN maps.maps m ON m.id = r.map_id AND m.published_rev = r.rev
			WHERE r.map_id = $1`, mapID).Scan(&raw, &rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// No published revision yet — fall back to draft for viewers.
		if which != "draft" {
			return s.Load(ctx, mapID, "draft")
		}
		return nil, 0, errx.New(errx.KindNotFound, "map revision not found")
	}
	if err != nil {
		return nil, 0, errx.Wrap(errx.KindTransient, err, "load map")
	}
	var def Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, 0, errx.Wrap(errx.KindInternal, err, "decode map")
	}
	return &def, rev, nil
}

// SaveDraft overwrites the current draft revision (autosave, FR-MAP-02).
func (s *Store) SaveDraft(ctx context.Context, mapID string, def *Definition, savedBy string) error {
	def.Schema = "netinv.map/1"
	raw, err := json.Marshal(def)
	if err != nil {
		return errx.Wrap(errx.KindInvalid, err, "encode definition")
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE maps.map_revisions SET definition = $2, saved_by = nullif($3,''), saved_at = now()
		WHERE map_id = $1 AND state = 'draft'
		  AND rev = (SELECT max(rev) FROM maps.map_revisions
		             WHERE map_id = $1 AND state = 'draft')`,
		mapID, raw, savedBy)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "save draft")
	}
	if tag.RowsAffected() == 0 {
		// No draft (all published) — open a new draft revision.
		_, err = s.Pool.Exec(ctx, `
			INSERT INTO maps.map_revisions (map_id, rev, state, definition, saved_by)
			SELECT $1, coalesce(max(rev),0)+1, 'draft', $2, nullif($3,'')
			FROM maps.map_revisions WHERE map_id = $1`, mapID, raw, savedBy)
		return errx.Wrap(errx.KindTransient, err, "open draft")
	}
	return nil
}

// Publish validates interface bindings, extracts map_links, marks the
// revision published, and opens a fresh draft copy (FR-MAP publish flow).
func (s *Store) Publish(ctx context.Context, mapID, actor string) (int, error) {
	def, rev, err := s.Load(ctx, mapID, "draft")
	if err != nil {
		return 0, err
	}
	// Validate endpoint bindings resolve to real interfaces (doc 09 §10: 422).
	type binding struct {
		linkKey string
		ifID    string
		side    string
	}
	var bindings []binding
	for _, l := range def.Links {
		for side, ep := range map[string]*Endpoint{"a": l.AEndpoint, "b": l.BEndpoint} {
			if ep == nil {
				continue
			}
			var ifID string
			err := s.Pool.QueryRow(ctx, `
				SELECT id FROM inventory.interfaces
				WHERE device_id = $1 AND if_index = $2 AND state != 'removed'`,
				ep.DeviceID, ep.IfIndex).Scan(&ifID)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, errx.New(errx.KindInvalid,
					"link %s: %s endpoint (device %s if %d) does not resolve",
					l.ID, side, ep.DeviceID, ep.IfIndex)
			}
			if err != nil {
				return 0, errx.Wrap(errx.KindTransient, err, "validate binding")
			}
			bindings = append(bindings, binding{linkKey: l.ID, ifID: ifID, side: side})
		}
	}
	err = pgxp.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE maps.map_revisions SET state='published', saved_at=now()
			WHERE map_id=$1 AND rev=$2`, mapID, rev); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE maps.maps SET published_rev=$2, updated_at=now() WHERE id=$1`,
			mapID, rev); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM maps.map_links WHERE map_id=$1`, mapID); err != nil {
			return err
		}
		byLink := map[string]map[string]string{}
		for _, b := range bindings {
			if byLink[b.linkKey] == nil {
				byLink[b.linkKey] = map[string]string{}
			}
			byLink[b.linkKey][b.side] = b.ifID
		}
		for key, sides := range byLink {
			if _, err := tx.Exec(ctx, `
				INSERT INTO maps.map_links (map_id, link_key, a_if_id, b_if_id)
				VALUES ($1,$2,nullif($3,''),nullif($4,''))`,
				mapID, key, sides["a"], sides["b"]); err != nil {
				return err
			}
		}
		// Fresh draft on top of the published revision.
		raw, _ := json.Marshal(def)
		_, err := tx.Exec(ctx, `
			INSERT INTO maps.map_revisions (map_id, rev, state, definition, saved_by)
			VALUES ($1,$2,'draft',$3,nullif($4,''))`, mapID, rev+1, raw, actor)
		return err
	})
	if err != nil {
		return 0, errx.Wrap(errx.KindTransient, err, "publish")
	}
	return rev, nil
}

func (s *Store) Delete(ctx context.Context, mapID string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM maps.maps WHERE id=$1`, mapID)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "delete map")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "map not found")
	}
	return nil
}

// Suggestions: LLDP adjacencies between managed devices (FR-MAP-06).
type Suggestion struct {
	ADeviceID string `json:"a_device_id"`
	ADevice   string `json:"a_device"`
	AIfIndex  int    `json:"a_if_index"`
	BDeviceID string `json:"b_device_id"`
	BDevice   string `json:"b_device"`
	BSysName  string `json:"b_sysname"`
	BPort     string `json:"b_port"`
}

func (s *Store) Suggestions(ctx context.Context) ([]Suggestion, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT tl.a_device_id, da.name, coalesce(i.if_index, 0),
		       coalesce(tl.b_device_id, ''), coalesce(db.name, ''),
		       coalesce(tl.b_sysname,''), coalesce(tl.b_port_descr,'')
		FROM inventory.topology_links tl
		JOIN inventory.devices da ON da.id = tl.a_device_id
		LEFT JOIN inventory.devices db ON db.id = tl.b_device_id
		LEFT JOIN inventory.interfaces i ON i.id = tl.a_if_id
		WHERE tl.state = 'active'`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "suggestions")
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		var sg Suggestion
		if err := rows.Scan(&sg.ADeviceID, &sg.ADevice, &sg.AIfIndex,
			&sg.BDeviceID, &sg.BDevice, &sg.BSysName, &sg.BPort); err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}
