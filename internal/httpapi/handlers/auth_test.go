package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/handlers"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

// authAPINow is the fake instant the auth/admin handler tests run at.
var authAPINow = time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)

// authAPI runs the real auth and admin routes behind the real router on a
// throwaway database. Authentication is a fake that trusts test headers, so
// these tests do not depend on the login flow they also test.
//
// The database is only reached through closures, so this file (like the other
// handler tests) does not import the store package.
type authAPI struct {
	h         http.Handler
	clk       *clock.Fake
	seedUser  func(t testing.TB, username, password string, role domain.Role) uuid.UUID
	seedToken func(t testing.TB, userID uuid.UUID, opts ...testutil.TokenOption) (string, uuid.UUID)
	revokedAt func(t testing.TB, tokenID uuid.UUID) *time.Time
	userBy    func(t testing.TB, username string) (uuid.UUID, domain.Role)
}

func newAuthAPI(t *testing.T) *authAPI {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(authAPINow)
	hasher, err := auth.NewHasher(auth.HasherConfig{
		MemoryKiB:     8192,
		Time:          1,
		Parallelism:   1,
		MaxConcurrent: 4,
	})
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	deps := service.Deps{
		DB:      db,
		Clock:   clk,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Hasher:  hasher,
		Limiter: auth.NewLoginLimiter(clk, auth.LoginLimiterConfig{MaxFailsUser: 3, MaxFailsIP: 20}),
	}
	authH := handlers.NewAuth(service.NewAuth(deps), handlers.Deps{})
	adminH := handlers.NewAdmin(service.NewAdmin(deps), handlers.Deps{})

	return &authAPI{
		clk: clk,
		h: httpapi.NewRouter(httpapi.RouterConfig{
			Handlers: httpapi.Handlers{
				Login:               authH.Login,
				Logout:              authH.Logout,
				RevokeTokens:        authH.RevokeTokens,
				ListTokens:          authH.ListTokens,
				Register:            adminH.Register,
				AdminRevokeLogin:    adminH.RevokeLogin,
				AdminChangePassword: adminH.ChangePassword,
			},
			Authenticator: authAPIFakeAuth,
			Logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
		}),
		seedUser: func(t testing.TB, username, password string, role domain.Role) uuid.UUID {
			t.Helper()
			hash, err := hasher.Hash(t.Context(), password)
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			id, _ := testutil.SeedUser(t, db, role,
				testutil.WithUsername(username), testutil.WithPasswordHash(hash))
			return id
		},
		seedToken: func(t testing.TB, userID uuid.UUID, opts ...testutil.TokenOption) (string, uuid.UUID) {
			t.Helper()
			return testutil.SeedToken(t, db, userID, opts...)
		},
		revokedAt: func(t testing.TB, tokenID uuid.UUID) *time.Time {
			t.Helper()
			var revoked *time.Time
			if err := db.QueryRow(t.Context(), `SELECT revoked_at FROM auth_tokens WHERE id = $1`, tokenID).Scan(&revoked); err != nil {
				t.Fatalf("read token: %v", err)
			}
			return revoked
		},
		userBy: func(t testing.TB, username string) (uuid.UUID, domain.Role) {
			t.Helper()
			var (
				id   uuid.UUID
				role string
			)
			if err := db.QueryRow(t.Context(), `SELECT id, role FROM users WHERE username = $1`, username).Scan(&id, &role); err != nil {
				t.Fatalf("read user: %v", err)
			}
			return id, domain.Role(role)
		},
	}
}

// authAPIFakeAuth builds the principal from test headers; no role means 401,
// like the real authenticator without a token.
func authAPIFakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			render.WriteError(w, r, domain.NewUnauthorized())
			return
		}
		p := domain.Principal{
			UserID: uuid.MustParse(r.Header.Get("X-Test-User")),
			Role:   domain.Role(role),
		}
		if token := r.Header.Get("X-Test-Token"); token != "" {
			p.TokenID = uuid.MustParse(token)
		}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

// do sends a request. A nil principal means an anonymous request (only the
// login route accepts one).
func (a *authAPI) do(t testing.TB, method, target, body string, p *domain.Principal) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req.Header.Set("X-Test-Role", string(p.Role))
		req.Header.Set("X-Test-User", p.UserID.String())
		if p.TokenID != uuid.Nil {
			req.Header.Set("X-Test-Token", p.TokenID.String())
		}
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func TestLoginHandlerOK(t *testing.T) {
	a := newAuthAPI(t)
	a.seedUser(t, "ihsan", "secret", domain.RoleUser)

	rec := a.do(t, http.MethodPost, "/v1/auth/login",
		`{"username":"Ihsan","password":"secret","device_name":"iPhone"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var res service.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("body: %v: %s", err, rec.Body)
	}
	if res.TokenType != "Bearer" || res.AccessToken == "" || res.TokenID == uuid.Nil {
		t.Errorf("login response = %+v", res)
	}
	if res.User.Username != "ihsan" || res.User.Role != domain.RoleUser {
		t.Errorf("user = %+v, want ihsan/user", res.User)
	}
}

func TestLoginHandlerValidation(t *testing.T) {
	a := newAuthAPI(t)
	long := strings.Repeat("d", domain.DeviceNameMaxLen+1)

	body := apitest.RequireError(t,
		a.do(t, http.MethodPost, "/v1/auth/login", `{"username":"ihsan"}`, nil),
		http.StatusUnprocessableEntity, "validation_failed")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Field != "password" || body.Error.Details[0].Issue != "required" {
		t.Errorf("details = %+v, want password required", body.Error.Details)
	}

	apitest.RequireError(t,
		a.do(t, http.MethodPost, "/v1/auth/login", `{"username":"ihsan","password":"pw","device_name":"`+long+`"}`, nil),
		http.StatusUnprocessableEntity, "validation_failed")

	apitest.RequireError(t,
		a.do(t, http.MethodPost, "/v1/auth/login", `{"username":"ihsan","password":"pw","extra":1}`, nil),
		http.StatusBadRequest, "bad_request")
}

func TestLoginHandlerBadCredentials(t *testing.T) {
	a := newAuthAPI(t)
	a.seedUser(t, "ihsan", "secret", domain.RoleUser)

	for _, body := range []string{
		`{"username":"ihsan","password":"wrong"}`,
		`{"username":"ghost","password":"wrong"}`,
	} {
		rec := a.do(t, http.MethodPost, "/v1/auth/login", body, nil)
		apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	}
}

func TestLoginHandlerRateLimitedEvenWithCorrectPassword(t *testing.T) {
	a := newAuthAPI(t)
	a.seedUser(t, "ihsan", "secret", domain.RoleUser)

	for range 3 {
		apitest.RequireError(t,
			a.do(t, http.MethodPost, "/v1/auth/login", `{"username":"ihsan","password":"wrong"}`, nil),
			http.StatusUnauthorized, "unauthorized")
	}
	rec := a.do(t, http.MethodPost, "/v1/auth/login", `{"username":"ihsan","password":"secret"}`, nil)
	apitest.RequireError(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}

func TestLogoutHandlerRevokesCurrentToken(t *testing.T) {
	a := newAuthAPI(t)
	userID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, tokenID := a.seedToken(t, userID)

	rec := a.do(t, http.MethodPost, "/v1/auth/logout", "", &domain.Principal{UserID: userID, Role: domain.RoleUser, TokenID: tokenID})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body)
	}
	if a.revokedAt(t, tokenID) == nil {
		t.Error("token was not revoked")
	}
}

func TestRevokeHandlerByIDAndScope(t *testing.T) {
	a := newAuthAPI(t)
	userID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, current := a.seedToken(t, userID)
	_, named := a.seedToken(t, userID)
	_, other := a.seedToken(t, userID)
	p := &domain.Principal{UserID: userID, Role: domain.RoleUser, TokenID: current}

	rec := a.do(t, http.MethodPost, "/v1/auth/revoke", `{"token_id":"`+named.String()+`"}`, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("by id: status = %d; body: %s", rec.Code, rec.Body)
	}
	var res service.RevokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.RevokedCount != 1 {
		t.Errorf("by id response = %+v, %v; want count 1", res, err)
	}

	rec = a.do(t, http.MethodPost, "/v1/auth/revoke", `{"scope":"others"}`, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("scope others: status = %d; body: %s", rec.Code, rec.Body)
	}
	if a.revokedAt(t, other) == nil {
		t.Error("scope others did not revoke the other token")
	}
	if a.revokedAt(t, current) != nil {
		t.Error("scope others revoked the current token")
	}

	rec = a.do(t, http.MethodPost, "/v1/auth/revoke", `{"scope":"all"}`, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("scope all: status = %d; body: %s", rec.Code, rec.Body)
	}
	if a.revokedAt(t, current) == nil {
		t.Error("scope all did not revoke the current token")
	}
}

func TestRevokeHandlerValidationAndNotFound(t *testing.T) {
	a := newAuthAPI(t)
	userID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	foreignID := a.seedUser(t, "other", "secret", domain.RoleUser)
	_, foreignToken := a.seedToken(t, foreignID)
	p := &domain.Principal{UserID: userID, Role: domain.RoleUser}

	for _, tt := range []struct {
		name string
		body string
		code int
		errc string
	}{
		{"neither", `{}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"both", `{"token_id":"` + uuid.NewString() + `","scope":"others"}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"bad scope", `{"scope":"some"}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"bad id", `{"token_id":"nope"}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"foreign id", `{"token_id":"` + foreignToken.String() + `"}`, http.StatusNotFound, "not_found"},
		{"unknown id", `{"token_id":"` + uuid.NewString() + `"}`, http.StatusNotFound, "not_found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apitest.RequireError(t, a.do(t, http.MethodPost, "/v1/auth/revoke", tt.body, p), tt.code, tt.errc)
		})
	}
	if a.revokedAt(t, foreignToken) != nil {
		t.Error("a foreign token was revoked")
	}
}

func TestListTokensHandler(t *testing.T) {
	a := newAuthAPI(t)
	userID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, first := a.seedToken(t, userID,
		testutil.WithTokenCreatedAt(authAPINow), testutil.WithTokenDevice("iPhone"))
	_, current := a.seedToken(t, userID,
		testutil.WithTokenCreatedAt(authAPINow.Add(time.Minute)), testutil.WithTokenDevice("iPad"))

	rec := a.do(t, http.MethodGet, "/v1/auth/tokens", "", &domain.Principal{UserID: userID, Role: domain.RoleUser, TokenID: current})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
	}
	var res service.TokenListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("body: %v: %s", err, rec.Body)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v, want 2", res.Items)
	}
	if res.Items[0].ID != current || !res.Items[0].Current {
		t.Errorf("items[0] = %+v, want the current token first", res.Items[0])
	}
	if res.Items[1].ID != first {
		t.Errorf("items[1] = %+v, want the older token", res.Items[1])
	}
	if strings.Contains(rec.Body.String(), "token_hash") || strings.Contains(rec.Body.String(), "wt_") {
		t.Errorf("list-tokens leaks a secret: %s", rec.Body)
	}
}
