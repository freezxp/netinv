package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// A device reachable on two addresses gets discovered twice and onboarded
// twice: two records, two polling schedules, two sets of graphs, and a history
// split down the middle. The addresses differ, so the unique constraint on
// mgmt_ip does not see it — the duplication is in the *identity* the device
// reports, not in how it is reached.
//
// Two signals are used, and neither is treated as proof.
//
//   - serial_number, when both devices report one. Two chassis do not share a
//     serial, so a match is as close to certain as inventory gets.
//   - sys_name, case-insensitively. Usually the hostname and usually unique,
//     but two genuinely different boxes can be misconfigured with the same
//     one, and a stack member sometimes inherits its master's.
//
// So this reports evidence and never merges anything on its own. Silently
// folding two devices into one on the strength of a hostname would be
// irreversible, and wrong exactly when a network is already misconfigured —
// the moment an operator can least afford a monitoring system inventing facts.

// DuplicateGroup is a set of managed devices that look like one device.
type DuplicateGroup struct {
	// Match is what tied them together: "serial" or "sys_name".
	Match   string           `json:"match"`
	Value   string           `json:"value"`
	Devices []DuplicateMatch `json:"devices"`
}

type DuplicateMatch struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MgmtIP   string `json:"mgmt_ip"`
	SysName  string `json:"sys_name,omitempty"`
	Serial   string `json:"serial_number,omitempty"`
	Status   string `json:"status"`
	SiteID   string `json:"site_id"`
	LastSeen string `json:"updated_at"`
}

// Duplicates lists suspected duplicate devices, strongest evidence first.
//
// Retired devices are excluded: they are already the answer to "this was a
// duplicate", and including them would mean the list never empties after
// someone acts on it.
func (r *DeviceRepo) Duplicates(ctx context.Context) ([]DuplicateGroup, error) {
	const q = `
		WITH live AS (
			SELECT id, name, host(mgmt_ip) AS ip, coalesce(sys_name,'') AS sys_name,
			       coalesce(serial_number,'') AS serial, status::text, site_id,
			       updated_at
			FROM inventory.devices
			WHERE status != 'retired'
		),
		by_serial AS (
			SELECT 'serial' AS match, serial AS value, id, name, ip, sys_name,
			       serial, status, site_id, updated_at
			FROM live WHERE serial <> ''
			  AND serial IN (SELECT serial FROM live WHERE serial <> ''
			                 GROUP BY serial HAVING count(*) > 1)
		),
		by_name AS (
			SELECT 'sys_name' AS match, lower(sys_name) AS value, id, name, ip,
			       sys_name, serial, status, site_id, updated_at
			FROM live WHERE sys_name <> ''
			  AND lower(sys_name) IN (SELECT lower(sys_name) FROM live
			                          WHERE sys_name <> ''
			                          GROUP BY lower(sys_name) HAVING count(*) > 1)
			  -- A serial match already covers these, and reporting the same
			  -- pair twice makes a list of two problems out of one.
			  AND id NOT IN (SELECT id FROM by_serial)
		)
		SELECT * FROM by_serial
		UNION ALL
		SELECT * FROM by_name
		ORDER BY match, value, ip`
	rows, err := r.Pool.Query(ctx, q)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "duplicate scan")
	}
	defer rows.Close()

	out := []DuplicateGroup{}
	var cur *DuplicateGroup
	for rows.Next() {
		var match, value string
		var d DuplicateMatch
		var updated any
		if err := rows.Scan(&match, &value, &d.ID, &d.Name, &d.MgmtIP,
			&d.SysName, &d.Serial, &d.Status, &d.SiteID, &updated); err != nil {
			return nil, err
		}
		if t, ok := updated.(interface{ Format(string) string }); ok {
			d.LastSeen = t.Format("2006-01-02T15:04:05Z07:00")
		}
		if cur == nil || cur.Match != match || cur.Value != value {
			out = append(out, DuplicateGroup{Match: match, Value: value})
			cur = &out[len(out)-1]
		}
		cur.Devices = append(cur.Devices, d)
	}
	return out, rows.Err()
}

// MatchByIdentity finds a managed device that reports the same identity as a
// candidate — used at discovery approval, before a second record exists.
//
// Serial is checked first and alone when present: it is the stronger signal,
// and falling through to a hostname match after a serial mismatch would pair
// devices the evidence has already separated.
func (r *DeviceRepo) MatchByIdentity(ctx context.Context, sysName, serial string) (*DuplicateMatch, string, error) {
	scan := func(where string, arg string) (*DuplicateMatch, error) {
		var d DuplicateMatch
		err := r.Pool.QueryRow(ctx, `
			SELECT id, name, host(mgmt_ip), coalesce(sys_name,''),
			       coalesce(serial_number,''), status::text, site_id
			FROM inventory.devices
			WHERE status != 'retired' AND `+where+`
			ORDER BY created_at LIMIT 1`, arg).
			Scan(&d.ID, &d.Name, &d.MgmtIP, &d.SysName, &d.Serial, &d.Status, &d.SiteID)
		if err != nil {
			return nil, err
		}
		return &d, nil
	}
	if serial != "" {
		d, err := scan("serial_number = $1", serial)
		if err == nil {
			return d, "serial", nil
		}
		if !isNoRows(err) {
			return nil, "", errx.Wrap(errx.KindTransient, err, "identity match")
		}
		return nil, "", nil
	}
	if sysName != "" {
		d, err := scan("lower(sys_name) = lower($1)", sysName)
		if err == nil {
			return d, "sys_name", nil
		}
		if !isNoRows(err) {
			return nil, "", errx.Wrap(errx.KindTransient, err, "identity match")
		}
	}
	return nil, "", nil
}

// AddAltAddress records another address a device answers on, without creating
// a second device for it.
//
// It is deliberately a note rather than a second polling target: NetInv polls
// one management address, and a device answering on two does not need polling
// twice — that would double its load and produce two sets of identical graphs.
// What an operator needs is for the second address to stop being proposed as a
// new device, and to be findable when someone searches for it.
func (r *DeviceRepo) AddAltAddress(ctx context.Context, deviceID, addr string) ([]string, error) {
	var addrs []string
	err := r.Pool.QueryRow(ctx, `
		UPDATE inventory.devices
		SET attrs = jsonb_set(attrs, '{alt_addresses}',
			coalesce(attrs->'alt_addresses', '[]'::jsonb) ||
			CASE WHEN coalesce(attrs->'alt_addresses','[]'::jsonb) @> to_jsonb($2::text)
			     THEN '[]'::jsonb ELSE jsonb_build_array($2::text) END),
			updated_at = now()
		WHERE id = $1 AND status != 'retired'
		RETURNING coalesce(array(SELECT jsonb_array_elements_text(attrs->'alt_addresses')), '{}')`,
		deviceID, addr).Scan(&addrs)
	if isNoRows(err) {
		return nil, errx.New(errx.KindNotFound, "device not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "record alternate address")
	}
	return addrs, nil
}

// MergeResult reports what a merge actually did, which matters because the
// word promises more than any monitoring system can deliver.
type MergeResult struct {
	KeptID       string   `json:"kept_id"`
	KeptName     string   `json:"kept_name"`
	RetiredID    string   `json:"retired_id"`
	RetiredName  string   `json:"retired_name"`
	AltAddresses []string `json:"alt_addresses"`
	TagsAdded    []string `json:"tags_added"`
	MetricsMoved bool     `json:"metrics_moved"`
	HistoryMoved bool     `json:"history_moved"`
}

// Merge folds a duplicate device into the one being kept.
//
// What it does: the duplicate's management address and any alternates are
// recorded on the survivor, its tags are unioned in, and the duplicate is
// **retired** — soft-deleted, keeping its history and its metrics.
//
// What it deliberately does not do: move metrics or history. Every series is
// keyed by device_id in VictoriaMetrics, so "moving" them would mean rewriting
// the metrics store; and asset history, topology links and map bindings point
// at the duplicate's own interface rows, which have their own series. A merge
// that silently relabelled a year of data would be inventing continuity that
// never existed — two records were collected separately and that is a fact
// about the past, not a defect to paper over.
//
// So the honest outcome is: from now on everything is collected under the
// survivor; what was collected under the duplicate stays readable under the
// duplicate, which is retired rather than purged. MetricsMoved and
// HistoryMoved are returned as false so a caller states this rather than
// implying otherwise.
func (r *DeviceRepo) Merge(ctx context.Context, keepID, dupID string) (*MergeResult, error) {
	if keepID == dupID {
		return nil, errx.New(errx.KindInvalid, "a device cannot be merged into itself")
	}
	res := &MergeResult{KeptID: keepID, RetiredID: dupID}
	err := pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var keepIP, dupIP string
		var dupStatus string
		if err := tx.QueryRow(ctx, `
			SELECT name, host(mgmt_ip) FROM inventory.devices
			WHERE id = $1 AND status != 'retired'`, keepID).
			Scan(&res.KeptName, &keepIP); err != nil {
			if isNoRows(err) {
				return errx.New(errx.KindNotFound, "the device to keep was not found")
			}
			return errx.Wrap(errx.KindTransient, err, "load survivor")
		}
		var dupTags []string
		var dupAlt []string
		if err := tx.QueryRow(ctx, `
			SELECT name, host(mgmt_ip), status::text,
			       coalesce(array(SELECT jsonb_array_elements_text(tags)), '{}'),
			       coalesce(array(SELECT jsonb_array_elements_text(attrs->'alt_addresses')), '{}')
			FROM inventory.devices WHERE id = $1`, dupID).
			Scan(&res.RetiredName, &dupIP, &dupStatus, &dupTags, &dupAlt); err != nil {
			if isNoRows(err) {
				return errx.New(errx.KindNotFound, "the duplicate was not found")
			}
			return errx.Wrap(errx.KindTransient, err, "load duplicate")
		}
		if dupStatus == "retired" {
			return errx.New(errx.KindConflict, "that device is already retired")
		}

		// Every address the duplicate answered on moves to the survivor, so
		// discovery stops proposing them and a search for either finds the
		// device that is actually being polled.
		addrs := append([]string{dupIP}, dupAlt...)
		for _, a := range addrs {
			got, err := addAltAddressTx(ctx, tx, keepID, a)
			if err != nil {
				return err
			}
			res.AltAddresses = got
		}

		// Tags are unioned rather than replaced: they are operator knowledge,
		// and whichever record someone happened to label is an accident of
		// which address was discovered first.
		if len(dupTags) > 0 {
			var added []string
			if err := tx.QueryRow(ctx, `
				UPDATE inventory.devices d SET tags = (
					SELECT coalesce(jsonb_agg(DISTINCT t), '[]'::jsonb)
					FROM (SELECT jsonb_array_elements_text(d.tags) AS t
					      UNION SELECT unnest($2::text[])) u
				), updated_at = now()
				WHERE d.id = $1
				RETURNING coalesce(array(SELECT jsonb_array_elements_text(tags)), '{}')`,
				keepID, dupTags).Scan(&added); err != nil {
				return errx.Wrap(errx.KindTransient, err, "merge tags")
			}
			res.TagsAdded = added
		}

		// Retired, not purged: purging would destroy the history and the
		// interfaces the duplicate's own metrics are keyed to, which is the
		// one thing a merge must not do.
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.devices
			SET status = 'retired', retired_at = now(), updated_at = now(),
			    notes = trim(both E'\n' from coalesce(notes,'') ||
			            E'\nMerged into ' || $2 || ' on ' || to_char(now(),'YYYY-MM-DD'))
			WHERE id = $1`, dupID, res.KeptName); err != nil {
			return errx.Wrap(errx.KindTransient, err, "retire duplicate")
		}
		// Its schedules stop immediately; leaving them would keep polling a
		// device the operator has just said does not separately exist.
		if _, err := tx.Exec(ctx,
			`UPDATE platform.polling_schedule SET enabled = false WHERE device_id = $1`,
			dupID); err != nil {
			return errx.Wrap(errx.KindTransient, err, "disable duplicate schedules")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func addAltAddressTx(ctx context.Context, tx pgx.Tx, deviceID, addr string) ([]string, error) {
	var addrs []string
	err := tx.QueryRow(ctx, `
		UPDATE inventory.devices
		SET attrs = jsonb_set(attrs, '{alt_addresses}',
			coalesce(attrs->'alt_addresses', '[]'::jsonb) ||
			CASE WHEN coalesce(attrs->'alt_addresses','[]'::jsonb) @> to_jsonb($2::text)
			     THEN '[]'::jsonb ELSE jsonb_build_array($2::text) END),
			updated_at = now()
		WHERE id = $1
		RETURNING coalesce(array(SELECT jsonb_array_elements_text(attrs->'alt_addresses')), '{}')`,
		deviceID, addr).Scan(&addrs)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "record alternate address")
	}
	return addrs, nil
}
