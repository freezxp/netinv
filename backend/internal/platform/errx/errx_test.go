package errx

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindOf(t *testing.T) {
	base := New(KindTransient, "vm write timeout")
	wrapped := fmt.Errorf("ingest batch 42: %w", base)
	if KindOf(wrapped) != KindTransient {
		t.Errorf("KindOf(wrapped) = %v, want transient", KindOf(wrapped))
	}
	if !Retryable(wrapped) {
		t.Error("wrapped transient should be retryable")
	}
}

func TestInnermostKindWins(t *testing.T) {
	origin := New(KindInvalid, "bad interval")
	rewrapped := Wrap(KindInternal, origin, "handler")
	if got := KindOf(rewrapped); got != KindInvalid {
		t.Errorf("KindOf = %v, want invalid (origin wins)", got)
	}
}

func TestWrapNil(t *testing.T) {
	if Wrap(KindTransient, nil, "x") != nil {
		t.Error("Wrap(nil) must be nil")
	}
}

func TestUnclassified(t *testing.T) {
	err := errors.New("plain")
	if KindOf(err) != KindUnknown {
		t.Errorf("plain error kind = %v, want unknown", KindOf(err))
	}
	if Retryable(err) {
		t.Error("unclassified errors are not retryable")
	}
}
