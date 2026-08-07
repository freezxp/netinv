package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/collection/domain"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// PollerRepo persists the poller fleet. The token_hash column stores the
// enrollment token hash until registration consumes it, then the poller's
// long-lived auth token hash (doc 20 §8 lifecycle).
type PollerRepo interface {
	CreatePending(ctx context.Context, p *domain.Poller, enrollTokenHash string) error
	FindByEnrollHash(ctx context.Context, hash string) (*domain.Poller, error)
	BindAuthToken(ctx context.Context, pollerID, authTokenHash, version string) error
	AuthByToken(ctx context.Context, pollerID, tokenHash string) (*domain.Poller, error)
	Heartbeat(ctx context.Context, pollerID string, version string,
		stats domain.HeartbeatStats, at time.Time) error
	List(ctx context.Context) ([]*domain.Poller, error)
	Get(ctx context.Context, pollerID string) (*domain.Poller, error)
	SetStatus(ctx context.Context, pollerID string, status domain.PollerStatus) error
}

type PollerService struct {
	Repo  PollerRepo
	Audit audit.Writer
}

type PollerMeta struct {
	Actor, SourceIP, UserAgent, TraceID string
}

func (m PollerMeta) event(action, rid string, detail map[string]any) audit.Event {
	return audit.Event{
		ActorKind: "user", ActorID: m.Actor, Action: action,
		ResourceKind: "poller", ResourceID: rid, Detail: detail,
		SourceIP: m.SourceIP, UserAgent: m.UserAgent, TraceID: m.TraceID,
	}
}

func newToken(prefix string) (plain, hash string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = prefix + "_" + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(sum[:]), nil
}

func hashOf(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// IssueEnrollToken creates a pending poller and returns the one-time token.
func (s *PollerService) IssueEnrollToken(ctx context.Context, name, siteID string,
	m PollerMeta) (*domain.Poller, string, error) {
	if name == "" || siteID == "" {
		return nil, "", errx.New(errx.KindInvalid, "name and site_id are required")
	}
	plain, hash, err := newToken("pe")
	if err != nil {
		return nil, "", err
	}
	p := &domain.Poller{
		ID: id.New("plr"), TenantID: "t_default", SiteID: siteID,
		Name: name, Status: domain.PollerPending,
	}
	if err := s.Repo.CreatePending(ctx, p, hash); err != nil {
		return nil, "", err
	}
	s.Audit.Write(ctx, m.event("poller.enroll_token.issue", p.ID,
		map[string]any{"name": name, "site_id": siteID}))
	return p, plain, nil
}

type RegisterResult struct {
	PollerID  string `json:"poller_id"`
	SiteID    string `json:"site_id"`
	AuthToken string `json:"auth_token"` // shown once; poller persists it
	Status    string `json:"status"`
}

// Register consumes an enrollment token (called by the poller itself).
func (s *PollerService) Register(ctx context.Context, enrollToken, version string,
	m PollerMeta) (*RegisterResult, error) {
	p, err := s.Repo.FindByEnrollHash(ctx, hashOf(enrollToken))
	if err != nil {
		return nil, errx.New(errx.KindUnauthorized, "invalid enrollment token")
	}
	authPlain, authHash, err := newToken("pt")
	if err != nil {
		return nil, err
	}
	if err := s.Repo.BindAuthToken(ctx, p.ID, authHash, version); err != nil {
		return nil, err
	}
	s.Audit.Write(ctx, audit.Event{
		ActorKind: "system", Action: "poller.register",
		ResourceKind: "poller", ResourceID: p.ID,
		Detail:   map[string]any{"version": version, "site_id": p.SiteID},
		SourceIP: m.SourceIP, TraceID: m.TraceID,
	})
	return &RegisterResult{
		PollerID: p.ID, SiteID: p.SiteID, AuthToken: authPlain,
		Status: string(p.Status),
	}, nil
}

// Heartbeat authenticates by poller token and records liveness (FR-PLT-02).
func (s *PollerService) Heartbeat(ctx context.Context, pollerID, token, version string,
	stats domain.HeartbeatStats) error {
	if _, err := s.Repo.AuthByToken(ctx, pollerID, hashOf(token)); err != nil {
		return errx.New(errx.KindUnauthorized, "invalid poller credentials")
	}
	return s.Repo.Heartbeat(ctx, pollerID, version, stats, time.Now().UTC())
}

func (s *PollerService) Approve(ctx context.Context, pollerID string, m PollerMeta) error {
	if err := s.Repo.SetStatus(ctx, pollerID, domain.PollerActive); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("poller.approve", pollerID, nil))
	return nil
}

func (s *PollerService) Disable(ctx context.Context, pollerID string, m PollerMeta) error {
	if err := s.Repo.SetStatus(ctx, pollerID, domain.PollerDisabled); err != nil {
		return err
	}
	s.Audit.Write(ctx, m.event("poller.disable", pollerID, nil))
	return nil
}
