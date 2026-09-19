package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/handlers"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

// The exercise handler tests drive the real router (httpapi.NewRouter) with the
// real service and a real database. Authentication is a fake that trusts two
// test headers, so nothing here depends on the auth task. The handlers package
// must not import store (TestHandlersDoNotImportStore also scans test files),
// so the database is only ever used through the untyped-by-name service.Deps.

const (
	exerciseAPIBaseURL = "https://api.example.com"
	exerciseAPIUserHdr = "X-Test-User"
	exerciseAPIRoleHdr = "X-Test-Role"
)

// exerciseAPIT0 is the time of the fake clock: the created_at of the spec's
// examples.
var exerciseAPIT0 = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

type exerciseAPI struct {
	t    *testing.T
	h    http.Handler
	deps service.Deps
	clk  *clock.Fake
	// user and admin are seeded accounts; user is the default caller.
	user, admin uuid.UUID
}

func exerciseNewAPI(t *testing.T) *exerciseAPI {
	t.Helper()
	env := map[string]string{
		"APP_ENV":        "development",
		"DATABASE_URL":   "postgres://unused@localhost/unused",
		"MEDIA_DIR":      t.TempDir(),
		"MEDIA_BASE_URL": exerciseAPIBaseURL,
		"HTTP_ADDR":      "127.0.0.1:0",
	}
	cfg, err := config.Parse(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clk := clock.NewFake(exerciseAPIT0)
	deps := service.Deps{DB: testutil.NewDB(t), Clock: clk, Config: cfg, Logger: logger}

	h := handlers.NewExercises(service.NewExercises(deps), handlers.Deps{Config: cfg, Logger: logger, Clock: clk})
	router := httpapi.NewRouter(httpapi.RouterConfig{
		Handlers: httpapi.Handlers{
			ListExercises:  h.List,
			CreateExercise: h.Create,
			UpdateExercise: h.Update,
			DeleteExercise: h.Delete,
		},
		Authenticator: exerciseAPIFakeAuth,
		Logger:        logger,
	})

	user, _ := testutil.SeedUser(t, deps.DB, domain.RoleUser)
	admin, _ := testutil.SeedUser(t, deps.DB, domain.RoleAdmin)
	return &exerciseAPI{t: t, h: router, deps: deps, clk: clk, user: user, admin: admin}
}

// exerciseAPIFakeAuth builds the principal from the test headers and answers
// 401 when there is no role header.
func exerciseAPIFakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get(exerciseAPIRoleHdr)
		if role == "" {
			render.WriteError(w, r, domain.NewUnauthorized())
			return
		}
		p := domain.Principal{UserID: uuid.MustParse(r.Header.Get(exerciseAPIUserHdr)), Role: domain.Role(role)}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

// exerciseCaller is who sends a request.
type exerciseCaller struct {
	a    *exerciseAPI
	user uuid.UUID
	role domain.Role
	anon bool
}

func (a *exerciseAPI) asUser() exerciseCaller {
	return exerciseCaller{a: a, user: a.user, role: domain.RoleUser}
}
func (a *exerciseAPI) asAdmin() exerciseCaller {
	return exerciseCaller{a: a, user: a.admin, role: domain.RoleAdmin}
}
func (a *exerciseAPI) anonymous() exerciseCaller {
	return exerciseCaller{a: a, anon: true}
}

// newUser seeds another regular user and returns a caller for it.
func (a *exerciseAPI) newUser() exerciseCaller {
	id, _ := testutil.SeedUser(a.t, a.deps.DB, domain.RoleUser)
	return exerciseCaller{a: a, user: id, role: domain.RoleUser}
}

// exerciseResp is a recorded response with a few decoding helpers.
type exerciseResp struct {
	*httptest.ResponseRecorder
	t *testing.T
}

// do sends a request with a JSON body (empty for none).
func (c exerciseCaller) do(method, path, body string) exerciseResp {
	c.a.t.Helper()
	return c.doAs(method, path, body, "application/json")
}

func (c exerciseCaller) doAs(method, path, body, contentType string) exerciseResp {
	c.a.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if !c.anon {
		req.Header.Set(exerciseAPIUserHdr, c.user.String())
		req.Header.Set(exerciseAPIRoleHdr, string(c.role))
	}
	rec := httptest.NewRecorder()
	c.a.h.ServeHTTP(rec, req)
	return exerciseResp{rec, c.a.t}
}

func (c exerciseCaller) get(path string) exerciseResp {
	c.a.t.Helper()
	return c.do(http.MethodGet, path, "")
}
func (c exerciseCaller) post(body string) exerciseResp {
	c.a.t.Helper()
	return c.do(http.MethodPost, "/v1/exercises", body)
}
func (c exerciseCaller) del(id string) exerciseResp {
	c.a.t.Helper()
	return c.do(http.MethodDelete, "/v1/exercises/"+id, "")
}
func (c exerciseCaller) put(id, body string) exerciseResp {
	c.a.t.Helper()
	return c.do(http.MethodPut, "/v1/exercises/"+id, body)
}

// status fails the test unless the response has the status.
func (r exerciseResp) status(want int) exerciseResp {
	r.t.Helper()
	if r.Code != want {
		r.t.Fatalf("status = %d, want %d; body: %s", r.Code, want, r.Body.String())
	}
	return r
}

// object decodes the body as a JSON object.
func (r exerciseResp) object() map[string]any {
	r.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		r.t.Fatalf("body is not a JSON object: %v: %s", err, r.Body.String())
	}
	return m
}

// items decodes a list response and returns items and next_cursor.
func (r exerciseResp) items() ([]map[string]any, any) {
	r.t.Helper()
	obj := r.object()
	raw, ok := obj["items"].([]any)
	if !ok {
		r.t.Fatalf("items is %T, want an array; body: %s", obj["items"], r.Body.String())
	}
	if len(obj) != 2 {
		r.t.Errorf("list response has keys %v, want exactly items and next_cursor", exerciseKeys(obj))
	}
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i] = v.(map[string]any)
	}
	return out, obj["next_cursor"]
}

func (r exerciseResp) names() []string {
	r.t.Helper()
	items, _ := r.items()
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it["name"].(string)
	}
	return out
}

// errorCode asserts the status and error.code and returns the error body.
func (r exerciseResp) errorCode(status int, code string) render.ErrorBody {
	r.t.Helper()
	return apitest.RequireError(r.t, r.ResponseRecorder, status, code)
}

// issues asserts a 422 with exactly these "field:issue" details, in order.
func (r exerciseResp) issues(want ...string) {
	r.t.Helper()
	body := r.errorCode(http.StatusUnprocessableEntity, "validation_failed")
	got := make([]string, len(body.Error.Details))
	for i, d := range body.Error.Details {
		got[i] = d.Field + ":" + d.Issue
	}
	if !reflect.DeepEqual(got, want) {
		r.t.Errorf("issues = %v, want %v", got, want)
	}
}

func exerciseKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// exerciseJSONEqual asserts the two JSON documents are equal, ignoring key
// order and whitespace (so it also proves the key sets are identical).
func exerciseJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got is not JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v: %s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("JSON differs.\ngot:  %s\nwant: %s", strings.TrimSpace(got), strings.TrimSpace(want))
	}
}

// exerciseBody is a valid create/update body with the given name.
func exerciseBody(name string) string {
	return `{"name":` + exerciseQuote(name) + `,"category":"strength","primary_muscle_group":"chest",` +
		`"secondary_muscle_groups":["shoulders","triceps"],"equipment":"dumbbell",` +
		`"measurement_type":"reps_weight","instructions":"Set bench to 30 degrees..."}`
}

func exerciseQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// create creates an exercise through the API and returns its id.
func (c exerciseCaller) create(name string) string {
	c.a.t.Helper()
	return c.post(exerciseBody(name)).status(http.StatusCreated).object()["id"].(string)
}

func (a *exerciseAPI) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.deps.DB.Exec(context.Background(), sql, args...); err != nil {
		a.t.Fatalf("exec %q: %v", sql, err)
	}
}
