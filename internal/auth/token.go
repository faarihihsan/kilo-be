package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"workout-tracker-be/internal/domain"
)

// TokenLen is the exact length of a raw token: domain.TokenPrefix plus the
// unpadded base64url form of domain.TokenRandomBytes random bytes (46).
var TokenLen = len(domain.TokenPrefix) + base64.RawURLEncoding.EncodedLen(domain.TokenRandomBytes)

// NewToken creates an opaque bearer token: the prefix `wt_` plus 32 bytes from
// the OS random source in base64url. raw is what the client stores and sends
// (shown once, never stored or logged by the server); hash is what the server
// stores in auth_tokens.token_hash.
func NewToken() (raw string, hash []byte, err error) {
	b := make([]byte, domain.TokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: random token: %w", err)
	}
	raw = domain.TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken returns the SHA-256 of the whole token string as sent after
// "Bearer ", prefix included. SHA-256 is enough here because the token is 256
// bits of randomness, unlike a password.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ParseBearer extracts the token from an Authorization header value. The
// scheme is matched case-insensitively and must be followed by exactly one
// space and a token of the exact shape NewToken produces (prefix, length,
// base64url characters), so malformed input is rejected before it reaches the
// database or a log line. It only checks the shape: whether the token exists
// is for the store to say.
func ParseBearer(authorization string) (raw string, ok bool) {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || !wellFormedToken(token) {
		return "", false
	}
	return token, true
}

func wellFormedToken(s string) bool {
	if len(s) != TokenLen || !strings.HasPrefix(s, domain.TokenPrefix) {
		return false
	}
	for i := len(domain.TokenPrefix); i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
