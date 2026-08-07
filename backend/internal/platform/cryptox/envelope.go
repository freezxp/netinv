// Package cryptox implements envelope encryption for secret material
// (ADR-011, doc 20 §7): per-secret random DEK encrypts the payload with
// AES-256-GCM; the DEK is itself encrypted (wrapped) with the versioned
// master key (KEK), also via AES-256-GCM. Key rotation re-wraps DEKs only.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

// KeyProvider wraps/unwraps data-encryption keys. EnvMasterKey is the v1
// implementation; a Vault/KMS provider is the designed future seam (doc 17 §5).
type KeyProvider interface {
	WrapDEK(dek []byte) (wrapped []byte, keyVersion int, err error)
	UnwrapDEK(wrapped []byte, keyVersion int) ([]byte, error)
	CurrentVersion() int
}

// Envelope is the sealed form persisted to the database.
type Envelope struct {
	Ciphertext []byte // nonce || AES-256-GCM(payload)
	WrappedDEK []byte // nonce || AES-256-GCM(dek) under the KEK
	KeyVersion int
}

// Encrypt seals plaintext under a fresh DEK.
func Encrypt(kp KeyProvider, plaintext []byte) (*Envelope, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("cryptox: dek: %w", err)
	}
	ct, err := gcmSeal(dek, plaintext)
	if err != nil {
		return nil, err
	}
	wrapped, ver, err := kp.WrapDEK(dek)
	if err != nil {
		return nil, err
	}
	return &Envelope{Ciphertext: ct, WrappedDEK: wrapped, KeyVersion: ver}, nil
}

// Decrypt opens an envelope. Tampering with any part fails authentication.
func Decrypt(kp KeyProvider, e *Envelope) ([]byte, error) {
	dek, err := kp.UnwrapDEK(e.WrappedDEK, e.KeyVersion)
	if err != nil {
		return nil, err
	}
	return gcmOpen(dek, e.Ciphertext)
}

// EnvMasterKey holds KEK versions supplied via environment/Kubernetes Secret.
type EnvMasterKey struct {
	keys    map[int][]byte
	current int
}

// NewEnvMasterKey builds a provider from explicit key material (32 bytes per
// version). current must exist in keys.
func NewEnvMasterKey(keys map[int][]byte, current int) (*EnvMasterKey, error) {
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("cryptox: current key version %d not provided", current)
	}
	for v, k := range keys {
		if len(k) != 32 {
			return nil, fmt.Errorf("cryptox: key version %d is %d bytes, want 32", v, len(k))
		}
	}
	return &EnvMasterKey{keys: keys, current: current}, nil
}

// LoadEnvMasterKey reads NETINV_MASTER_KEY (base64, 32 bytes) as version 1;
// NETINV_MASTER_KEY_PREV, if set, is loaded as version 0 for rotation windows.
func LoadEnvMasterKey() (*EnvMasterKey, error) {
	raw := os.Getenv("NETINV_MASTER_KEY")
	if raw == "" {
		return nil, fmt.Errorf("cryptox: NETINV_MASTER_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("cryptox: NETINV_MASTER_KEY: %w", err)
	}
	keys := map[int][]byte{1: key}
	if prev := os.Getenv("NETINV_MASTER_KEY_PREV"); prev != "" {
		pk, err := base64.StdEncoding.DecodeString(prev)
		if err != nil {
			return nil, fmt.Errorf("cryptox: NETINV_MASTER_KEY_PREV: %w", err)
		}
		keys[0] = pk
	}
	return NewEnvMasterKey(keys, 1)
}

func (m *EnvMasterKey) CurrentVersion() int { return m.current }

func (m *EnvMasterKey) WrapDEK(dek []byte) ([]byte, int, error) {
	wrapped, err := gcmSeal(m.keys[m.current], dek)
	return wrapped, m.current, err
}

func (m *EnvMasterKey) UnwrapDEK(wrapped []byte, keyVersion int) ([]byte, error) {
	key, ok := m.keys[keyVersion]
	if !ok {
		return nil, fmt.Errorf("cryptox: unknown key version %d", keyVersion)
	}
	return gcmOpen(key, wrapped)
}

func gcmSeal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func gcmOpen(key, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("cryptox: sealed data too short")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("cryptox: authentication failed: %w", err)
	}
	return pt, nil
}
