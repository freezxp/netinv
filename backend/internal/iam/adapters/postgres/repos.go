// Package postgres — IAM repositories (repository pattern behind app/ports.go).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

type UserRepo struct{ Pool *pgxpool.Pool }

const userCols = `id, tenant_id, username, email, password_hash, display_name,
	status, password_change_required, last_login_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	u := &domain.User{}
	var status string
	err := row.Scan(&u.ID, &u.TenantID, &u.Username, &u.Email, &u.PasswordHash,
		&u.DisplayName, &status, &u.PasswordChangeRequired, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "user not found")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "scan user")
	}
	u.Status = domain.UserStatus(status)
	return u, nil
}

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	return scanUser(r.Pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM iam.users WHERE username = $1`, username))
}

func (r *UserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return scanUser(r.Pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM iam.users WHERE id = $1`, id))
}

func (r *UserRepo) RoleNames(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT r.name FROM iam.roles r
		JOIN iam.user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = $1 ORDER BY r.name`, userID)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "role names")
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func (r *UserRepo) Create(ctx context.Context, u *domain.User, roleIDs []string) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO iam.users (id, tenant_id, username, email, password_hash,
				display_name, status, password_change_required)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			u.ID, u.TenantID, u.Username, u.Email, u.PasswordHash,
			u.DisplayName, string(u.Status), u.PasswordChangeRequired)
		if err != nil {
			return errx.Wrap(errx.KindConflict, err, "insert user")
		}
		for _, roleID := range roleIDs {
			if _, err := tx.Exec(ctx,
				`INSERT INTO iam.user_roles (user_id, role_id) VALUES ($1,$2)`,
				u.ID, roleID); err != nil {
				return errx.Wrap(errx.KindInvalid, err, "grant role")
			}
		}
		return nil
	})
}

func (r *UserRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.Pool.QueryRow(ctx, `SELECT count(*) FROM iam.users`).Scan(&n)
	return n, errx.Wrap(errx.KindTransient, err, "count users")
}

func (r *UserRepo) RecordLogin(ctx context.Context, userID string, at time.Time) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE iam.users SET last_login_at = $2, updated_at = now() WHERE id = $1`,
		userID, at)
	return errx.Wrap(errx.KindTransient, err, "record login")
}

type RefreshTokenRepo struct{ Pool *pgxpool.Pool }

func (r *RefreshTokenRepo) Insert(ctx context.Context, t *domain.RefreshToken) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO iam.refresh_tokens (id, user_id, token_hash, family_id, issued_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		t.ID, t.UserID, t.TokenHash, t.FamilyID, t.IssuedAt, t.ExpiresAt)
	return errx.Wrap(errx.KindTransient, err, "insert refresh token")
}

func (r *RefreshTokenRepo) GetByHash(ctx context.Context, hash string) (*domain.RefreshToken, error) {
	t := &domain.RefreshToken{}
	err := r.Pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, family_id, issued_at, expires_at, rotated_at, revoked_at
		FROM iam.refresh_tokens WHERE token_hash = $1`, hash).
		Scan(&t.ID, &t.UserID, &t.TokenHash, &t.FamilyID, &t.IssuedAt,
			&t.ExpiresAt, &t.RotatedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "refresh token not found")
	}
	return t, errx.Wrap(errx.KindTransient, err, "get refresh token")
}

func (r *RefreshTokenRepo) MarkRotated(ctx context.Context, id string, at time.Time) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE iam.refresh_tokens SET rotated_at = $2 WHERE id = $1`, id, at)
	return errx.Wrap(errx.KindTransient, err, "mark rotated")
}

func (r *RefreshTokenRepo) Revoke(ctx context.Context, id string, at time.Time) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE iam.refresh_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at)
	return errx.Wrap(errx.KindTransient, err, "revoke token")
}

func (r *RefreshTokenRepo) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE iam.refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, at)
	return errx.Wrap(errx.KindTransient, err, "revoke family")
}
