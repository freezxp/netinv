package pgx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	assertFlowStalenessRules(ctx, t, pool)

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

// assertFlowStalenessRules guards the two flow rules from migration 0012, whose
// expressions are load-bearing in ways a tidy-up would not notice (doc 34 §5.1).
//
// The gate on ar_flow_absent is the one worth a test. Bare
// `absent_over_time(netinv_flow_bytes[20m])` returns 1 for a metric that has
// *never* existed, so without the `count(last_over_time(...))` clause the rule
// fires on every deployment that never configured flow — and flow is optional,
// so that is most of them. The symptom would be a permanently firing built-in
// alert on a healthy install, which reads as a NetInv bug rather than a rule bug.
//
// ar_flow_exporter_stale must aggregate by exporter: the whole reason it exists
// is that one exporter can stop while others continue, and an expression that
// lost `by (exporter)` would collapse to a fleet-wide check that stays green
// through exactly the 42-hour partial outage that prompted these rules.
func assertFlowStalenessRules(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	type rule struct {
		expr    string
		enabled bool
	}
	rules := map[string]rule{}
	rows, err := pool.Query(ctx,
		`SELECT id, expr, enabled FROM alerting.alert_rules
		   WHERE id IN ('ar_flow_absent','ar_flow_exporter_stale') AND is_builtin`)
	if err != nil {
		t.Fatalf("query flow rules: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, expr string
		var enabled bool
		if err := rows.Scan(&id, &expr, &enabled); err != nil {
			t.Fatalf("scan flow rule: %v", err)
		}
		rules[id] = rule{expr, enabled}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate flow rules: %v", err)
	}

	for _, id := range []string{"ar_flow_absent", "ar_flow_exporter_stale"} {
		r, ok := rules[id]
		if !ok {
			t.Errorf("builtin rule %s missing", id)
			continue
		}
		if !r.enabled {
			t.Errorf("%s is disabled; it is meant to ship enabled", id)
		}
	}
	if r, ok := rules["ar_flow_absent"]; ok &&
		!strings.Contains(r.expr, "last_over_time") {
		t.Errorf("ar_flow_absent lost its seen-recently gate — it will now fire "+
			"on every deployment that never configured flow; expr = %q", r.expr)
	}
	if r, ok := rules["ar_flow_exporter_stale"]; ok &&
		!strings.Contains(r.expr, "by (exporter)") {
		t.Errorf("ar_flow_exporter_stale no longer aggregates by exporter, so it "+
			"cannot detect one exporter stopping while others continue; expr = %q",
			r.expr)
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
