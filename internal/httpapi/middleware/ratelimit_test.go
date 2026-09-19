package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/testutil"
)

func authFrom(remoteAddr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = remoteAddr }
}

func authXFF(values ...string) func(*http.Request) {
	return func(r *http.Request) {
		for _, v := range values {
			r.Header.Add("X-Forwarded-For", v)
		}
	}
}

func TestRateLimitBurstThenTooManyRequestsThenRecovers(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())

	for i := 1; i <= middleware.RateLimitBurst; i++ {
		if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (burst of %d)", i, rec.Code, middleware.RateLimitBurst)
		}
	}
	probes := env.probes.Load()

	rec := env.do(http.MethodPost, "/v1/auth/login", "")
	body := apitest.RequireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	if len(body.Error.Details) != 0 {
		t.Errorf("details = %v, want none", body.Error.Details)
	}
	// 30 per minute is one request per 2 seconds.
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	if env.probes.Load() != probes {
		t.Error("a limited request reached the handler")
	}

	// Waiting the advertised time admits exactly one more request.
	env.clk.Advance(2 * time.Second)
	if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusOK {
		t.Fatalf("after Retry-After: status = %d, want 200", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request right after: status = %d, want 429", rec.Code)
	}

	// A quiet minute restores the whole burst.
	env.clk.Advance(time.Minute)
	for i := 1; i <= middleware.RateLimitBurst; i++ {
		if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusOK {
			t.Fatalf("after a minute, request %d: status = %d", i, rec.Code)
		}
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("burst after recovery exceeded %d", middleware.RateLimitBurst)
	}
}

func TestRateLimitDocumentedConstants(t *testing.T) {
	if middleware.RateLimitPerMinute != 30 || middleware.RateLimitBurst != 10 {
		t.Errorf("limits are %d/min burst %d; docs and the report say 30/min burst 10",
			middleware.RateLimitPerMinute, middleware.RateLimitBurst)
	}
}

func TestRateLimitIsPerClientIP(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	for range middleware.RateLimitBurst {
		env.do(http.MethodPost, "/v1/auth/login", "")
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first client not limited: %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", "", authFrom("198.51.100.7:4000")); rec.Code != http.StatusOK {
		t.Errorf("another client: status = %d, want 200", rec.Code)
	}
	// The port is not part of the identity.
	if rec := env.do(http.MethodPost, "/v1/auth/login", "", authFrom("192.0.2.1:9999")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same client from another port: status = %d, want 429", rec.Code)
	}
}

func TestRateLimitGroupsIPv6ClientsBySlash64(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	for i := range middleware.RateLimitBurst {
		addr := "[2001:db8:1:2:" + strconv.Itoa(i+1) + "::1]:5000" // a different address every time, one /64
		if rec := env.do(http.MethodPost, "/v1/auth/login", "", authFrom(addr)); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, rec.Code)
		}
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", "", authFrom("[2001:db8:1:2:ffff::9]:5000")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("rotating addresses inside one /64 escaped the limit: %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/v1/auth/login", "", authFrom("[2001:db8:1:3::1]:5000")); rec.Code != http.StatusOK {
		t.Errorf("a different /64: status = %d, want 200", rec.Code)
	}
}

func TestRateLimitOnlyGuardsTheFlaggedRoutes(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	raw, _ := testutil.SeedToken(t, env.db, env.user())

	// A workout data route is not flagged: no limit, and no client IP in the context.
	for i := range 5 * middleware.RateLimitBurst {
		rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d to a data route: status = %d", i, rec.Code)
		}
		if seen := authDecodeSeen(t, rec); seen.ClientIP != "" {
			t.Fatalf("client IP %q set on a route that skips the limiter", seen.ClientIP)
		}
	}
	// The flagged ones are: list-tokens, logout, register, login.
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/auth/tokens"},
		{http.MethodPost, "/v1/auth/logout"},
		{http.MethodPost, "/v1/auth/register"},
		{http.MethodPost, "/v1/auth/login"},
	} {
		env.clk.Advance(time.Minute) // a full bucket for each route
		for range middleware.RateLimitBurst {
			env.do(rt.method, rt.path, "")
		}
		apitest.RequireError(t, env.do(rt.method, rt.path, ""), http.StatusTooManyRequests, "rate_limited")
	}
}

// Login attempts, garbage tokens and valid requests all draw from one bucket,
// and the limiter runs before authentication: a flood of bad tokens gets 429,
// not an endless stream of 401s.
func TestRateLimitCountsUnauthenticatedRequests(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	for range middleware.RateLimitBurst {
		authRequireUnauthorized(t, env.do(http.MethodPost, "/v1/auth/logout", "Bearer nonsense"))
	}
	apitest.RequireError(t, env.do(http.MethodPost, "/v1/auth/logout", "Bearer nonsense"), http.StatusTooManyRequests, "rate_limited")
	// The 429 is not a way around the login limit: it is per IP, shared by routes.
	apitest.RequireError(t, env.do(http.MethodPost, "/v1/auth/login", ""), http.StatusTooManyRequests, "rate_limited")
	// And a limited request never leaves a token in the way of authentication:
	// a valid token from the same IP is limited too, until the bucket refills.
	raw, _ := testutil.SeedToken(t, env.db, env.user())
	apitest.RequireError(t, env.do(http.MethodPost, "/v1/auth/logout", "Bearer "+raw), http.StatusTooManyRequests, "rate_limited")
	env.clk.Advance(2 * time.Second)
	if rec := env.do(http.MethodPost, "/v1/auth/logout", "Bearer "+raw); rec.Code != http.StatusOK {
		t.Errorf("after the wait: status = %d", rec.Code)
	}
}

func TestRateLimitStoresTheClientIPForTheHandlers(t *testing.T) {
	proxy := netip.MustParsePrefix("127.0.0.1/32")
	env := newAuthEnv(t, authWithRateLimit(proxy))

	tests := []struct {
		name string
		mods []func(*http.Request)
		want string
	}{
		{"direct peer", nil, "192.0.2.1"},
		{"spoofed header from an untrusted peer", []func(*http.Request){authXFF("1.2.3.4")}, "192.0.2.1"},
		{"trusted proxy passes the client on", []func(*http.Request){authFrom("127.0.0.1:8080"), authXFF("203.0.113.9")}, "203.0.113.9"},
		{"client-supplied prefix is ignored", []func(*http.Request){authFrom("127.0.0.1:8080"), authXFF("1.2.3.4, 203.0.113.9")}, "203.0.113.9"},
		{"header split over several lines", []func(*http.Request){authFrom("127.0.0.1:8080"), authXFF("1.2.3.4", "203.0.113.9")}, "203.0.113.9"},
		{"trusted proxy without header", []func(*http.Request){authFrom("127.0.0.1:8080")}, "127.0.0.1"},
		{"IPv6 client", []func(*http.Request){authFrom("127.0.0.1:8080"), authXFF("2001:db8::9")}, "2001:db8::9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh clock second per case keeps the shared bucket from running dry.
			env.clk.Advance(time.Minute)
			rec := env.do(http.MethodPost, "/v1/auth/login", "", tc.mods...)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
			}
			if got := authDecodeSeen(t, rec).ClientIP; got != tc.want {
				t.Errorf("ClientIPFrom(ctx) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRateLimitUsesTheForwardedClientBehindATrustedProxy(t *testing.T) {
	proxy := netip.MustParsePrefix("127.0.0.1/32")
	env := newAuthEnv(t, authWithRateLimit(proxy))
	viaProxy := func(client string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = "127.0.0.1:8080"; r.Header.Set("X-Forwarded-For", client) }
	}

	// One client behind the proxy exhausts its own bucket...
	for range middleware.RateLimitBurst {
		env.do(http.MethodPost, "/v1/auth/login", "", viaProxy("203.0.113.9"))
	}
	apitest.RequireError(t, env.do(http.MethodPost, "/v1/auth/login", "", viaProxy("203.0.113.9")), http.StatusTooManyRequests, "rate_limited")
	// ...and does not take the others down with it.
	if rec := env.do(http.MethodPost, "/v1/auth/login", "", viaProxy("203.0.113.10")); rec.Code != http.StatusOK {
		t.Errorf("another client behind the proxy: status = %d", rec.Code)
	}
	// Spoofing the front of the list does not help: the right-most non-proxy entry counts.
	apitest.RequireError(t, env.do(http.MethodPost, "/v1/auth/login", "", viaProxy("198.51.100.99, 203.0.113.9")), http.StatusTooManyRequests, "rate_limited")
}

func TestRateLimitCannotBeBypassedByForgingForwardedFor(t *testing.T) {
	proxy := netip.MustParsePrefix("127.0.0.1/32")
	env := newAuthEnv(t, authWithRateLimit(proxy))

	// The attacker connects directly and rotates X-Forwarded-For.
	limited := 0
	for i := range 3 * middleware.RateLimitBurst {
		rec := env.do(http.MethodPost, "/v1/auth/login", "", authXFF("203.0.113."+strconv.Itoa(i+1)))
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if want := 2 * middleware.RateLimitBurst; limited != want {
		t.Errorf("%d requests limited, want %d: a forged header from an untrusted peer must not create new buckets", limited, want)
	}
}

func TestRateLimitBodyNeverEchoesRequestData(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	raw, _ := testutil.SeedToken(t, env.db, env.user())
	var last string
	for range middleware.RateLimitBurst + 1 {
		rec := env.do(http.MethodPost, "/v1/auth/logout", "Bearer "+raw, authXFF("secret-forwarded-value"))
		last = rec.Body.String() + rec.Header().Get("Retry-After")
	}
	for _, leak := range []string{raw, "secret-forwarded-value", "192.0.2.1"} {
		if strings.Contains(last, leak) {
			t.Errorf("429 response contains %q", leak)
		}
	}
	if strings.Contains(env.logs.String(), raw) {
		t.Error("token in the logs")
	}
}

func TestRateLimitConfigOverridesAndNilTrusted(t *testing.T) {
	env := newAuthEnv(t)
	mw := middleware.NewRateLimit(middleware.RateLimitConfig{Clock: env.clk, PerMinute: 6, Burst: 2})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	do := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		return rec.Code
	}
	if do() != 204 || do() != 204 {
		t.Fatal("burst of 2 not admitted")
	}
	if got := do(); got != http.StatusTooManyRequests {
		t.Errorf("third request: %d, want 429", got)
	}
}
