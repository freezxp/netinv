// Package postgres — Alerting repositories.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/alerting/app"
	"github.com/freezxp/netinv/backend/internal/alerting/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

type Store struct{ Pool *pgxpool.Pool }

// ---- rules ----

func (s *Store) EnabledRules(ctx context.Context) ([]*domain.Rule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, kind, severity, coalesce(expr,''), annotations, enabled, is_builtin
		FROM alerting.alert_rules WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "enabled rules")
	}
	defer rows.Close()
	var out []*domain.Rule
	for rows.Next() {
		r := &domain.Rule{}
		var sev string
		var ann []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &sev, &r.Expr, &ann,
			&r.Enabled, &r.Builtin); err != nil {
			return nil, err
		}
		r.Severity = domain.Severity(sev)
		_ = json.Unmarshal(ann, &r.Annotations)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- instances ----

const instCols = `ai.id, ai.rule_id, r.name, ai.fingerprint, ai.state, ai.severity,
	coalesce(ai.device_id,''), coalesce(ai.interface_id,''), ai.labels,
	coalesce(ai.value,0), ai.fired_at, ai.acked_at, coalesce(ai.acked_by,''),
	coalesce(ai.ack_comment,''), ai.resolved_at, ai.flap_count`

func scanInst(row pgx.Row) (*domain.Instance, error) {
	i := &domain.Instance{}
	var state, sev string
	var labels []byte
	err := row.Scan(&i.ID, &i.RuleID, &i.RuleName, &i.Fingerprint, &state, &sev,
		&i.DeviceID, &i.InterfaceID, &labels, &i.Value, &i.FiredAt, &i.AckedAt,
		&i.AckedBy, &i.AckComment, &i.ResolvedAt, &i.FlapCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "alert not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "scan alert")
	}
	i.State, i.Severity = domain.AlertState(state), domain.Severity(sev)
	_ = json.Unmarshal(labels, &i.Labels)
	return i, nil
}

func (s *Store) LiveByRule(ctx context.Context, ruleID string) ([]*domain.Instance, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+instCols+` FROM alerting.alert_instances ai
		JOIN alerting.alert_rules r ON r.id = ai.rule_id
		WHERE ai.rule_id = $1 AND ai.state != 'resolved'`, ruleID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "live instances")
	}
	defer rows.Close()
	var out []*domain.Instance
	for rows.Next() {
		i, err := scanInst(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) RecentResolved(ctx context.Context, ruleID, fp string, since time.Time) (*domain.Instance, error) {
	i, err := scanInst(s.Pool.QueryRow(ctx, `
		SELECT `+instCols+` FROM alerting.alert_instances ai
		JOIN alerting.alert_rules r ON r.id = ai.rule_id
		WHERE ai.rule_id = $1 AND ai.fingerprint = $2 AND ai.state = 'resolved'
		  AND ai.resolved_at >= $3
		ORDER BY ai.resolved_at DESC LIMIT 1`, ruleID, fp, since))
	if errx.KindOf(err) == errx.KindNotFound {
		return nil, nil
	}
	return i, err
}

func (s *Store) Fire(ctx context.Context, inst *domain.Instance) error {
	labels, _ := json.Marshal(inst.Labels)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO alerting.alert_instances
			(id, rule_id, fingerprint, state, severity, device_id, interface_id,
			 labels, value, fired_at, flap_count, last_eval_at)
		VALUES ($1,$2,$3,'firing',$4,nullif($5,''),nullif($6,''),$7,$8,$9,$10,now())`,
		inst.ID, inst.RuleID, inst.Fingerprint, string(inst.Severity),
		inst.DeviceID, inst.InterfaceID, labels, inst.Value, inst.FiredAt,
		inst.FlapCount)
	return errx.Wrap(errx.KindTransient, err, "fire alert")
}

func (s *Store) Resolve(ctx context.Context, instID string, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE alerting.alert_instances
		SET state='resolved', resolved_at=$2, last_eval_at=now()
		WHERE id = $1 AND state != 'resolved'`, instID, at)
	return errx.Wrap(errx.KindTransient, err, "resolve alert")
}

func (s *Store) SetFlapping(ctx context.Context, instID string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE alerting.alert_instances SET state='flapping' WHERE id=$1`, instID)
	return errx.Wrap(errx.KindTransient, err, "set flapping")
}

func (s *Store) AppendEvent(ctx context.Context, instID, event, actorID string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO alerting.alert_events (alert_id, event, actor_id, detail)
		VALUES ($1,$2,nullif($3,''),$4)`, instID, event, actorID, raw)
	return errx.Wrap(errx.KindTransient, err, "append alert event")
}

// DeleteRule removes a rule and the alert history belonging to it.
//
// alert_instances.rule_id has no ON DELETE action, so the rule cannot go while
// instances reference it; alert_events then cascade from the instances. Both
// steps run in one transaction so a rule can never be left half-deleted with
// its history detached.
func (s *Store) DeleteRule(ctx context.Context, ruleID string) error {
	return pgxp.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM alerting.alert_instances WHERE rule_id = $1`, ruleID); err != nil {
			return errx.Wrap(errx.KindTransient, err, "delete alert history")
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM alerting.alert_rules WHERE id = $1`, ruleID)
		if err != nil {
			return errx.Wrap(errx.KindTransient, err, "delete rule")
		}
		if tag.RowsAffected() == 0 {
			return errx.New(errx.KindNotFound, "rule not found")
		}
		return nil
	})
}

// ---- read side + ack (API) ----

type ListFilter struct {
	States   []string
	Severity []string
	DeviceID string
	Limit    int
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]*domain.Instance, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := `SELECT ` + instCols + ` FROM alerting.alert_instances ai
		JOIN alerting.alert_rules r ON r.id = ai.rule_id WHERE true`
	args := []any{}
	if len(f.States) > 0 {
		args = append(args, f.States)
		q += ` AND ai.state = any($1::alerting.alert_state[])`
	} else {
		q += ` AND ai.state != 'resolved'`
	}
	if len(f.Severity) > 0 {
		args = append(args, f.Severity)
		q += ` AND ai.severity = any($` + itoa(len(args)) + `::alerting.severity[])`
	}
	if f.DeviceID != "" {
		args = append(args, f.DeviceID)
		q += ` AND ai.device_id = $` + itoa(len(args))
	}
	args = append(args, f.Limit)
	// Severity then recency — never chronology alone (PRD §4.2b).
	q += ` ORDER BY ai.severity, ai.fired_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list alerts")
	}
	defer rows.Close()
	var out []*domain.Instance
	for rows.Next() {
		i, err := scanInst(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, instID string) (*domain.Instance, error) {
	return scanInst(s.Pool.QueryRow(ctx, `
		SELECT `+instCols+` FROM alerting.alert_instances ai
		JOIN alerting.alert_rules r ON r.id = ai.rule_id WHERE ai.id = $1`, instID))
}

// Ack transitions firing/flapping → acknowledged with CAS semantics.
func (s *Store) Ack(ctx context.Context, instID, userID, comment string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE alerting.alert_instances
		SET state='acknowledged', acked_at=now(), acked_by=$2, ack_comment=nullif($3,'')
		WHERE id=$1 AND state IN ('firing','flapping')`, instID, userID, comment)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "ack")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindConflict, "alert is not in an acknowledgeable state")
	}
	return nil
}

func (s *Store) Unack(ctx context.Context, instID string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE alerting.alert_instances
		SET state='firing', acked_at=NULL, acked_by=NULL, ack_comment=NULL
		WHERE id=$1 AND state='acknowledged'`, instID)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "unack")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindConflict, "alert is not acknowledged")
	}
	return nil
}

func (s *Store) Events(ctx context.Context, instID string) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT event, coalesce(actor_id,''), detail, at
		FROM alerting.alert_events WHERE alert_id=$1 ORDER BY at`, instID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "alert events")
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var event, actor string
		var detail []byte
		var at time.Time
		if err := rows.Scan(&event, &actor, &detail, &at); err != nil {
			return nil, err
		}
		var d map[string]any
		_ = json.Unmarshal(detail, &d)
		out = append(out, map[string]any{
			"event": event, "actor_id": actor, "detail": d,
			"at": at.UTC().Format(time.RFC3339),
		})
	}
	return out, rows.Err()
}

// InterfaceID implements app.InterfaceResolver (best-effort).
func (s *Store) InterfaceID(ctx context.Context, deviceID, ifIndex string) string {
	var id string
	_ = s.Pool.QueryRow(ctx, `
		SELECT id FROM inventory.interfaces
		WHERE device_id=$1 AND if_index=$2::int`, deviceID, ifIndex).Scan(&id)
	return id
}

// Interfaces implements app.InterfaceResolver: if_index → name and service
// history for a whole device in one round trip, so alert suppression costs one
// query per device per cycle rather than one per alerting series.
func (s *Store) Interfaces(ctx context.Context, deviceID string) map[string]app.InterfaceInfo {
	rows, err := s.Pool.Query(ctx, `
		SELECT if_index, coalesce(name,''), ever_up FROM inventory.interfaces
		WHERE device_id=$1 AND state != 'removed'`, deviceID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]app.InterfaceInfo{}
	for rows.Next() {
		var idx int
		var info app.InterfaceInfo
		if rows.Scan(&idx, &info.Name, &info.EverUp) == nil {
			out[strconv.Itoa(idx)] = info
		}
	}
	return out
}

// DeviceByExporter implements app.ExporterResolver (best-effort).
//
// Two ways an address maps to a device, tried in that order:
//
//  1. an explicit claim in devices.attrs->'flow_exporters'. Gateways export
//     from their WAN address rather than their management IP (doc 34 §3.1), so
//     the claim list is the only thing that can connect the two.
//  2. the management address itself, which is what a device exports from when
//     the collector sits on its own LAN. Without this fallback the most common
//     single-site deployment resolves nothing at all.
//
// Retired devices are excluded: an address reassigned to a live device must not
// keep resolving to the one that used to hold it.
func (s *Store) DeviceByExporter(ctx context.Context, addr string) (string, string) {
	var id, name string
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name FROM inventory.devices
		WHERE status <> 'retired'
		  AND (attrs->'flow_exporters' @> to_jsonb($1::text)
		       OR host(mgmt_ip) = $1)
		ORDER BY (attrs->'flow_exporters' @> to_jsonb($1::text)) DESC
		LIMIT 1`, addr).Scan(&id, &name)
	if err != nil {
		return "", ""
	}
	return id, name
}

// Silenced implements app.SilenceChecker: any active silence whose scope
// labels are all present in the alert labels suppresses it.
func (s *Store) Silenced(ctx context.Context, labels map[string]string) (bool, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT scope FROM alerting.silences
		WHERE revoked_at IS NULL AND starts_at <= now() AND ends_at > now()`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var scope map[string]string
		if err := json.Unmarshal(raw, &scope); err != nil {
			continue
		}
		match := len(scope) > 0
		for k, v := range scope {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return true, nil
		}
	}
	return false, rows.Err()
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
