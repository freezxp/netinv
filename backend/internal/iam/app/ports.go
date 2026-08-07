// Package app — IAM use cases (doc 05 §1 CQRS-lite). ports.go declares the
// interfaces adapters must implement (repository pattern).
package app

import (
	"context"
	"time"

	"github.com/freezxp/netinv/backend/internal/iam/domain"
)

type UserRepo interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
	RoleNames(ctx context.Context, userID string) ([]string, error)
	Create(ctx context.Context, u *domain.User, roleIDs []string) error
	Count(ctx context.Context) (int, error)
	RecordLogin(ctx context.Context, userID string, at time.Time) error
}

type RefreshTokenRepo interface {
	Insert(ctx context.Context, t *domain.RefreshToken) error
	GetByHash(ctx context.Context, hash string) (*domain.RefreshToken, error)
	MarkRotated(ctx context.Context, id string, at time.Time) error
	Revoke(ctx context.Context, id string, at time.Time) error
	RevokeFamily(ctx context.Context, familyID string, at time.Time) error
}

// Lockout throttles failed logins (FR-AUTH-04). Redis-backed in production.
type Lockout interface {
	Locked(ctx context.Context, key string) (bool, error)
	// RecordFailure returns true when this failure crossed the lock threshold.
	RecordFailure(ctx context.Context, key string) (bool, error)
	Reset(ctx context.Context, key string) error
}
