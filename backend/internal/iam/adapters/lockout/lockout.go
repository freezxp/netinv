// Package lockout — failed-login throttling (FR-AUTH-04): 5 failures locks
// the account key for 15 minutes. Redis-backed in deployments; in-memory
// fallback for tests and DB-less skeleton mode.
package lockout

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	Threshold = 5
	Window    = 15 * time.Minute
)

type Redis struct{ Client *redis.Client }

func key(k string) string { return "lockout:" + k }

func (l *Redis) Locked(ctx context.Context, k string) (bool, error) {
	n, err := l.Client.Get(ctx, key(k)).Int()
	if err == redis.Nil {
		return false, nil
	}
	return n >= Threshold, err
}

func (l *Redis) RecordFailure(ctx context.Context, k string) (bool, error) {
	pipe := l.Client.TxPipeline()
	incr := pipe.Incr(ctx, key(k))
	pipe.Expire(ctx, key(k), Window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return incr.Val() == Threshold, nil
}

func (l *Redis) Reset(ctx context.Context, k string) error {
	return l.Client.Del(ctx, key(k)).Err()
}

// Memory implements the same contract without Redis.
type Memory struct {
	mu      sync.Mutex
	entries map[string]*entry
	Now     func() time.Time
}

type entry struct {
	count int
	until time.Time
}

func NewMemory() *Memory {
	return &Memory{entries: map[string]*entry{}, Now: time.Now}
}

func (m *Memory) get(k string) *entry {
	e, ok := m.entries[k]
	if !ok || m.Now().After(e.until) {
		e = &entry{until: m.Now().Add(Window)}
		m.entries[k] = e
	}
	return e
}

func (m *Memory) Locked(_ context.Context, k string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[k]
	return ok && m.Now().Before(e.until) && e.count >= Threshold, nil
}

func (m *Memory) RecordFailure(_ context.Context, k string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.get(k)
	e.count++
	e.until = m.Now().Add(Window)
	return e.count == Threshold, nil
}

func (m *Memory) Reset(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, k)
	return nil
}
