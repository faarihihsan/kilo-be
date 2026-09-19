package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

// planAPI is the real plan handlers, service and store behind httpapi.NewRouter
// on a migrated database. Authentication is a fake: a request names its caller
// in two headers and the authenticator turns them into a Principal, so nothing
// here depends on the real login.
type planAPI struct {
	t      *testing.T
	router http.Handler
	logs   *apitest.Logs
	clock  *clock.Fake

	user, other, admin planCaller
	// exercises are three exercises of the master.
	exercises []uuid.UUID

	seedPlan     func(userID uuid.UUID, opts ...testutil.PlanOption) uuid.UUID
	seedExercise func(opts ...testutil.ExerciseOption) uuid.UUID
}

// planResponse is what the tests read a response from.
type planResponse = httptest.ResponseRecorder

// planCaller is who a request acts as; the zero value is anonymous.
type planCaller struct {
	id   uuid.UUID
	role domain.Role
}

var (
	planAnon = planCaller{}
	// planHTTPNow is the fake server time; client timestamps in the tests are
	// relative to it.
	planHTTPNow = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
)

const (
	planHeaderUser = "X-Test-User"
	planHeaderRole = "X-Test-Role"
)

// planFakeAuth authenticates from the test headers. A missing user is a 401.
func planFakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.Header.Get(planHeaderUser))
		if err != nil {
			render.WriteError(w, r, domain.NewUnauthorized())
			return
		}
		p := domain.Principal{UserID: id, Role: domain.Role(r.Header.Get(planHeaderRole))}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

func newPlanAPI(t *testing.T) *planAPI {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(planHTTPNow)
	logger, logs := apitest.NewLogs()

	plans := handlers.NewPlans(service.NewPlans(service.Deps{DB: db, Clock: clk}), handlers.Deps{Clock: clk, Logger: logger})
	router := httpapi.NewRouter(httpapi.RouterConfig{
		Handlers: httpapi.Handlers{
			ListWorkoutPlans:  plans.List,
			GetWorkoutPlan:    plans.Get,
			SaveWorkoutPlan:   plans.Save,
			DeleteWorkoutPlan: plans.Delete,
		},
		Authenticator: planFakeAuth,
		Logger:        logger,
	})

	a := &planAPI{t: t, router: router, logs: logs, clock: clk}
	userID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	otherID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin)
	a.user = planCaller{userID, domain.RoleUser}
	a.other = planCaller{otherID, domain.RoleUser}
	a.admin = planCaller{adminID, domain.RoleAdmin}
	a.seedExercise = func(opts ...testutil.ExerciseOption) uuid.UUID {
		return testutil.SeedExercise(t, db, userID, opts...)
	}
	a.seedPlan = func(owner uuid.UUID, opts ...testutil.PlanOption) uuid.UUID {
		// An old client time by default, so a PUT at the fake clock's time is newer.
		opts = append([]testutil.PlanOption{testutil.WithPlanClientUpdatedAt(planHTTPNow.Add(-24 * time.Hour))}, opts...)
		return testutil.SeedPlan(t, db, owner, opts...)
	}
	for range 3 {
		a.exercises = append(a.exercises, a.seedExercise())
	}
	return a
}

// do sends a request as who. A non-empty body is JSON.
func (a *planAPI) do(method, path string, who planCaller, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.doType(method, path, who, body, "application/json")
}

// doType is do with an explicit Content-Type for a non-empty body.
func (a *planAPI) doType(method, path string, who planCaller, body, contentType string) *httptest.ResponseRecorder {
	a.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if who != planAnon {
		req.Header.Set(planHeaderUser, who.id.String())
		req.Header.Set(planHeaderRole, string(who.role))
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec
}

func (a *planAPI) put(who planCaller, id uuid.UUID, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(http.MethodPut, "/v1/workout-plans/"+id.String(), who, body)
}

func (a *planAPI) get(who planCaller, id uuid.UUID) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(http.MethodGet, "/v1/workout-plans/"+id.String(), who, "")
}

func (a *planAPI) del(who planCaller, id uuid.UUID) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(http.MethodDelete, "/v1/workout-plans/"+id.String(), who, "")
}

func (a *planAPI) list(who planCaller, query string) *httptest.ResponseRecorder {
	a.t.Helper()
	path := "/v1/workout-plans"
	if query != "" {
		path += "?" + query
	}
	return a.do(http.MethodGet, path, who, "")
}

// planBody builds a save request. exercises are JSON objects (see planExercise).
func planBody(name, updatedAt string, exercises ...string) string {
	return fmt.Sprintf(`{"name":%q,"updated_at":%q,"exercises":[%s]}`, name, updatedAt, strings.Join(exercises, ","))
}

// planExercise is one exercises[] item on exercise id at a position with 3 sets;
// extra is more members, such as `,"target_reps":8`.
func planExercise(id uuid.UUID, position int, extra string) string {
	return fmt.Sprintf(`{"exercise_id":%q,"position":%d,"target_sets":3%s}`, id, position, extra)
}

// planTime is a client timestamp relative to the fake server time.
func planTime(offset time.Duration) string {
	return planHTTPNow.Add(offset).Format(time.RFC3339Nano)
}

// planJSONOf decodes a response body, keeping numbers as text.
func planJSONOf(t testing.TB, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	return m
}

func planKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }

// planRequireStatus fails unless rec has the status, with the body for context.
func planRequireStatus(t testing.TB, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
}

// planRequireOK checks a success status, the JSON content type and that the
// body is one JSON object; it returns the decoded body.
func planRequireOK(t testing.TB, rec *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	planRequireStatus(t, rec, status)
	if ct := rec.Header().Get("Content-Type"); ct != render.ContentTypeJSON {
		t.Errorf("Content-Type = %q, want %q", ct, render.ContentTypeJSON)
	}
	return planJSONOf(t, rec)
}

// planRequireNoServerErrors fails when the app logged an unexpected error, that
// is, answered any request with a 500.
func (a *planAPI) planRequireNoServerErrors() {
	a.t.Helper()
	if strings.Contains(a.logs.String(), "unhandled error") || strings.Contains(a.logs.String(), "panic") {
		a.t.Errorf("the app logged an unexpected error:\n%s", a.logs.String())
	}
}

// planDetails returns error.details of an error response as generic maps.
func planDetails(t testing.TB, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	errObj, _ := planJSONOf(t, rec)["error"].(map[string]any)
	raw, _ := errObj["details"].([]any)
	out := make([]map[string]any, len(raw))
	for i, d := range raw {
		out[i], _ = d.(map[string]any)
	}
	return out
}

// planIssueList reduces details to "field:issue" strings.
func planIssueList(t testing.TB, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out []string
	for _, d := range planDetails(t, rec) {
		field, _ := d["field"].(string)
		issue, _ := d["issue"].(string)
		out = append(out, field+":"+issue)
	}
	return out
}
