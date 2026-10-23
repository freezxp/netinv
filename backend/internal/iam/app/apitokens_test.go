package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type fakeAPITokens struct {
	byHash  map[string]*domain.APIToken
	owners  map[string]*domain.TokenOwner // by user id
	touched int
}

func (f *fakeAPITokens) Insert(_ context.Context, t *domain.APIToken) error {
	f.byHash[t.TokenHash] = t
	return nil
}
func (f *fakeAPITokens) ListByUser(_ context.Context, userID string) ([]*domain.APIToken, error) {
	var out []*domain.APIToken
	for _, t := range f.byHash {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeAPITokens) Revoke(_ context.Context, userID, id string, at time.Time) error {
	for _, t := range f.byHash {
		if t.ID == id && t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &at
			return nil
		}
	}
	return errx.New(errx.KindNotFound, "api token not found")
}
func (f *fakeAPITokens) Lookup(_ context.Context, hash string) (*domain.APIToken, *domain.TokenOwner, error) {
	t, ok := f.byHash[hash]
	if !ok {
		return nil, nil, errx.New(errx.KindNotFound, "api token not found")
	}
	return t, f.owners[t.UserID], nil
}
func (f *fakeAPITokens) Touch(context.Context, string, time.Time) error { f.touched++; return nil }

func newTokenSvc() (*APITokenService, *fakeAPITokens) {
	repo := &fakeAPITokens{
		byHash: map[string]*domain.APIToken{},
		owners: map[string]*domain.TokenOwner{
			"u_op": {Username: "op", TenantID: "t_default", Status: domain.UserActive, Roles: []string{"operator"}},
		},
	}
	return &APITokenService{
		Repo:    repo,
		Checker: authz.Static{"operator": {authz.DevicesRead, authz.DevicesWrite, authz.MetricsRead}},
		Audit:   &fakeAudit{},
	}, repo
}

func operator() *authn.Claims {
	c := &authn.Claims{Username: "op", Roles: []string{"operator"}}
	c.Subject = "u_op"
	return c
}

func TestAPITokenRoundTrip(t *testing.T) {
	svc, repo := newTokenSvc()
	tok, plain, err := svc.Create(context.Background(), operator(), CreateAPITokenCmd{
		Name: "grafana", Scopes: []string{authz.DevicesRead, authz.MetricsRead, authz.DevicesRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "nv_") {
		t.Fatalf("token %q lacks the nv_ prefix", plain)
	}
	if _, stored := repo.byHash[plain]; stored {
		t.Fatal("the plain token must never be stored")
	}
	if len(tok.Scopes) != 2 {
		t.Errorf("duplicate scopes should collapse, got %v", tok.Scopes)
	}

	c, err := svc.Verify(plain)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "u_op" || c.Interactive() || c.APITokenID != tok.ID {
		t.Errorf("claims = %+v", c)
	}
	if !c.Allows(authz.MetricsRead) || c.Allows(authz.DevicesWrite) {
		t.Errorf("scopes must admit exactly what was granted: %v", c.Scopes)
	}
	if repo.touched != 1 {
		t.Errorf("last-used not recorded")
	}
}

// A token is a narrowed copy of its owner; asking for more must fail rather
// than mint something the owner could not do themselves.
func TestAPITokenCannotExceedOwner(t *testing.T) {
	svc, _ := newTokenSvc()
	for _, scopes := range [][]string{{authz.UsersWrite}, {"devices:everything"}, nil} {
		_, _, err := svc.Create(context.Background(), operator(), CreateAPITokenCmd{Name: "x", Scopes: scopes})
		if errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("scopes %v: err = %v, want invalid", scopes, err)
		}
	}
}

func TestAPITokenRejected(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)

	cases := map[string]func(svc *APITokenService, repo *fakeAPITokens, tok *domain.APIToken){
		"revoked": func(svc *APITokenService, _ *fakeAPITokens, tok *domain.APIToken) {
			_ = svc.Revoke(ctx, "u_op", tok.ID, ClientMeta{})
		},
		"expired": func(_ *APITokenService, _ *fakeAPITokens, tok *domain.APIToken) {
			tok.ExpiresAt = &past
		},
		"owner deactivated": func(_ *APITokenService, repo *fakeAPITokens, _ *domain.APIToken) {
			repo.owners["u_op"].Status = domain.UserDeactivated
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			svc, repo := newTokenSvc()
			tok, plain, err := svc.Create(ctx, operator(), CreateAPITokenCmd{Name: "x", Scopes: []string{authz.DevicesRead}})
			if err != nil {
				t.Fatal(err)
			}
			spoil(svc, repo, tok)
			if _, err := svc.Verify(plain); errx.KindOf(err) != errx.KindUnauthorized {
				t.Errorf("err = %v, want unauthorized", err)
			}
		})
	}

	svc, _ := newTokenSvc()
	if _, err := svc.Verify("nv_never-issued"); errx.KindOf(err) != errx.KindUnauthorized {
		t.Errorf("unknown token: err = %v, want unauthorized", err)
	}
}

// Revoking is owner-scoped: one user cannot revoke another's token by id.
func TestAPITokenRevokeOtherUser(t *testing.T) {
	svc, _ := newTokenSvc()
	tok, _, _ := svc.Create(context.Background(), operator(), CreateAPITokenCmd{Name: "x", Scopes: []string{authz.DevicesRead}})
	if err := svc.Revoke(context.Background(), "u_someone_else", tok.ID, ClientMeta{}); errx.KindOf(err) != errx.KindNotFound {
		t.Errorf("err = %v, want not found", err)
	}
}
