package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"workout-tracker-be/internal/domain"
)

const (
	saltLen = 16
	keyLen  = 32

	// DefaultHasherMaxWait is how long a hash waits for a free slot when
	// HasherConfig.MaxWait is zero.
	DefaultHasherMaxWait = 2 * time.Second

	// busyRetryAfter is the Retry-After of the 429 sent when no slot freed up
	// in time.
	busyRetryAfter = time.Second

	// Bounds for the parameters in a stored hash and for the configured ones.
	// The stored string is written by this code, so these only stop a corrupt
	// or hostile row from allocating gigabytes or looping for minutes.
	maxMemoryKiB   = 1 << 20 // 1 GiB
	maxTime        = 100
	minEncodedSalt = 8
	maxEncodedSalt = 64
	minEncodedKey  = 16
	maxEncodedKey  = 64
)

// ErrInvalidHash is returned (wrapped) by Verify for a stored hash that is not
// a well-formed argon2id PHC string.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// HasherConfig configures NewHasher. The first three fields have the types of
// config.Config.Argon2MemoryKiB, Argon2Time and Argon2Parallelism.
type HasherConfig struct {
	MemoryKiB   uint32
	Time        uint32
	Parallelism uint8
	// MaxConcurrent caps hashes running at once; each needs MemoryKiB of RAM.
	MaxConcurrent int
	// MaxWait bounds the wait for a slot once MaxConcurrent hashes are running.
	// Zero means DefaultHasherMaxWait.
	MaxWait time.Duration
}

// Hasher hashes and verifies passwords with argon2id. It is safe for
// concurrent use.
//
// Hashes are PHC strings, `$argon2id$v=19$m=65536,t=2,p=1$<salt>$<hash>`, with
// unpadded standard base64. Verify reads the parameters from the string, so
// changing the configured cost never locks out an existing user.
//
// At most MaxConcurrent hashes run at once (each holds MemoryKiB of memory).
// An operation that cannot get a slot within MaxWait fails with a
// *domain.RateLimitedError (429), so a burst of logins cannot exhaust RAM.
type Hasher struct {
	params  hashParams
	sem     chan struct{}
	maxWait time.Duration
	dummy   string

	// key is argon2.IDKey; tests replace it to observe or block the work.
	key func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte
}

type hashParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

// validate reports whether p is usable by argon2.IDKey (which panics on
// time < 1 or threads < 1) and within the bounds above.
func (p hashParams) validate() error {
	switch {
	case p.threads < 1:
		return errors.New("parallelism must be at least 1")
	case p.time < 1 || p.time > maxTime:
		return fmt.Errorf("time must be between 1 and %d", maxTime)
	case p.memory < 8*uint32(p.threads):
		return errors.New("memory must be at least 8 KiB per thread")
	case p.memory > maxMemoryKiB:
		return fmt.Errorf("memory must be at most %d KiB", maxMemoryKiB)
	}
	return nil
}

// NewHasher validates cfg and precomputes the dummy hash used by VerifyDummy,
// which takes about as long as one Hash. It returns an error for parameters
// argon2 cannot use.
func NewHasher(cfg HasherConfig) (*Hasher, error) {
	p := hashParams{memory: cfg.MemoryKiB, time: cfg.Time, threads: cfg.Parallelism}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("auth: argon2 parameters: %w", err)
	}
	if cfg.MaxConcurrent < 1 {
		return nil, errors.New("auth: argon2 max concurrent hashes must be at least 1")
	}
	maxWait := cfg.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultHasherMaxWait
	}
	h := &Hasher{
		params:  p,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		maxWait: maxWait,
		key:     argon2.IDKey,
	}

	// The dummy is a hash of a random password nobody knows, made with the
	// same parameters as real hashes. It is not throttled: it runs at startup.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("auth: random dummy password: %w", err)
	}
	dummy, err := h.derive(secret)
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the argon2id PHC string of password with a fresh random salt.
// It waits for a free slot (see Hasher) and returns ctx's error if ctx ends
// first. The password is not validated here: domain.ValidatePassword does
// that.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	pw := []byte(password)
	defer clear(pw)
	return h.derive(pw)
}

// derive hashes pw with a fresh salt; the caller holds a slot if it needs one.
func (h *Hasher) derive(pw []byte) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: random salt: %w", err)
	}
	key := h.key(pw, salt, h.params.time, h.params.memory, h.params.threads, keyLen)
	return encodeHash(h.params, salt, key), nil
}

// Verify reports whether password matches the encoded hash, comparing in
// constant time. It uses the parameters stored in encoded, not the configured
// ones. A malformed encoded hash returns false and an error wrapping
// ErrInvalidHash without doing any hashing. Like Hash it waits for a slot and
// can fail with a 429 *domain.RateLimitedError or ctx's error; in those cases
// the bool is false and means nothing about the password.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	pw := []byte(password)
	defer clear(pw)
	got := h.key(pw, salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyDummy does the work of a failed Verify against a precomputed dummy
// hash and always "fails". Call it when the username does not exist so that
// unknown and known usernames cost the same (docs/api/endpoints/02-login.md,
// timing-safe verification). The error is only ever a busy-hasher 429 or ctx's
// error, exactly as for Verify.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	_, err := h.Verify(ctx, password, h.dummy)
	return err
}

// acquire takes a hashing slot. It fails immediately if ctx is already done,
// with ctx's error if ctx ends while waiting, and with a 429 after maxWait.
func (h *Hasher) acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release = func() { <-h.sem }
	select {
	case h.sem <- struct{}{}:
		return release, nil
	default:
	}

	timer := time.NewTimer(h.maxWait)
	defer timer.Stop()
	select {
	case h.sem <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, domain.NewRateLimited(busyRetryAfter)
	}
}

func encodeHash(p hashParams, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// decodeHash parses and bounds-checks a PHC string. Its errors never contain
// the input.
func decodeHash(encoded string) (p hashParams, salt, key []byte, err error) {
	bad := func(why string) (hashParams, []byte, []byte, error) {
		return hashParams{}, nil, nil, fmt.Errorf("%w: %s", ErrInvalidHash, why)
	}

	// "" $ argon2id $ v=19 $ m=..,t=..,p=.. $ salt $ hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return bad("wrong layout")
	}
	if parts[1] != "argon2id" {
		return bad("not argon2id")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return bad("unsupported version")
	}

	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return bad("wrong parameters")
	}
	var vals [3]uint64
	for i, name := range [3]string{"m=", "t=", "p="} {
		num, ok := strings.CutPrefix(kv[i], name)
		if !ok {
			return bad("wrong parameters")
		}
		bits := 32
		if name == "p=" {
			bits = 8
		}
		v, perr := strconv.ParseUint(num, 10, bits)
		if perr != nil {
			return bad("wrong parameters")
		}
		vals[i] = v
	}
	p = hashParams{memory: uint32(vals[0]), time: uint32(vals[1]), threads: uint8(vals[2])}
	if perr := p.validate(); perr != nil {
		return bad("parameters out of range")
	}

	salt, serr := base64.RawStdEncoding.DecodeString(parts[4])
	if serr != nil || len(salt) < minEncodedSalt || len(salt) > maxEncodedSalt {
		return bad("bad salt")
	}
	key, kerr := base64.RawStdEncoding.DecodeString(parts[5])
	if kerr != nil || len(key) < minEncodedKey || len(key) > maxEncodedKey {
		return bad("bad hash")
	}
	return p, salt, key, nil
}
