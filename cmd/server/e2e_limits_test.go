package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

func limitsE2EApp(t *testing.T) (*app, *store.DB, *apitest.Logs) {
	t.Helper()
	db := testutil.NewDB(t)
	logger, logs := apitest.NewLogs()
	a, err := newApp(testConfig(t, devEnv(t, "postgres://unused@localhost/unused")), logger, db)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	return a, db, logs
}

func limitsE2ERequest(t *testing.T, h http.Handler, method, path, bearer string, body []byte, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", render.ContentTypeJSON)
	}
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

func limitsE2EJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return b
}

func limitsE2EFail(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()
	apitest.RequireError(t, rec, status, code)
	return rec.Body.String()
}

type e2eLimitsLoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

func TestE2ESecretsNeverInLogsOrBodies(t *testing.T) {
	a, db, logs := limitsE2EApp(t)
	h := a.Handler()

	const (
		loginPassword   = "E2ELimits-correct-horse-9F3a!"
		wrongPassword   = "E2ELimits-wrong-horse-7B1c!"
		registerPass    = "E2ELimits-register-4D8e!"
		changedPassword = "E2ELimits-changed-2A6f!"
	)

	hash, err := a.deps.Hasher.Hash(t.Context(), loginPassword)
	if err != nil {
		t.Fatalf("hash seed password: %v", err)
	}
	_, username := testutil.SeedUser(t, db, domain.RoleUser,
		testutil.WithUsername("e2elimitslogin"), testutil.WithPasswordHash(hash))
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin, testutil.WithUsername("e2elimitsadmin"))
	adminToken, _ := testutil.SeedToken(t, db, adminID)

	var secrets []string
	var errorBodies []string

	rec := limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/login", "",
		limitsE2EJSON(t, map[string]any{"username": username, "password": loginPassword, "device_name": "e2e phone"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var res e2eLimitsLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("login body is not JSON: %v: %s", err, rec.Body)
	}
	if res.AccessToken == "" || res.TokenType != "Bearer" {
		t.Fatalf("login response = %+v, want an access_token and Bearer", res)
	}
	if !strings.Contains(rec.Body.String(), res.AccessToken) {
		t.Error("login response body does not contain the returned access_token")
	}
	for k, vs := range rec.Header() {
		for _, v := range vs {
			if strings.Contains(v, res.AccessToken) {
				t.Errorf("login response header %s leaked the access token", k)
			}
		}
	}
	secrets = append(secrets, res.AccessToken, "Bearer "+res.AccessToken, loginPassword)

	rec = limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/login", "",
		limitsE2EJSON(t, map[string]any{"username": username, "password": wrongPassword}))
	errorBodies = append(errorBodies, limitsE2EFail(t, rec, http.StatusUnauthorized, "unauthorized"))
	secrets = append(secrets, wrongPassword)

	rec = limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/register", adminToken,
		limitsE2EJSON(t, map[string]any{"username": "e2elimitsreg", "password": registerPass}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, want 201; body: %s", rec.Code, rec.Body)
	}
	secrets = append(secrets, registerPass, adminToken, "Bearer "+adminToken)

	rec = limitsE2ERequest(t, h, http.MethodPut, "/v1/admin/users/e2elimitsreg/password", adminToken,
		limitsE2EJSON(t, map[string]any{"password": changedPassword}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change-password: status = %d, want 204; body: %s", rec.Code, rec.Body)
	}
	secrets = append(secrets, changedPassword)

	logs.Find(t, "http request")
	logged := logs.String()
	for _, s := range secrets {
		if strings.Contains(logged, s) {
			t.Errorf("log output contains a secret %q:\n%s", s, logged)
		}
	}
	for _, body := range errorBodies {
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("error body contains a secret %q: %s", s, body)
			}
		}
	}
}

func TestE2ESecretsErrorBodiesAreGeneric(t *testing.T) {
	a, db, _ := limitsE2EApp(t)
	h := a.Handler()

	const presented = "wt_e2eLIMITSfakeToken0000000000000000000000000000"
	rec := limitsE2ERequest(t, h, http.MethodGet, "/v1/exercises", presented, nil)
	body := limitsE2EFail(t, rec, http.StatusUnauthorized, "unauthorized")
	if strings.Contains(body, presented) {
		t.Errorf("401 body echoes the presented token: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "password") {
		t.Errorf("401 body mentions a password: %s", body)
	}

	rec = limitsE2ERequest(t, h, http.MethodGet, "/v1/exercises", "", nil)
	limitsE2EFail(t, rec, http.StatusUnauthorized, "unauthorized")

	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin, testutil.WithUsername("e2elimitsadmin"))
	adminToken, _ := testutil.SeedToken(t, db, adminID)

	longPassword := strings.Repeat("E2ELimitsPW", 12)
	if len(longPassword) <= domain.PasswordMaxBytes {
		t.Fatalf("test password is %d bytes, want more than %d", len(longPassword), domain.PasswordMaxBytes)
	}
	rec = limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/register", adminToken,
		limitsE2EJSON(t, map[string]any{"username": "e2elimitsbad", "password": longPassword}))
	errBody := apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	if strings.Contains(rec.Body.String(), longPassword) {
		t.Errorf("validation error echoes the password value: %s", rec.Body)
	}
	if len(errBody.Error.Details) == 0 || errBody.Error.Details[0].Field != "password" {
		t.Errorf("validation error details = %+v, want the password field", errBody.Error.Details)
	}
}

func TestE2ELimitsBodySize(t *testing.T) {
	a, db, _ := limitsE2EApp(t)
	h := a.Handler()

	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	token, _ := testutil.SeedToken(t, db, uid)

	general := []byte(`{"name":"` + strings.Repeat("x", domain.MaxBodyBytes) + `"}`)
	image := []byte(strings.Repeat("x", domain.MaxImageBytes+1))

	cases := []struct {
		name                string
		method, path, token string
		body                []byte
	}{
		{"create exercise over 1 MiB", http.MethodPost, "/v1/exercises", token, general},
		{"save progress over 1 MiB", http.MethodPut, pathFor("/v1/progress/{id}"), token, general},
		{"set image over 2 MiB", http.MethodPut, pathFor("/v1/exercises/{id}/image"), token, image},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := limitsE2ERequest(t, h, tc.method, tc.path, tc.token, tc.body)
			apitest.RequireError(t, rec, http.StatusRequestEntityTooLarge, "payload_too_large")
		})
	}
}

func TestE2ELimitsRateLimit(t *testing.T) {
	a, _, _ := limitsE2EApp(t)
	h := a.Handler()

	for i := range middleware.RateLimitBurst {
		rec := limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/login", "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("login request %d: status = %d, want 400 (empty body)", i+1, rec.Code)
		}
	}
	limited := limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/login", "", nil)
	limitsE2EFail(t, limited, http.StatusTooManyRequests, "rate_limited")
	if limited.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}

	other := func(r *http.Request) { r.RemoteAddr = "198.51.100.9:4567" }
	rec := limitsE2ERequest(t, h, http.MethodPost, "/v1/auth/login", "", nil, other)
	apitest.RequireError(t, rec, http.StatusBadRequest, "bad_request")

	for i := range 3 * middleware.RateLimitBurst {
		rec := limitsE2ERequest(t, h, http.MethodGet, "/v1/progress", "", nil)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("data request %d was rate limited, want no per-IP limit", i+1)
		}
		apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	}
}
