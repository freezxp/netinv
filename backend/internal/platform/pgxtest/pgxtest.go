// Package pgxtest gives integration tests a database of their own.
//
// It exists because they were run against whatever NETINV_TEST_PG_DSN pointed
// at — in practice a live deployment — where they created credentials and
// devices in the operator's inventory and left audit rows behind. Audit is
// append-only by design, so those entries cannot be tidied up afterwards: the
// only fix is not to write them there in the first place.
//
// Depends on `testing`, like net/http/httptest, and is imported only by tests.
package pgxtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// Throwaway creates a fresh database for one test, applies every migration,
// and drops it afterwards. It skips the test when NETINV_TEST_PG_DSN is unset,
// so unit-only runs stay green without a database.
//
// The DSN it returns points at the new database, never at the one the caller
// supplied — pass it to anything that needs its own connection.
func Throwaway(t *testing.T) (dsn string, pool *pgxpool.Pool) {
	t.Helper()
	baseDSN := os.Getenv("NETINV_TEST_PG_DSN")
	if baseDSN == "" {
		t.Skip("NETINV_TEST_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	admin, err := pgxp.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatalf("pgxtest: connect to %s: %v", redact(baseDSN), err)
	}
	name := fmt.Sprintf("netinv_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("pgxtest: create %s: %v", name, err)
	}
	admin.Close()

	// Registered before the pool is opened, so cleanup order (LIFO) closes the
	// pool first and the DROP is not blocked by its own connections.
	t.Cleanup(func() {
		a, err := pgxp.Connect(context.Background(), baseDSN)
		if err != nil {
			t.Errorf("pgxtest: could not drop %s: %v", name, err)
			return
		}
		defer a.Close()
		if _, err := a.Exec(context.Background(),
			"DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("pgxtest: could not drop %s: %v", name, err)
		}
	})

	dsn = SwapDBName(baseDSN, name)
	if err := pgxp.Migrate(ctx, dsn, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("pgxtest: migrate %s: %v", name, err)
	}
	pool, err = pgxp.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxtest: connect to %s: %v", name, err)
	}
	t.Cleanup(pool.Close)
	return dsn, pool
}

// SwapDBName rewrites the database component of a DSN, keeping any query
// string intact.
func SwapDBName(dsn, name string) string {
	i := strings.LastIndex(dsn, "/")
	if i < 0 {
		return dsn
	}
	rest := dsn[i+1:]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		return dsn[:i+1] + name + rest[q:]
	}
	return dsn[:i+1] + name
}

// redact strips credentials from a DSN before it reaches a failure message.
func redact(dsn string) string {
	if at := strings.LastIndex(dsn, "@"); at >= 0 {
		if scheme := strings.Index(dsn, "://"); scheme >= 0 && scheme+3 < at {
			return dsn[:scheme+3] + "…@" + dsn[at+1:]
		}
	}
	return dsn
}
