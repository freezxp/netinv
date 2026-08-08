package pgx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// Integration test — runs when NETINV_TEST_PG_DSN points at a PostgreSQL
// instance. Because it drops the whole schema down to zero, it MUST NOT share
// a database with other integration tests (they run in parallel packages
// against the same instance in CI), so it creates and drops its own throwaway
// database.
func TestMigrateUpDownUp(t *testing.T) {
	baseDSN := os.Getenv("NETINV_TEST_PG_DSN")
	if baseDSN == "" {
		t.Skip("NETINV_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log := slog.New(slog.DiscardHandler)

	// Isolate in a per-run database.
	admin, err := Connect(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	dbName := fmt.Sprintf("netinv_migtest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close()
		t.Fatalf("create test db: %v", err)
	}
	admin.Close()
	dsn := swapDBName(baseDSN, dbName)
	t.Cleanup(func() {
		a, err := Connect(context.Background(), baseDSN)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(context.Background(),
			"DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	})

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

	// Roll all the way down and back up — every Down must work, and the
	// schema must be fully reconstructable (NFR-51).
	if err := MigrateDownTo(ctx, dsn, 0, log); err != nil {
		t.Fatalf("down-to-zero: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'iam'").Scan(&n); err != nil {
		t.Fatalf("query after down: %v", err)
	}
	if n != 0 {
		t.Errorf("iam schema still present after full down")
	}
	if err := Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM iam.roles WHERE is_builtin").Scan(&n); err != nil || n != 4 {
		t.Fatalf("roles after re-up = %d err=%v, want 4", n, err)
	}
}

// swapDBName replaces the database path in a postgres URL DSN.
func swapDBName(dsn, name string) string {
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		rest := dsn[i+1:]
		if q := strings.IndexByte(rest, '?'); q >= 0 {
			return dsn[:i+1] + name + rest[q:]
		}
		return dsn[:i+1] + name
	}
	return dsn
}
