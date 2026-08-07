package authz

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// TestStaticChecker covers the pure checking logic.
func TestStaticChecker(t *testing.T) {
	c := Static{
		"admin":    {"*"},
		"operator": {DevicesRead, DevicesWrite, AlertsAck},
	}
	if !c.Has([]string{"admin"}, UsersWrite) {
		t.Error("admin wildcard must grant everything")
	}
	if !c.Has([]string{"operator"}, DevicesWrite) {
		t.Error("operator must have devices:write")
	}
	if c.Has([]string{"operator"}, UsersWrite) {
		t.Error("operator must not have users:write")
	}
	if c.Has([]string{"ghost-role"}, DevicesRead) {
		t.Error("unknown roles must fail closed")
	}
	if c.Has(nil, DevicesRead) {
		t.Error("no roles must fail closed")
	}
}

// TestSeededMatrix verifies the seeded builtin roles against the doc 20 §5
// permission matrix, using the real PGChecker against a migrated database.
func TestSeededMatrix(t *testing.T) {
	dsn := os.Getenv("NETINV_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("NETINV_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := slog.New(slog.DiscardHandler)
	if err := pgx.Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	checker, err := NewPGChecker(ctx, pool, log)
	if err != nil {
		t.Fatal(err)
	}

	// The doc 20 §5 matrix, verbatim: permission → allowed roles.
	matrix := map[string][]string{
		DevicesRead:      {"admin", "operator", "readonly", "auditor"},
		MetricsRead:      {"admin", "operator", "readonly", "auditor"},
		MapsRead:         {"admin", "operator", "readonly", "auditor"},
		AlertsRead:       {"admin", "operator", "readonly", "auditor"},
		PlatformRead:     {"admin", "operator", "readonly", "auditor"},
		ExportsRun:       {"admin", "operator", "readonly", "auditor"},
		AlertsAck:        {"admin", "operator"},
		MapsWrite:        {"admin", "operator"},
		DevicesWrite:     {"admin", "operator"},
		AlertsAdmin:      {"admin", "operator"},
		DevicesAdmin:     {"admin"},
		CredentialsRead:  {"admin"},
		CredentialsWrite: {"admin"},
		PlatformWrite:    {"admin"},
		UsersRead:        {"admin"},
		UsersWrite:       {"admin"},
		SettingsWrite:    {"admin"},
		AuditRead:        {"admin", "auditor"},
	}
	all := []string{"admin", "operator", "readonly", "auditor"}
	for perm, allowed := range matrix {
		allowedSet := map[string]bool{}
		for _, r := range allowed {
			allowedSet[r] = true
		}
		for _, role := range all {
			got := checker.Has([]string{role}, perm)
			if got != allowedSet[role] {
				t.Errorf("role %s × %s = %v, want %v (doc 20 §5)", role, perm, got, allowedSet[role])
			}
		}
	}
}
