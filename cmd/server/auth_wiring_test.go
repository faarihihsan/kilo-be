package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/testutil"
)

// The tests in this file check that newApp plugs the real authenticator and
// rate limiter into the router and builds the hasher and login limiter from
// the configuration. Their behavior is tested where it lives (internal/auth,
// internal/httpapi/middleware).

func authWiringDo(h http.Handler, method, path, bearer string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAppAuthenticatesWithRealTokens(t *testing.T) {
	a, db := newTestApp(t)
	h := a.Handler()

	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin)
	userToken, userTokenID := testutil.SeedToken(t, db, uid)
	adminToken, _ := testutil.SeedToken(t, db, adminID)
	revoked, _ := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(time.Now().Add(-time.Hour)))
	expired, _ := testutil.SeedToken(t, db, uid, testutil.WithTokenExpired())

	// A valid user token gets through authentication and role check to the
	// (still stubbed) handler.
	apitest.RequireError(t, authWiringDo(h, http.MethodGet, "/v1/exercises", userToken), http.StatusNotImplemented, "not_implemented")

	// Admins are for management only.
	apitest.RequireError(t, authWiringDo(h, http.MethodGet, "/v1/exercises", adminToken), http.StatusForbidden, "forbidden")
	apitest.RequireError(t, authWiringDo(h, http.MethodPost, "/v1/auth/register", userToken), http.StatusForbidden, "forbidden")
	apitest.RequireError(t, authWiringDo(h, http.MethodPost, "/v1/auth/register", adminToken), http.StatusNotImplemented, "not_implemented")

	for name, token := range map[string]string{"none": "", "garbage": "nonsense", "revoked": revoked, "expired": expired} {
		rec := authWiringDo(h, http.MethodGet, "/v1/exercises", token)
		apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
		if rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s token: missing WWW-Authenticate challenge", name)
		}
	}

	// The lazy last_used_at update reached the database through the real store.
	var lastUsed *time.Time
	if err := db.QueryRow(t.Context(), `SELECT last_used_at FROM auth_tokens WHERE id = $1`, userTokenID).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed == nil {
		t.Error("last_used_at was not recorded")
	}
}

func TestAppRateLimitsTheAuthRoutesPerIP(t *testing.T) {
	a, _ := newTestApp(t)
	h := a.Handler()

	for i := range middleware.RateLimitBurst {
		rec := authWiringDo(h, http.MethodPost, "/v1/auth/login", "")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("login request %d: status = %d, want the stub's 501", i+1, rec.Code)
		}
	}
	limited := authWiringDo(h, http.MethodPost, "/v1/auth/login", "")
	apitest.RequireError(t, limited, http.StatusTooManyRequests, "rate_limited")
	if limited.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	// Other addresses are unaffected; data routes are not limited at all.
	other := func(r *http.Request) { r.RemoteAddr = "198.51.100.7:1234" }
	apitest.RequireError(t, authWiringDo(h, http.MethodPost, "/v1/auth/login", "", other), http.StatusNotImplemented, "not_implemented")
	for range 3 * middleware.RateLimitBurst {
		apitest.RequireError(t, authWiringDo(h, http.MethodGet, "/v1/progress", ""), http.StatusUnauthorized, "unauthorized")
	}
}

func TestAppRateLimiterHonorsTrustedProxyConfig(t *testing.T) {
	forged := func(i int) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i%9))) }
	}
	limited := func(t *testing.T, env map[string]string) int {
		t.Helper()
		db := testutil.NewDB(t)
		a, err := newApp(testConfig(t, env), discardLogger(), db)
		if err != nil {
			t.Fatal(err)
		}
		h := a.Handler()
		n := 0
		for i := range 3 * middleware.RateLimitBurst {
			if authWiringDo(h, http.MethodPost, "/v1/auth/login", "", forged(i)).Code == http.StatusTooManyRequests {
				n++
			}
		}
		return n
	}

	// Default TRUSTED_PROXY_CIDRS is loopback: the test peer (192.0.2.1) is
	// not a proxy, so rotating X-Forwarded-For buys nothing.
	if got := limited(t, devEnv(t, "postgres://unused@localhost/unused")); got != 2*middleware.RateLimitBurst {
		t.Errorf("default config: %d requests limited, want %d", got, 2*middleware.RateLimitBurst)
	}
	// With the peer listed as a proxy the forwarded addresses are believed
	// and each gets its own bucket.
	env := devEnv(t, "postgres://unused@localhost/unused")
	env["TRUSTED_PROXY_CIDRS"] = "192.0.2.0/24"
	if got := limited(t, env); got != 0 {
		t.Errorf("trusted proxy config: %d requests limited, want 0 (nine clients within their burst)", got)
	}
}

func TestNewServiceDepsBuildsHasherAndLimiterFromConfig(t *testing.T) {
	db := testutil.NewDB(t)
	env := devEnv(t, "postgres://unused@localhost/unused")
	env["ARGON2_MEMORY_KIB"] = "8192"
	env["ARGON2_TIME"] = "1"
	env["ARGON2_PARALLELISM"] = "2"
	env["LOGIN_MAX_FAILS_USER"] = "2"
	env["LOGIN_MAX_FAILS_IP"] = "3"
	env["LOGIN_LOCK_MINUTES"] = "7"

	d, err := newServiceDeps(testConfig(t, env), discardLogger(), db)
	if err != nil {
		t.Fatalf("newServiceDeps: %v", err)
	}
	if d.Hasher == nil || d.Limiter == nil {
		t.Fatalf("Deps.Hasher = %v, Deps.Limiter = %v; want both set", d.Hasher, d.Limiter)
	}

	encoded, err := d.Hasher.Hash(t.Context(), "pw")
	if err != nil || !strings.HasPrefix(encoded, "$argon2id$v=19$m=8192,t=1,p=2$") {
		t.Errorf("Hash = %q, %v; want the configured cost in the hash", encoded, err)
	}
	if ok, err := d.Hasher.Verify(t.Context(), "pw", encoded); err != nil || !ok {
		t.Errorf("Verify = %v, %v", ok, err)
	}

	// Two failures for one username lock it for 7 minutes; the IP needs three.
	d.Limiter.RecordFailure("ihsan", "192.0.2.1")
	if _, blocked := d.Limiter.Check("ihsan", "192.0.2.9"); blocked {
		t.Error("locked after one failure, want 2 (LOGIN_MAX_FAILS_USER)")
	}
	d.Limiter.RecordFailure("ihsan", "192.0.2.2")
	// The real clock is in use, so allow for the time since the failure.
	if retry, blocked := d.Limiter.Check("ihsan", "192.0.2.9"); !blocked || retry > 7*time.Minute || retry < 7*time.Minute-time.Minute {
		t.Errorf("Check = %v, %v; want blocked for about 7m (LOGIN_LOCK_MINUTES)", retry, blocked)
	}
	for _, name := range []string{"a", "b"} {
		d.Limiter.RecordFailure(name, "192.0.2.50")
	}
	if _, blocked := d.Limiter.Check("c", "192.0.2.50"); blocked {
		t.Error("IP locked after 2 failures, want 3 (LOGIN_MAX_FAILS_IP)")
	}
	d.Limiter.RecordFailure("c", "192.0.2.50")
	if _, blocked := d.Limiter.Check("d", "192.0.2.50"); !blocked {
		t.Error("IP not locked after 3 failures (LOGIN_MAX_FAILS_IP)")
	}
}

func TestNewServiceDepsRejectsUnusableArgon2Settings(t *testing.T) {
	db := testutil.NewDB(t)
	env := devEnv(t, "postgres://unused@localhost/unused")
	env["ARGON2_TIME"] = "1000000" // valid for the config parser, unusable for the hasher

	_, err := newServiceDeps(testConfig(t, env), discardLogger(), db)
	if err == nil || !strings.Contains(err.Error(), "ARGON2") {
		t.Fatalf("err = %v, want an ARGON2 error", err)
	}
}
