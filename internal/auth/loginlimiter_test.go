package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"workout-tracker-be/internal/clock"
)

var testEpoch = time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)

// Spec 02 defaults: 5 per username, 20 per IP, 15 minute window, 15 minute lock.
func newTestLoginLimiter(mutate ...func(*LoginLimiterConfig)) (*LoginLimiter, *clock.Fake) {
	clk := clock.NewFake(testEpoch)
	cfg := LoginLimiterConfig{MaxFailsUser: 5, MaxFailsIP: 20, Lock: 15 * time.Minute}
	for _, m := range mutate {
		m(&cfg)
	}
	return NewLoginLimiter(clk, cfg), clk
}

// failFrom records n failures for username from a different IP each time, so
// only the username counter moves.
func failFrom(l *LoginLimiter, username string, n int) {
	for i := range n {
		l.RecordFailure(username, fmt.Sprintf("10.0.%d.%d", i/250, i%250+1))
	}
}

func mustNotBlock(t *testing.T, l *LoginLimiter, username, ip string) {
	t.Helper()
	if d, blocked := l.Check(username, ip); blocked {
		t.Fatalf("Check(%q, %q) blocked for %v, want allowed", username, ip, d)
	}
}

func mustBlock(t *testing.T, l *LoginLimiter, username, ip string, wantRetry time.Duration) {
	t.Helper()
	d, blocked := l.Check(username, ip)
	if !blocked {
		t.Fatalf("Check(%q, %q) allowed, want blocked for %v", username, ip, wantRetry)
	}
	if d != wantRetry {
		t.Fatalf("Check(%q, %q) retry after %v, want %v", username, ip, d, wantRetry)
	}
}

func TestUsernameLocksAfterTheFifthFailure(t *testing.T) {
	l, _ := newTestLoginLimiter()

	for i := 1; i <= 4; i++ {
		failFrom(l, "ihsan", 1)
		mustNotBlock(t, l, "ihsan", "192.0.2.1")
	}
	failFrom(l, "ihsan", 1) // 5th
	mustBlock(t, l, "ihsan", "192.0.2.1", 15*time.Minute)
	// The lock follows the username, not the address.
	mustBlock(t, l, "ihsan", "203.0.113.5", 15*time.Minute)
	// Other usernames are unaffected.
	mustNotBlock(t, l, "someone_else", "192.0.2.1")
}

func TestLockIsReportedWithShrinkingRetryAfterAndExpires(t *testing.T) {
	l, clk := newTestLoginLimiter()
	failFrom(l, "ihsan", 5)

	clk.Advance(10*time.Minute + 30*time.Second)
	mustBlock(t, l, "ihsan", "192.0.2.1", 4*time.Minute+30*time.Second)

	clk.Advance(4*time.Minute + 29*time.Second)
	mustBlock(t, l, "ihsan", "192.0.2.1", time.Second)

	clk.Advance(time.Second) // exactly at the end of the lock
	mustNotBlock(t, l, "ihsan", "192.0.2.1")

	// A fresh window: the old failures are gone, so it takes 5 more.
	failFrom(l, "ihsan", 4)
	mustNotBlock(t, l, "ihsan", "192.0.2.1")
	failFrom(l, "ihsan", 1)
	mustBlock(t, l, "ihsan", "192.0.2.1", 15*time.Minute)
}

func TestFailuresOutsideTheSlidingWindowDoNotCount(t *testing.T) {
	l, clk := newTestLoginLimiter()

	failFrom(l, "ihsan", 3) // t=0
	clk.Advance(10 * time.Minute)
	failFrom(l, "ihsan", 2) // t=10m: 5 in total, but the window is what counts...
	mustBlock(t, l, "ihsan", "192.0.2.1", 15*time.Minute)

	l2, clk2 := newTestLoginLimiter()
	failFrom(l2, "ihsan", 3) // t=0
	clk2.Advance(15 * time.Minute)
	failFrom(l2, "ihsan", 2) // t=15m: the first three are exactly window-old, gone
	mustNotBlock(t, l2, "ihsan", "192.0.2.1")
	failFrom(l2, "ihsan", 3)
	mustBlock(t, l2, "ihsan", "192.0.2.1", 15*time.Minute)
}

func TestWindowSlidesFailureByFailure(t *testing.T) {
	l, clk := newTestLoginLimiter()
	// One failure every 4 minutes: at most 4 fit into 15 minutes (t, t-4, t-8, t-12), never 5.
	for range 50 {
		failFrom(l, "ihsan", 1)
		mustNotBlock(t, l, "ihsan", "192.0.2.1")
		clk.Advance(4 * time.Minute)
	}
	// One failure every 3 minutes: 5 fit (t, t-3, ... t-12) and it locks.
	l2, clk2 := newTestLoginLimiter()
	for i := range 5 {
		failFrom(l2, "ihsan", 1)
		if i < 4 {
			mustNotBlock(t, l2, "ihsan", "192.0.2.1")
		}
		clk2.Advance(3 * time.Minute)
	}
	if _, blocked := l2.Check("ihsan", "192.0.2.1"); !blocked {
		t.Error("5 failures within 12 minutes did not lock")
	}
}

func TestIPLocksAfterTwentyFailuresAcrossUsernames(t *testing.T) {
	l, _ := newTestLoginLimiter()
	// 20 failures, each for a different (mostly unknown) username: no username
	// reaches 5, the IP reaches 20.
	for i := range 19 {
		l.RecordFailure(fmt.Sprintf("guess%d", i), "198.51.100.7")
	}
	mustNotBlock(t, l, "brand_new", "198.51.100.7")
	l.RecordFailure("guess19", "198.51.100.7")

	mustBlock(t, l, "brand_new", "198.51.100.7", 15*time.Minute)
	// Another IP may still log in as anyone, including the guessed names.
	mustNotBlock(t, l, "brand_new", "198.51.100.8")
	mustNotBlock(t, l, "guess3", "198.51.100.8")
}

func TestUserAndIPLimitsAreIndependent(t *testing.T) {
	l, _ := newTestLoginLimiter()

	// Username lock from many IPs leaves every IP usable for other users.
	failFrom(l, "victim", 5)
	mustBlock(t, l, "victim", "10.0.0.1", 15*time.Minute)
	mustNotBlock(t, l, "other", "10.0.0.1")

	// IP lock leaves other usernames usable from other IPs.
	for range 20 {
		l.RecordFailure("spray", "203.0.113.9")
	}
	mustBlock(t, l, "other", "203.0.113.9", 15*time.Minute)
	mustNotBlock(t, l, "other", "203.0.113.10")
}

func TestLongestLockIsReported(t *testing.T) {
	l, clk := newTestLoginLimiter()
	failFrom(l, "ihsan", 5) // username locked until t+15m
	clk.Advance(5 * time.Minute)
	for range 20 { // IP locked until t+20m
		l.RecordFailure("x"+strings.Repeat("y", 3), "198.51.100.7")
	}
	// User lock has 10 minutes left, IP lock 15.
	mustBlock(t, l, "ihsan", "198.51.100.7", 15*time.Minute)
	mustBlock(t, l, "ihsan", "192.0.2.1", 10*time.Minute)
}

func TestSuccessResetsTheUsernameCounterOnly(t *testing.T) {
	l, _ := newTestLoginLimiter()

	failFrom(l, "ihsan", 4)
	l.RecordSuccess("ihsan")
	failFrom(l, "ihsan", 4) // 4 again, not 8
	mustNotBlock(t, l, "ihsan", "192.0.2.1")
	failFrom(l, "ihsan", 1)
	mustBlock(t, l, "ihsan", "192.0.2.1", 15*time.Minute)

	// The IP counter is not reset by a success (an attacker with one valid
	// account must not be able to wipe it between guesses).
	l2, _ := newTestLoginLimiter()
	for range 19 {
		l2.RecordFailure("guess", "198.51.100.7")
	}
	l2.RecordSuccess("guess")
	l2.RecordSuccess("mine")
	l2.RecordFailure("other", "198.51.100.7")
	mustBlock(t, l2, "brand_new", "198.51.100.7", 15*time.Minute)
}

func TestUsernamesAreNormalized(t *testing.T) {
	l, _ := newTestLoginLimiter()
	for _, name := range []string{"Ihsan", "IHSAN", " ihsan ", "ihsan", "iHsAn"} {
		l.RecordFailure(name, "192.0.2.1")
	}
	mustBlock(t, l, "ihsan", "192.0.2.99", 15*time.Minute)
}

func TestUnknownAndKnownUsernamesBehaveIdentically(t *testing.T) {
	// The limiter has no notion of "exists": that is the point.
	l, _ := newTestLoginLimiter()
	failFrom(l, "no_such_user_at_all", 5)
	mustBlock(t, l, "no_such_user_at_all", "192.0.2.1", 15*time.Minute)
}

func TestFailuresWhileLockedDoNotExtendTheLock(t *testing.T) {
	l, clk := newTestLoginLimiter()
	failFrom(l, "ihsan", 5)
	clk.Advance(5 * time.Minute)
	failFrom(l, "ihsan", 3) // e.g. attempts that raced past Check
	mustBlock(t, l, "ihsan", "192.0.2.1", 10*time.Minute)
}

func TestShorterLockThanWindowRelocksOnTheNextFailure(t *testing.T) {
	l, clk := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.Lock = time.Minute })
	failFrom(l, "ihsan", 5)
	mustBlock(t, l, "ihsan", "192.0.2.1", time.Minute)
	clk.Advance(time.Minute)
	mustNotBlock(t, l, "ihsan", "192.0.2.1")
	// The five failures are still inside the 15 minute window.
	failFrom(l, "ihsan", 1)
	mustBlock(t, l, "ihsan", "192.0.2.1", time.Minute)
}

func TestLongerLockThanWindowKeepsTheLock(t *testing.T) {
	l, clk := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.Lock = time.Hour })
	failFrom(l, "ihsan", 5)
	clk.Advance(30 * time.Minute) // the failures left the window, the lock did not
	mustBlock(t, l, "ihsan", "192.0.2.1", 30*time.Minute)
	l.Sweep()
	mustBlock(t, l, "ihsan", "192.0.2.1", 30*time.Minute) // a sweep must not drop a live lock
}

func TestConfiguredThresholds(t *testing.T) {
	l, _ := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxFailsUser = 2; c.MaxFailsIP = 3; c.Lock = 3 * time.Minute })
	failFrom(l, "a", 1)
	mustNotBlock(t, l, "a", "192.0.2.1")
	failFrom(l, "a", 1)
	mustBlock(t, l, "a", "192.0.2.1", 3*time.Minute)

	for range 3 {
		l.RecordFailure("b"+strings.Repeat("x", 1), "198.51.100.7")
		l.RecordFailure("c", "198.51.100.7")
	}
	mustBlock(t, l, "zzz", "198.51.100.7", 3*time.Minute)

	// Degenerate configuration: a threshold of 1 locks on the first failure.
	l1, _ := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxFailsUser = 0 })
	failFrom(l1, "a", 1)
	mustBlock(t, l1, "a", "192.0.2.1", 15*time.Minute)
}

func TestIPv6ClientsAreGroupedBySlash64(t *testing.T) {
	l, _ := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxFailsIP = 3 })
	l.RecordFailure("a", "2001:db8:1:2:aaaa::1")
	l.RecordFailure("b", "2001:db8:1:2:bbbb::2")
	l.RecordFailure("c", "2001:db8:1:2::3")
	mustBlock(t, l, "d", "2001:db8:1:2:cccc::4", 15*time.Minute)
	mustNotBlock(t, l, "d", "2001:db8:1:3::4")
	// IPv4-mapped addresses are the same client as the plain IPv4 address.
	l4, _ := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxFailsIP = 2 })
	l4.RecordFailure("a", "192.0.2.1")
	l4.RecordFailure("b", "::ffff:192.0.2.1")
	mustBlock(t, l4, "c", "192.0.2.1", 15*time.Minute)
}

func TestSweepFreesMemory(t *testing.T) {
	l, clk := newTestLoginLimiter()
	for i := range 100 {
		l.RecordFailure(fmt.Sprintf("user%d", i), fmt.Sprintf("10.0.0.%d", i))
	}
	failFrom(l, "locked_user", 5)
	if len(l.users.entries) == 0 || len(l.ips.entries) == 0 {
		t.Fatal("nothing tracked")
	}

	l.Sweep()
	if got := len(l.users.entries); got != 101 {
		t.Errorf("sweep dropped live entries: %d usernames left, want 101", got)
	}

	clk.Advance(15 * time.Minute)
	l.Sweep()
	if u, i := len(l.users.entries), len(l.ips.entries); u != 0 || i != 0 {
		t.Errorf("after sweep: %d usernames, %d IPs tracked; want 0 and 0", u, i)
	}
	mustNotBlock(t, l, "locked_user", "10.0.0.1")
}

func TestFailuresSweepOpportunisticallyWithoutRun(t *testing.T) {
	l, clk := newTestLoginLimiter()
	for i := range 50 {
		l.RecordFailure(fmt.Sprintf("user%d", i), fmt.Sprintf("10.0.0.%d", i))
	}
	clk.Advance(DefaultSweepInterval + DefaultLoginWindow)
	l.RecordFailure("late", "192.0.2.1") // triggers the due sweep
	if u, i := len(l.users.entries), len(l.ips.entries); u != 1 || i != 1 {
		t.Errorf("tracked %d usernames and %d IPs after the due sweep, want 1 and 1", u, i)
	}
}

func TestRunSweepsUntilContextIsDone(t *testing.T) {
	l, clk := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.SweepInterval = 5 * time.Millisecond })
	l.RecordFailure("a", "192.0.2.1")
	clk.Advance(time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	deadline := time.After(5 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.users.entries) + len(l.ips.entries)
		l.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not sweep")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestMemoryIsBoundedWhenFull(t *testing.T) {
	l, clk := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxKeys = 10 })

	// Twice the cap of distinct usernames and IPs.
	for i := range 20 {
		l.RecordFailure(fmt.Sprintf("user%d", i), fmt.Sprintf("10.0.0.%d", i))
		clk.Advance(time.Second)
	}
	if u, i := len(l.users.entries), len(l.ips.entries); u != 10 || i != 10 {
		t.Fatalf("tracked %d usernames and %d IPs, want the cap of 10 each", u, i)
	}
	// The oldest were evicted, the newest kept.
	if _, ok := l.users.entries["user19"]; !ok {
		t.Error("newest username was not tracked")
	}
	if _, ok := l.users.entries["user0"]; ok {
		t.Error("oldest username was not evicted")
	}
}

func TestLockedEntriesAreNeverEvicted(t *testing.T) {
	l, _ := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxKeys = 3; c.MaxFailsUser = 1 })
	for i := range 3 { // three locked usernames fill the table
		l.RecordFailure(fmt.Sprintf("locked%d", i), "192.0.2.1")
	}
	l.RecordFailure("newcomer", "192.0.2.1") // nothing can be evicted: not tracked
	if len(l.users.entries) != 3 {
		t.Errorf("tracked %d usernames, want 3", len(l.users.entries))
	}
	for i := range 3 {
		mustBlock(t, l, fmt.Sprintf("locked%d", i), "203.0.113.1", 15*time.Minute)
	}
	mustNotBlock(t, l, "newcomer", "203.0.113.1")
}

func TestPerKeyMemoryIsBounded(t *testing.T) {
	l, _ := newTestLoginLimiter()
	for range 1000 {
		l.RecordFailure("ihsan", "192.0.2.1")
	}
	if n := len(l.users.entries["ihsan"].fails); n > 5 {
		t.Errorf("%d timestamps kept for one username, want at most 5", n)
	}
	if n := len(l.ips.entries["192.0.2.1"].fails); n > 20 {
		t.Errorf("%d timestamps kept for one IP, want at most 20", n)
	}
}

func TestHugeUsernamesAreBounded(t *testing.T) {
	l, _ := newTestLoginLimiter()
	huge := strings.Repeat("a", 1<<20)
	failFrom(l, huge, 5)
	mustBlock(t, l, huge, "192.0.2.1", 15*time.Minute)
	for k := range l.users.entries {
		if len(k) > maxKeyLen+len("sha256:")+64 {
			t.Errorf("key of %d bytes stored", len(k))
		}
	}
	// A different long name is a different key.
	mustNotBlock(t, l, huge+"b", "192.0.2.1")
}

func TestLoginLimiterIsSafeForConcurrentUse(t *testing.T) {
	l, clk := newTestLoginLimiter(func(c *LoginLimiterConfig) { c.MaxKeys = 50 })
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				user := fmt.Sprintf("user%d", (g*7+i)%80)
				ip := fmt.Sprintf("10.1.%d.%d", g, i%40)
				l.Check(user, ip)
				l.RecordFailure(user, ip)
				if i%9 == 0 {
					l.RecordSuccess(user)
				}
				if i%50 == 0 {
					l.Sweep()
					clk.Advance(30 * time.Second)
				}
			}
		}()
	}
	wg.Wait()
	if len(l.users.entries) > 50 || len(l.ips.entries) > 50 {
		t.Errorf("tables exceeded MaxKeys: %d usernames, %d IPs", len(l.users.entries), len(l.ips.entries))
	}
}

func TestConcurrentFailuresLockExactlyOnce(t *testing.T) {
	l, _ := newTestLoginLimiter()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); l.RecordFailure("ihsan", "192.0.2.1") }()
	}
	wg.Wait()
	mustBlock(t, l, "ihsan", "192.0.2.1", 15*time.Minute)
}
