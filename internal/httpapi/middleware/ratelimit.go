package middleware

import (
	"net/http"
	"net/netip"
	"strings"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

// The generic per-IP limit of the routes flagged AuthRateLimited (register,
// login, logout, revoke, list-tokens and the admin endpoints; conventions.md,
// "Auth details"). It is a token bucket per client IP: a burst of
// RateLimitBurst requests, then RateLimitPerMinute sustained. That is far
// above what the mobile app does (a login, a device list, a logout) and low
// enough to make scripted guessing pointless. Login has its own, much
// stricter, failure limiter on top (auth.LoginLimiter).
const (
	RateLimitPerMinute = 30
	RateLimitBurst     = 10
	// RateLimitMaxKeys caps the client IPs tracked at once; see
	// auth.RateLimiter for what happens when it is reached.
	RateLimitMaxKeys = 10_000
)

// RateLimitConfig configures NewRateLimit.
type RateLimitConfig struct {
	// Clock is the time source (clock.Real{} in production).
	Clock clock.Clock
	// TrustedProxies is config.Config.TrustedProxyCIDRs: the only peers whose
	// X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// PerMinute, Burst and MaxKeys override the constants above when non-zero.
	PerMinute int
	Burst     int
	MaxKeys   int
}

// NewRateLimit returns the middleware for the RouterConfig.RateLimit slot. For
// every request it
//
//  1. resolves the client IP (ClientIP, honoring X-Forwarded-For only from a
//     trusted proxy) and stores it in the request context (WithClientIP), so a
//     handler behind it, the login handler, can read ClientIPFrom;
//  2. charges the IP's bucket and, when it is empty, answers 429 rate_limited
//     with Retry-After without calling the next handler.
//
// Because it runs before authentication, unauthenticated floods count too.
// IPv6 clients share a bucket per /64 (auth.IPKey). The bucket table is
// bounded and swept as it goes; see auth.RateLimiter.
func NewRateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	limiter := auth.NewRateLimiter(cfg.Clock, auth.RateLimiterConfig{
		PerMinute: rateLimitDefault(cfg.PerMinute, RateLimitPerMinute),
		Burst:     rateLimitDefault(cfg.Burst, RateLimitBurst),
		MaxKeys:   rateLimitDefault(cfg.MaxKeys, RateLimitMaxKeys),
	})
	trusted := append([]netip.Prefix(nil), cfg.TrustedProxies...)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r.RemoteAddr, strings.Join(r.Header.Values("X-Forwarded-For"), ","), trusted)
			r = r.WithContext(WithClientIP(r.Context(), ip))

			if retryAfter, ok := limiter.Allow(auth.IPKey(ip)); !ok {
				render.WriteError(w, r, domain.NewRateLimited(retryAfter))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func rateLimitDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}
