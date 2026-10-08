package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
)

const (
	testRoleHeader = "X-Test-Role"
	testUserHeader = "X-Test-User"
)

// fakeAuth stands in for the token authenticator: it builds the principal from
// test headers and answers 401 when there is no role header. record, when
// given, is told that the authenticator ran.
func fakeAuth(record func(string)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if record != nil {
				record("auth")
			}
			role := r.Header.Get(testRoleHeader)
			if role == "" {
				render.WriteError(w, r, domain.NewUnauthorized())
				return
			}
			p := domain.Principal{UserID: uuid.New(), Role: domain.Role(role)}
			if u := r.Header.Get(testUserHeader); u != "" {
				p.UserID = uuid.MustParse(u)
			}
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
		})
	}
}

// pathFor fills the wildcards of a route path.
func pathFor(p string) string {
	p = strings.ReplaceAll(p, "{id}", "0195f3a2-bbbb-7000-8000-000000000010")
	return strings.ReplaceAll(p, "{username}", "alice")
}

func do(h http.Handler, method, path, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if role != "" {
		req.Header.Set(testRoleHeader, role)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouterRoleMatrixOverAllRoutes(t *testing.T) {
	h := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: discardLogger()})

	for _, r := range Routes() {
		path := pathFor(r.Path)
		t.Run(r.Name, func(t *testing.T) {
			// anonymous
			rec := do(h, r.Method, path, "")
			if r.Anonymous {
				apitest.RequireError(t, rec, 501, "not_implemented")
			} else {
				apitest.RequireError(t, rec, 401, "unauthorized")
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("anonymous: WWW-Authenticate = %q, want Bearer", got)
				}
			}

			// each role
			for _, role := range domain.Roles {
				rec := do(h, r.Method, path, string(role))
				switch {
				case r.Anonymous || slices.Contains(r.Roles, role):
					apitest.RequireError(t, rec, 501, "not_implemented")
				default:
					apitest.RequireError(t, rec, 403, "forbidden")
					if rec.Header().Get("WWW-Authenticate") != "" {
						t.Errorf("%s: 403 must not carry WWW-Authenticate", role)
					}
				}
			}
		})
	}
}

func TestRouterMatrixMatchesConventions(t *testing.T) {
	// The role matrix of docs/api/conventions.md written out independently of
	// the table: endpoint -> [user status, admin status].
	want := map[int][2]int{
		1: {403, 501}, 2: {501, 501}, 3: {501, 403}, 4: {501, 403}, 5: {501, 403}, 6: {501, 403},
		7: {501, 403}, 8: {501, 403}, 9: {501, 403}, 10: {501, 403}, 11: {501, 501}, 12: {501, 403},
		13: {403, 501}, 14: {403, 501}, 15: {501, 403}, 16: {501, 403}, 17: {501, 403}, 18: {501, 403},
		19: {501, 403}, 20: {501, 403}, 21: {501, 403}, 0: {501, 501},
	}
	h := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: discardLogger()})
	for _, r := range Routes() {
		got := [2]int{
			do(h, r.Method, pathFor(r.Path), "user").Code,
			do(h, r.Method, pathFor(r.Path), "admin").Code,
		}
		if got != want[r.Number] {
			t.Errorf("endpoint %d %s: user/admin = %v, want %v", r.Number, r.Name, got, want[r.Number])
		}
	}
}

func TestRouterDefaultAuthenticatorRejectsEverythingNeedingAuth(t *testing.T) {
	h := NewRouter(RouterConfig{Logger: discardLogger()})
	for _, r := range Routes() {
		rec := do(h, r.Method, pathFor(r.Path), "admin") // even with a role header
		if r.Anonymous {
			apitest.RequireError(t, rec, 501, "not_implemented")
			continue
		}
		apitest.RequireError(t, rec, 401, "unauthorized")
		if rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing WWW-Authenticate: Bearer", r.Name)
		}
	}
}

func TestNotImplementedHandlers(t *testing.T) {
	h := NotImplementedHandlers()
	for _, rt := range table {
		f := *rt.field(&h)
		if f == nil {
			t.Fatalf("%s is nil", rt.Name)
		}
		rec := httptest.NewRecorder()
		f(rec, httptest.NewRequest("GET", "/x", nil))
		body := apitest.RequireError(t, rec, 501, "not_implemented")
		if len(body.Error.Details) != 0 {
			t.Errorf("%s: unexpected details", rt.Name)
		}
	}
}

func TestRouterUsesProvidedHandlersAndFillsTheRest(t *testing.T) {
	var gotPrincipal domain.Principal
	var gotID string
	h := NewRouter(RouterConfig{
		Authenticator: fakeAuth(nil),
		Logger:        discardLogger(),
		Handlers: Handlers{
			SaveProgress: func(w http.ResponseWriter, r *http.Request) {
				gotPrincipal = middleware.MustPrincipal(r.Context())
				gotID = r.PathValue("id")
				w.WriteHeader(http.StatusNoContent)
			},
			Healthz: HealthHandler(nil),
		},
	})

	uid := uuid.New()
	req := httptest.NewRequest("PUT", "/v1/progress/abc-123", nil)
	req.Header.Set(testRoleHeader, "user")
	req.Header.Set(testUserHeader, uid.String())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || gotID != "abc-123" || gotPrincipal.UserID != uid || gotPrincipal.Role != domain.RoleUser {
		t.Errorf("status %d, id %q, principal %+v", rec.Code, gotID, gotPrincipal)
	}

	if rec := do(h, "GET", "/healthz", ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("healthz: %d %s", rec.Code, rec.Body.String())
	}
	apitest.RequireError(t, do(h, "GET", "/v1/progress", "user"), 501, "not_implemented")
}

func TestRouterUnknownRouteIs404JSON(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: logger})

	for _, path := range []string{"/", "/nope", "/v1", "/v1/unknown", "/v1/progress/x/y", "/media/exercises/a/b.webp", "/v1/progress/"} {
		rec := do(h, "GET", path, "user")
		apitest.RequireError(t, rec, 404, "not_found")
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: 404 without X-Request-Id", path)
		}
	}
	notFound := 0
	for _, e := range logs.Entries(t) {
		if e["status"] == float64(404) {
			notFound++
		}
	}
	if notFound == 0 {
		t.Errorf("no 404 response was access-logged:\n%s", logs.String())
	}
}

func TestRouterWrongMethodIs405JSONWithAllow(t *testing.T) {
	h := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: discardLogger()})
	tests := []struct {
		method, path string
		wantAllow    []string
	}{
		{"DELETE", "/v1/progress", []string{"GET"}},
		{"PATCH", "/v1/progress/0195f3a2-bbbb-7000-8000-000000000010", []string{"GET", "PUT", "DELETE"}},
		{"GET", "/v1/auth/login", []string{"POST"}},
		{"POST", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", []string{"PUT", "DELETE"}},
		{"POST", "/healthz", []string{"GET"}},
		{"GET", "/v1/admin/users/alice/password", []string{"PUT"}},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := do(h, tc.method, tc.path, "user")
			body := apitest.RequireError(t, rec, 405, "method_not_allowed")
			if body.Error.Message == "" {
				t.Error("empty message")
			}
			allow := rec.Header().Get("Allow")
			if allow == "" {
				t.Fatal("no Allow header")
			}
			for _, m := range tc.wantAllow {
				if !slices.Contains(strings.Split(allow, ", "), m) {
					t.Errorf("Allow = %q, missing %s", allow, m)
				}
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("405 without X-Request-Id")
			}
		})
	}
}

func TestRouter401Shape(t *testing.T) {
	h := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: discardLogger()})
	rec := do(h, "GET", "/v1/progress", "")
	body := apitest.RequireError(t, rec, 401, "unauthorized")
	if rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
	}
	if len(body.Error.Details) != 0 {
		t.Errorf("details = %v", body.Error.Details)
	}
}

// --- pipeline order ---

type order struct{ steps []string }

func (o *order) add(s string) { o.steps = append(o.steps, s) }

func (o *order) mw(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o.add(name)
			next.ServeHTTP(w, r)
		})
	}
}

func recordingHandler(o *order, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o.add("handler")
		w.WriteHeader(status)
	}
}

func TestRouterPipelineOrder(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   []string
	}{
		{"flagged authenticated route", "POST", "/v1/auth/register", []string{"ratelimit", "auth", "handler"}},
		{"flagged authenticated route (logout)", "POST", "/v1/auth/logout", []string{"ratelimit", "auth", "handler"}},
		{"flagged anonymous route", "POST", "/v1/auth/login", []string{"ratelimit", "handler"}},
		{"unflagged authenticated route", "GET", "/v1/progress", []string{"auth", "handler"}},
		{"unflagged anonymous route", "GET", "/healthz", []string{"handler"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var o order
			h := okHandlers(&o)
			r := NewRouter(RouterConfig{
				Handlers:      h,
				Authenticator: fakeAuth(o.add),
				RateLimit:     o.mw("ratelimit"),
				Logger:        discardLogger(),
			})
			// Admin may call register, user may call the rest; both may call logout.
			role := "user"
			if tc.path == "/v1/auth/register" {
				role = "admin"
			}
			rec := do(r, tc.method, tc.path, role)
			if rec.Code != 200 {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if !slices.Equal(o.steps, tc.want) {
				t.Errorf("steps = %v, want %v", o.steps, tc.want)
			}
		})
	}
}

// okHandlers answers 200 from every route and records "handler".
func okHandlers(o *order) Handlers {
	var h Handlers
	for _, rt := range table {
		*rt.field(&h) = recordingHandler(o, 200)
	}
	return h
}

func TestRouterRateLimitRunsBeforeAuthentication(t *testing.T) {
	var o order
	r := NewRouter(RouterConfig{
		Handlers:      okHandlers(&o),
		Authenticator: fakeAuth(o.add),
		Logger:        discardLogger(),
		RateLimit: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				o.add("ratelimit")
				render.WriteError(w, r, domain.NewRateLimited(30*time.Second))
			})
		},
	})
	rec := do(r, "POST", "/v1/auth/logout", "user")
	apitest.RequireError(t, rec, 429, "rate_limited")
	if rec.Header().Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if !slices.Equal(o.steps, []string{"ratelimit"}) {
		t.Errorf("steps = %v; the limiter must stop the request before auth and the handler", o.steps)
	}

	// Unflagged routes never see the limiter.
	o.steps = nil
	if rec := do(r, "GET", "/v1/progress", "user"); rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !slices.Equal(o.steps, []string{"auth", "handler"}) {
		t.Errorf("steps = %v", o.steps)
	}
}

func TestRouterAuthAndRolesRunBeforeTheHandler(t *testing.T) {
	var o order
	r := NewRouter(RouterConfig{Handlers: okHandlers(&o), Authenticator: fakeAuth(o.add), Logger: discardLogger()})

	do(r, "GET", "/v1/progress", "")      // 401 from the authenticator
	do(r, "GET", "/v1/progress", "admin") // 403 from RequireRoles
	if slices.Contains(o.steps, "handler") {
		t.Errorf("handler ran for a rejected request: %v", o.steps)
	}
	if !slices.Equal(o.steps, []string{"auth", "auth"}) {
		t.Errorf("steps = %v", o.steps)
	}
}

func TestRouterBodyLimitRunsFirst(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		role       string
		size       int
		streamed   bool
		wantStatus int
	}{
		{"1 MiB is accepted", "POST", "/v1/exercises", "user", domain.MaxBodyBytes, false, 200},
		{"1 MiB + 1 is rejected on Content-Length", "POST", "/v1/exercises", "user", domain.MaxBodyBytes + 1, false, 413},
		{"1 MiB + 1 is rejected when streamed", "POST", "/v1/exercises", "user", domain.MaxBodyBytes + 1, true, 413},
		{"progress limit is 1 MiB", "PUT", "/v1/progress/0195f3a2-bbbb-7000-8000-000000000010", "user", domain.MaxBodyBytes + 1, false, 413},
		{"image route accepts 2 MiB", "PUT", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", "user", domain.MaxImageBytes, false, 200},
		{"image route accepts 2 MiB streamed", "PUT", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", "user", domain.MaxImageBytes, true, 200},
		{"image route rejects 2 MiB + 1", "PUT", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", "user", domain.MaxImageBytes + 1, false, 413},
		{"image route rejects 2 MiB + 1 streamed", "PUT", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", "user", domain.MaxImageBytes + 1, true, 413},
		{"the other image route method keeps 1 MiB", "DELETE", "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image", "user", domain.MaxBodyBytes + 1, false, 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var o order
			readBody := func(w http.ResponseWriter, r *http.Request) {
				o.add("handler")
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					render.WriteError(w, r, err)
					return
				}
			}
			var h Handlers
			for _, rt := range table {
				*rt.field(&h) = readBody
			}
			router := NewRouter(RouterConfig{Handlers: h, Authenticator: fakeAuth(o.add), RateLimit: o.mw("ratelimit"), Logger: discardLogger()})

			var body io.Reader = bytes.NewReader(make([]byte, tc.size))
			if tc.streamed {
				body = struct{ io.Reader }{body}
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set(testRoleHeader, tc.role)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if tc.wantStatus == 200 {
				if rec.Code != 200 {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
				return
			}
			apitest.RequireError(t, rec, 413, "payload_too_large")
			if !tc.streamed && slices.Contains(o.steps, "handler") {
				t.Errorf("Content-Length over the limit must be rejected before the handler: %v", o.steps)
			}
		})
	}
}

func TestRouterBodyLimitBeforeRateLimitAndAuth(t *testing.T) {
	var o order
	r := NewRouter(RouterConfig{Handlers: okHandlers(&o), Authenticator: fakeAuth(o.add), RateLimit: o.mw("ratelimit"), Logger: discardLogger()})

	req := httptest.NewRequest("POST", "/v1/auth/register", bytes.NewReader(make([]byte, domain.MaxBodyBytes+1)))
	rec := httptest.NewRecorder() // no credentials at all
	r.ServeHTTP(rec, req)

	apitest.RequireError(t, rec, 413, "payload_too_large")
	if len(o.steps) != 0 {
		t.Errorf("steps = %v; body limit must come before rate limit, auth and handler", o.steps)
	}
}

func TestRouterRecoversPanicsAndKeepsServing(t *testing.T) {
	logger, logs := apitest.NewLogs()
	var h Handlers
	h.SaveProgress = func(http.ResponseWriter, *http.Request) { panic("db exploded: password=hunter2") }
	router := NewRouter(RouterConfig{Handlers: h, Authenticator: fakeAuth(nil), Logger: logger})

	uid := uuid.New()
	req := httptest.NewRequest("PUT", "/v1/progress/0195f3a2-bbbb-7000-8000-000000000010", nil)
	req.Header.Set(testRoleHeader, "user")
	req.Header.Set(testUserHeader, uid.String())
	req.Header.Set(middleware.RequestIDHeader, "rid-panic")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	apitest.RequireError(t, rec, 500, "internal")
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("panic value leaked: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") != "rid-panic" {
		t.Errorf("X-Request-Id = %q", rec.Header().Get("X-Request-Id"))
	}

	if e := logs.Find(t, "panic recovered"); e["request_id"] != "rid-panic" || !strings.Contains(e["panic"].(string), "hunter2") {
		t.Errorf("panic log = %v", e)
	}
	// The access log still gets its lines, as a 500, with the user.
	var reqEntry, respEntry map[string]any
	for _, e := range logs.Entries(t) {
		msg, _ := e["msg"].(string)
		switch {
		case strings.HasPrefix(msg, "Request = method:PUT"):
			reqEntry = e
		case strings.HasPrefix(msg, "Response = time:"):
			respEntry = e
		}
	}
	if reqEntry == nil || reqEntry["request_id"] != "rid-panic" || reqEntry["user_id"] != uid.String() {
		t.Errorf("access request log = %v", reqEntry)
	}
	if respEntry == nil || respEntry["status"] != float64(500) || respEntry["level"] != "ERROR" {
		t.Errorf("access response log = %v", respEntry)
	}

	apitest.RequireError(t, do(router, "GET", "/v1/progress", "user"), 501, "not_implemented")
}

func TestRouterRequestIDAndAccessLog(t *testing.T) {
	logger, logs := apitest.NewLogs()
	router := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: logger})

	// generated when missing
	rec := do(router, "GET", "/v1/progress", "user")
	generated := rec.Header().Get("X-Request-Id")
	if _, err := uuid.Parse(generated); err != nil {
		t.Errorf("generated X-Request-Id %q: %v", generated, err)
	}

	// propagated, and secrets stay out of the log
	req := httptest.NewRequest("POST", "/v1/auth/login?password=q-secret", strings.NewReader(`{"password":"body-secret"}`))
	req.Header.Set("Authorization", "Bearer wt_header-secret")
	req.Header.Set(middleware.RequestIDHeader, "client-id-1")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != "client-id-1" {
		t.Errorf("inbound id not kept: %q", rec.Header().Get("X-Request-Id"))
	}

	entries := logs.Entries(t)
	if len(entries) != 4 {
		t.Fatalf("got %d log lines:\n%s", len(entries), logs.String())
	}
	if entries[0]["request_id"] != generated || entries[0]["msg"] != "Request = method:GET, uri:/v1/progress" {
		t.Errorf("first request = %v", entries[0])
	}
	if entries[1]["request_id"] != generated || entries[1]["status"] != float64(501) {
		t.Errorf("first response = %v", entries[1])
	}
	if entries[2]["request_id"] != "client-id-1" || entries[2]["msg"] != "Request = method:POST, uri:/v1/auth/login?password=***" {
		t.Errorf("second request = %v", entries[2])
	}
	for _, secret := range []string{"q-secret", "body-secret", "header-secret", "Bearer"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}

func TestRouterUnexpectedHandlerErrorIsLoggedNotLeaked(t *testing.T) {
	logger, logs := apitest.NewLogs()
	var h Handlers
	h.ListProgress = func(w http.ResponseWriter, r *http.Request) {
		render.WriteError(w, r, context.DeadlineExceeded) // stands in for any unexpected error
	}
	router := NewRouter(RouterConfig{Handlers: h, Authenticator: fakeAuth(nil), Logger: logger})
	req := httptest.NewRequest("GET", "/v1/progress", nil)
	req.Header.Set(testRoleHeader, "user")
	req.Header.Set(middleware.RequestIDHeader, "rid-9")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	apitest.RequireError(t, rec, 500, "internal")
	if strings.Contains(rec.Body.String(), "deadline") {
		t.Errorf("leaked: %s", rec.Body.String())
	}
	e := logs.Find(t, "unhandled error")
	if e["request_id"] != "rid-9" || !strings.Contains(e["error"].(string), "deadline") {
		t.Errorf("entry = %v", e)
	}
}

func TestRouterHandlerSeesOnlyItsOwnPathValues(t *testing.T) {
	var got string
	var h Handlers
	h.AdminChangePassword = func(w http.ResponseWriter, r *http.Request) {
		got = r.PathValue("username")
		w.WriteHeader(http.StatusNoContent)
	}
	router := NewRouter(RouterConfig{Handlers: h, Authenticator: fakeAuth(nil), Logger: discardLogger()})
	if rec := do(router, "PUT", "/v1/admin/users/bob_1/password", "admin"); rec.Code != 204 || got != "bob_1" {
		t.Errorf("status %d, username %q", rec.Code, got)
	}
}

func TestRouterHeadOnGetRoute(t *testing.T) {
	router := NewRouter(RouterConfig{Handlers: Handlers{Healthz: HealthHandler(nil)}, Logger: discardLogger()})
	rec := do(router, "HEAD", "/healthz", "")
	if rec.Code != 200 {
		t.Errorf("HEAD /healthz = %d", rec.Code)
	}
}

func TestRouterServesConcurrentlyUnderRace(t *testing.T) {
	router := NewRouter(RouterConfig{Authenticator: fakeAuth(nil), Logger: discardLogger()})
	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				role := []string{"", "user", "admin"}[(i+j)%3]
				do(router, "GET", "/v1/progress/"+strconv.Itoa(j), role)
				do(router, "GET", "/nope", role)
			}
		}()
	}
	for range 8 {
		<-done
	}
}
