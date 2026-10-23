package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// APITokenPrefix marks a personal API token (doc 20 §2). The prefix is what
// lets one Bearer header carry either credential, and what lets a secret
// scanner recognise one committed by mistake.
const APITokenPrefix = "nv_"

// NewAPIToken returns a fresh token and the hash to store. The plain value is
// shown once and never persisted.
func NewAPIToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = APITokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashAPIToken(plain), nil
}

// HashAPIToken is SHA-256, not Argon2: the token is 256 bits of randomness,
// so there is nothing to brute-force, and it is checked on every request.
func HashAPIToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func IsAPIToken(raw string) bool { return strings.HasPrefix(raw, APITokenPrefix) }

// Chain routes a bearer value to the verifier for its kind: API tokens by
// prefix, everything else as a JWT.
type Chain struct {
	JWT TokenVerifier
	API TokenVerifier
}

func (c Chain) Verify(raw string) (*Claims, error) {
	if IsAPIToken(raw) && c.API != nil {
		return c.API.Verify(raw)
	}
	return c.JWT.Verify(raw)
}
