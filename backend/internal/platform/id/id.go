// Package id generates prefixed ULIDs (doc 08 conventions: d_, if_, al_, …).
package id

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns a prefixed ULID, e.g. New("d") → "d_01J9…".
func New(prefix string) string {
	mu.Lock()
	u := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	mu.Unlock()
	return prefix + "_" + u.String()
}
