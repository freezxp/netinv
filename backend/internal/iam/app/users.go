package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"regexp"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// UserAdminRepo extends UserRepo with management operations (doc 09 §3).
type UserAdminRepo interface {
	UserRepo
	List(ctx context.Context) ([]*domain.User, error)
	UpdateProfile(ctx context.Context, id string, email, displayName *string) error
	SetStatus(ctx context.Context, id string, status domain.UserStatus) error
	SetPassword(ctx context.Context, id, hash string, changeRequired bool) error
	SetRoles(ctx context.Context, userID string, roleIDs []string, grantedBy string) error
	RoleIDsExist(ctx context.Context, roleIDs []string) (bool, error)
}

// TokenRevoker invalidates a user's refresh tokens on deactivation (FR-AUTH-07).
type TokenRevoker interface {
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
}

type UserService struct {
	Users  UserAdminRepo
	Tokens TokenRevoker
	Audit  audit.Writer
	Argon  authn.Argon2Params
	Log    *slog.Logger
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// ValidatePassword enforces doc 20 §4 (length floor; breach-list check is P1).
func ValidatePassword(pw string) error {
	if len(pw) < 12 {
		return errx.New(errx.KindInvalid, "password must be at least 12 characters")
	}
	return nil
}

type CreateUserCmd struct {
	Username, Email, DisplayName string
	RoleIDs                      []string
	TempPassword                 string // generated when empty
	Actor                        string
	Meta                         ClientMeta
}

// CreateUser returns the created user and the temp password (to display once).
func (s *UserService) CreateUser(ctx context.Context, cmd CreateUserCmd) (*domain.User, string, error) {
	if !usernameRe.MatchString(cmd.Username) {
		return nil, "", errx.New(errx.KindInvalid, "invalid username (lowercase, 2-63 chars, [a-z0-9._-])")
	}
	if cmd.Email == "" || cmd.DisplayName == "" {
		return nil, "", errx.New(errx.KindInvalid, "email and display_name are required")
	}
	if len(cmd.RoleIDs) == 0 {
		return nil, "", errx.New(errx.KindInvalid, "at least one role is required")
	}
	if ok, err := s.Users.RoleIDsExist(ctx, cmd.RoleIDs); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", errx.New(errx.KindInvalid, "unknown role id")
	}
	pw := cmd.TempPassword
	if pw == "" {
		buf := make([]byte, 15)
		if _, err := rand.Read(buf); err != nil {
			return nil, "", err
		}
		pw = base64.RawURLEncoding.EncodeToString(buf)
	} else if err := ValidatePassword(pw); err != nil {
		return nil, "", err
	}
	hash, err := authn.HashPassword(pw, s.Argon)
	if err != nil {
		return nil, "", errx.Wrap(errx.KindInternal, err, "hash password")
	}
	u := &domain.User{
		ID: id.New("u"), TenantID: defaultTenant,
		Username: cmd.Username, Email: cmd.Email, DisplayName: cmd.DisplayName,
		PasswordHash: hash, Status: domain.UserActive, PasswordChangeRequired: true,
	}
	if err := s.Users.Create(ctx, u, cmd.RoleIDs); err != nil {
		return nil, "", err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: cmd.Actor, Action: "user.create",
		ResourceKind: "user", ResourceID: u.ID,
		After:    map[string]any{"username": u.Username, "roles": cmd.RoleIDs},
		SourceIP: cmd.Meta.SourceIP, UserAgent: cmd.Meta.UserAgent, TraceID: cmd.Meta.TraceID,
	})
	return u, pw, nil
}

func (s *UserService) SetRoles(ctx context.Context, userID string, roleIDs []string, actor string, meta ClientMeta) error {
	if ok, err := s.Users.RoleIDsExist(ctx, roleIDs); err != nil {
		return err
	} else if !ok {
		return errx.New(errx.KindInvalid, "unknown role id")
	}
	before, err := s.Users.RoleNames(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.Users.SetRoles(ctx, userID, roleIDs, actor); err != nil {
		return err
	}
	after, _ := s.Users.RoleNames(ctx, userID)
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: actor, Action: "user.roles.set",
		ResourceKind: "user", ResourceID: userID,
		Before:   map[string]any{"roles": before},
		After:    map[string]any{"roles": after},
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return nil
}

func (s *UserService) Deactivate(ctx context.Context, userID, actor string, meta ClientMeta) error {
	if userID == actor {
		return errx.New(errx.KindInvalid, "cannot deactivate your own account")
	}
	if err := s.Users.SetStatus(ctx, userID, domain.UserDeactivated); err != nil {
		return err
	}
	if err := s.Tokens.RevokeAllForUser(ctx, userID, time.Now().UTC()); err != nil {
		s.Log.Error("revoke tokens on deactivation failed", "user", userID, "err", err)
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: actor, Action: "user.deactivate",
		ResourceKind: "user", ResourceID: userID,
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return nil
}

func (s *UserService) Activate(ctx context.Context, userID, actor string, meta ClientMeta) error {
	if err := s.Users.SetStatus(ctx, userID, domain.UserActive); err != nil {
		return err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: actor, Action: "user.activate",
		ResourceKind: "user", ResourceID: userID,
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return nil
}

// ResetPassword sets a fresh temp password (returned once) and forces change.
func (s *UserService) ResetPassword(ctx context.Context, userID, actor string, meta ClientMeta) (string, error) {
	buf := make([]byte, 15)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(buf)
	hash, err := authn.HashPassword(pw, s.Argon)
	if err != nil {
		return "", err
	}
	if err := s.Users.SetPassword(ctx, userID, hash, true); err != nil {
		return "", err
	}
	if err := s.Tokens.RevokeAllForUser(ctx, userID, time.Now().UTC()); err != nil {
		s.Log.Error("revoke tokens on reset failed", "user", userID, "err", err)
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: actor, Action: "user.password.reset",
		ResourceKind: "user", ResourceID: userID,
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return pw, nil
}

// ChangeOwnPassword verifies the old password before setting the new one.
func (s *UserService) ChangeOwnPassword(ctx context.Context, userID, oldPw, newPw string, meta ClientMeta) error {
	if err := ValidatePassword(newPw); err != nil {
		return err
	}
	u, err := s.Users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := authn.VerifyPassword(oldPw, u.PasswordHash)
	if err != nil {
		return errx.Wrap(errx.KindInternal, err, "verify password")
	}
	if !ok {
		return errx.New(errx.KindUnauthorized, "current password incorrect")
	}
	hash, err := authn.HashPassword(newPw, s.Argon)
	if err != nil {
		return err
	}
	if err := s.Users.SetPassword(ctx, userID, hash, false); err != nil {
		return err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "user", ActorID: userID, Action: "user.password.change",
		ResourceKind: "user", ResourceID: userID,
		SourceIP: meta.SourceIP, UserAgent: meta.UserAgent, TraceID: meta.TraceID,
	})
	return nil
}
