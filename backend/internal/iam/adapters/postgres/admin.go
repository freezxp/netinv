package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// UserAdminRepo methods (app.UserAdminRepo) on the same UserRepo struct.

func (r *UserRepo) List(ctx context.Context) ([]*domain.User, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT `+userCols+` FROM iam.users ORDER BY username`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list users")
	}
	defer rows.Close()
	var out []*domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *UserRepo) UpdateProfile(ctx context.Context, id string, email, displayName *string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE iam.users SET
			email = coalesce($2, email),
			display_name = coalesce($3, display_name),
			updated_at = now()
		WHERE id = $1`, id, email, displayName)
	return errx.Wrap(errx.KindTransient, err, "update profile")
}

func (r *UserRepo) SetStatus(ctx context.Context, id string, status domain.UserStatus) error {
	tag, err := r.Pool.Exec(ctx,
		`UPDATE iam.users SET status = $2, updated_at = now() WHERE id = $1`,
		id, string(status))
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "set status")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "user not found")
	}
	return nil
}

func (r *UserRepo) SetPassword(ctx context.Context, id, hash string, changeRequired bool) error {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE iam.users SET password_hash = $2, password_change_required = $3,
			updated_at = now() WHERE id = $1`, id, hash, changeRequired)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "set password")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "user not found")
	}
	return nil
}

func (r *UserRepo) SetRoles(ctx context.Context, userID string, roleIDs []string, grantedBy string) error {
	return pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM iam.user_roles WHERE user_id = $1`, userID); err != nil {
			return errx.Wrap(errx.KindTransient, err, "clear roles")
		}
		for _, rid := range roleIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO iam.user_roles (user_id, role_id, granted_by)
				VALUES ($1,$2,nullif($3,''))`, userID, rid, grantedBy); err != nil {
				return errx.Wrap(errx.KindInvalid, err, "grant role")
			}
		}
		return nil
	})
}

func (r *UserRepo) RoleIDsExist(ctx context.Context, roleIDs []string) (bool, error) {
	var n int
	err := r.Pool.QueryRow(ctx,
		`SELECT count(*) FROM iam.roles WHERE id = any($1)`, roleIDs).Scan(&n)
	return n == len(roleIDs), errx.Wrap(errx.KindTransient, err, "check roles")
}

// Role listing for GET /roles.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"is_builtin"`
}

func (r *UserRepo) Roles(ctx context.Context) ([]Role, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT id, name, coalesce(description,''), permissions, is_builtin
		 FROM iam.roles ORDER BY name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list roles")
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name, &role.Description,
			&role.Permissions, &role.Builtin); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

func (r *RefreshTokenRepo) RevokeAllForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE iam.refresh_tokens SET revoked_at = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, userID, at)
	return errx.Wrap(errx.KindTransient, err, "revoke all tokens")
}
