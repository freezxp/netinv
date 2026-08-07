package authn

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

var fast = Argon2Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1}

func TestPasswordRoundTrip(t *testing.T) {
	phc, err := HashPassword("correct horse battery staple", fast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$") {
		t.Fatalf("not PHC format: %s", phc)
	}
	ok, err := VerifyPassword("correct horse battery staple", phc)
	if err != nil || !ok {
		t.Fatalf("verify ok=%v err=%v", ok, err)
	}
	ok, _ = VerifyPassword("wrong", phc)
	if ok {
		t.Fatal("wrong password verified")
	}
}

func TestHashUniqueness(t *testing.T) {
	a, _ := HashPassword("pw", fast)
	b, _ := HashPassword("pw", fast)
	if a == b {
		t.Fatal("salts must differ")
	}
}

func newIssuer(t *testing.T, ttl time.Duration) *Issuer {
	t.Helper()
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	iss, err := NewIssuer(seed, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

func TestJWTRoundTrip(t *testing.T) {
	iss := newIssuer(t, time.Minute)
	raw, err := iss.Issue("u_1", "nadia", "t_default", []string{"operator"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := iss.Verify(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "u_1" || claims.Username != "nadia" ||
		len(claims.Roles) != 1 || claims.Roles[0] != "operator" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestJWTExpired(t *testing.T) {
	iss := newIssuer(t, -time.Minute)
	raw, _ := iss.Issue("u_1", "n", "t", nil)
	if _, err := iss.Verify(raw); err == nil {
		t.Fatal("expired token verified")
	}
}

func TestJWTWrongKey(t *testing.T) {
	raw, _ := newIssuer(t, time.Minute).Issue("u_1", "n", "t", nil)
	if _, err := newIssuer(t, time.Minute).Verify(raw); err == nil {
		t.Fatal("token verified with wrong key")
	}
}

func TestJWTTampered(t *testing.T) {
	iss := newIssuer(t, time.Minute)
	raw, _ := iss.Issue("u_1", "n", "t", nil)
	if _, err := iss.Verify(raw[:len(raw)-3] + "abc"); err == nil {
		t.Fatal("tampered token verified")
	}
}
