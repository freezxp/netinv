package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type APITokenRepo struct{ Pool *pgxpool.Pool }

// Column names, not a credential: G101 matches on the word "token".
const apiTokenCols = `t.id, t.user_id, t.name, t.token_hash, t.scopes, t.expires_at, t.last_used_at, t.revoked_at, t.created_at` //nolint:gosec

func scanAPIToken(row pgx.Row, extra ...any) (*domain.APIToken, error) {
	t := &domain.APIToken{}
	var scopes []byte
	dest := append([]any{&t.ID, &t.UserID, &t.Name, &t.TokenHash, &scopes,
		&t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "api token not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "scan api token")
	}
	if err := json.Unmarshal(scopes, &t.Scopes); err != nil {
		return nil, errx.Wrap(errx.KindInternal, err, "api token scopes")
	}
	if t.Scopes == nil {
		t.Scopes = []string{}
	}
	return t, nil
}

func (r *APITokenRepo) Insert(ctx context.Context, t *domain.APIToken) error {
	scopes, err := json.Marshal(t.Scopes)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO iam.api_tokens (id, user_id, name, token_hash, scopes, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.UserID, t.Name, t.TokenHash, scopes, t.ExpiresAt, t.CreatedAt)
	return errx.Wrap(errx.KindTransient, err, "insert api token")
}

// ListByUser returns the user's tokens, revoked ones included: a token that
// stopped working should still be visible as revoked rather than vanish.
func (r *APITokenRepo) ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	rows, err := r.Pool.Query(ctx, `SELECT `+apiTokenCols+` FROM iam.api_tokens t
		WHERE t.user_id = $1 ORDER BY t.created_at DESC`, userID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list api tokens")
	}
	defer rows.Close()
	out := []*domain.APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke is scoped to the owner, so one user cannot revoke another's token by
// guessing its id; a miss is NotFound either way.
func (r *APITokenRepo) Revoke(ctx context.Context, userID, id string, at time.Time) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE iam.api_tokens SET revoked_at = $3
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID, at)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "revoke api token")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "api token not found")
	}
	return nil
}

// Lookup resolves a token hash to the token and its owner's *current* state in
// one round trip — roles and status are read live so that demoting or
// deactivating a user takes effect on their tokens immediately.
func (r *APITokenRepo) Lookup(ctx context.Context, hash string) (*domain.APIToken, *domain.TokenOwner, error) {
	o := &domain.TokenOwner{}
	var status string
	t, err := scanAPIToken(r.Pool.QueryRow(ctx, `
		SELECT `+apiTokenCols+`, u.username, u.tenant_id, u.status,
			coalesce((SELECT array_agg(ro.name ORDER BY ro.name) FROM iam.user_roles ur
				JOIN iam.roles ro ON ro.id = ur.role_id WHERE ur.user_id = u.id), '{}')
		FROM iam.api_tokens t JOIN iam.users u ON u.id = t.user_id
		WHERE t.token_hash = $1`, hash), &o.Username, &o.TenantID, &status, &o.Roles)
	if err != nil {
		return nil, nil, err
	}
	o.Status = domain.UserStatus(status)
	return t, o, nil
}

// Touch records use at most once a minute, so a busy integration does not turn
// every read into a write.
func (r *APITokenRepo) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.Pool.Exec(ctx, `UPDATE iam.api_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, at)
	return errx.Wrap(errx.KindTransient, err, "touch api token")
}
