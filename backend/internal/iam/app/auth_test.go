package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/iam/adapters/lockout"
	"github.com/freezxp/netinv/backend/internal/iam/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authn"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// ---- fakes ----

type fakeUsers struct {
	byName map[string]*domain.User
	roles  map[string][]string
}

func (f *fakeUsers) GetByUsername(_ context.Context, u string) (*domain.User, error) {
	if user, ok := f.byName[u]; ok {
		return user, nil
	}
	return nil, errx.New(errx.KindNotFound, "user not found")
}
func (f *fakeUsers) GetByID(_ context.Context, id string) (*domain.User, error) {
	for _, u := range f.byName {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, errx.New(errx.KindNotFound, "user not found")
}
func (f *fakeUsers) RoleNames(_ context.Context, id string) ([]string, error) {
	return f.roles[id], nil
}
func (f *fakeUsers) Create(_ context.Context, u *domain.User, _ []string) error {
	f.byName[u.Username] = u
	return nil
}
func (f *fakeUsers) Count(context.Context) (int, error)                   { return len(f.byName), nil }
func (f *fakeUsers) RecordLogin(context.Context, string, time.Time) error { return nil }

type fakeTokens struct {
	mu     sync.Mutex
	byHash map[string]*domain.RefreshToken
}

func newFakeTokens() *fakeTokens { return &fakeTokens{byHash: map[string]*domain.RefreshToken{}} }

func (f *fakeTokens) Insert(_ context.Context, t *domain.RefreshToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *t
	f.byHash[t.TokenHash] = &cp
	return nil
}
func (f *fakeTokens) GetByHash(_ context.Context, h string) (*domain.RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.byHash[h]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, errx.New(errx.KindNotFound, "not found")
}
func (f *fakeTokens) find(id string) *domain.RefreshToken {
	for _, t := range f.byHash {
		if t.ID == id {
			return t
		}
	}
	return nil
}
func (f *fakeTokens) MarkRotated(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.find(id).RotatedAt = &at
	return nil
}
func (f *fakeTokens) Revoke(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.find(id).RevokedAt = &at
	return nil
}
func (f *fakeTokens) RevokeFamily(_ context.Context, fam string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.byHash {
		if t.FamilyID == fam && t.RevokedAt == nil {
			t.RevokedAt = &at
		}
	}
	return nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAudit) Write(_ context.Context, e audit.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}
func (f *fakeAudit) has(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Action == action {
			return true
		}
	}
	return false
}

// ---- helpers ----

var fastArgon = authn.Argon2Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1}

func newService(t *testing.T) (*AuthService, *fakeAudit) {
	t.Helper()
	seed := make([]byte, 32)
	iss, err := authn.NewIssuer(seed, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := authn.HashPassword("hunter2hunter2", fastArgon)
	users := &fakeUsers{
		byName: map[string]*domain.User{
			"nadia": {ID: "u_1", TenantID: "t_default", Username: "nadia",
				PasswordHash: hash, Status: domain.UserActive},
		},
		roles: map[string][]string{"u_1": {"operator"}},
	}
	aud := &fakeAudit{}
	return &AuthService{
		Users: users, Tokens: newFakeTokens(), Lockout: lockout.NewMemory(),
		Issuer: iss, Audit: aud, Argon: fastArgon,
		Log: slog.New(slog.DiscardHandler),
	}, aud
}

// ---- tests ----

func TestLoginSuccess(t *testing.T) {
	s, aud := newService(t)
	res, err := s.Login(context.Background(), "nadia", "hunter2hunter2", ClientMeta{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.AccessToken == "" || res.RefreshPlain == "" {
		t.Fatal("tokens missing")
	}
	claims, err := s.Issuer.Verify(res.AccessToken)
	if err != nil || claims.Subject != "u_1" || claims.Roles[0] != "operator" {
		t.Fatalf("claims: %+v err=%v", claims, err)
	}
	if !aud.has("auth.login.success") {
		t.Error("missing success audit event")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s, aud := newService(t)
	if _, err := s.Login(context.Background(), "nadia", "wrong", ClientMeta{}); errx.KindOf(err) != errx.KindUnauthorized {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if !aud.has("auth.login.failure") {
		t.Error("missing failure audit event")
	}
}

func TestLoginUnknownUserSameError(t *testing.T) {
	s, _ := newService(t)
	_, errUnknown := s.Login(context.Background(), "ghost", "x", ClientMeta{})
	_, errWrongPw := s.Login(context.Background(), "nadia", "x", ClientMeta{})
	if errUnknown.Error() != errWrongPw.Error() {
		t.Error("unknown-user and wrong-password must be indistinguishable")
	}
}

func TestLockoutAfterFiveFailures(t *testing.T) {
	s, aud := newService(t)
	ctx := context.Background()
	for range 5 {
		_, _ = s.Login(ctx, "nadia", "wrong", ClientMeta{})
	}
	_, err := s.Login(ctx, "nadia", "hunter2hunter2", ClientMeta{})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err = %v, want locked", err)
	}
	if !aud.has("auth.login.lockout") {
		t.Error("missing lockout audit event")
	}
}

func TestRefreshRotation(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	login, _ := s.Login(ctx, "nadia", "hunter2hunter2", ClientMeta{})

	r1, err := s.Refresh(ctx, login.RefreshPlain, ClientMeta{})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if r1.RefreshPlain == login.RefreshPlain {
		t.Fatal("refresh token must rotate")
	}
	// Old token single-use: second exchange must fail…
	if _, err := s.Refresh(ctx, login.RefreshPlain, ClientMeta{}); err == nil {
		t.Fatal("reused refresh token accepted")
	}
	// …and reuse detection must have revoked the whole family (doc 07 §4).
	if _, err := s.Refresh(ctx, r1.RefreshPlain, ClientMeta{}); err == nil {
		t.Fatal("family survived reuse detection")
	}
}

func TestLogoutRevokes(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	login, _ := s.Login(ctx, "nadia", "hunter2hunter2", ClientMeta{})
	s.Logout(ctx, login.RefreshPlain, ClientMeta{})
	if _, err := s.Refresh(ctx, login.RefreshPlain, ClientMeta{}); err == nil {
		t.Fatal("refresh after logout accepted")
	}
}

func TestBootstrapCreatesAdminOnce(t *testing.T) {
	s, aud := newService(t)
	s.Users.(*fakeUsers).byName = map[string]*domain.User{} // empty install
	pw, err := s.Bootstrap(context.Background(), "")
	if err != nil || pw == "" {
		t.Fatalf("bootstrap: pw=%q err=%v", pw, err)
	}
	admin, err := s.Users.GetByUsername(context.Background(), "admin")
	if err != nil || !admin.PasswordChangeRequired {
		t.Fatalf("admin: %+v err=%v", admin, err)
	}
	if !aud.has("user.bootstrap") {
		t.Error("missing bootstrap audit event")
	}
	// Second boot: no-op.
	pw2, err := s.Bootstrap(context.Background(), "")
	if err != nil || pw2 != "" {
		t.Fatalf("second bootstrap: pw=%q err=%v", pw2, err)
	}
}
