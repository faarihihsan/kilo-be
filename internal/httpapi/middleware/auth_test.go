package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// These tests drive the real router (httpapi.NewRouter) with the real
// authenticator and rate limiter over a real database. Probe handlers stand in
// for the resource handlers and report what they saw.

// authSeen is what a probe handler saw.
type authSeen struct {
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	TokenID  string `json:"token_id"`
	ClientIP string `json:"client_ip"`
}

// authSpyTokens wraps the real token store to count calls and inject errors.
type authSpyTokens struct {
	inner     *store.AuthTokens
	lookups   atomic.Int32
	touches   atomic.Int32
	lookupErr error
	touchErr  error
}

func (s *authSpyTokens) LookupByHash(ctx context.Context, hash []byte) (store.TokenRecord, error) {
	s.lookups.Add(1)
	if s.lookupErr != nil {
		return store.TokenRecord{}, s.lookupErr
	}
	return s.inner.LookupByHash(ctx, hash)
}

func (s *authSpyTokens) TouchLastUsed(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	s.touches.Add(1)
	if s.touchErr != nil {
		return false, s.touchErr
	}
	return s.inner.TouchLastUsed(ctx, id, now)
}

type authEnv struct {
	t       *testing.T
	db      *store.DB
	clk     *clock.Fake
	tokens  *authSpyTokens
	logs    *apitest.Logs
	handler http.Handler
	probes  atomic.Int32 // handler invocations
}

type authEnvOptions struct {
	rateLimit bool
	trusted   []netip.Prefix
}

func newAuthEnv(t *testing.T, mutate ...func(*authEnvOptions)) *authEnv {
	t.Helper()
	var opts authEnvOptions
	for _, m := range mutate {
		m(&opts)
	}
	env := &authEnv{
		t:   t,
		db:  testutil.NewDB(t),
		clk: clock.NewFake(time.Now().UTC().Truncate(time.Microsecond)),
	}
	env.tokens = &authSpyTokens{inner: store.NewAuthTokens(env.db)}
	logger, logs := apitest.NewLogs()
	env.logs = logs

	probe := func(w http.ResponseWriter, r *http.Request) {
		env.probes.Add(1)
		seen := authSeen{ClientIP: middleware.ClientIPFrom(r.Context())}
		if p, ok := middleware.PrincipalFrom(r.Context()); ok {
			seen.UserID, seen.Role, seen.TokenID = p.UserID.String(), string(p.Role), p.TokenID.String()
		}
		render.WriteJSON(w, http.StatusOK, seen)
	}
	cfg := httpapi.RouterConfig{
		Handlers: httpapi.Handlers{
			ListExercises: probe, // GET  /v1/exercises           user only
			Logout:        probe, // POST /v1/auth/logout         both roles, rate limited
			ListTokens:    probe, // GET  /v1/auth/tokens         user only, rate limited
			Register:      probe, // POST /v1/auth/register       admin only, rate limited
			Login:         probe, // POST /v1/auth/login          anonymous, rate limited
		},
		Authenticator: middleware.NewAuthenticator(env.tokens, env.clk),
		Logger:        logger,
	}
	if opts.rateLimit {
		cfg.RateLimit = middleware.NewRateLimit(middleware.RateLimitConfig{Clock: env.clk, TrustedProxies: opts.trusted})
	}
	env.handler = httpapi.NewRouter(cfg)
	return env
}

func authWithRateLimit(trusted ...netip.Prefix) func(*authEnvOptions) {
	return func(o *authEnvOptions) { o.rateLimit = true; o.trusted = trusted }
}

// do sends a request with the given Authorization header value (empty means
// none), from 192.0.2.1 unless a modifier says otherwise.
func (e *authEnv) do(method, path, authorization string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *authEnv) user() (id uuid.UUID) {
	e.t.Helper()
	id, _ = testutil.SeedUser(e.t, e.db, domain.RoleUser)
	return id
}

func (e *authEnv) admin() (id uuid.UUID) {
	e.t.Helper()
	id, _ = testutil.SeedUser(e.t, e.db, domain.RoleAdmin)
	return id
}

func authDecodeSeen(t *testing.T, rec *httptest.ResponseRecorder) authSeen {
	t.Helper()
	var seen authSeen
	if err := json.Unmarshal(rec.Body.Bytes(), &seen); err != nil {
		t.Fatalf("probe body is not JSON: %v: %s", err, rec.Body)
	}
	return seen
}

func authRequireUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
}

func authState(t *testing.T, db *store.DB, id uuid.UUID) (lastUsed *time.Time) {
	t.Helper()
	if err := db.QueryRow(t.Context(), `SELECT last_used_at FROM auth_tokens WHERE id = $1`, id).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	return lastUsed
}

func TestAuthenticatorAcceptsAValidToken(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	raw, tokenID := testutil.SeedToken(t, env.db, uid)

	for _, header := range []string{"Bearer " + raw, "bearer " + raw, "BEARER " + raw} {
		rec := env.do(http.MethodGet, "/v1/exercises", header)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200; body: %s", header[:8], rec.Code, rec.Body)
		}
		seen := authDecodeSeen(t, rec)
		want := authSeen{UserID: uid.String(), Role: "user", TokenID: tokenID.String()}
		if seen != want {
			t.Errorf("principal = %+v, want %+v", seen, want)
		}
	}
	if got := env.probes.Load(); got != 3 {
		t.Errorf("handler ran %d times, want 3", got)
	}
}

func TestAuthenticatorRejectsBadCredentials(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	now := env.clk.Now()

	valid, _ := testutil.SeedToken(t, env.db, uid)
	expired, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenExpiresAt(now.Add(-time.Second)))
	expiringNow, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenExpiresAt(now))
	revoked, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenRevoked(now.Add(-time.Minute)))
	revokedAndExpired, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenRevoked(now.Add(-2*time.Hour)), testutil.WithTokenExpired())
	unknown, _, _ := auth.NewToken()
	otherPrefix := "xx_" + strings.TrimPrefix(valid, domain.TokenPrefix)

	tests := []struct {
		name        string
		header      string
		wantLookups int32 // a token the server has never seen must reach the database, junk must not
	}{
		{"no header", "", 0},
		{"basic auth", "Basic dXNlcjpwYXNz", 0},
		{"scheme only", "Bearer", 0},
		{"empty token", "Bearer ", 0},
		{"token without scheme", valid, 0},
		{"two spaces", "Bearer  " + valid, 0},
		{"trailing space", "Bearer " + valid + " ", 0},
		{"garbage", "Bearer not-a-token", 0},
		{"wrong prefix", "Bearer " + otherPrefix, 0},
		{"a JWT-looking value", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln", 0},
		{"oversized", "Bearer " + strings.Repeat("A", 100_000), 0},
		{"well-formed but unknown", "Bearer " + unknown, 1},
		{"expired", "Bearer " + expired, 1},
		{"expires right now", "Bearer " + expiringNow, 1},
		{"revoked", "Bearer " + revoked, 1},
		{"revoked and expired", "Bearer " + revokedAndExpired, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := env.tokens.lookups.Load()
			probes := env.probes.Load()

			rec := env.do(http.MethodGet, "/v1/exercises", tc.header)
			authRequireUnauthorized(t, rec)
			if env.probes.Load() != probes {
				t.Error("the handler ran for a rejected request")
			}
			if got := env.tokens.lookups.Load() - before; got != tc.wantLookups {
				t.Errorf("%d database lookups, want %d", got, tc.wantLookups)
			}
			// Every failure looks the same: a client cannot tell why.
			body := apitest.DecodeError(t, rec)
			if body.Error.Message != "Authentication is required." || len(body.Error.Details) != 0 {
				t.Errorf("body = %+v, want the generic 401", body)
			}
		})
	}

	// The valid token next to all of them still works.
	if rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+valid); rec.Code != http.StatusOK {
		t.Errorf("valid token: status = %d", rec.Code)
	}
}

func TestAuthenticatorRoleEnforcement(t *testing.T) {
	env := newAuthEnv(t)
	userRaw, _ := testutil.SeedToken(t, env.db, env.user())
	adminRaw, _ := testutil.SeedToken(t, env.db, env.admin())

	tests := []struct {
		name         string
		method, path string
		token        string
		want         int
	}{
		{"user on a user route", http.MethodGet, "/v1/exercises", userRaw, 200},
		{"admin on a user route", http.MethodGet, "/v1/exercises", adminRaw, 403},
		{"admin on list-tokens", http.MethodGet, "/v1/auth/tokens", adminRaw, 403},
		{"user on list-tokens", http.MethodGet, "/v1/auth/tokens", userRaw, 200},
		{"user on an admin route", http.MethodPost, "/v1/auth/register", userRaw, 403},
		{"admin on an admin route", http.MethodPost, "/v1/auth/register", adminRaw, 200},
		{"admin on logout", http.MethodPost, "/v1/auth/logout", adminRaw, 200},
		{"user on logout", http.MethodPost, "/v1/auth/logout", userRaw, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(tc.method, tc.path, "Bearer "+tc.token)
			if tc.want == 403 {
				apitest.RequireError(t, rec, http.StatusForbidden, "forbidden")
				if rec.Header().Get("WWW-Authenticate") != "" {
					t.Error("a 403 must not carry a Bearer challenge")
				}
				return
			}
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
	// A wrong role is only decided after the token is valid: without one it is a 401.
	authRequireUnauthorized(t, env.do(http.MethodPost, "/v1/auth/register", ""))
}

func TestAuthenticatorSeesAdminPrincipal(t *testing.T) {
	env := newAuthEnv(t)
	adminID := env.admin()
	raw, tokenID := testutil.SeedToken(t, env.db, adminID)

	rec := env.do(http.MethodPost, "/v1/auth/logout", "Bearer "+raw)
	seen := authDecodeSeen(t, rec)
	if seen.UserID != adminID.String() || seen.Role != "admin" || seen.TokenID != tokenID.String() {
		t.Errorf("principal = %+v", seen)
	}
}

// Revocation and expiry are checked on every request: nothing is cached.
func TestAuthenticatorFollowsRevocationAndExpiryImmediately(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	tokens := store.NewAuthTokens(env.db)
	raw, tokenID := testutil.SeedToken(t, env.db, uid, testutil.WithTokenExpiresAt(env.clk.Now().Add(2*time.Hour)))

	if rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw); rec.Code != 200 {
		t.Fatalf("before: status = %d", rec.Code)
	}
	// Time passes: the token expires.
	env.clk.Advance(2 * time.Hour)
	authRequireUnauthorized(t, env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw))
	env.clk.Advance(-time.Hour)
	if rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw); rec.Code != 200 {
		t.Fatalf("back in time: status = %d", rec.Code)
	}

	// Logout: the next request with the same token fails.
	if n, err := tokens.RevokeByID(t.Context(), uid, tokenID, env.clk.Now()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	authRequireUnauthorized(t, env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw))
}

func TestAuthenticatorTouchesLastUsedAtMostHourly(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	raw, tokenID := testutil.SeedToken(t, env.db, uid)
	get := func() int { return int(env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw).Code) }

	if lu := authState(t, env.db, tokenID); lu != nil {
		t.Fatalf("seeded last_used_at = %v", lu)
	}

	first := env.clk.Now()
	if get() != 200 {
		t.Fatal("first request failed")
	}
	if lu := authState(t, env.db, tokenID); lu == nil || !lu.Equal(first) {
		t.Fatalf("last_used_at = %v after the first request, want %v", lu, first)
	}
	if env.tokens.touches.Load() != 1 {
		t.Fatalf("%d touches after the first request, want 1", env.tokens.touches.Load())
	}

	// Within the hour: many requests, no further write, not even attempted.
	// Cumulative offsets: 1s, 10m1s, 59m59s.
	for _, d := range []time.Duration{time.Second, 10 * time.Minute, 49*time.Minute + 58*time.Second} {
		env.clk.Advance(d)
		if get() != 200 {
			t.Fatal("request failed")
		}
	}
	if env.tokens.touches.Load() != 1 {
		t.Errorf("%d touches within the hour, want still 1", env.tokens.touches.Load())
	}
	if lu := authState(t, env.db, tokenID); !lu.Equal(first) {
		t.Errorf("last_used_at moved to %v within the hour", lu)
	}

	// An hour after the recorded use: touched once more.
	env.clk.Set(first.Add(time.Hour))
	if get() != 200 {
		t.Fatal("request failed")
	}
	if env.tokens.touches.Load() != 2 {
		t.Errorf("%d touches after an hour, want 2", env.tokens.touches.Load())
	}
	if lu := authState(t, env.db, tokenID); !lu.Equal(first.Add(time.Hour)) {
		t.Errorf("last_used_at = %v, want %v", lu, first.Add(time.Hour))
	}
	get()
	if env.tokens.touches.Load() != 2 {
		t.Errorf("second request in the same instant touched again")
	}
}

func TestAuthenticatorTouchDecisionUsesTheLookedUpValue(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	now := env.clk.Now()
	recent, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenLastUsedAt(now.Add(-59*time.Minute)))
	stale, staleID := testutil.SeedToken(t, env.db, uid, testutil.WithTokenLastUsedAt(now.Add(-61*time.Minute)))

	env.do(http.MethodGet, "/v1/exercises", "Bearer "+recent)
	if got := env.tokens.touches.Load(); got != 0 {
		t.Errorf("a token used 59 minutes ago was touched (%d)", got)
	}
	env.do(http.MethodGet, "/v1/exercises", "Bearer "+stale)
	if got := env.tokens.touches.Load(); got != 1 {
		t.Errorf("a token used 61 minutes ago was touched %d times, want 1", got)
	}
	if lu := authState(t, env.db, staleID); !lu.Equal(now) {
		t.Errorf("last_used_at = %v, want %v", lu, now)
	}

	// Rejected requests never touch anything.
	revoked, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenRevoked(now.Add(-time.Minute)))
	env.do(http.MethodGet, "/v1/exercises", "Bearer "+revoked)
	if got := env.tokens.touches.Load(); got != 1 {
		t.Errorf("a revoked token was touched (%d touches)", got)
	}
}

func TestAuthenticatorTouchFailureDoesNotFailTheRequest(t *testing.T) {
	env := newAuthEnv(t)
	raw, tokenID := testutil.SeedToken(t, env.db, env.user())
	env.tokens.touchErr = errors.New("connection reset by peer")

	rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even though the touch failed; body: %s", rec.Code, rec.Body)
	}
	entry := env.logs.Find(t, "auth: could not record token use")
	if entry["level"] != "WARN" || entry["token_id"] != tokenID.String() || entry["request_id"] == nil {
		t.Errorf("log entry = %v, want a WARN with token_id and request_id", entry)
	}
	if strings.Contains(env.logs.String(), raw) {
		t.Error("the raw token was logged")
	}
}

// An outage is a 500, never a 401: clients must not conclude they were logged out.
func TestAuthenticatorLookupFailureIsAnInternalError(t *testing.T) {
	env := newAuthEnv(t)
	raw, _ := testutil.SeedToken(t, env.db, env.user())
	env.tokens.lookupErr = errors.New("dial tcp 127.0.0.1:5432: connection refused")

	rec := env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw)
	apitest.RequireError(t, rec, http.StatusInternalServerError, "internal")
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("a 500 must not carry a Bearer challenge")
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("internal error text leaked to the client: %s", rec.Body)
	}
	if env.probes.Load() != 0 {
		t.Error("the handler ran without authentication")
	}
	if !strings.Contains(env.logs.String(), "connection refused") {
		t.Error("the failure was not logged")
	}
}

// Tokens are secrets: no response, header or log line may carry one, whatever
// the outcome of the request.
func TestTokensNeverAppearInResponsesOrLogs(t *testing.T) {
	env := newAuthEnv(t, authWithRateLimit())
	uid := env.user()
	now := env.clk.Now()

	secrets := map[string]string{}
	add := func(name, raw string) string { secrets[name] = raw; return "Bearer " + raw }
	valid := add("valid", func() string { r, _ := testutil.SeedToken(t, env.db, uid); return r }())
	expired := add("expired", func() string {
		r, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenExpiresAt(now.Add(-time.Hour)))
		return r
	}())
	revoked := add("revoked", func() string {
		r, _ := testutil.SeedToken(t, env.db, uid, testutil.WithTokenRevoked(now.Add(-time.Hour)))
		return r
	}())
	unknownRaw, _, _ := auth.NewToken()
	unknown := add("unknown", unknownRaw)
	forbidden := add("forbidden", func() string { r, _ := testutil.SeedToken(t, env.db, env.admin()); return r }())
	// Malformed values that contain a real token inside them.
	wrapped := "Bearer  " + secrets["valid"]
	junk := "Basic " + secrets["valid"]

	requests := []struct{ method, path, header string }{
		{"GET", "/v1/exercises", valid},
		{"GET", "/v1/exercises", valid},
		{"GET", "/v1/exercises", expired},
		{"GET", "/v1/exercises", revoked},
		{"GET", "/v1/exercises", unknown},
		{"GET", "/v1/exercises", forbidden},
		{"GET", "/v1/exercises", wrapped},
		{"GET", "/v1/exercises", junk},
		{"POST", "/v1/auth/logout", valid},
	}
	env.tokens.touchErr = errors.New("touch failed")
	for i := 0; i < 15; i++ { // also runs into the rate limiter
		requests = append(requests, struct{ method, path, header string }{"POST", "/v1/auth/logout", unknown})
	}

	var responses strings.Builder
	for _, rq := range requests {
		rec := env.do(rq.method, rq.path, rq.header)
		responses.WriteString(rec.Body.String())
		for k, v := range rec.Header() {
			responses.WriteString(k + ": " + strings.Join(v, ",") + "\n")
		}
	}
	env.tokens.touchErr = nil
	env.tokens.lookupErr = errors.New("db down")
	rec := env.do("GET", "/v1/exercises", valid)
	responses.WriteString(rec.Body.String())

	everything := responses.String() + "\n" + env.logs.String()
	for name, raw := range secrets {
		body := strings.TrimPrefix(raw, domain.TokenPrefix)
		for _, needle := range []string{raw, body} {
			if strings.Contains(everything, needle) {
				t.Errorf("the %s token appears in a response or a log line", name)
			}
		}
	}
	for _, line := range env.logs.Entries(t) {
		for key := range line {
			if strings.EqualFold(key, "authorization") || strings.EqualFold(key, "token") {
				t.Errorf("log entry has a %q attribute: %v", key, line)
			}
		}
	}
	if !strings.Contains(env.logs.String(), `"msg":"Request = method:`) {
		t.Fatal("no access log lines were captured; the check above proves nothing")
	}
}

func TestAccessLogNamesTheAuthenticatedUser(t *testing.T) {
	env := newAuthEnv(t)
	uid := env.user()
	raw, _ := testutil.SeedToken(t, env.db, uid)

	env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw)
	env.do(http.MethodGet, "/v1/exercises", "Bearer "+raw+"x")

	var withUser, withoutUser int
	for _, e := range env.logs.Entries(t) {
		msg, _ := e["msg"].(string)
		if !strings.HasPrefix(msg, "Request = method:GET, uri:/v1/exercises") {
			continue
		}
		if e["user_id"] == uid.String() {
			withUser++
		} else if _, has := e["user_id"]; !has {
			withoutUser++
		}
	}
	if withUser != 1 || withoutUser != 1 {
		t.Errorf("access log: %d entries with the user, %d 401 entries without; want 1 and 1\n%s", withUser, withoutUser, env.logs)
	}
}
