// Package leader — Redis-lease leader election for singleton loops
// (doc 05 §9): acquire with TTL, extend while alive, natural failover on
// expiry. Fencing comes from the lock token.
package leader

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/freezxp/netinv/backend/internal/platform/redisx"
)

type Lease struct {
	Client *redis.Client
	Key    string        // e.g. "leader:scheduler"
	TTL    time.Duration // default 15s
	Log    *slog.Logger

	lock *redisx.Lock
}

func (l *Lease) ttl() time.Duration {
	if l.TTL == 0 {
		return 15 * time.Second
	}
	return l.TTL
}

// TryAcquire implements app.Leader: renews when holding, acquires when not.
func (l *Lease) TryAcquire(ctx context.Context) bool {
	if l.lock != nil {
		ok, err := l.lock.Extend(ctx, l.ttl())
		if err == nil && ok {
			return true
		}
		l.Log.Warn("leadership lost", "key", l.Key, "err", err)
		l.lock = nil
	}
	lock, ok, err := redisx.TryLock(ctx, l.Client, l.Key, l.ttl())
	if err != nil {
		l.Log.Warn("leader acquire failed", "key", l.Key, "err", err)
		return false
	}
	if !ok {
		return false
	}
	l.Log.Info("leadership acquired", "key", l.Key)
	l.lock = lock
	return true
}

// Release gives up the lease (graceful shutdown).
func (l *Lease) Release(ctx context.Context) {
	if l.lock != nil {
		_ = l.lock.Release(ctx)
		l.lock = nil
	}
}
