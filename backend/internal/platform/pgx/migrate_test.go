package pgx

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Integration test — runs when NETINV_TEST_PG_DSN points at a disposable
// database (the compose stack provides one; CI uses testcontainers later).
func TestMigrateUpDownUp(t *testing.T) {
	dsn := os.Getenv("NETINV_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("NETINV_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log := slog.New(slog.DiscardHandler)

	if err := Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("up: %v", err)
	}

	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM iam.roles WHERE is_builtin").Scan(&n); err != nil {
		t.Fatalf("query roles: %v", err)
	}
	if n != 4 {
		t.Errorf("builtin roles = %d, want 4", n)
	}
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM alerting.alert_rules WHERE is_builtin").Scan(&n); err != nil {
		t.Fatalf("query rules: %v", err)
	}
	if n < 8 {
		t.Errorf("builtin alert rules = %d, want >= 8", n)
	}

	// Roll back the seed migration and reapply — both directions must work.
	if err := MigrateDown(ctx, dsn, log); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM iam.roles").Scan(&n); err != nil {
		t.Fatalf("query after down: %v", err)
	}
	if n != 0 {
		t.Errorf("roles after down = %d, want 0", n)
	}
	if err := Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("re-up: %v", err)
	}
}
