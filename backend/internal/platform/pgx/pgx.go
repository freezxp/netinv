// Package pgx provides the PostgreSQL pool and transaction helper (doc 13).
package pgx

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Connect opens a pool and verifies connectivity, dependency-patient
// (doc 23 §7): retries ping until ctx deadline/cancel.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errx.Wrap(errx.KindInvalid, err, "pgx: parse dsn")
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errx.Wrap(errx.KindInternal, err, "pgx: new pool")
	}
	for {
		if err := pool.Ping(ctx); err == nil {
			return pool, nil
		} else if ctx.Err() != nil {
			pool.Close()
			return nil, errx.Wrap(errx.KindTransient, err, "pgx: ping")
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, errx.Wrap(errx.KindTransient, ctx.Err(), "pgx: waiting for database")
		case <-time.After(time.Second):
		}
	}
}

// InTx runs fn in a transaction, committing on nil and rolling back otherwise.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "pgx: begin")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errx.Wrap(errx.KindTransient, err, "pgx: commit")
	}
	return nil
}

// AdvisoryLock takes a session advisory lock on the given key, used to
// single-run migrations across replicas (doc 19 §4).
func AdvisoryLock(ctx context.Context, conn *pgxpool.Conn, key int64) error {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("pgx: advisory lock: %w", err)
	}
	return nil
}
