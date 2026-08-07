// Package domain — Identity & Access entities (doc 16 §3).
package domain

import "time"

type UserStatus string

const (
	UserActive      UserStatus = "active"
	UserLocked      UserStatus = "locked"
	UserDeactivated UserStatus = "deactivated"
)

type User struct {
	ID                     string
	TenantID               string
	Username               string
	Email                  string
	PasswordHash           string
	DisplayName            string
	Status                 UserStatus
	PasswordChangeRequired bool
	LastLoginAt            *time.Time
}

// CanLogin reports whether authentication may proceed for this account state.
func (u *User) CanLogin() bool { return u.Status == UserActive }

// RefreshToken is the persisted form of an opaque refresh token (doc 07 §4).
type RefreshToken struct {
	ID        string
	UserID    string
	TokenHash string
	FamilyID  string
	IssuedAt  time.Time
	ExpiresAt time.Time
	RotatedAt *time.Time
	RevokedAt *time.Time
}

// Usable reports whether the token can be exchanged right now.
func (t *RefreshToken) Usable(now time.Time) bool {
	return t.RotatedAt == nil && t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// Compromised reports the reuse-detection condition: presenting a token that
// was already rotated or revoked means the family must be revoked (FR-AUTH-02).
func (t *RefreshToken) Compromised() bool {
	return t.RotatedAt != nil || t.RevokedAt != nil
}
