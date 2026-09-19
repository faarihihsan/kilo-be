// Package auth holds the authentication primitives that need no database and
// no HTTP: the argon2id password hasher, opaque bearer tokens, and the
// in-memory limiters (login brute-force lockout and a generic per-key rate
// limiter).
//
// Everything here is safe for concurrent use and takes its time from an
// injected clock.Clock, except where a type says otherwise (the hasher's
// bounded wait for a hashing slot is real time).
//
// Typical login flow, as used by the auth service:
//
//	if retry, blocked := limiter.Check(username, ip); blocked {
//		return domain.NewRateLimited(retry)
//	}
//	user, err := users.GetByUsername(ctx, username)
//	switch {
//	case errors.Is(err, domain.ErrNotFound):
//		err = hasher.VerifyDummy(ctx, password) // same cost as a real check
//		ok = false
//	case err == nil:
//		ok, err = hasher.Verify(ctx, password, user.PasswordHash)
//	}
//	// err != nil: 429 from a busy hasher, or an internal error. Do not
//	// count it as a failed guess.
//	if !ok { limiter.RecordFailure(username, ip); return domain.NewUnauthorized() }
//	limiter.RecordSuccess(username)
//	raw, hash, _ := auth.NewToken() // store hash, return raw once
//
// The package imports domain and clock only.
package auth
