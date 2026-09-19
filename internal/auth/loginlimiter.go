package auth

import (
	"context"
	"sync"
	"time"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
)

const (
	// DefaultLoginWindow is the sliding window failures are counted in
	// (docs/api/endpoints/02-login.md, brute-force protection).
	DefaultLoginWindow = 15 * time.Minute
	// DefaultLoginLock is the lock duration when LoginLimiterConfig.Lock is 0.
	DefaultLoginLock = 15 * time.Minute
	// DefaultLoginMaxKeys is the cap on distinct usernames, and separately on
	// distinct IPs, tracked at once.
	DefaultLoginMaxKeys = 10_000
	// DefaultSweepInterval is how often expired entries are dropped.
	DefaultSweepInterval = 5 * time.Minute
)

// LoginLimiterConfig configures NewLoginLimiter. MaxFailsUser, MaxFailsIP and
// Lock come from config.Config (LoginMaxFailsUser, LoginMaxFailsIP,
// LoginLock); the rest have defaults.
type LoginLimiterConfig struct {
	// MaxFailsUser is how many failed logins for one username within Window
	// lock that username; MaxFailsIP the same for one client IP. Values below
	// 1 are treated as 1.
	MaxFailsUser int
	MaxFailsIP   int
	// Lock is how long a tripped key stays locked (default DefaultLoginLock).
	Lock time.Duration
	// Window is the sliding failure window (default DefaultLoginWindow).
	Window time.Duration
	// MaxKeys caps the distinct usernames and, separately, IPs tracked
	// (default DefaultLoginMaxKeys). See LoginLimiter for what happens when full.
	MaxKeys int
	// SweepInterval is the cleanup period of Run and of the opportunistic
	// sweep (default DefaultSweepInterval).
	SweepInterval time.Duration
}

// LoginLimiter is the in-memory brute-force protection of the login endpoint,
// following docs/api/endpoints/02-login.md:
//
//   - Failed logins are counted per username and per client IP in a sliding
//     Window. The failure that reaches MaxFailsUser (MaxFailsIP) locks that
//     key for Lock. Unknown usernames are counted like known ones, so lockouts
//     reveal nothing about which usernames exist.
//   - While a key is locked Check reports it, even for a correct password.
//     Callers must not verify the password then and must not record a failure
//     for a blocked attempt (it would only be noise).
//   - A success clears the username's counter (RecordSuccess) but not the
//     IP's: otherwise an attacker holding one valid account could reset the IP
//     counter between guesses.
//   - The lock ends after Lock. Failures stay in the window after that (when
//     Lock is shorter than Window), so the first new failure after an early
//     unlock locks again.
//
// Limits and known trade-offs:
//
//   - State lives in memory and resets on restart; one server instance only.
//   - Memory is bounded: at most MaxKeys usernames and MaxKeys IPs, at most
//     MaxFails timestamps each, keys longer than 64 bytes are hashed, IPv6
//     addresses are grouped by /64 (IPKey). When a table is full the expired
//     entries are dropped first, then the unlocked entry with the oldest
//     failure is evicted. If every entry is locked, a failure for a new key is
//     not recorded (fail open for that key; the other dimension still counts).
//   - Attempts that passed Check before a lock tripped may still finish, so a
//     burst of parallel requests can overshoot the limit by the hasher's
//     capacity (Argon2MaxConcurrent plus its short queue), not more.
//
// Cleanup runs on its own inside RecordFailure every SweepInterval, so no
// goroutine is required; Run additionally frees memory while idle.
type LoginLimiter struct {
	clk    clock.Clock
	window time.Duration
	lock   time.Duration
	sweep  time.Duration

	mu        sync.Mutex
	users     loginTable
	ips       loginTable
	lastSweep time.Time
}

// NewLoginLimiter returns a limiter reading time from clk.
func NewLoginLimiter(clk clock.Clock, cfg LoginLimiterConfig) *LoginLimiter {
	if cfg.Window <= 0 {
		cfg.Window = DefaultLoginWindow
	}
	if cfg.Lock <= 0 {
		cfg.Lock = DefaultLoginLock
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = DefaultLoginMaxKeys
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = DefaultSweepInterval
	}
	return &LoginLimiter{
		clk:       clk,
		window:    cfg.Window,
		lock:      cfg.Lock,
		sweep:     cfg.SweepInterval,
		users:     newLoginTable(cfg.MaxFailsUser, cfg.MaxKeys),
		ips:       newLoginTable(cfg.MaxFailsIP, cfg.MaxKeys),
		lastSweep: clk.Now(),
	}
}

// Check reports whether a login attempt for username from ip is currently
// blocked, and if so for how long (the Retry-After; use
// domain.NewRateLimited(retryAfter)). It changes nothing. username is
// normalized like domain.NormalizeUsername; ip is the resolved client IP.
func (l *LoginLimiter) Check(username, ip string) (retryAfter time.Duration, blocked bool) {
	now := l.clk.Now()
	uk, ik := userKey(username), IPKey(ip)

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range [2]*loginEntry{l.users.entries[uk], l.ips.entries[ik]} {
		if e != nil {
			retryAfter = max(retryAfter, e.lockedUntil.Sub(now))
		}
	}
	return retryAfter, retryAfter > 0
}

// RecordFailure counts one failed login (wrong password or unknown username)
// against the username and the IP.
func (l *LoginLimiter) RecordFailure(username, ip string) {
	now := l.clk.Now()
	uk, ik := userKey(username), IPKey(ip)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepIfDue(now)
	l.users.fail(uk, now, l.window, l.lock)
	l.ips.fail(ik, now, l.window, l.lock)
}

// RecordSuccess clears the failure counter and any lock of username after a
// correct password. The IP counter is deliberately left alone (see
// LoginLimiter), which is why there is no ip parameter.
func (l *LoginLimiter) RecordSuccess(username string) {
	uk := userKey(username)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.users.entries, uk)
}

// Sweep drops every entry that has no failure left in the window and is not
// locked.
func (l *LoginLimiter) Sweep() {
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

// Run sweeps every SweepInterval (real time) until ctx is done. Start it in
// its own goroutine; it is optional, see LoginLimiter.
func (l *LoginLimiter) Run(ctx context.Context) {
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

func (l *LoginLimiter) sweepIfDue(now time.Time) {
	if now.Sub(l.lastSweep) >= l.sweep {
		l.sweepLocked(now)
	}
}

func (l *LoginLimiter) sweepLocked(now time.Time) {
	l.users.sweep(now, l.window)
	l.ips.sweep(now, l.window)
	l.lastSweep = now
}

// userKey normalizes a username the way the rest of the application does and
// bounds its size.
func userKey(username string) string {
	return boundKey(domain.NormalizeUsername(username))
}

// loginTable is the failure state of one dimension (usernames or IPs).
type loginTable struct {
	maxFails int
	maxKeys  int
	entries  map[string]*loginEntry
}

type loginEntry struct {
	// fails are the newest failure times, ascending, at most maxFails of them:
	// that is all it takes to know whether maxFails happened in the window.
	fails       []time.Time
	lockedUntil time.Time
}

func newLoginTable(maxFails, maxKeys int) loginTable {
	return loginTable{maxFails: max(maxFails, 1), maxKeys: maxKeys, entries: make(map[string]*loginEntry)}
}

func (t *loginTable) fail(key string, now time.Time, window, lock time.Duration) {
	e := t.entries[key]
	if e == nil {
		if len(t.entries) >= t.maxKeys && !t.makeRoom(now, window) {
			return
		}
		e = &loginEntry{}
		t.entries[key] = e
	}
	e.prune(now, window)
	if len(e.fails) >= t.maxFails {
		// The entry is at its cap (locked, or unlocked early while its failures
		// are still in the window): keep the newest maxFails-1 so the append
		// below stays within the bound.
		e.fails = append(e.fails[:0], e.fails[len(e.fails)-t.maxFails+1:]...)
	}
	e.fails = append(e.fails, now)
	if len(e.fails) >= t.maxFails && !e.lockedUntil.After(now) {
		e.lockedUntil = now.Add(lock)
	}
}

// makeRoom frees one slot: expired entries first, else the unlocked entry
// whose newest failure is the oldest. It reports false when every entry is
// locked.
func (t *loginTable) makeRoom(now time.Time, window time.Duration) bool {
	t.sweep(now, window)
	if len(t.entries) < t.maxKeys {
		return true
	}
	var (
		victim string
		oldest time.Time
		found  bool
	)
	for k, e := range t.entries {
		if e.lockedUntil.After(now) {
			continue
		}
		var last time.Time
		if n := len(e.fails); n > 0 {
			last = e.fails[n-1]
		}
		if !found || last.Before(oldest) {
			victim, oldest, found = k, last, true
		}
	}
	if found {
		delete(t.entries, victim)
	}
	return found
}

func (t *loginTable) sweep(now time.Time, window time.Duration) {
	for k, e := range t.entries {
		e.prune(now, window)
		if len(e.fails) == 0 && !e.lockedUntil.After(now) {
			delete(t.entries, k)
		}
	}
}

// prune drops failures that left the sliding window (now-window, now].
func (e *loginEntry) prune(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	i := 0
	for i < len(e.fails) && !e.fails[i].After(cutoff) {
		i++
	}
	if i > 0 {
		e.fails = append(e.fails[:0], e.fails[i:]...)
	}
}
