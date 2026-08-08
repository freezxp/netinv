package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/collection/app"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

type DiscoveryRepo struct{ Pool *pgxpool.Pool }

func (r *DiscoveryRepo) CreateRule(ctx context.Context, rule *app.DiscoveryRule) error {
	creds, _ := json.Marshal(rule.CredentialIDs)
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO platform.discovery_rules
			(id, tenant_id, site_id, cidr, credential_ids, enabled)
		VALUES ($1,'t_default',$2,$3::cidr,$4,true)`,
		rule.ID, rule.SiteID, rule.CIDR, creds)
	if err != nil {
		return errx.Wrap(errx.KindInvalid, err, "insert discovery rule")
	}
	return nil
}

const ruleCols = `id, site_id, text(cidr), credential_ids, enabled, created_at`

func scanRule(row pgx.Row) (*app.DiscoveryRule, error) {
	var rule app.DiscoveryRule
	var creds []byte
	var created time.Time
	err := row.Scan(&rule.ID, &rule.SiteID, &rule.CIDR, &creds, &rule.Enabled, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "discovery rule not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "scan discovery rule")
	}
	_ = json.Unmarshal(creds, &rule.CredentialIDs)
	rule.CreatedAt = created.UTC().Format(time.RFC3339)
	return &rule, nil
}

func (r *DiscoveryRepo) ListRules(ctx context.Context) ([]app.DiscoveryRule, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT `+ruleCols+` FROM platform.discovery_rules ORDER BY created_at DESC`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list discovery rules")
	}
	defer rows.Close()
	out := []app.DiscoveryRule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

func (r *DiscoveryRepo) GetRule(ctx context.Context, ruleID string) (*app.DiscoveryRule, error) {
	return scanRule(r.Pool.QueryRow(ctx,
		`SELECT `+ruleCols+` FROM platform.discovery_rules WHERE id = $1`, ruleID))
}

func (r *DiscoveryRepo) DeleteRule(ctx context.Context, ruleID string) error {
	tag, err := r.Pool.Exec(ctx, `DELETE FROM platform.discovery_rules WHERE id=$1`, ruleID)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "delete discovery rule")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "discovery rule not found")
	}
	return nil
}

func (r *DiscoveryRepo) UpsertFound(ctx context.Context, ruleID string,
	hosts []wire.DiscoveredHost, match func(string, string) string) (int, error) {
	n := 0
	for _, h := range hosts {
		connector := ""
		if match != nil {
			connector = match(h.SysObjectID, h.SysDescr)
		}
		// Re-finding a host refreshes what it reports but never resurrects an
		// ignored entry — an operator's decision sticks until they change it.
		if _, err := r.Pool.Exec(ctx, `
			INSERT INTO platform.discovered_devices
				(id, rule_id, ip, sys_name, sys_object_id, sys_descr,
				 matched_connector_id, responding_credential_id, state)
			VALUES ($1,$2,$3::inet,nullif($4,''),nullif($5,''),nullif($6,''),
			        nullif($7,''),$8,'pending')
			ON CONFLICT (rule_id, ip) DO UPDATE SET
				sys_name = excluded.sys_name,
				sys_object_id = excluded.sys_object_id,
				sys_descr = excluded.sys_descr,
				matched_connector_id = excluded.matched_connector_id,
				responding_credential_id = excluded.responding_credential_id,
				seen_last_at = now()`,
			id.New("dd"), ruleID, h.IP, h.SysName, h.SysObjectID, h.SysDescr,
			connector, h.CredentialID); err != nil {
			return n, errx.Wrap(errx.KindTransient, err, "upsert discovered device")
		}
		n++
	}
	return n, nil
}

// ListFound joins the rule for site context and flags addresses already
// managed, so the approval queue never offers a duplicate.
func (r *DiscoveryRepo) ListFound(ctx context.Context, state string) ([]app.Discovered, error) {
	q := `
		SELECT dd.id, dd.rule_id, dr.site_id, host(dd.ip),
		       coalesce(dd.sys_name,''), coalesce(dd.sys_descr,''),
		       coalesce(dd.sys_object_id,''),
		       coalesce(dd.matched_connector_id,''),
		       coalesce(dd.responding_credential_id,''), dd.state::text,
		       EXISTS(SELECT 1 FROM inventory.devices d
		              WHERE d.mgmt_ip = dd.ip AND d.status != 'retired'),
		       dd.seen_last_at
		FROM platform.discovered_devices dd
		JOIN platform.discovery_rules dr ON dr.id = dd.rule_id`
	args := []any{}
	if state != "" {
		args = append(args, state)
		q += ` WHERE dd.state = $1::platform.discovered_state`
	}
	q += ` ORDER BY dd.seen_last_at DESC LIMIT 500`
	rows, err := r.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list discovered")
	}
	defer rows.Close()
	out := []app.Discovered{}
	for rows.Next() {
		var d app.Discovered
		var seen time.Time
		if err := rows.Scan(&d.ID, &d.RuleID, &d.SiteID, &d.IP, &d.SysName,
			&d.SysDescr, &d.SysObjectID, &d.ConnectorID, &d.CredentialID,
			&d.State, &d.Managed, &seen); err != nil {
			return nil, err
		}
		d.SeenLastAt = seen.UTC().Format(time.RFC3339)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *DiscoveryRepo) GetFound(ctx context.Context, foundID string) (*app.Discovered, error) {
	all, err := r.ListFound(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == foundID {
			return &all[i], nil
		}
	}
	return nil, errx.New(errx.KindNotFound, "discovered device not found")
}

func (r *DiscoveryRepo) SetFoundState(ctx context.Context, foundID, state string) error {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE platform.discovered_devices SET state = $2::platform.discovered_state
		WHERE id = $1`, foundID, state)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "set discovered state")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "discovered device not found")
	}
	return nil
}

// NamedCredLookup resolves candidate credentials for a sweep, decrypting each
// (poller-path use, doc 20 §6).
type NamedCredLookup struct {
	Resolve func(ctx context.Context, credentialID string) (wire.SNMPCred, error)
	Names   func(ctx context.Context, credentialID string) string
}

func (l *NamedCredLookup) Named(ctx context.Context, ids []string) ([]wire.NamedCred, error) {
	out := []wire.NamedCred{}
	for _, cid := range ids {
		cred, err := l.Resolve(ctx, cid)
		if err != nil {
			continue // a deleted or unreadable credential just drops out
		}
		name := ""
		if l.Names != nil {
			name = l.Names(ctx, cid)
		}
		out = append(out, wire.NamedCred{CredentialID: cid, Name: name, Cred: cred})
	}
	return out, nil
}
