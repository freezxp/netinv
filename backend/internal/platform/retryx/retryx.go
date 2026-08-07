// Package retryx: uniform retry with exponential backoff + jitter and a
// simple circuit breaker (doc 23 §2). Only errx-transient errors are retried.
package retryx

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type Policy struct {
	MaxAttempts int           // 0 = unlimited
	BaseDelay   time.Duration // first backoff
	MaxDelay    time.Duration // backoff cap
}

// Do runs fn until success, a non-retryable error, attempt exhaustion, or ctx
// cancellation. Sleep durations are jittered ±20%.
func Do(ctx context.Context, p Policy, fn func() error) error {
	delay := p.BaseDelay
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !errx.Retryable(err) {
			return err
		}
		if p.MaxAttempts > 0 && attempt >= p.MaxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(delay)):
		}
		delay = min(delay*2, p.MaxDelay)
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	f := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * f)
}

// Breaker is a minimal circuit breaker: opens after Threshold consecutive
// failures, half-opens after Cooldown.
type Breaker struct {
	Threshold int
	Cooldown  time.Duration

	mu       sync.Mutex
	failures int
	openedAt time.Time
}

// Allow reports whether a call may proceed.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.Threshold {
		return true
	}
	return time.Since(b.openedAt) >= b.Cooldown // half-open probe
}

func (b *Breaker) Record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures == b.Threshold {
		b.openedAt = time.Now()
	} else if b.failures > b.Threshold {
		b.openedAt = time.Now() // failed half-open probe re-opens
	}
}

func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= b.Threshold && time.Since(b.openedAt) < b.Cooldown
}
