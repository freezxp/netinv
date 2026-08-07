// Package errx implements the error taxonomy of doc 23 §1. Classify once at
// origin; wrap with context everywhere else; never reclassify upward.
package errx

import (
	"errors"
	"fmt"
)

type Kind int

const (
	KindUnknown Kind = iota
	KindInvalid
	KindUnauthorized
	KindForbidden
	KindNotFound
	KindConflict
	KindTransient
	KindInternal
)

func (k Kind) String() string {
	switch k {
	case KindInvalid:
		return "invalid"
	case KindUnauthorized:
		return "unauthorized"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindTransient:
		return "transient"
	case KindInternal:
		return "internal"
	default:
		return "unknown"
	}
}

type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

// New creates a classified error.
func New(kind Kind, format string, args ...any) error {
	return &kindError{kind: kind, err: fmt.Errorf(format, args...)}
}

// Wrap classifies an existing error, preserving the chain. Wrapping nil
// returns nil so call sites can wrap unconditionally.
func Wrap(kind Kind, err error, msg string) error {
	if err == nil {
		return nil
	}
	return &kindError{kind: kind, err: fmt.Errorf("%s: %w", msg, err)}
}

// KindOf walks the chain and returns the innermost classification — the
// origin's verdict wins over any accidental re-wrap (doc 23 §1 rule).
func KindOf(err error) Kind {
	kind := KindUnknown
	for err != nil {
		var ke *kindError
		if errors.As(err, &ke) {
			kind = ke.kind
			err = errors.Unwrap(ke)
			continue
		}
		err = errors.Unwrap(err)
	}
	return kind
}

// Retryable reports whether the error is worth retrying (doc 23 §2).
func Retryable(err error) bool { return KindOf(err) == KindTransient }
