package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"workout-tracker-be/internal/clock"
)

func newTestRateLimiter(mutate ...func(*RateLimiterConfig)) (*RateLimiter, *clock.Fake) {
	clk := clock.NewFake(testEpoch)
	cfg := RateLimiterConfig{PerMinute: 30, Burst: 10}
	for _, m := range mutate {
		m(&cfg)
	}
	return NewRateLimiter(clk, cfg), clk
}

func TestRateLimiterBurstThenThrottle(t *testing.T) {
	l, _ := newTestRateLimiter()
	for i := 1; i <= 10; i++ {
		if d, ok := l.Allow("k"); !ok {
			t.Fatalf("request %d throttled (retry %v), want the burst of 10 admitted", i, d)
		}
	}
	d, ok := l.Allow("k")
	if ok {
		t.Fatal("request 11 admitted, want throttled")
	}
	if d != 2*time.Second { // 30 per minute: one every 2 s
		t.Errorf("retry after %v, want 2s", d)
	}
}

func TestRateLimiterRecoversAtTheSustainedRate(t *testing.T) {
	l, clk := newTestRateLimiter()
	for range 10 {
		l.Allow("k")
	}
	// Hammering does not push the recovery further away.
	for range 100 {
		if _, ok := l.Allow("k"); ok {
			t.Fatal("throttled key admitted")
		}
	}
	clk.Advance(1900 * time.Millisecond)
	d, ok := l.Allow("k")
	if ok || d != 100*time.Millisecond {
		t.Fatalf("Allow = %v, %v; want throttled for 100ms", d, ok)
	}
	clk.Advance(100 * time.Millisecond)
	if _, ok := l.Allow("k"); !ok {
		t.Fatal("not admitted after the advertised wait")
	}
	if _, ok := l.Allow("k"); ok {
		t.Error("second request right after admitted: refill is one token per 2s")
	}

	// A long pause restores the full burst, not more.
	clk.Advance(time.Hour)
	for i := 1; i <= 10; i++ {
		if _, ok := l.Allow("k"); !ok {
			t.Fatalf("request %d after a pause throttled", i)
		}
	}
	if _, ok := l.Allow("k"); ok {
		t.Error("burst after a pause exceeded 10")
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestRateLimiter()
	for range 10 {
		l.Allow("a")
	}
	if _, ok := l.Allow("a"); ok {
		t.Fatal("a not throttled")
	}
	if _, ok := l.Allow("b"); !ok {
		t.Error("b throttled by a's traffic")
	}
}

func TestRateLimiterSlowTrafficIsNeverThrottled(t *testing.T) {
	l, clk := newTestRateLimiter()
	for range 1000 {
		if _, ok := l.Allow("k"); !ok {
			t.Fatal("a request every 2s (the sustained rate) was throttled")
		}
		clk.Advance(2 * time.Second)
	}
}

func TestRateLimiterBurstOfOneAndOddRates(t *testing.T) {
	l, clk := newTestRateLimiter(func(c *RateLimiterConfig) { c.PerMinute = 7; c.Burst = 1 })
	if _, ok := l.Allow("k"); !ok {
		t.Fatal("first request throttled")
	}
	d, ok := l.Allow("k")
	if ok {
		t.Fatal("second request admitted with burst 1")
	}
	clk.Advance(d) // advertised wait must be exact even when the interval does not divide evenly
	if _, ok := l.Allow("k"); !ok {
		t.Fatalf("throttled after waiting the advertised %v", d)
	}

	l0, _ := newTestRateLimiter(func(c *RateLimiterConfig) { c.PerMinute = 0; c.Burst = 0 })
	if _, ok := l0.Allow("k"); !ok {
		t.Error("degenerate config: first request throttled")
	}
}

func TestRateLimiterSweepFreesRefilledKeys(t *testing.T) {
	l, clk := newTestRateLimiter()
	for i := range 100 {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	for range 10 {
		l.Allow("busy")
	}
	l.Sweep()
	if n := len(l.tat); n != 101 {
		t.Fatalf("sweep dropped keys still in debt: %d tracked, want 101", n)
	}
	clk.Advance(10 * time.Second) // single-request keys have refilled (2s), busy has 10s of debt left
	l.Sweep()
	if n := len(l.tat); n != 1 {
		t.Errorf("%d keys tracked after the sweep, want only the busy one", n)
	}
	clk.Advance(time.Minute)
	l.Sweep()
	if n := len(l.tat); n != 0 {
		t.Errorf("%d keys tracked after everything refilled, want 0", n)
	}
}

func TestRateLimiterSweepsOpportunistically(t *testing.T) {
	l, clk := newTestRateLimiter()
	for i := range 50 {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	clk.Advance(DefaultSweepInterval)
	l.Allow("late")
	if n := len(l.tat); n != 1 {
		t.Errorf("%d keys tracked after the due sweep, want 1", n)
	}
}

func TestRateLimiterMemoryIsBounded(t *testing.T) {
	l, clk := newTestRateLimiter(func(c *RateLimiterConfig) { c.MaxKeys = 10; c.Burst = 3 })
	// Every key makes 2 requests, so all stay in debt for a while.
	for i := range 100 {
		key := fmt.Sprintf("k%d", i)
		l.Allow(key)
		clk.Advance(10 * time.Millisecond)
		l.Allow(key)
	}
	if n := len(l.tat); n > 10 {
		t.Errorf("%d keys tracked, want at most the cap of 10", n)
	}
	if _, ok := l.Allow("fresh"); !ok {
		t.Error("a new key was throttled when the table was full")
	}
}

func TestRateLimiterRunSweepsUntilContextIsDone(t *testing.T) {
	l, clk := newTestRateLimiter(func(c *RateLimiterConfig) { c.SweepInterval = 5 * time.Millisecond })
	l.Allow("k")
	clk.Advance(time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.tat)
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

func TestRateLimiterIsSafeForConcurrentUse(t *testing.T) {
	l, clk := newTestRateLimiter(func(c *RateLimiterConfig) { c.MaxKeys = 20 })
	var wg sync.WaitGroup
	admitted := make([]int, 8)
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				if _, ok := l.Allow(fmt.Sprintf("k%d", i%30)); ok {
					admitted[g]++
				}
				if i%100 == 0 {
					l.Sweep()
					clk.Advance(time.Second)
				}
			}
		}()
	}
	wg.Wait()
	if len(l.tat) > 20 {
		t.Errorf("%d keys tracked, want at most 20", len(l.tat))
	}
}

func TestRateLimiterNeverAdmitsMoreThanTheBurstAtOnceUnderConcurrency(t *testing.T) {
	l, _ := newTestRateLimiter()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Allow("k"); ok {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 10 {
		t.Errorf("%d of 100 simultaneous requests admitted, want exactly the burst of 10", admitted)
	}
}
