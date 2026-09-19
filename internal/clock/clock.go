// Package clock is the time source used by services and stores so that expiry,
// rate limiting and the sync conflict rule can be tested deterministically.
package clock

import (
	"sync"
	"time"
)

// Clock returns the current time. Implementations return UTC.
type Clock interface {
	Now() time.Time
}

// Real is the production clock.
type Real struct{}

// Now returns the current wall-clock time in UTC (no monotonic reading).
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a manually controlled clock for tests. It is safe for concurrent use.
type Fake struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFake returns a Fake set to t (normalised to UTC).
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

// Set moves the fake to t (normalised to UTC). It may move time backwards.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}

// Advance moves the fake forward by d. A negative d moves it backwards.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// TruncateMicro drops sub-microsecond precision, which is all PostgreSQL
// timestamptz stores. Apply it to any time that is compared with a value read
// back from the database (for example the conflict rule's stored updated_at).
func TruncateMicro(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

var (
	_ Clock = Real{}
	_ Clock = (*Fake)(nil)
)
