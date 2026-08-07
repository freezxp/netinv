// Package redisx wraps Redis for caching, distributed locks, and leader
// leases (doc 05 §3). Locks use SET NX PX with token-checked release.
package redisx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

func Connect(ctx context.Context, addr string) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{Addr: addr})
	for {
		if err := c.Ping(ctx).Err(); err == nil {
			return c, nil
		} else if ctx.Err() != nil {
			_ = c.Close()
			return nil, errx.Wrap(errx.KindTransient, err, "redisx: waiting for redis")
		}
		select {
		case <-ctx.Done():
			_ = c.Close()
			return nil, errx.Wrap(errx.KindTransient, ctx.Err(), "redisx: connect")
		case <-time.After(time.Second):
		}
	}
}

// releaseScript deletes the lock only if the caller still owns it.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0`)

// Lock is a single-holder distributed lock (FR-SYNC-06, doc 05 §9).
type Lock struct {
	client *redis.Client
	key    string
	token  string
}

// TryLock attempts to acquire key for ttl. Returns nil, false when held
// elsewhere.
func TryLock(ctx context.Context, client *redis.Client, key string, ttl time.Duration) (*Lock, bool, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, false, err
	}
	token := hex.EncodeToString(buf)
	ok, err := client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, errx.Wrap(errx.KindTransient, err, "redisx: setnx")
	}
	if !ok {
		return nil, false, nil
	}
	return &Lock{client: client, key: key, token: token}, true, nil
}

// Release frees the lock if still owned; releasing an expired/stolen lock is
// a no-op rather than an error.
func (l *Lock) Release(ctx context.Context) error {
	return releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Err()
}

// Extend renews the ttl if still owned.
func (l *Lock) Extend(ctx context.Context, ttl time.Duration) (bool, error) {
	extendScript := redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`)
	n, err := extendScript.Run(ctx, l.client, []string{l.key}, l.token, ttl.Milliseconds()).Int()
	return n == 1, err
}
