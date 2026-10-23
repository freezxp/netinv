package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/pgxtest"
)

// The repository against real Postgres. Touch's comparison once compiled
// against a fake and failed on every request in a real database (Postgres
// inferred its parameter as an interval), leaving last_used_at null forever.
func TestAPITokenRepo(t *testing.T) {
	_, pool := pgxtest.Throwaway(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO iam.users (id, tenant_id, username, email, password_hash, display_name, status)
		VALUES ('u_tok', 't_default', 'tok', 'tok@example.invalid', 'x', 'Tok', 'active');
		INSERT INTO iam.user_roles (user_id, role_id)
		SELECT 'u_tok', id FROM iam.roles WHERE name = 'operator'`); err != nil {
		t.Fatal(err)
	}
	repo := &APITokenRepo{Pool: pool}
	tok := &domain.APIToken{
		ID: "tok_1", UserID: "u_tok", Name: "grafana", TokenHash: "h1",
		Scopes: []string{"devices:read"}, CreatedAt: time.Now().UTC(),
	}
	if err := repo.Insert(ctx, tok); err != nil {
		t.Fatal(err)
	}

	got, owner, err := repo.Lookup(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "tok_1" || len(got.Scopes) != 1 || owner.Username != "tok" ||
		owner.Status != domain.UserActive || len(owner.Roles) != 1 || owner.Roles[0] != "operator" {
		t.Errorf("lookup = %+v / %+v", got, owner)
	}

	now := time.Now().UTC()
	if err := repo.Touch(ctx, "tok_1", now); err != nil {
		t.Fatalf("touch: %v", err)
	}
	// A second touch inside the minute must not move it.
	if err := repo.Touch(ctx, "tok_1", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.Lookup(ctx, "h1")
	if got.LastUsedAt == nil || got.LastUsedAt.Sub(now).Abs() > time.Millisecond {
		t.Errorf("last_used_at = %v, want %v", got.LastUsedAt, now)
	}

	if err := repo.Revoke(ctx, "u_other", "tok_1", now); errx.KindOf(err) != errx.KindNotFound {
		t.Errorf("revoke by another user: %v, want not found", err)
	}
	if err := repo.Revoke(ctx, "u_tok", "tok_1", now); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListByUser(ctx, "u_tok")
	if err != nil || len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("list after revoke = %+v, %v", list, err)
	}
	if _, _, err := repo.Lookup(ctx, "nope"); errx.KindOf(err) != errx.KindNotFound {
		t.Errorf("unknown hash: %v, want not found", err)
	}
}
