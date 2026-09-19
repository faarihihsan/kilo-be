package auth

import (
	"context"
	"sync"
	"time"

	"workout-tracker-be/internal/clock"
)

const (
	// DefaultRateLimiterMaxKeys is the cap on distinct keys tracked at once.
	DefaultRateLimiterMaxKeys = 10_000
)

// RateLimiterConfig configures NewRateLimiter.
type RateLimiterConfig struct {
	// PerMinute is the sustained rate: one request every minute/PerMinute.
	// Values below 1 are treated as 1.
	PerMinute int
	// Burst is how many requests a key may make at once before it is
	// throttled. Values below 1 are treated as 1.
	Burst int
	// MaxKeys caps the distinct keys tracked (default
	// DefaultRateLimiterMaxKeys).
	MaxKeys int
	// SweepInterval is the cleanup period of Run and of the opportunistic
	// sweep (default DefaultSweepInterval).
	SweepInterval time.Duration
}

// RateLimiter is an in-memory rate limiter with one bucket per key (usually
// an IPKey). It implements the generic cell rate algorithm, a token bucket
// that needs one timestamp per key and no floating point: a key may burst
// Burst requests, after which it is admitted every minute/PerMinute.
//
// Memory is bounded: a key whose bucket has refilled holds no state, so idle
// keys cost nothing once swept, and at most MaxKeys keys are tracked. When the
// table is full of keys still in debt, the one closest to a full bucket is
// evicted (it forgets the least). Cleanup runs opportunistically inside Allow
// every SweepInterval, so no goroutine is required; Run additionally frees
// memory while idle.
//
// State resets on restart; one server instance only.
type RateLimiter struct {
	clk       clock.Clock
	interval  time.Duration // emission interval: minute / PerMinute
	tolerance time.Duration // interval * (Burst-1)
	maxKeys   int
	sweep     time.Duration

	mu        sync.Mutex
	tat       map[string]time.Time // theoretical arrival time per key
	lastSweep time.Time
}

// NewRateLimiter returns a limiter reading time from clk.
func NewRateLimiter(clk clock.Clock, cfg RateLimiterConfig) *RateLimiter {
	perMinute := max(cfg.PerMinute, 1)
	burst := max(cfg.Burst, 1)
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = DefaultRateLimiterMaxKeys
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = DefaultSweepInterval
	}
	interval := time.Minute / time.Duration(perMinute)
	return &RateLimiter{
		clk:       clk,
		interval:  interval,
		tolerance: interval * time.Duration(burst-1),
		maxKeys:   cfg.MaxKeys,
		sweep:     cfg.SweepInterval,
		tat:       make(map[string]time.Time),
		lastSweep: clk.Now(),
	}
}

// Allow admits or throttles one request for key. When it throttles, retryAfter
// is how long until a request would be admitted (the Retry-After). Throttled
// requests are not charged, so hammering a limited key does not extend the
// wait.
func (l *RateLimiter) Allow(key string) (retryAfter time.Duration, ok bool) {
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= l.sweep {
		l.sweepLocked(now)
	}
	tat, tracked := l.tat[key]
	if !tracked && len(l.tat) >= l.maxKeys {
		l.makeRoom(now)
	}
	tat = later(tat, now) // an idle key starts from a full bucket
	if wait := tat.Sub(now) - l.tolerance; wait > 0 {
		return wait, false
	}
	l.tat[key] = tat.Add(l.interval)
	return 0, true
}

// Sweep drops keys whose bucket has refilled completely.
func (l *RateLimiter) Sweep() {
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

// Run sweeps every SweepInterval (real time) until ctx is done. Start it in
// its own goroutine; it is optional, see RateLimiter.
func (l *RateLimiter) Run(ctx context.Context) {
	t := time.NewTicker(l.sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Sweep()
		}
	}
}

func (l *RateLimiter) sweepLocked(now time.Time) {
	for k, tat := range l.tat {
		if !tat.After(now) {
			delete(l.tat, k)
		}
	}
	l.lastSweep = now
}

// makeRoom frees one slot: refilled keys first, else the key closest to a
// full bucket.
func (l *RateLimiter) makeRoom(now time.Time) {
	l.sweepLocked(now)
	if len(l.tat) < l.maxKeys {
		return
	}
	var (
		victim string
		lowest time.Time
		first  = true
	)
	for k, tat := range l.tat {
		if first || tat.Before(lowest) {
			victim, lowest, first = k, tat, false
		}
	}
	delete(l.tat, victim)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
