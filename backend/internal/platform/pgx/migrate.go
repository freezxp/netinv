package pgx

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/freezxp/netinv/backend/migrations"
)

const migrationLockKey = 0x6e657469 // "neti" — cross-replica migration mutex

// Migrate applies embedded migrations (goose), serialized via advisory lock so
// multiple API replicas can race safely at boot (doc 19 §4, NFR-51).
func Migrate(ctx context.Context, dsn string, log *slog.Logger) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// MigrateDown rolls back one migration — used by tests and break-glass ops.
func MigrateDown(ctx context.Context, dsn string, log *slog.Logger) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.DownContext(ctx, db, ".")
}

// MigrateDownTo rolls back to the given version (0 = empty schema). Tests use
// it to prove every Down migration works, not just the latest.
func MigrateDownTo(ctx context.Context, dsn string, version int64, log *slog.Logger) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.DownToContext(ctx, db, ".", version)
}

type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info(fmt.Sprintf("goose: "+format, v...))
}
func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error(fmt.Sprintf("goose: "+format, v...))
}

var _ embed.FS // keep embed import intent obvious
