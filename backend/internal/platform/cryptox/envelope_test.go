package cryptox

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func provider(t *testing.T) *EnvMasterKey {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kp, err := NewEnvMasterKey(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func TestRoundTrip(t *testing.T) {
	kp := provider(t)
	secret := []byte(`{"community":"s3cret","version":"v2c"}`)
	env, err := Encrypt(kp, secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(env.Ciphertext, []byte("s3cret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := Decrypt(kp, env)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("round trip mismatch")
	}
}

func TestUniqueDEKs(t *testing.T) {
	kp := provider(t)
	a, _ := Encrypt(kp, []byte("same"))
	b, _ := Encrypt(kp, []byte("same"))
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Error("two encryptions must not share DEK/nonce material")
	}
}

func TestWrongKeyFails(t *testing.T) {
	env, err := Encrypt(provider(t), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(provider(t), env); err == nil {
		t.Fatal("decrypt with different KEK must fail")
	}
}

func TestTamperDetection(t *testing.T) {
	kp := provider(t)
	env, _ := Encrypt(kp, []byte("secret"))
	env.Ciphertext[len(env.Ciphertext)-1] ^= 0xFF
	if _, err := Decrypt(kp, env); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestKeyRotationRewrap(t *testing.T) {
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	_, _ = rand.Read(oldKey)
	_, _ = rand.Read(newKey)

	oldKP, _ := NewEnvMasterKey(map[int][]byte{1: oldKey}, 1)
	env, _ := Encrypt(oldKP, []byte("secret"))

	// Rotation window: both keys loaded, version 2 current (doc 20 §7).
	rotKP, _ := NewEnvMasterKey(map[int][]byte{1: oldKey, 2: newKey}, 2)
	dek, err := rotKP.UnwrapDEK(env.WrappedDEK, env.KeyVersion)
	if err != nil {
		t.Fatalf("unwrap with old version during rotation: %v", err)
	}
	rewrapped, ver, err := rotKP.WrapDEK(dek)
	if err != nil || ver != 2 {
		t.Fatalf("rewrap: ver=%d err=%v", ver, err)
	}
	env.WrappedDEK, env.KeyVersion = rewrapped, ver

	// After retirement of the old key, payload still decrypts (no re-encrypt).
	newKP, _ := NewEnvMasterKey(map[int][]byte{2: newKey}, 2)
	got, err := Decrypt(newKP, env)
	if err != nil || string(got) != "secret" {
		t.Fatalf("post-rotation decrypt: %q err=%v", got, err)
	}
}
