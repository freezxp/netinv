package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type PollerRepo struct{ Pool *pgxpool.Pool }

const pollerCols = `id, tenant_id, site_id, name, status, coalesce(version,''),
	last_heartbeat_at, stats, created_at`

func scanPoller(row pgx.Row) (*domain.Poller, error) {
	p := &domain.Poller{}
	var status string
	var stats []byte
	err := row.Scan(&p.ID, &p.TenantID, &p.SiteID, &p.Name, &status, &p.Version,
		&p.LastHeartbeatAt, &stats, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "poller not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "scan poller")
	}
	p.Status = domain.PollerStatus(status)
	_ = json.Unmarshal(stats, &p.Stats)
	return p, nil
}

func (r *PollerRepo) CreatePending(ctx context.Context, p *domain.Poller, enrollTokenHash string) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO platform.pollers (id, tenant_id, site_id, name, enrollment_token_hash, status)
		VALUES ($1,$2,$3,$4,$5,'pending')`,
		p.ID, p.TenantID, p.SiteID, p.Name, enrollTokenHash)
	return errx.Wrap(errx.KindTransient, err, "insert poller")
}

func (r *PollerRepo) FindByEnrollHash(ctx context.Context, hash string) (*domain.Poller, error) {
	// Only pending pollers can consume an enrollment token, and only within
	// 15 minutes of issuance (doc 20 §8).
	return scanPoller(r.Pool.QueryRow(ctx, `
		SELECT `+pollerCols+` FROM platform.pollers
		WHERE enrollment_token_hash = $1 AND status = 'pending'
		  AND last_heartbeat_at IS NULL
		  AND created_at > now() - interval '15 minutes'`, hash))
}

func (r *PollerRepo) BindAuthToken(ctx context.Context, pollerID, authTokenHash, version string) error {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE platform.pollers
		SET enrollment_token_hash = $2, version = nullif($3,''), updated_at = now()
		WHERE id = $1`, pollerID, authTokenHash, version)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "bind auth token")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "poller not found")
	}
	return nil
}

func (r *PollerRepo) AuthByToken(ctx context.Context, pollerID, tokenHash string) (*domain.Poller, error) {
	return scanPoller(r.Pool.QueryRow(ctx, `
		SELECT `+pollerCols+` FROM platform.pollers
		WHERE id = $1 AND enrollment_token_hash = $2 AND status != 'disabled'`,
		pollerID, tokenHash))
}

func (r *PollerRepo) Heartbeat(ctx context.Context, pollerID, version string,
	stats domain.HeartbeatStats, at time.Time) error {
	raw, _ := json.Marshal(stats)
	_, err := r.Pool.Exec(ctx, `
		UPDATE platform.pollers
		SET last_heartbeat_at = $2, version = coalesce(nullif($3,''), version),
		    stats = $4, updated_at = now()
		WHERE id = $1`, pollerID, at, version, raw)
	return errx.Wrap(errx.KindTransient, err, "heartbeat")
}

func (r *PollerRepo) List(ctx context.Context) ([]*domain.Poller, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT `+pollerCols+` FROM platform.pollers ORDER BY name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list pollers")
	}
	defer rows.Close()
	var out []*domain.Poller
	for rows.Next() {
		p, err := scanPoller(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PollerRepo) Get(ctx context.Context, pollerID string) (*domain.Poller, error) {
	return scanPoller(r.Pool.QueryRow(ctx,
		`SELECT `+pollerCols+` FROM platform.pollers WHERE id = $1`, pollerID))
}

func (r *PollerRepo) SetStatus(ctx context.Context, pollerID string, status domain.PollerStatus) error {
	tag, err := r.Pool.Exec(ctx,
		`UPDATE platform.pollers SET status = $2, updated_at = now() WHERE id = $1`,
		pollerID, string(status))
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "set poller status")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "poller not found")
	}
	return nil
}
