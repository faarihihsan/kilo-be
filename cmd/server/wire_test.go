package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// The smoke tests run the fully wired application (real router, stub
// handlers, real database) in-process through httptest: no port, no token.

const testRoleHeader = "X-Test-Role"

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// newTestApp wires the application on a throwaway migrated database.
func newTestApp(t *testing.T) (*app, *store.DB) {
	t.Helper()
	db := testutil.NewDB(t)
	cfg := testConfig(t, devEnv(t, "postgres://unused@localhost/unused"))
	a, err := newApp(cfg, discardLogger(), db)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	return a, db
}

// fakeAuth stands in for the token authenticator (T1): the role comes from a
// test header, no header means 401.
func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get(testRoleHeader)
		if role == "" {
			render.WriteError(w, r, domain.NewUnauthorized())
			return
		}
		p := domain.Principal{UserID: uuid.New(), Role: domain.Role(role)}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
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

func TestHealthz(t *testing.T) {
	a, _ := newTestApp(t)
	rec := do(a.Handler(), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	if !reflect.DeepEqual(body, map[string]string{"status": "ok"}) {
		t.Errorf("body = %v, want {status: ok}", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != render.ContentTypeJSON {
		t.Errorf("Content-Type = %q, want %q", ct, render.ContentTypeJSON)
	}
}

func TestHealthzUnavailableWhenDatabaseIsDown(t *testing.T) {
	a, db := newTestApp(t)
	db.Close() // pings on a closed pool fail; the test cleanup closes again, which is safe

	rec := do(a.Handler(), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	if body["status"] != "unavailable" {
		t.Errorf("body = %v, want status unavailable", body)
	}
}

func TestAllRoutesArePresent(t *testing.T) {
	// Guards the two loops below against silently iterating over nothing.
	if n := len(httpapi.Routes()); n != 22 {
		t.Fatalf("httpapi.Routes() has %d entries, want 21 endpoints + /healthz", n)
	}
}

// Without an Authenticator (T1 has not plugged one in) every protected route
// answers 401 with the challenge header, and the anonymous ones still work.
func TestProtectedRoutesRejectAnonymousRequests(t *testing.T) {
	a, _ := newTestApp(t)
	h := a.Handler()

	for _, r := range httpapi.Routes() {
		t.Run(r.Name, func(t *testing.T) {
			rec := do(h, r.Method, pathFor(r.Path), "")
			switch {
			case r.Name == "Healthz":
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}
			case r.Anonymous: // login: reaches its (stub) handler
				apitest.RequireError(t, rec, http.StatusNotImplemented, "not_implemented")
			default:
				apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("WWW-Authenticate = %q, want Bearer", got)
				}
			}
		})
	}
}

// With an authenticator that injects a principal, the role table decides:
// every role outside Route.Roles gets 403, every role inside reaches the stub
// handler (501). Iterating httpapi.Routes() keeps this in sync with the
// access matrix.
func TestRoleMatrixAcrossAllRoutes(t *testing.T) {
	a, _ := newTestApp(t)
	a.router.Authenticator = fakeAuth
	h := a.Handler()

	for _, r := range httpapi.Routes() {
		t.Run(r.Name, func(t *testing.T) {
			for _, role := range domain.Roles {
				rec := do(h, r.Method, pathFor(r.Path), string(role))
				switch {
				case r.Name == "Healthz":
					if rec.Code != http.StatusOK {
						t.Errorf("%s: status = %d, want 200", role, rec.Code)
					}
				case r.Anonymous || slices.Contains(r.Roles, role):
					apitest.RequireError(t, rec, http.StatusNotImplemented, "not_implemented")
				default:
					apitest.RequireError(t, rec, http.StatusForbidden, "forbidden")
				}
			}
		})
	}
}

func TestUnknownRoutesAnswerJSON(t *testing.T) {
	a, _ := newTestApp(t)
	h := a.Handler()

	apitest.RequireError(t, do(h, http.MethodGet, "/v1/nope", ""), http.StatusNotFound, "not_found")
	apitest.RequireError(t, do(h, http.MethodGet, "/", ""), http.StatusNotFound, "not_found")
	rec := do(h, http.MethodDelete, "/healthz", "")
	apitest.RequireError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
	if rec.Header().Get("Allow") == "" {
		t.Error("405 without Allow header")
	}
}

// The router answers 501 for a nil handler, so a route left out of newApp
// would pass every test above. This checks each httpapi.Handlers field is set
// and bound to the right handler method.
func TestAppWiresEveryHandler(t *testing.T) {
	a, _ := newTestApp(t)

	const pkg = "workout-tracker-be/internal/httpapi/handlers."
	want := map[string]string{
		"Register":            pkg + "(*Admin).Register",
		"Login":               pkg + "(*Auth).Login",
		"SaveProgress":        pkg + "(*Progress).Save",
		"GetProgress":         pkg + "(*Progress).Get",
		"ListProgress":        pkg + "(*Progress).List",
		"ListWorkoutPlans":    pkg + "(*Plans).List",
		"GetWorkoutPlan":      pkg + "(*Plans).Get",
		"ListExercises":       pkg + "(*Exercises).List",
		"CreateExercise":      pkg + "(*Exercises).Create",
		"SaveWorkoutPlan":     pkg + "(*Plans).Save",
		"Logout":              pkg + "(*Auth).Logout",
		"RevokeTokens":        pkg + "(*Auth).RevokeTokens",
		"AdminRevokeLogin":    pkg + "(*Admin).RevokeLogin",
		"AdminChangePassword": pkg + "(*Admin).ChangePassword",
		"ListTokens":          pkg + "(*Auth).ListTokens",
		"DeleteProgress":      pkg + "(*Progress).Delete",
		"DeleteWorkoutPlan":   pkg + "(*Plans).Delete",
		"UpdateExercise":      pkg + "(*Exercises).Update",
		"DeleteExercise":      pkg + "(*Exercises).Delete",
		"SetExerciseImage":    pkg + "(*ExerciseImages).Set",
		"DeleteExerciseImage": pkg + "(*ExerciseImages).Delete",
	}

	v := reflect.ValueOf(a.router.Handlers)
	typ := v.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		f := v.Field(i)
		expected, known := want[name]
		delete(want, name)
		if f.IsNil() {
			t.Errorf("Handlers.%s is not wired in newApp", name)
			continue
		}
		got := runtimeFuncName(f)
		if name == "Healthz" {
			if !strings.HasPrefix(got, "workout-tracker-be/internal/httpapi.") {
				t.Errorf("Handlers.Healthz = %s, want httpapi.HealthHandler", got)
			}
			continue
		}
		if !known {
			t.Errorf("Handlers.%s is new: add it to this test", name)
		} else if got != expected {
			t.Errorf("Handlers.%s = %s, want %s", name, got, expected)
		}
	}
	for name := range want {
		t.Errorf("Handlers has no field %s", name)
	}
}

// runtimeFuncName is the qualified name of the function behind a func value.
// A method value such as auth.Login is a compiler-made wrapper named
// "pkg.(*Auth).Login-fm".
func runtimeFuncName(f reflect.Value) string {
	return strings.TrimSuffix(runtime.FuncForPC(f.Pointer()).Name(), "-fm")
}

func TestNewServiceDepsMediaDirectory(t *testing.T) {
	db := testutil.NewDB(t)

	t.Run("development creates it", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "media")
		env := devEnv(t, "postgres://unused@localhost/unused")
		env["MEDIA_DIR"] = dir
		d, err := newServiceDeps(testConfig(t, env), discardLogger(), db)
		if err != nil {
			t.Fatalf("newServiceDeps: %v", err)
		}
		if d.Media == nil || d.Clock == nil || d.DB != db || d.Logger == nil || d.Config == nil {
			t.Errorf("Deps not complete: %+v", d)
		}
		if fi, err := os.Stat(filepath.Join(dir, "exercises")); err != nil || !fi.IsDir() {
			t.Errorf("media directory layout not created: %v", err)
		}
	})

	prodEnv := func(t *testing.T, dir string) map[string]string {
		return map[string]string{
			"APP_ENV":        "production",
			"DATABASE_URL":   "postgres://unused@localhost/unused",
			"MEDIA_DIR":      dir,
			"MEDIA_BASE_URL": "https://api.example.com",
		}
	}

	t.Run("production requires it to exist", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		_, err := newServiceDeps(testConfig(t, prodEnv(t, dir)), discardLogger(), db)
		if err == nil || !strings.Contains(err.Error(), "MEDIA_DIR") {
			t.Fatalf("err = %v, want a MEDIA_DIR error", err)
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Error("production must not create MEDIA_DIR")
		}
	})

	t.Run("production uses an existing directory", func(t *testing.T) {
		if _, err := newServiceDeps(testConfig(t, prodEnv(t, t.TempDir())), discardLogger(), db); err != nil {
			t.Fatalf("newServiceDeps: %v", err)
		}
	})
}
