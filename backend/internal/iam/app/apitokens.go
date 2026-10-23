package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type APITokenRepo interface {
	Insert(ctx context.Context, t *domain.APIToken) error
	ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error)
	Revoke(ctx context.Context, userID, id string, at time.Time) error
	Lookup(ctx context.Context, hash string) (*domain.APIToken, *domain.TokenOwner, error)
	Touch(ctx context.Context, id string, at time.Time) error
}

// APITokenService issues and verifies personal API tokens (doc 20 §2) — the
// credential another application uses to read NetInv.
type APITokenService struct {
	Repo    APITokenRepo
	Checker authz.Checker
	Audit   audit.Writer
	Log     *slog.Logger
	// Now is injectable for tests.
	Now func() time.Time
}

// verifyTimeout bounds the lookup behind a request. TokenVerifier carries no
// context — a JWT needs none — so the deadline is set here instead.
const verifyTimeout = 3 * time.Second

func (s *APITokenService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type CreateAPITokenCmd struct {
	Name      string
	Scopes    []string
	ExpiresAt *time.Time
	Meta      ClientMeta
}

// Create mints a token for the caller. Every scope must be one the caller holds
// today: a token is a narrowed copy of its owner, never a way to obtain
// something the owner lacks. Returns the plain value, shown once.
func (s *APITokenService) Create(ctx context.Context, caller *authn.Claims, cmd CreateAPITokenCmd) (*domain.APIToken, string, error) {
	name := strings.TrimSpace(cmd.Name)
	if name == "" || len(name) > 100 {
		return nil, "", errx.New(errx.KindInvalid, "name is required (at most 100 characters)")
	}
	if len(cmd.Scopes) == 0 {
		return nil, "", errx.New(errx.KindInvalid, "at least one scope is required")
	}
	known := map[string]bool{}
	for _, p := range authz.All {
		known[p] = true
	}
	seen := map[string]bool{}
	scopes := make([]string, 0, len(cmd.Scopes))
	for _, sc := range cmd.Scopes {
		if !known[sc] {
			return nil, "", errx.New(errx.KindInvalid, "unknown scope %q", sc)
		}
		if !s.Checker.Has(caller.Roles, sc) {
			return nil, "", errx.New(errx.KindInvalid, "scope %q is not one of your permissions", sc)
		}
		if !seen[sc] {
			seen[sc] = true
			scopes = append(scopes, sc)
		}
	}
	now := s.now().UTC()
	if cmd.ExpiresAt != nil && !cmd.ExpiresAt.After(now) {
		return nil, "", errx.New(errx.KindInvalid, "expires_at must be in the future")
	}
	plain, hash, err := authn.NewAPIToken()
	if err != nil {
		return nil, "", errx.Wrap(errx.KindInternal, err, "generate api token")
	}
	t := &domain.APIToken{
		ID: id.New("tok"), UserID: caller.Subject, Name: name, TokenHash: hash,
		Scopes: scopes, ExpiresAt: cmd.ExpiresAt, CreatedAt: now,
	}
	if err := s.Repo.Insert(ctx, t); err != nil {
		return nil, "", err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: caller.Subject, Action: "api_token.create",
		ResourceKind: "api_token", ResourceID: t.ID,
		After:    map[string]any{"name": name, "scopes": scopes, "expires_at": cmd.ExpiresAt},
		SourceIP: cmd.Meta.SourceIP, UserAgent: cmd.Meta.UserAgent, TraceID: cmd.Meta.TraceID,
	})
	return t, plain, nil
}

func (s *APITokenService) List(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	return s.Repo.ListByUser(ctx, userID)
}

func (s *APITokenService) Revoke(ctx context.Context, userID, tokenID string, meta ClientMeta) error {
	if err := s.Repo.Revoke(ctx, userID, tokenID, s.now().UTC()); err != nil {
		return err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: userID, Action: "api_token.revoke",
		ResourceKind: "api_token", ResourceID: tokenID,
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return nil
}

// Verify implements authn.TokenVerifier for nv_ tokens. Every failure is the
// same 401 so a caller cannot tell a revoked token from one that never existed.
func (s *APITokenService) Verify(raw string) (*authn.Claims, error) {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	denied := errx.New(errx.KindUnauthorized, "invalid api token")

	t, owner, err := s.Repo.Lookup(ctx, authn.HashAPIToken(raw))
	if err != nil {
		if errx.KindOf(err) == errx.KindNotFound {
			return nil, denied
		}
		return nil, err
	}
	now := s.now()
	if !t.Usable(now) || owner.Status != domain.UserActive {
		return nil, denied
	}
	if err := s.Repo.Touch(ctx, t.ID, now.UTC()); err != nil && s.Log != nil {
		s.Log.Warn("api token last-used update failed", "token", t.ID, "err", err)
	}
	c := &authn.Claims{
		Username: owner.Username, Roles: owner.Roles, Tenant: owner.TenantID,
		APITokenID: t.ID, Scopes: t.Scopes,
	}
	c.Subject = t.UserID
	return c, nil
}
