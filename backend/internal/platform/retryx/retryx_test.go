package retryx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

func TestDoRetriesTransient(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return errx.New(errx.KindTransient, "flaky")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err=%v calls=%d, want nil/3", err, calls)
	}
}

func TestDoStopsOnPermanent(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		calls++
		return errx.New(errx.KindInvalid, "bad input")
	})
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on invalid)", calls)
	}
	if errx.KindOf(err) != errx.KindInvalid {
		t.Errorf("kind = %v", errx.KindOf(err))
	}
}

func TestDoExhausts(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		calls++
		return errx.New(errx.KindTransient, "always down")
	})
	if calls != 3 || err == nil {
		t.Errorf("calls=%d err=%v, want 3 attempts then error", calls, err)
	}
}

func TestDoContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Do(ctx, Policy{BaseDelay: time.Hour, MaxDelay: time.Hour}, func() error {
		return errx.New(errx.KindTransient, "x")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestBreaker(t *testing.T) {
	b := &Breaker{Threshold: 2, Cooldown: 50 * time.Millisecond}
	fail := errors.New("dep down")
	if !b.Allow() {
		t.Fatal("closed breaker must allow")
	}
	b.Record(fail)
	b.Record(fail)
	if b.Allow() {
		t.Fatal("open breaker must block")
	}
	time.Sleep(60 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("half-open breaker must allow a probe")
	}
	b.Record(nil)
	if !b.Allow() || b.Open() {
		t.Fatal("successful probe must close breaker")
	}
}
