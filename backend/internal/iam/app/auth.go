package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

const (
	refreshTTL    = 30 * 24 * time.Hour
	defaultTenant = "t_default"
)

// ClientMeta carries request attribution into audit events (FR-AUTH-05).
type ClientMeta struct {
	SourceIP  string
	UserAgent string
	TraceID   string
}

type LoginResult struct {
	AccessToken            string
	ExpiresIn              int
	RefreshPlain           string // set as httpOnly cookie by the handler
	User                   *domain.User
	Roles                  []string
	PasswordChangeRequired bool
}

// AuthService implements login/refresh/logout (docs 07 §4, 20 §2).
type AuthService struct {
	Users    UserRepo
	Tokens   RefreshTokenRepo
	Lockout  Lockout
	Issuer   *authn.Issuer
	Audit    audit.Writer
	Argon    authn.Argon2Params
	Log      *slog.Logger
	Now      func() time.Time // injectable clock for tests
}

func (s *AuthService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

var errBadCredentials = errx.New(errx.KindUnauthorized, "invalid credentials")

// ErrLocked distinguishes 423 from 401 at the handler.
var ErrLocked = errx.New(errx.KindForbidden, "account temporarily locked")

func (s *AuthService) Login(ctx context.Context, username, password string, meta ClientMeta) (*LoginResult, error) {
	authEvent := func(action string, actorID string, detail map[string]any) {
		s.Audit.Write(ctx, audit.Event{
			ActorKind: "user", ActorID: actorID, Action: action,
			SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
			Detail: detail,
		})
	}

	locked, err := s.Lockout.Locked(ctx, username)
	if err != nil {
		return nil, err
	}
	if locked {
		authEvent("auth.login.locked_attempt", "", map[string]any{"username": username})
		return nil, ErrLocked
	}

	u, err := s.Users.GetByUsername(ctx, username)
	if err != nil && errx.KindOf(err) != errx.KindNotFound {
		return nil, err
	}
	// Verify against a dummy hash on unknown users to keep timing flat.
	verified := false
	if u != nil {
		verified, err = authn.VerifyPassword(password, u.PasswordHash)
		if err != nil {
			return nil, errx.Wrap(errx.KindInternal, err, "verify password")
		}
	} else {
		_, _ = authn.VerifyPassword(password, dummyHash)
	}
	if u == nil || !verified || !u.CanLogin() {
		nowLocked, lerr := s.Lockout.RecordFailure(ctx, username)
		if lerr != nil {
			s.Log.Warn("lockout record failed", "err", lerr)
		}
		action := "auth.login.failure"
		if nowLocked {
			action = "auth.login.lockout"
		}
		authEvent(action, "", map[string]any{"username": username})
		return nil, errBadCredentials
	}
	_ = s.Lockout.Reset(ctx, username)

	roles, err := s.Users.RoleNames(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	access, err := s.Issuer.Issue(u.ID, u.Username, u.TenantID, roles)
	if err != nil {
		return nil, errx.Wrap(errx.KindInternal, err, "issue access token")
	}
	refreshPlain, tok, err := s.newRefreshToken(u.ID, id.New("fam"))
	if err != nil {
		return nil, err
	}
	if err := s.Tokens.Insert(ctx, tok); err != nil {
		return nil, err
	}
	if err := s.Users.RecordLogin(ctx, u.ID, s.now()); err != nil {
		s.Log.Warn("record login failed", "err", err)
	}
	authEvent("auth.login.success", u.ID, map[string]any{"username": username})

	return &LoginResult{
		AccessToken: access, ExpiresIn: int(s.Issuer.TTL().Seconds()),
		RefreshPlain: refreshPlain, User: u, Roles: roles,
		PasswordChangeRequired: u.PasswordChangeRequired,
	}, nil
}

// Refresh rotates a refresh token (single use). Reuse of a consumed token
// revokes the whole family (doc 07 §4).
func (s *AuthService) Refresh(ctx context.Context, refreshPlain string, meta ClientMeta) (*LoginResult, error) {
	t, err := s.Tokens.GetByHash(ctx, hashToken(refreshPlain))
	if err != nil {
		if errx.KindOf(err) == errx.KindNotFound {
			return nil, errBadCredentials
		}
		return nil, err
	}
	now := s.now()
	if t.Compromised() {
		_ = s.Tokens.RevokeFamily(ctx, t.FamilyID, now)
		s.Audit.Write(ctx, audit.Event{
			ActorKind: "user", ActorID: t.UserID, Action: "auth.token.reuse",
			SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
			Detail: map[string]any{"family_id": t.FamilyID},
		})
		return nil, errBadCredentials
	}
	if !t.Usable(now) {
		return nil, errBadCredentials
	}
	u, err := s.Users.GetByID(ctx, t.UserID)
	if err != nil || !u.CanLogin() {
		return nil, errBadCredentials
	}
	roles, err := s.Users.RoleNames(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if err := s.Tokens.MarkRotated(ctx, t.ID, now); err != nil {
		return nil, err
	}
	refreshPlainNew, tokNew, err := s.newRefreshToken(u.ID, t.FamilyID)
	if err != nil {
		return nil, err
	}
	if err := s.Tokens.Insert(ctx, tokNew); err != nil {
		return nil, err
	}
	access, err := s.Issuer.Issue(u.ID, u.Username, u.TenantID, roles)
	if err != nil {
		return nil, errx.Wrap(errx.KindInternal, err, "issue access token")
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: u.ID, Action: "auth.token.refresh",
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return &LoginResult{
		AccessToken: access, ExpiresIn: int(s.Issuer.TTL().Seconds()),
		RefreshPlain: refreshPlainNew, User: u, Roles: roles,
		PasswordChangeRequired: u.PasswordChangeRequired,
	}, nil
}

func (s *AuthService) Logout(ctx context.Context, refreshPlain string, meta ClientMeta) {
	t, err := s.Tokens.GetByHash(ctx, hashToken(refreshPlain))
	if err != nil {
		return // logout is best-effort; no user-visible failure
	}
	_ = s.Tokens.Revoke(ctx, t.ID, s.now())
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: t.UserID, Action: "auth.logout",
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
}

// Bootstrap creates the initial admin when no users exist (doc 20 §12).
// Returns the generated password (empty if NETINV_ADMIN_PASSWORD supplied or
// users already exist) so the caller can log it exactly once.
func (s *AuthService) Bootstrap(ctx context.Context, envPassword string) (string, error) {
	n, err := s.Users.Count(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	password := envPassword
	generated := ""
	if password == "" {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		password = base64.RawURLEncoding.EncodeToString(buf)
		generated = password
	}
	hash, err := authn.HashPassword(password, s.Argon)
	if err != nil {
		return "", err
	}
	u := &domain.User{
		ID: id.New("u"), TenantID: defaultTenant,
		Username: "admin", Email: "admin@localhost.invalid",
		PasswordHash: hash, DisplayName: "Administrator",
		Status: domain.UserActive, PasswordChangeRequired: true,
	}
	if err := s.Users.Create(ctx, u, []string{"role_admin"}); err != nil {
		return "", err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "system", Action: "user.bootstrap",
		ResourceKind: "user", ResourceID: u.ID,
		Detail: map[string]any{"username": "admin"},
	})
	return generated, nil
}

func (s *AuthService) newRefreshToken(userID, familyID string) (string, *domain.RefreshToken, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	plain := base64.RawURLEncoding.EncodeToString(buf)
	now := s.now()
	return plain, &domain.RefreshToken{
		ID: id.New("rt"), UserID: userID, TokenHash: hashToken(plain),
		FamilyID: familyID, IssuedAt: now, ExpiresAt: now.Add(refreshTTL),
	}, nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// dummyHash: fixed Argon2id hash of an unguessable value, used to equalize
// timing for unknown usernames.
var dummyHash = func() string {
	h, _ := authn.HashPassword("netinv-dummy-timing-equalizer", authn.Argon2Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1})
	return h
}()
