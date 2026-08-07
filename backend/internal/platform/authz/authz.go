// Package authz — resource:action permission checking (doc 20 §5, FR-RBAC).
// Roles come from JWT claims; role→permission sets load from iam.roles and
// refresh periodically, so role edits apply without re-login (≤ refresh lag).
package authz

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Permission constants — the single vocabulary shared by routes and seeds.
const (
	DevicesRead      = "devices:read"
	DevicesWrite     = "devices:write"
	DevicesAdmin     = "devices:admin"
	MetricsRead      = "metrics:read"
	MapsRead         = "maps:read"
	MapsWrite        = "maps:write"
	AlertsRead       = "alerts:read"
	AlertsAck        = "alerts:ack"
	AlertsAdmin      = "alerts:admin"
	CredentialsRead  = "credentials:read"
	CredentialsWrite = "credentials:write"
	PlatformRead     = "platform:read"
	PlatformWrite    = "platform:write"
	UsersRead        = "users:read"
	UsersWrite       = "users:write"
	SettingsWrite    = "settings:write"
	AuditRead        = "audit:read"
	ExportsRun       = "exports:run"
)

// Checker answers "may these roles do perm?". Fail-closed on unknown roles.
type Checker interface {
	Has(roles []string, perm string) bool
}

// PGChecker caches role→permissions from iam.roles.
type PGChecker struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Refresh time.Duration

	mu    sync.RWMutex
	perms map[string]map[string]bool // role name → permission set ("*" = all)
}

func NewPGChecker(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*PGChecker, error) {
	c := &PGChecker{Pool: pool, Log: log, Refresh: time.Minute}
	if err := c.load(ctx); err != nil {
		return nil, err
	}
	go c.refreshLoop(ctx)
	return c, nil
}

func (c *PGChecker) load(ctx context.Context) error {
	rows, err := c.Pool.Query(ctx, `SELECT name, permissions FROM iam.roles`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := map[string]map[string]bool{}
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return err
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
		set := map[string]bool{}
		for _, p := range list {
			set[p] = true
		}
		next[name] = set
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.perms = next
	c.mu.Unlock()
	return nil
}

func (c *PGChecker) refreshLoop(ctx context.Context) {
	t := time.NewTicker(c.Refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.load(ctx); err != nil {
				c.Log.Warn("authz refresh failed", "err", err)
			}
		}
	}
}

func (c *PGChecker) Has(roles []string, perm string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range roles {
		set, ok := c.perms[r]
		if !ok {
			continue
		}
		if set["*"] || set[perm] {
			return true
		}
	}
	return false
}

// Static is a fixed-map Checker for tests.
type Static map[string][]string

func (s Static) Has(roles []string, perm string) bool {
	for _, r := range roles {
		for _, p := range s[r] {
			if p == "*" || p == perm {
				return true
			}
		}
	}
	return false
}
