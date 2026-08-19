// Package settings reads and writes operator configuration held in
// config.settings — values that belong to the deployment rather than to a
// device, and that an operator should be able to change without editing a file
// and restarting a service.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// MirrorKey is the settings row holding the metrics-copy configuration.
const MirrorKey = "metrics.mirror"

// Mirror is where collected metrics are copied, in addition to the primary
// store. Enabled is separate from the list so an operator can switch copying
// off during maintenance without losing the addresses they configured.
type Mirror struct {
	Enabled bool     `json:"enabled"`
	URLs    []string `json:"urls"`
}

// Targets returns the addresses to write to, honouring Enabled.
func (m Mirror) Targets() []string {
	if !m.Enabled {
		return nil
	}
	return m.URLs
}

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) GetMirror(ctx context.Context) (Mirror, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT value FROM config.settings WHERE key = $1`, MirrorKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		// Absent means "never configured", which is off — not an error. A
		// deployment that has never set this must not log a failure a minute
		// forever.
		return Mirror{URLs: []string{}}, nil
	}
	if err != nil {
		return Mirror{}, errx.Wrap(errx.KindTransient, err, "read mirror setting")
	}
	var m Mirror
	if err := json.Unmarshal(raw, &m); err != nil {
		return Mirror{}, errx.Wrap(errx.KindInternal, err, "decode mirror setting")
	}
	if m.URLs == nil {
		m.URLs = []string{}
	}
	return m, nil
}

// PutMirror validates and stores the configuration.
func (s *Store) PutMirror(ctx context.Context, m Mirror, actor string) (Mirror, error) {
	clean := make([]string, 0, len(m.URLs))
	seen := map[string]bool{}
	for _, raw := range m.URLs {
		u := strings.TrimRight(strings.TrimSpace(raw), "/")
		if u == "" {
			continue
		}
		if err := validateURL(u); err != nil {
			return Mirror{}, err
		}
		if seen[u] {
			// Writing the same batch twice to one instance is not a second
			// copy; it is the same copy and twice the work.
			continue
		}
		seen[u] = true
		clean = append(clean, u)
	}
	if m.Enabled && len(clean) == 0 {
		return Mirror{}, errx.New(errx.KindInvalid,
			"copying is enabled but no destination is set")
	}
	m.URLs = clean
	raw, err := json.Marshal(m)
	if err != nil {
		return Mirror{}, errx.Wrap(errx.KindInternal, err, "encode mirror setting")
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO config.settings (key, tenant_id, value, value_schema, updated_by, updated_at)
		VALUES ($1, 't_default', $2, 'netinv.metrics.mirror/1', nullif($3,''), now())
		ON CONFLICT (key) DO UPDATE SET
			value = excluded.value, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`, MirrorKey, raw, actor); err != nil {
		return Mirror{}, errx.Wrap(errx.KindTransient, err, "write mirror setting")
	}
	return m, nil
}

// validateURL rejects what would silently never work. A destination stored as
// "vm-backup:8428" looks right in a text box and produces a request to a
// relative path that fails on every batch.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errx.New(errx.KindInvalid, "%q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errx.New(errx.KindInvalid,
			"%q needs an http:// or https:// scheme", raw)
	}
	if u.Host == "" {
		return errx.New(errx.KindInvalid, "%q has no host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		// The writer appends /api/v1/import; a path here produces a URL nobody
		// intended and a 404 per batch.
		return errx.New(errx.KindInvalid,
			"%q should be the base address only — the import path is added for you", raw)
	}
	return nil
}

// Cache holds the last read value so a hot write path can consult the setting
// without a database round trip per batch, and keeps serving the last known
// good value if the database is briefly unreachable — losing the mirror
// because a query timed out would be a worse outcome than a stale list.
type Cache struct {
	store   *Store
	refresh time.Duration

	mu   sync.RWMutex
	val  Mirror
	last time.Time
}

func NewCache(store *Store, refresh time.Duration) *Cache {
	if refresh <= 0 {
		refresh = 30 * time.Second
	}
	return &Cache{store: store, refresh: refresh}
}

// Targets is safe to call per batch.
func (c *Cache) Targets(ctx context.Context) []string {
	c.mu.RLock()
	fresh := time.Since(c.last) < c.refresh
	val := c.val
	c.mu.RUnlock()
	if fresh {
		return val.Targets()
	}
	m, err := c.store.GetMirror(ctx)
	if err != nil {
		// Keep the previous value rather than dropping the mirror on a blip.
		c.mu.Lock()
		c.last = time.Now()
		c.mu.Unlock()
		return val.Targets()
	}
	c.mu.Lock()
	c.val, c.last = m, time.Now()
	c.mu.Unlock()
	return m.Targets()
}
