package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"workout-tracker-be/internal/domain"
)

// Small parameters keep the tests fast; the algorithm is the same.
func testHasherConfig() HasherConfig {
	return HasherConfig{MemoryKiB: 8, Time: 1, Parallelism: 1, MaxConcurrent: 2, MaxWait: 5 * time.Second}
}

func newTestHasher(t *testing.T, mutate ...func(*HasherConfig)) *Hasher {
	t.Helper()
	cfg := testHasherConfig()
	for _, m := range mutate {
		m(&cfg)
	}
	h, err := NewHasher(cfg)
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	return h
}

func TestHashAndVerifyRoundTrip(t *testing.T) {
	h := newTestHasher(t)
	ctx := t.Context()

	encoded, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Errorf("encoded = %q, want a PHC argon2id string with the configured parameters", encoded)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Error("encoded hash contains the password")
	}

	ok, err := h.Verify(ctx, "correct horse", encoded)
	if err != nil || !ok {
		t.Errorf("Verify(correct) = %v, %v; want true, nil", ok, err)
	}
	for _, wrong := range []string{"correct horsf", "", "Correct horse", "correct horse "} {
		ok, err := h.Verify(ctx, wrong, encoded)
		if err != nil || ok {
			t.Errorf("Verify(%q) = %v, %v; want false, nil", wrong, ok, err)
		}
	}
}

func TestHashUsesFreshSaltAndStandardEncoding(t *testing.T) {
	h := newTestHasher(t)
	a, _ := h.Hash(t.Context(), "same")
	b, _ := h.Hash(t.Context(), "same")
	if a == b {
		t.Fatal("two hashes of the same password are identical: salt is not random")
	}

	_, salt, key, err := decodeHash(a)
	if err != nil {
		t.Fatalf("decodeHash: %v", err)
	}
	if len(salt) != 16 || len(key) != 32 {
		t.Errorf("salt %d bytes, key %d bytes; want 16 and 32", len(salt), len(key))
	}
	// The fields are unpadded standard base64 (no '=', no '-' or '_').
	for _, field := range strings.Split(a, "$")[4:] {
		if strings.ContainsAny(field, "=-_") {
			t.Errorf("field %q is not unpadded standard base64", field)
		}
	}
}

func TestHashMatchesReferenceArgon2(t *testing.T) {
	h := newTestHasher(t)
	encoded, _ := h.Hash(t.Context(), "pw")
	_, salt, key, err := decodeHash(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := argon2.IDKey([]byte("pw"), salt, 1, 8, 1, 32)
	if string(key) != string(want) {
		t.Error("hash differs from argon2.IDKey with the same parameters")
	}
}

// Parameters are read from the encoded string, so a hash made under old
// settings still verifies after the configuration changed.
func TestVerifyUsesParametersFromTheEncodedString(t *testing.T) {
	old := newTestHasher(t, func(c *HasherConfig) { c.MemoryKiB, c.Time, c.Parallelism = 16, 2, 2 })
	encoded, err := old.Hash(t.Context(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, "$m=16,t=2,p=2$") {
		t.Fatalf("encoded = %q", encoded)
	}

	current := newTestHasher(t) // m=8,t=1,p=1
	if ok, err := current.Verify(t.Context(), "pw", encoded); err != nil || !ok {
		t.Errorf("Verify across configurations = %v, %v; want true, nil", ok, err)
	}
	if ok, _ := current.Verify(t.Context(), "nope", encoded); ok {
		t.Error("wrong password verified")
	}
}

func TestVerifyMalformedEncodings(t *testing.T) {
	h := newTestHasher(t)
	good, _ := h.Hash(t.Context(), "pw")
	join := func(p ...string) string { return strings.Join(p, "$") }
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))

	tests := []struct{ name, encoded string }{
		{"empty", ""},
		{"plain text", "not a hash"},
		{"bcrypt", "$2a$12$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"},
		{"argon2i", join("", "argon2i", "v=19", "m=8,t=1,p=1", salt, key)},
		{"argon2d", join("", "argon2d", "v=19", "m=8,t=1,p=1", salt, key)},
		{"missing version", join("", "argon2id", "m=8,t=1,p=1", salt, key)},
		{"old version", join("", "argon2id", "v=16", "m=8,t=1,p=1", salt, key)},
		{"future version", join("", "argon2id", "v=20", "m=8,t=1,p=1", salt, key)},
		{"missing hash", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt)},
		{"extra field", good + "$extra"},
		{"no leading dollar", strings.TrimPrefix(good, "$")},
		{"params reordered", join("", "argon2id", "v=19", "t=1,m=8,p=1", salt, key)},
		{"params missing p", join("", "argon2id", "v=19", "m=8,t=1", salt, key)},
		{"params extra", join("", "argon2id", "v=19", "m=8,t=1,p=1,x=1", salt, key)},
		{"param not a number", join("", "argon2id", "v=19", "m=eight,t=1,p=1", salt, key)},
		{"param negative", join("", "argon2id", "v=19", "m=-8,t=1,p=1", salt, key)},
		{"param signed", join("", "argon2id", "v=19", "m=+8,t=1,p=1", salt, key)},
		{"param empty", join("", "argon2id", "v=19", "m=,t=1,p=1", salt, key)},
		{"time zero would panic argon2", join("", "argon2id", "v=19", "m=8,t=0,p=1", salt, key)},
		{"parallelism zero would panic argon2", join("", "argon2id", "v=19", "m=8,t=1,p=0", salt, key)},
		{"parallelism over 255", join("", "argon2id", "v=19", "m=8,t=1,p=256", salt, key)},
		{"memory below 8 KiB per thread", join("", "argon2id", "v=19", "m=15,t=1,p=2", salt, key)},
		{"memory over the cap", join("", "argon2id", "v=19", "m=4194304,t=1,p=1", salt, key)},
		{"memory overflows uint32", join("", "argon2id", "v=19", "m=4294967296,t=1,p=1", salt, key)},
		{"time over the cap", join("", "argon2id", "v=19", "m=8,t=1000000,p=1", salt, key)},
		{"salt not base64", join("", "argon2id", "v=19", "m=8,t=1,p=1", "!!!!", key)},
		{"salt padded", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt+"==", key)},
		{"salt too short", join("", "argon2id", "v=19", "m=8,t=1,p=1", base64.RawStdEncoding.EncodeToString(make([]byte, 4)), key)},
		{"salt empty", join("", "argon2id", "v=19", "m=8,t=1,p=1", "", key)},
		{"hash not base64", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt, "***")},
		{"hash too short", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt, base64.RawStdEncoding.EncodeToString(make([]byte, 4)))},
		{"hash empty", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt, "")},
		{"hash too long", join("", "argon2id", "v=19", "m=8,t=1,p=1", salt, base64.RawStdEncoding.EncodeToString(make([]byte, 100)))},
		{"url-safe base64 alphabet", join("", "argon2id", "v=19", "m=8,t=1,p=1", "a-b_cdefghijklmnopqrstuv", key)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := h.Verify(t.Context(), "pw", tc.encoded)
			if ok {
				t.Error("Verify accepted a malformed hash")
			}
			if !errors.Is(err, ErrInvalidHash) {
				t.Errorf("err = %v, want ErrInvalidHash", err)
			}
			if err != nil && tc.encoded != "" && strings.Contains(err.Error(), tc.encoded) {
				t.Error("error message repeats the stored hash")
			}
		})
	}
}

func TestVerifyTestutilDummyHashFormatIsAccepted(t *testing.T) {
	// The seed helper's placeholder must be a well-formed hash that simply
	// never matches (m=65536 is the production cost, so this is one slow call).
	if testing.Short() {
		t.Skip("uses production-sized parameters")
	}
	dummy := "$argon2id$v=19$m=65536,t=2,p=1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	h := newTestHasher(t)
	ok, err := h.Verify(t.Context(), "anything", dummy)
	if ok || err != nil {
		t.Errorf("Verify = %v, %v; want false, nil", ok, err)
	}
}

func TestNewHasherRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HasherConfig)
	}{
		{"zero time", func(c *HasherConfig) { c.Time = 0 }},
		{"zero parallelism", func(c *HasherConfig) { c.Parallelism = 0 }},
		{"memory below minimum", func(c *HasherConfig) { c.MemoryKiB = 4 }},
		{"memory above cap", func(c *HasherConfig) { c.MemoryKiB = 1<<20 + 1 }},
		{"time above cap", func(c *HasherConfig) { c.Time = 101 }},
		{"zero concurrency", func(c *HasherConfig) { c.MaxConcurrent = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testHasherConfig()
			tc.mutate(&cfg)
			if h, err := NewHasher(cfg); err == nil {
				t.Errorf("NewHasher accepted %+v (%v)", cfg, h)
			}
		})
	}
}

// instrument replaces the hasher's key function with one that counts the
// operations in flight, records the parameters it was called with and runs
// the real argon2 after an optional pause.
type instrument struct {
	inFlight, maxInFlight atomic.Int32
	calls                 atomic.Int32
	mu                    sync.Mutex
	params                []string
	pause                 time.Duration
	block                 chan struct{} // when set, every call waits for it
	entered               chan struct{} // when set, receives one value per call
}

func (in *instrument) install(h *Hasher) {
	h.key = func(pw, salt []byte, tm, mem uint32, threads uint8, kl uint32) []byte {
		n := in.inFlight.Add(1)
		for {
			m := in.maxInFlight.Load()
			if n <= m || in.maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		defer in.inFlight.Add(-1)
		in.calls.Add(1)
		in.mu.Lock()
		in.params = append(in.params, fmt.Sprintf("m=%d,t=%d,p=%d,len=%d", mem, tm, threads, kl))
		in.mu.Unlock()
		if in.entered != nil {
			in.entered <- struct{}{}
		}
		if in.block != nil {
			<-in.block
		}
		time.Sleep(in.pause)
		return argon2.IDKey(pw, salt, tm, mem, threads, kl)
	}
}

func TestConcurrencyIsCapped(t *testing.T) {
	const limit = 2
	h := newTestHasher(t, func(c *HasherConfig) { c.MaxConcurrent = limit })
	in := &instrument{pause: 20 * time.Millisecond}
	in.install(h)

	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = h.Hash(t.Context(), "pw")
			} else {
				_, err = h.Verify(t.Context(), "pw", h.dummy)
			}
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if got := in.maxInFlight.Load(); got > limit {
		t.Errorf("max concurrent hashes = %d, want <= %d", got, limit)
	}
	if got := in.maxInFlight.Load(); got < limit {
		t.Errorf("max concurrent hashes = %d, want the limit of %d to be used", got, limit)
	}
	if got := in.calls.Load(); got != 12 {
		t.Errorf("hashes run = %d, want 12", got)
	}
}

func TestBusyHasherAnswersRateLimitedAfterMaxWait(t *testing.T) {
	h := newTestHasher(t, func(c *HasherConfig) { c.MaxConcurrent = 1; c.MaxWait = 60 * time.Millisecond })
	in := &instrument{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	in.install(h)

	first := make(chan error, 1)
	go func() { _, err := h.Hash(t.Context(), "pw"); first <- err }()
	<-in.entered // the only slot is taken

	start := time.Now()
	_, err := h.Hash(t.Context(), "pw")
	waited := time.Since(start)

	var rl *domain.RateLimitedError
	if !errors.As(err, &rl) || !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("err = %v, want *domain.RateLimitedError", err)
	}
	if rl.RetryAfterSeconds() < 1 {
		t.Errorf("Retry-After = %d s, want at least 1", rl.RetryAfterSeconds())
	}
	if waited < 50*time.Millisecond || waited > 2*time.Second {
		t.Errorf("waited %v, want about the 60ms MaxWait", waited)
	}
	// Verify and VerifyDummy are throttled the same way.
	if _, err := h.Verify(t.Context(), "pw", h.dummy); !errors.Is(err, domain.ErrRateLimited) {
		t.Errorf("Verify while busy: err = %v, want rate limited", err)
	}
	if err := h.VerifyDummy(t.Context(), "pw"); !errors.Is(err, domain.ErrRateLimited) {
		t.Errorf("VerifyDummy while busy: err = %v, want rate limited", err)
	}

	close(in.block)
	if err := <-first; err != nil {
		t.Errorf("first hash: %v", err)
	}
	// The slot is free again.
	if _, err := h.Hash(t.Context(), "pw"); err != nil {
		t.Errorf("hash after release: %v", err)
	}
}

func TestWaitingForASlotSucceedsWhenOneFreesUpInTime(t *testing.T) {
	h := newTestHasher(t, func(c *HasherConfig) { c.MaxConcurrent = 1; c.MaxWait = 5 * time.Second })
	in := &instrument{block: make(chan struct{}), entered: make(chan struct{}, 2)}
	in.install(h)

	results := make(chan error, 2)
	go func() { _, err := h.Hash(t.Context(), "a"); results <- err }()
	<-in.entered
	go func() { _, err := h.Hash(t.Context(), "b"); results <- err }() // queues
	time.Sleep(30 * time.Millisecond)
	close(in.block)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("hash: %v", err)
		}
	}
}

func TestContextCancelWhileWaitingAndBeforeStart(t *testing.T) {
	h := newTestHasher(t, func(c *HasherConfig) { c.MaxConcurrent = 1 })
	in := &instrument{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	in.install(h)

	go func() { _, _ = h.Hash(context.Background(), "holder") }()
	<-in.entered

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := h.Hash(ctx, "waiter"); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled waiter did not return")
	}

	// An already cancelled context never starts work, even with a free slot.
	close(in.block)
	time.Sleep(50 * time.Millisecond) // let the holder finish
	callsBefore := in.calls.Load()
	ctx2, cancel2 := context.WithCancel(t.Context())
	cancel2()
	if _, err := h.Hash(ctx2, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash with cancelled ctx: err = %v", err)
	}
	if _, err := h.Verify(ctx2, "x", h.dummy); !errors.Is(err, context.Canceled) {
		t.Errorf("Verify with cancelled ctx: err = %v", err)
	}
	if in.calls.Load() != callsBefore {
		t.Error("work started with a cancelled context")
	}
}

// The dummy check must cost what a real check costs: same parameters, same
// output length, through the same throttled path.
func TestVerifyDummyDoesTheSameWorkAsVerify(t *testing.T) {
	h := newTestHasher(t, func(c *HasherConfig) { c.MemoryKiB, c.Time, c.Parallelism = 32, 3, 2 })
	realHash, err := h.Hash(t.Context(), "real password")
	if err != nil {
		t.Fatal(err)
	}
	in := &instrument{}
	in.install(h)

	if _, err := h.Verify(t.Context(), "wrong", realHash); err != nil {
		t.Fatal(err)
	}
	if err := h.VerifyDummy(t.Context(), "wrong"); err != nil {
		t.Fatal(err)
	}
	if len(in.params) != 2 || in.params[0] != in.params[1] {
		t.Errorf("key derivation parameters differ: %v", in.params)
	}
	if want := "m=32,t=3,p=2,len=32"; in.params[0] != want {
		t.Errorf("parameters = %q, want %q", in.params[0], want)
	}
}

func TestVerifyDummyNeverSucceedsAndUsesAFreshSecret(t *testing.T) {
	a, b := newTestHasher(t), newTestHasher(t)
	if a.dummy == b.dummy {
		t.Error("two hashers share a dummy hash")
	}
	for _, pw := range []string{"", "password", "123", strings.Repeat("x", 128)} {
		if err := a.VerifyDummy(t.Context(), pw); err != nil {
			t.Errorf("VerifyDummy(%q) = %v", pw, err)
		}
		if ok, _ := a.Verify(t.Context(), pw, a.dummy); ok {
			t.Errorf("the dummy hash verifies %q", pw)
		}
	}
}

// A loose sanity check on wall time; the exact-parameter test above is the
// real guarantee. Minimum of several runs, wide tolerance.
func TestVerifyDummyTimingIsInTheSameBallparkAsVerify(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	h := newTestHasher(t, func(c *HasherConfig) { c.MemoryKiB, c.Time = 4096, 2 })
	realHash, _ := h.Hash(t.Context(), "pw")

	fastest := func(f func()) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 7 {
			start := time.Now()
			f()
			best = min(best, time.Since(start))
		}
		return best
	}
	tReal := fastest(func() { _, _ = h.Verify(t.Context(), "wrong", realHash) })
	tDummy := fastest(func() { _ = h.VerifyDummy(t.Context(), "wrong") })
	if tDummy < tReal/3 || tDummy > tReal*3 {
		t.Errorf("dummy verify %v vs real verify %v: not comparable", tDummy, tReal)
	}
}

func TestPasswordIsNotInErrorsOrHashes(t *testing.T) {
	h := newTestHasher(t)
	const secret = "s3cret-pass-word"
	_, err := h.Verify(t.Context(), secret, "garbage")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("err = %v", err)
	}
	enc, _ := h.Hash(t.Context(), secret)
	if strings.Contains(enc, secret) {
		t.Error("hash contains the password")
	}
}
