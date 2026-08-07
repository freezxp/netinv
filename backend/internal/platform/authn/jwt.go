package authn

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// Claims are OIDC-shaped (ADR-010) so a future Keycloak issuer is a config
// change, not a code change.
type Claims struct {
	Username string   `json:"preferred_username"`
	Roles    []string `json:"roles"`
	Tenant   string   `json:"tenant"`
	jwt.RegisteredClaims
}

// TokenVerifier is the issuer-swap seam (doc 17 §5).
type TokenVerifier interface {
	Verify(raw string) (*Claims, error)
}

// Issuer signs short-lived access tokens with Ed25519 (doc 20 §2).
type Issuer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	ttl  time.Duration
}

const (
	issuer   = "netinv"
	audience = "netinv-api"
)

// NewIssuer builds an issuer from a 32-byte Ed25519 seed.
func NewIssuer(seed []byte, ttl time.Duration) (*Issuer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("authn: signing seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Issuer{priv: priv, pub: priv.Public().(ed25519.PublicKey), ttl: ttl}, nil
}

// NewIssuerFromBase64 decodes a base64 seed (NETINV_JWT_SIGNING_KEY).
func NewIssuerFromBase64(b64 string, ttl time.Duration) (*Issuer, error) {
	seed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("authn: signing key: %w", err)
	}
	return NewIssuer(seed, ttl)
}

func (i *Issuer) TTL() time.Duration { return i.ttl }

// Issue creates a signed access token.
func (i *Issuer) Issue(userID, username, tenant string, roles []string) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		Roles:    roles,
		Tenant:   tenant,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
			ID:        id.New("jti"),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(i.priv)
}

// Verify implements TokenVerifier for locally issued tokens.
func (i *Issuer) Verify(raw string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(raw, claims,
		func(t *jwt.Token) (any, error) { return i.pub, nil },
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !tok.Valid {
		return nil, errx.Wrap(errx.KindUnauthorized, fmt.Errorf("%w", err), "authn: token")
	}
	return claims, nil
}
