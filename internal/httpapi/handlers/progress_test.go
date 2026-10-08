package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
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

// progressAPI is the four progress routes behind the real router on a
// throwaway database. Authentication is a fake that trusts two test headers,
// so nothing here depends on the real login.
type progressAPI struct {
	h                  http.Handler
	user, other, admin uuid.UUID
	ex                 [3]uuid.UUID
	deletedEx          uuid.UUID
	plan               uuid.UUID // the user's plan
	deletedPlan        uuid.UUID // the user's soft-deleted plan
	otherPlan          uuid.UUID // the other user's plan
	seed               func(t *testing.T, userID uuid.UUID, opts ...testutil.ProgressOption) uuid.UUID
	count              func(t *testing.T, sql string, args ...any) int
}

// progressAPINow is the fake clock the service validates updated_at against.
var progressAPINow = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

func progressAPINew(t *testing.T) *progressAPI {
	t.Helper()
	db := testutil.NewDB(t)
	a := &progressAPI{}
	a.user, _ = testutil.SeedUser(t, db, domain.RoleUser)
	a.other, _ = testutil.SeedUser(t, db, domain.RoleUser)
	a.admin, _ = testutil.SeedUser(t, db, domain.RoleAdmin)
	for i := range a.ex {
		a.ex[i] = testutil.SeedExercise(t, db, a.user)
	}
	a.deletedEx = testutil.SeedExercise(t, db, a.user, testutil.WithExerciseDeletedAt(progressAPINow.Add(-time.Hour)))
	a.plan = testutil.SeedPlan(t, db, a.user)
	a.deletedPlan = testutil.SeedPlan(t, db, a.user, testutil.WithPlanDeletedAt(progressAPINow.Add(-time.Hour)))
	a.otherPlan = testutil.SeedPlan(t, db, a.other)
	a.seed = func(t *testing.T, userID uuid.UUID, opts ...testutil.ProgressOption) uuid.UUID {
		t.Helper()
		return testutil.SeedProgress(t, db, userID, opts...)
	}
	a.count = func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", sql, err)
		}
		return n
	}

	hd := handlers.NewProgress(
		service.NewProgress(service.Deps{DB: db, Clock: clock.NewFake(progressAPINow)}),
		handlers.Deps{})
	a.h = httpapi.NewRouter(httpapi.RouterConfig{
		Handlers: httpapi.Handlers{
			SaveProgress:   hd.Save,
			GetProgress:    hd.Get,
			ListProgress:   hd.List,
			DeleteProgress: hd.Delete,
		},
		Authenticator: progressAPIFakeAuth,
		Logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	return a
}

// progressAPIFakeAuth builds the principal from X-Test-User and X-Test-Role and
// answers 401 without a role, like the real authenticator does for no token.
func progressAPIFakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			render.WriteError(w, r, domain.NewUnauthorized())
			return
		}
		p := domain.Principal{UserID: uuid.MustParse(r.Header.Get("X-Test-User")), Role: domain.Role(role)}
		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

// do sends a request as (user, role). An empty body sends none.
func (a *progressAPI) do(t testing.TB, user uuid.UUID, role domain.Role, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if role != "" {
		req.Header.Set("X-Test-Role", string(role))
		req.Header.Set("X-Test-User", user.String())
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// as is do for the test user.
func (a *progressAPI) as(t testing.TB, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return a.do(t, a.user, domain.RoleUser, method, target, body)
}

func (a *progressAPI) put(t testing.TB, id uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return a.as(t, http.MethodPut, "/v1/progress/"+id.String(), body)
}

func (a *progressAPI) get(t testing.TB, id uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return a.as(t, http.MethodGet, "/v1/progress/"+id.String(), "")
}

func progressAPINewID(t testing.TB) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// progressAPIJSON decodes a JSON body keeping numbers as their text, so
// "80.0" and "80" stay different.
func progressAPIJSON(t testing.TB, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	return v
}

func progressAPIObject(t testing.TB, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	m, ok := progressAPIJSON(t, rec.Body.Bytes()).(map[string]any)
	if !ok {
		t.Fatalf("body is not a JSON object: %s", rec.Body.String())
	}
	return m
}

// progressAPIRequire asserts the status of a success response and its JSON content type.
func progressAPIRequire(t testing.TB, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	if status != http.StatusNoContent {
		if ct := rec.Header().Get("Content-Type"); ct != render.ContentTypeJSON {
			t.Fatalf("Content-Type = %q, want %q", ct, render.ContentTypeJSON)
		}
	}
}

// progressAPIIssues returns error.details as "field: issue" strings.
func progressAPIIssues(body render.ErrorBody) []string {
	out := make([]string, len(body.Error.Details))
	for i, d := range body.Error.Details {
		out[i] = d.Field + ": " + d.Issue
	}
	return out
}

// body builds a valid save request. updatedAt is RFC 3339; edit may change the
// decoded request before it is encoded.
func (a *progressAPI) body(t testing.TB, updatedAt string, edit ...func(*domain.ProgressSaveRequest)) string {
	t.Helper()
	r := domain.ProgressSaveRequest{
		Name:            "Push Day A",
		Notes:           progressAPIPtr("Felt strong"),
		StartedAt:       "2026-09-19T08:00:00Z",
		EndedAt:         "2026-09-19T08:55:00Z",
		DurationSeconds: progressAPIPtr(3120),
		UpdatedAt:       updatedAt,
		Exercises: []domain.ProgressExerciseRequest{
			{ExerciseID: a.ex[0].String(), Position: progressAPIPtr(0), Sets: []domain.ProgressSetRequest{
				{Position: progressAPIPtr(0), Type: "warmup", Reps: progressAPIPtr(12), Weight: progressAPIDec("40.0"), Completed: progressAPIPtr(true)},
				{Position: progressAPIPtr(1), Type: "normal", Reps: progressAPIPtr(8), Weight: progressAPIDec("80.0"), RPE: progressAPIDec("8.5"), Completed: progressAPIPtr(true)},
			}},
			{ExerciseID: a.ex[1].String(), Position: progressAPIPtr(1), Notes: progressAPIPtr("plank"), Sets: []domain.ProgressSetRequest{
				{Position: progressAPIPtr(0), Type: "failure", DurationSeconds: progressAPIPtr(90), Completed: progressAPIPtr(false)},
			}},
		},
	}
	for _, e := range edit {
		e(&r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func progressAPIPtr[T any](v T) *T { return &v }

func progressAPIDec(s string) *domain.ProgressDecimal {
	return progressAPIPtr(domain.ProgressDecimal(s))
}

// specRequest is the request example of docs/api/endpoints/03-save-progress.md
// with the ids replaced by the ones seeded for the test.
func (a *progressAPI) specRequest() string {
	return fmt.Sprintf(`{
  "workout_plan_id": %q,
  "name": "Push Day A",
  "notes": "Felt strong",
  "started_at": "2026-09-19T08:00:00Z",
  "ended_at": "2026-09-19T08:55:00Z",
  "duration_seconds": 3120,
  "updated_at": "2026-09-19T08:55:02Z",
  "exercises": [
    {
      "exercise_id": %q,
      "position": 0,
      "notes": null,
      "sets": [
        {"position": 0, "type": "warmup", "reps": 12, "weight": 40.0, "duration_seconds": null,
         "distance_meters": null, "rpe": null, "completed": true},
        {"position": 1, "type": "normal", "reps": 8, "weight": 80.0, "duration_seconds": null,
         "distance_meters": null, "rpe": 8.5, "completed": true}
      ]
    }
  ]
}`, a.plan, a.ex[0])
}

// ---------------------------------------------------------------------------

// TestProgressAPIGoldenShapes: the spec examples are accepted verbatim and the
// responses have exactly the documented keys and number spellings.
func TestProgressAPIGoldenShapes(t *testing.T) {
	a := progressAPINew(t)
	id := uuid.MustParse("0195f3a2-cccc-7000-8000-000000000100")

	rec := a.put(t, id, a.specRequest())
	progressAPIRequire(t, rec, http.StatusCreated)
	got := progressAPIObject(t, rec)

	// Spec 04: the request plus id, created_at, server_updated_at, deleted_at.
	want := progressAPIJSON(t, []byte(a.specRequest())).(map[string]any)
	want["id"] = id.String()
	want["deleted_at"] = nil
	for _, k := range []string{"created_at", "server_updated_at"} {
		ts, err := time.Parse(time.RFC3339, fmt.Sprint(got[k]))
		if err != nil || !strings.HasSuffix(fmt.Sprint(got[k]), "Z") || time.Since(ts) > time.Minute {
			t.Errorf("%s = %v, want a recent RFC 3339 UTC time (%v)", k, got[k], err)
		}
		want[k] = got[k]
	}
	if got["created_at"] != got["server_updated_at"] {
		t.Errorf("created_at %v != server_updated_at %v on the first save", got["created_at"], got["server_updated_at"])
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PUT response differs from the spec shape\n got: %s\nwant: %s", progressAPIMustJSON(t, got), progressAPIMustJSON(t, want))
	}

	// GET returns the same bytes as the PUT that created the session.
	rec2 := a.get(t, id)
	progressAPIRequire(t, rec2, http.StatusOK)
	if rec2.Body.String() != rec.Body.String() {
		t.Errorf("GET =\n%s\nPUT returned\n%s", rec2.Body.String(), rec.Body.String())
	}

	t.Run("summary keys of spec 05", func(t *testing.T) {
		rec := a.as(t, http.MethodGet, "/v1/progress", "")
		progressAPIRequire(t, rec, http.StatusOK)
		page := progressAPIObject(t, rec)
		if !slices.Equal(progressAPIKeys(page), []string{"items", "next_cursor"}) || page["next_cursor"] != nil {
			t.Errorf("envelope = %v", page)
		}
		item := page["items"].([]any)[0].(map[string]any)
		wantKeys := []string{"deleted_at", "duration_seconds", "ended_at", "exercise_count", "id", "name",
			"server_updated_at", "set_count", "started_at", "updated_at", "workout_plan_id"}
		if !slices.Equal(progressAPIKeys(item), wantKeys) {
			t.Errorf("summary keys = %v, want %v", progressAPIKeys(item), wantKeys)
		}
		if item["exercise_count"].(json.Number) != "1" || item["set_count"].(json.Number) != "2" {
			t.Errorf("counts = %v, %v", item["exercise_count"], item["set_count"])
		}
		if item["id"] != id.String() || item["workout_plan_id"] != a.plan.String() || item["updated_at"] != "2026-09-19T08:55:02Z" {
			t.Errorf("summary = %v", item)
		}
	})

	t.Run("expand=exercises items are the get-progress object", func(t *testing.T) {
		rec := a.as(t, http.MethodGet, "/v1/progress?expand=exercises", "")
		progressAPIRequire(t, rec, http.StatusOK)
		items := progressAPIObject(t, rec)["items"].([]any)
		if len(items) != 1 || !reflect.DeepEqual(items[0], progressAPIJSON(t, rec2.Body.Bytes())) {
			t.Errorf("expanded items = %s\nwant [%s]", progressAPIMustJSON(t, items), rec2.Body.String())
		}
	})

	t.Run("decimals are written as numbers with at least one decimal", func(t *testing.T) {
		id := progressAPINewID(t)
		body := a.body(t, "2026-09-19T08:55:02Z", func(r *domain.ProgressSaveRequest) {
			r.Exercises[0].Sets[0].Weight = progressAPIDec("80.50")
			r.Exercises[0].Sets[1].Weight = progressAPIDec("100")
			r.Exercises[0].Sets[1].DistanceMeters = progressAPIDec("5000.25")
		})
		rec := a.put(t, id, body)
		progressAPIRequire(t, rec, http.StatusCreated)
		for _, want := range []string{`"weight":80.5,`, `"weight":100.0,`, `"distance_meters":5000.25,`, `"rpe":8.5,`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("body does not contain %s:\n%s", want, rec.Body.String())
			}
		}
	})
}

func progressAPIMustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func progressAPIKeys(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}

// ---------------------------------------------------------------------------

// TestProgressAPIConflictTable is the conflict rule of spec 03 through HTTP.
func TestProgressAPIConflictTable(t *testing.T) {
	a := progressAPINew(t)
	id := progressAPINewID(t)

	rec := a.put(t, id, a.body(t, "2026-09-19T08:55:00Z"))
	progressAPIRequire(t, rec, http.StatusCreated)
	first := progressAPIObject(t, rec)

	t.Run("equal updated_at is a 200 no-op with the stored copy", func(t *testing.T) {
		body := a.body(t, "2026-09-19T08:55:00Z", func(r *domain.ProgressSaveRequest) { r.Name = "ignored"; r.Exercises = r.Exercises[:1] })
		rec := a.put(t, id, body)
		progressAPIRequire(t, rec, http.StatusOK)
		if got := progressAPIObject(t, rec); !reflect.DeepEqual(got, first) {
			t.Errorf("no-op returned\n%s\nwant the stored copy\n%s", rec.Body.String(), progressAPIMustJSON(t, first))
		}
		if a.get(t, id).Body.String() != rec.Body.String() {
			t.Error("server_updated_at or content changed on a no-op")
		}
	})

	t.Run("older updated_at is a 409 stale with the GET shape in details[0].current", func(t *testing.T) {
		rec := a.put(t, id, a.body(t, "2026-09-19T08:54:59Z", func(r *domain.ProgressSaveRequest) { r.Name = "older" }))
		body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "stale" || body.Error.Details[0].Field != "" {
			t.Errorf("details = %+v", body.Error.Details)
		}
		details := progressAPIObject(t, rec)["error"].(map[string]any)["details"].([]any)
		current := details[0].(map[string]any)["current"]
		if want := progressAPIJSON(t, a.get(t, id).Body.Bytes()); !reflect.DeepEqual(current, want) {
			t.Errorf("current =\n%s\nwant the GET body\n%s", progressAPIMustJSON(t, current), progressAPIMustJSON(t, want))
		}
		if !slices.Equal(progressAPIKeys(current.(map[string]any)), progressAPIKeys(first)) {
			t.Errorf("current keys = %v", progressAPIKeys(current.(map[string]any)))
		}
	})

	t.Run("newer updated_at is a 200 that replaces the children and advances server_updated_at", func(t *testing.T) {
		body := a.body(t, "2026-09-19T08:56:00Z", func(r *domain.ProgressSaveRequest) {
			r.Name = "newer"
			r.Notes = nil
			r.Exercises = []domain.ProgressExerciseRequest{{ExerciseID: a.ex[2].String(), Position: progressAPIPtr(3),
				Sets: []domain.ProgressSetRequest{{Position: progressAPIPtr(0), Type: "drop", Reps: progressAPIPtr(5), Completed: progressAPIPtr(true)}}}}
		})
		rec := a.put(t, id, body)
		progressAPIRequire(t, rec, http.StatusOK)
		got := progressAPIObject(t, rec)
		if got["name"] != "newer" || got["notes"] != nil || got["updated_at"] != "2026-09-19T08:56:00Z" {
			t.Errorf("session = %s", rec.Body.String())
		}
		exs := got["exercises"].([]any)
		if len(exs) != 1 || exs[0].(map[string]any)["exercise_id"] != a.ex[2].String() ||
			len(exs[0].(map[string]any)["sets"].([]any)) != 1 {
			t.Errorf("exercises = %s", progressAPIMustJSON(t, exs))
		}
		if got["created_at"] != first["created_at"] {
			t.Errorf("created_at changed: %v -> %v", first["created_at"], got["created_at"])
		}
		before, _ := time.Parse(time.RFC3339Nano, first["server_updated_at"].(string))
		after, _ := time.Parse(time.RFC3339Nano, got["server_updated_at"].(string))
		if !after.After(before) {
			t.Errorf("server_updated_at %v not after %v", after, before)
		}
		if n := a.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); n != 1 {
			t.Errorf("%d exercise rows, want 1", n)
		}
	})

	t.Run("deleted is a 409 deleted, even for a newer updated_at, and has no current", func(t *testing.T) {
		rec := a.as(t, http.MethodDelete, "/v1/progress/"+id.String(), "")
		progressAPIRequire(t, rec, http.StatusNoContent)
		for _, at := range []string{"2026-09-19T08:00:00Z", "2026-09-19T08:56:00Z", "2026-09-19T09:04:00Z"} {
			rec := a.put(t, id, a.body(t, at))
			body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
			if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "deleted" || body.Error.Details[0].Current != nil {
				t.Errorf("updated_at %s: details = %+v", at, body.Error.Details)
			}
		}
	})

	t.Run("a new id is a 201 again", func(t *testing.T) {
		progressAPIRequire(t, a.put(t, progressAPINewID(t), a.body(t, "2026-09-19T08:55:00Z")), http.StatusCreated)
	})
}

// TestProgressAPISaveStatuses: every status of the spec 03 response table.
func TestProgressAPISaveStatuses(t *testing.T) {
	a := progressAPINew(t)
	valid := a.body(t, "2026-09-19T08:55:02Z")
	path := func(id uuid.UUID) string { return "/v1/progress/" + id.String() }

	t.Run("201 then 200", func(t *testing.T) {
		id := progressAPINewID(t)
		progressAPIRequire(t, a.put(t, id, valid), http.StatusCreated)
		progressAPIRequire(t, a.put(t, id, a.body(t, "2026-09-19T08:55:03Z")), http.StatusOK)
	})

	t.Run("400 bad request", func(t *testing.T) {
		big := `{"notes":"` + strings.Repeat("a", domain.MaxBodyBytes) + `"}`
		tests := []struct {
			name   string
			target string
			body   string
			status int
			code   string
		}{
			{"malformed json", path(progressAPINewID(t)), `{"name":`, 400, "bad_request"},
			{"not json", path(progressAPINewID(t)), `hello`, 400, "bad_request"},
			{"trailing data", path(progressAPINewID(t)), valid + valid, 400, "bad_request"},
			{"unknown top-level field", path(progressAPINewID(t)), strings.Replace(valid, `"name"`, `"bogus":1,"name"`, 1), 400, "bad_request"},
			{"unknown set field", path(progressAPINewID(t)), strings.Replace(valid, `"completed"`, `"extra":true,"completed"`, 1), 400, "bad_request"},
			{"server-owned field", path(progressAPINewID(t)), strings.Replace(valid, `"name"`, `"server_updated_at":"2026-09-19T08:55:02Z","name"`, 1), 400, "bad_request"},
			{"weight as string", path(progressAPINewID(t)), strings.Replace(valid, `"weight":80.0`, `"weight":"80.0"`, 1), 400, "bad_request"},
			{"reps as float", path(progressAPINewID(t)), strings.Replace(valid, `"reps":8`, `"reps":8.5`, 1), 400, "bad_request"},
			{"exercises as object", path(progressAPINewID(t)), strings.Replace(valid, `"exercises":[`, `"exercises":{"a":[`, 1), 400, "bad_request"},
			{"empty body", path(progressAPINewID(t)), ``, 400, "bad_request"},
			{"path id is not a uuid", "/v1/progress/not-a-uuid", valid, 400, "bad_request"},
			{"path id without hyphens", "/v1/progress/0195f3a2cccc70008000000000000100", valid, 400, "bad_request"},
			{"413 body over 1 MiB", path(progressAPINewID(t)), big, 413, "payload_too_large"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := a.as(t, http.MethodPut, tt.target, tt.body)
				apitest.RequireError(t, rec, tt.status, tt.code)
			})
		}
	})

	t.Run("415 wrong content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, path(progressAPINewID(t)), strings.NewReader(valid))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Test-Role", "user")
		req.Header.Set("X-Test-User", a.user.String())
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		apitest.RequireError(t, rec, http.StatusUnsupportedMediaType, "unsupported_media_type")
	})

	t.Run("401 without a token", func(t *testing.T) {
		rec := a.do(t, uuid.Nil, "", http.MethodPut, path(progressAPINewID(t)), valid)
		apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	})

	t.Run("403 for an admin", func(t *testing.T) {
		id := progressAPINewID(t)
		rec := a.do(t, a.admin, domain.RoleAdmin, http.MethodPut, path(id), valid)
		apitest.RequireError(t, rec, http.StatusForbidden, "forbidden")
		if n := a.count(t, `SELECT count(*) FROM progress WHERE id = $1`, id); n != 0 {
			t.Error("an admin wrote a session")
		}
	})

	t.Run("404 for another user's id, nothing changes", func(t *testing.T) {
		id := progressAPINewID(t)
		progressAPIRequire(t, a.put(t, id, valid), http.StatusCreated)
		before := a.get(t, id).Body.String()
		for _, at := range []string{"2026-09-19T08:55:01Z", "2026-09-19T08:55:02Z", "2026-09-19T08:59:00Z"} {
			rec := a.do(t, a.other, domain.RoleUser, http.MethodPut, path(id), a.body(t, at, func(r *domain.ProgressSaveRequest) { r.Name = "taken over" }))
			apitest.RequireError(t, rec, http.StatusNotFound, "not_found")
		}
		if after := a.get(t, id).Body.String(); after != before {
			t.Error("another user's PUT changed the session")
		}
		// The 404 is the same as for an id that never existed: no existence leak.
		foreign := a.do(t, a.other, domain.RoleUser, http.MethodPut, path(id), valid)
		unknown := a.do(t, a.other, domain.RoleUser, http.MethodGet, path(progressAPINewID(t)), "")
		if foreign.Body.String() != unknown.Body.String() {
			t.Errorf("foreign save 404 %q differs from unknown-id 404 %q", foreign.Body.String(), unknown.Body.String())
		}
	})
}

// TestProgressAPIValidation: 422 with the field paths of the spec.
func TestProgressAPIValidation(t *testing.T) {
	a := progressAPINew(t)
	unknownEx, unknownPlan := progressAPINewID(t), progressAPINewID(t)

	tests := []struct {
		name string
		edit func(*domain.ProgressSaveRequest)
		want []string
	}{
		{"soft-deleted exercise is accepted", func(r *domain.ProgressSaveRequest) { r.Exercises[0].ExerciseID = a.deletedEx.String() }, nil},
		{"soft-deleted own plan is accepted", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = progressAPIPtr(a.deletedPlan.String()) }, nil},
		{"own plan is accepted", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = progressAPIPtr(a.plan.String()) }, nil},
		{"no plan is a freestyle workout", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = nil }, nil},
		{"unknown exercise", func(r *domain.ProgressSaveRequest) { r.Exercises[1].ExerciseID = unknownEx.String() },
			[]string{"exercises[1].exercise_id: unknown_reference"}},
		{"unknown plan", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = progressAPIPtr(unknownPlan.String()) },
			[]string{"workout_plan_id: unknown_reference"}},
		{"another user's plan", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = progressAPIPtr(a.otherPlan.String()) },
			[]string{"workout_plan_id: unknown_reference"}},
		{"malformed plan id", func(r *domain.ProgressSaveRequest) { r.WorkoutPlanID = progressAPIPtr("plan-1") },
			[]string{"workout_plan_id: invalid_format"}},
		{"malformed exercise id", func(r *domain.ProgressSaveRequest) { r.Exercises[0].ExerciseID = "bench" },
			[]string{"exercises[0].exercise_id: invalid_format"}},
		{"duplicate exercise positions", func(r *domain.ProgressSaveRequest) { r.Exercises[1].Position = progressAPIPtr(0) },
			[]string{"exercises[1].position: duplicate"}},
		{"duplicate set positions", func(r *domain.ProgressSaveRequest) { r.Exercises[0].Sets[1].Position = progressAPIPtr(0) },
			[]string{"exercises[0].sets[1].position: duplicate"}},
		{"updated_at too far in the future", func(r *domain.ProgressSaveRequest) { r.UpdatedAt = "2026-09-19T09:05:01Z" },
			[]string{"updated_at: too_far_in_future"}},
		{"updated_at exactly 5 minutes ahead", func(r *domain.ProgressSaveRequest) { r.UpdatedAt = "2026-09-19T09:05:00Z" }, nil},
		{"ended before started", func(r *domain.ProgressSaveRequest) { r.EndedAt = "2026-09-19T07:59:59Z" },
			[]string{"ended_at: out_of_range"}},
		{"duration out of range", func(r *domain.ProgressSaveRequest) { r.DurationSeconds = progressAPIPtr(86401) },
			[]string{"duration_seconds: out_of_range"}},
		{"unknown set type", func(r *domain.ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "superset" },
			[]string{"exercises[0].sets[0].type: invalid_value"}},
		{"weight with three decimals", func(r *domain.ProgressSaveRequest) { r.Exercises[0].Sets[0].Weight = progressAPIDec("80.125") },
			[]string{"exercises[0].sets[0].weight: too_many_decimals"}},
		{"rpe off the half-point grid", func(r *domain.ProgressSaveRequest) { r.Exercises[0].Sets[1].RPE = progressAPIDec("8.25") },
			[]string{"exercises[0].sets[1].rpe: invalid_value"}},
		{"completed missing", func(r *domain.ProgressSaveRequest) { r.Exercises[1].Sets[0].Completed = nil },
			[]string{"exercises[1].sets[0].completed: required"}},
		{"many problems are reported together, validation first then references", func(r *domain.ProgressSaveRequest) {
			r.Name = ""
			r.Exercises[0].Sets[0].Reps = progressAPIPtr(-1)
			r.Exercises[1].ExerciseID = unknownEx.String() // not reached: validation fails first
		}, []string{"name: required", "exercises[0].sets[0].reps: out_of_range"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := progressAPINewID(t)
			rec := a.put(t, id, a.body(t, "2026-09-19T08:55:02Z", tt.edit))
			if tt.want == nil {
				progressAPIRequire(t, rec, http.StatusCreated)
				return
			}
			body := apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
			if got := progressAPIIssues(body); !slices.Equal(got, tt.want) {
				t.Errorf("details = %q, want %q", got, tt.want)
			}
			if n := a.count(t, `SELECT count(*) FROM progress WHERE id = $1`, id); n != 0 {
				t.Error("a rejected save wrote a row")
			}
		})
	}

	t.Run("a null body reports every required field", func(t *testing.T) {
		rec := a.put(t, progressAPINewID(t), `null`)
		body := apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		want := []string{"name: required", "started_at: required", "ended_at: required",
			"duration_seconds: required", "updated_at: required", "exercises: required"}
		if got := progressAPIIssues(body); !slices.Equal(got, want) {
			t.Errorf("details = %q, want %q", got, want)
		}
	})

	t.Run("invalid values leave the stored session as it was", func(t *testing.T) {
		id := progressAPINewID(t)
		progressAPIRequire(t, a.put(t, id, a.body(t, "2026-09-19T08:55:02Z")), http.StatusCreated)
		before := a.get(t, id).Body.String()
		rec := a.put(t, id, a.body(t, "2026-09-19T08:56:00Z", func(r *domain.ProgressSaveRequest) {
			r.Exercises[0].ExerciseID = unknownEx.String()
		}))
		apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		if a.get(t, id).Body.String() != before {
			t.Error("a rejected update changed the session")
		}
	})
}

// TestProgressAPIGetAndDelete: specs 04 and 16.
func TestProgressAPIGetAndDelete(t *testing.T) {
	a := progressAPINew(t)
	id := progressAPINewID(t)
	progressAPIRequire(t, a.put(t, id, a.body(t, "2026-09-19T08:55:02Z")), http.StatusCreated)
	target := "/v1/progress/" + id.String()

	t.Run("get: statuses", func(t *testing.T) {
		progressAPIRequire(t, a.get(t, id), http.StatusOK)
		apitest.RequireError(t, a.get(t, progressAPINewID(t)), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.do(t, a.other, domain.RoleUser, http.MethodGet, target, ""), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.do(t, a.admin, domain.RoleAdmin, http.MethodGet, target, ""), http.StatusForbidden, "forbidden")
		apitest.RequireError(t, a.do(t, uuid.Nil, "", http.MethodGet, target, ""), http.StatusUnauthorized, "unauthorized")
		apitest.RequireError(t, a.as(t, http.MethodGet, "/v1/progress/xyz", ""), http.StatusBadRequest, "bad_request")
	})

	t.Run("delete: statuses before the delete", func(t *testing.T) {
		apitest.RequireError(t, a.as(t, http.MethodDelete, "/v1/progress/"+progressAPINewID(t).String(), ""), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.do(t, a.other, domain.RoleUser, http.MethodDelete, target, ""), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.do(t, a.admin, domain.RoleAdmin, http.MethodDelete, target, ""), http.StatusForbidden, "forbidden")
		apitest.RequireError(t, a.do(t, uuid.Nil, "", http.MethodDelete, target, ""), http.StatusUnauthorized, "unauthorized")
		apitest.RequireError(t, a.as(t, http.MethodDelete, "/v1/progress/xyz", ""), http.StatusBadRequest, "bad_request")
		progressAPIRequire(t, a.get(t, id), http.StatusOK) // still there
	})

	t.Run("delete: 204 without a body, then idempotent", func(t *testing.T) {
		rec := a.as(t, http.MethodDelete, target, "")
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("delete = %d with body %q, want 204 and no body", rec.Code, rec.Body.String())
		}
		const micros = `SELECT (extract(epoch FROM server_updated_at) * 1000000)::bigint FROM progress WHERE id = $1`
		stamp := a.count(t, micros, id)
		if n := a.count(t, `SELECT count(*) FROM progress WHERE id = $1 AND deleted_at = server_updated_at`, id); n != 1 {
			t.Error("deleted_at and server_updated_at are not the same instant")
		}
		rec = a.as(t, http.MethodDelete, target, "")
		if rec.Code != http.StatusNoContent {
			t.Errorf("second delete = %d, want 204", rec.Code)
		}
		if a.count(t, micros, id) != stamp {
			t.Error("a repeated delete moved server_updated_at")
		}
	})

	t.Run("after the delete", func(t *testing.T) {
		apitest.RequireError(t, a.get(t, id), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.put(t, id, a.body(t, "2026-09-19T08:58:00Z")), http.StatusConflict, "conflict")
		if n := a.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); n != 2 {
			t.Errorf("%d exercise rows kept, want 2", n)
		}
		list := progressAPIObject(t, a.as(t, http.MethodGet, "/v1/progress", ""))
		if items := list["items"].([]any); len(items) != 0 {
			t.Errorf("the list still shows the deleted session: %v", items)
		}
	})

	t.Run("another user's session is untouched by a delete attempt", func(t *testing.T) {
		mine := progressAPINewID(t)
		progressAPIRequire(t, a.put(t, mine, a.body(t, "2026-09-19T08:55:02Z")), http.StatusCreated)
		apitest.RequireError(t, a.do(t, a.other, domain.RoleUser, http.MethodDelete, "/v1/progress/"+mine.String(), ""), http.StatusNotFound, "not_found")
		progressAPIRequire(t, a.get(t, mine), http.StatusOK)
	})
}

// progressAPIWalk follows next_cursor from target (a list URL with a limit)
// and returns the ids of all items and the number of pages.
func progressAPIWalk(t *testing.T, a *progressAPI, target string) ([]string, int) {
	t.Helper()
	var ids []string
	sep := "&"
	if !strings.Contains(target, "?") {
		sep = "?"
	}
	cursor := ""
	for pages := 1; pages < 20; pages++ {
		u := target
		if cursor != "" {
			u += sep + "cursor=" + cursor
		}
		rec := a.as(t, http.MethodGet, u, "")
		progressAPIRequire(t, rec, http.StatusOK)
		page := progressAPIObject(t, rec)
		for _, it := range page["items"].([]any) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			return ids, pages
		}
		cursor = next
	}
	t.Fatal("cursor walk does not end")
	return nil, 0
}

// progressAPIListSetup saves five sessions started on days 10 to 14 (a plan on
// days 11 and 13) and returns their ids in that order.
func progressAPIListSetup(t *testing.T, a *progressAPI) []string {
	t.Helper()
	var ids []string
	for i := range 5 {
		id := progressAPINewID(t)
		day := 10 + i
		rec := a.put(t, id, a.body(t, "2026-09-19T08:55:02Z", func(r *domain.ProgressSaveRequest) {
			r.StartedAt = fmt.Sprintf("2026-01-%02dT08:00:00Z", day)
			r.EndedAt = fmt.Sprintf("2026-01-%02dT09:00:00Z", day)
			if i == 1 || i == 3 {
				r.WorkoutPlanID = progressAPIPtr(a.plan.String())
			}
		}))
		progressAPIRequire(t, rec, http.StatusCreated)
		ids = append(ids, id.String())
	}
	return ids
}

func TestProgressAPIList(t *testing.T) {
	a := progressAPINew(t)
	s := progressAPIListSetup(t, a)
	// Another user's newer session is never listed for the caller.
	a.seed(t, a.other, testutil.WithProgressStartedAt(time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)))
	newestFirst := []string{s[4], s[3], s[2], s[1], s[0]}

	t.Run("default order and cursor walk", func(t *testing.T) {
		got, pages := progressAPIWalk(t, a, "/v1/progress?limit=2")
		if !slices.Equal(got, newestFirst) || pages != 3 {
			t.Errorf("walk = %v in %d pages, want %v in 3", got, pages, newestFirst)
		}
		all, pages := progressAPIWalk(t, a, "/v1/progress")
		if !slices.Equal(all, newestFirst) || pages != 1 {
			t.Errorf("default page = %v in %d pages", all, pages)
		}
	})

	t.Run("expand=exercises walks the same order", func(t *testing.T) {
		got, _ := progressAPIWalk(t, a, "/v1/progress?limit=2&expand=exercises")
		if !slices.Equal(got, newestFirst) {
			t.Errorf("walk = %v", got)
		}
		rec := a.as(t, http.MethodGet, "/v1/progress?expand=exercises&limit=1", "")
		item := progressAPIObject(t, rec)["items"].([]any)[0].(map[string]any)
		if _, has := item["exercise_count"]; has {
			t.Error("expanded item has exercise_count")
		}
		if len(item["exercises"].([]any)) != 2 {
			t.Errorf("item = %s", progressAPIMustJSON(t, item))
		}
	})

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			query string
			want  []string
		}{
			{"workout_plan_id=" + a.plan.String(), []string{s[3], s[1]}},
			// A bare date is a day in UTC: from starts at its midnight, to
			// runs to its last microsecond (sessions start at 08:00Z).
			{"from=2026-01-12", []string{s[4], s[3], s[2]}},
			{"to=2026-01-12", []string{s[2], s[1], s[0]}},
			{"from=2026-01-12&to=2026-01-12", []string{s[2]}},
			{"from=2026-01-11&to=2026-01-13", []string{s[3], s[2], s[1]}},
			// A timestamp with an offset (a device's local midnight) is exact.
			{"to=2026-01-12T00:00:00%2B07:00", []string{s[1], s[0]}},
			{"from=2026-01-12T00:00:00%2B07:00&to=2026-01-12T23:59:59%2B07:00", []string{s[2]}},
			{"from=2026-01-12T08:00:00Z", []string{s[4], s[3], s[2]}},
			{"to=2026-01-12T08:00:00Z", []string{s[2], s[1], s[0]}},
			{"from=2026-01-11T00:00:00Z&to=2026-01-13T23:59:59Z", []string{s[3], s[2], s[1]}},
			{"from=2026-01-11T00:00:00Z&to=2026-01-13T23:59:59Z&workout_plan_id=" + a.plan.String(), []string{s[3], s[1]}},
			{"workout_plan_id=" + a.otherPlan.String(), nil},
		}
		for _, tt := range tests {
			got, _ := progressAPIWalk(t, a, "/v1/progress?"+tt.query)
			if !slices.Equal(got, tt.want) {
				t.Errorf("?%s = %v, want %v", tt.query, got, tt.want)
			}
		}
	})

	t.Run("updated_since is the sync feed: oldest first, deleted rows included", func(t *testing.T) {
		since := "/v1/progress?updated_since=1970-01-01T00:00:00Z"
		got, _ := progressAPIWalk(t, a, since+"&limit=2")
		if !slices.Equal(got, s) {
			t.Fatalf("feed = %v, want creation order %v", got, s)
		}

		// A delete and an update show up in the next pull, deleted flagged.
		last := progressAPIObject(t, a.as(t, http.MethodGet, since+"&limit=200", ""))
		items := last["items"].([]any)
		cursorTime := items[len(items)-1].(map[string]any)["server_updated_at"].(string) // the newest seen
		progressAPIRequire(t, a.as(t, http.MethodDelete, "/v1/progress/"+s[2], ""), http.StatusNoContent)
		progressAPIRequire(t, a.put(t, uuid.MustParse(s[0]), a.body(t, "2026-09-19T08:58:00Z", func(r *domain.ProgressSaveRequest) {
			r.Name = "edited"
			r.StartedAt, r.EndedAt = "2026-01-10T08:00:00Z", "2026-01-10T09:00:00Z"
		})), http.StatusOK)
		rec := a.as(t, http.MethodGet, "/v1/progress?updated_since="+cursorTime+"&expand=exercises", "")
		progressAPIRequire(t, rec, http.StatusOK)
		next := progressAPIObject(t, rec)["items"].([]any)
		if len(next) != 2 {
			t.Fatalf("second pull = %s", progressAPIMustJSON(t, next))
		}
		if d := next[0].(map[string]any); d["id"] != s[2] || d["deleted_at"] == nil {
			t.Errorf("first item = %v, want the deleted session with deleted_at", d)
		}
		if e := next[1].(map[string]any); e["id"] != s[0] || e["name"] != "edited" || e["deleted_at"] != nil {
			t.Errorf("second item = %v, want the edited session", e)
		}
	})

	t.Run("include_deleted", func(t *testing.T) {
		got, _ := progressAPIWalk(t, a, "/v1/progress?include_deleted=true")
		if !slices.Equal(got, newestFirst) { // s[2] is deleted by now and must be listed again
			t.Errorf("include_deleted = %v, want %v", got, newestFirst)
		}
		got, _ = progressAPIWalk(t, a, "/v1/progress")
		if !slices.Equal(got, []string{s[4], s[3], s[1], s[0]}) {
			t.Errorf("default list = %v, want the deleted session hidden", got)
		}
		got, _ = progressAPIWalk(t, a, "/v1/progress?include_deleted=false")
		if len(got) != 4 {
			t.Errorf("include_deleted=false = %v", got)
		}
	})

	t.Run("an empty list is an empty array", func(t *testing.T) {
		rec := a.do(t, uuid.New(), domain.RoleUser, http.MethodGet, "/v1/progress", "") // a user without sessions
		progressAPIRequire(t, rec, http.StatusOK)
		if got := strings.TrimSpace(rec.Body.String()); got != `{"items":[],"next_cursor":null}` {
			t.Errorf("body = %s", got)
		}
	})

	t.Run("the caller sees only their own sessions", func(t *testing.T) {
		rec := a.do(t, a.other, domain.RoleUser, http.MethodGet, "/v1/progress?updated_since=1970-01-01T00:00:00Z", "")
		progressAPIRequire(t, rec, http.StatusOK)
		if items := progressAPIObject(t, rec)["items"].([]any); len(items) != 1 {
			t.Errorf("other user's items = %d, want their 1 seeded session", len(items))
		}
	})
}

func TestProgressAPIListErrors(t *testing.T) {
	a := progressAPINew(t)
	progressAPIListSetup(t, a)

	tests := []struct {
		query  string
		status int
		code   string
		field  string // of the first detail
	}{
		{"limit=0", 422, "validation_failed", "limit"},
		{"limit=-1", 422, "validation_failed", "limit"},
		{"limit=201", 422, "validation_failed", "limit"},
		{"limit=abc", 422, "validation_failed", "limit"},
		{"limit=1", 200, "", ""},
		{"limit=200", 200, "", ""},
		{"expand=children", 422, "validation_failed", "expand"},
		{"expand=", 422, "validation_failed", "expand"},
		{"expand=Exercises", 422, "validation_failed", "expand"},
		{"include_deleted=maybe", 422, "validation_failed", "include_deleted"},
		{"cursor=garbage", 400, "bad_request", ""},
		{"cursor=AAAA", 400, "bad_request", ""},
		{"cursor=garbage&expand=exercises", 400, "bad_request", ""},
		{"updated_since=yesterday", 400, "bad_request", ""},
		{"updated_since=1758268502", 400, "bad_request", ""},
		{"from=2026-01-01", 200, "", ""},
		{"to=2026-01-01", 200, "", ""},
		{"from=2026/01/01", 400, "bad_request", ""},
		{"to=2026-02-30", 400, "bad_request", ""},
		{"to=nope", 400, "bad_request", ""},
		{"workout_plan_id=nope", 400, "bad_request", ""},
		{"workout_plan_id=", 400, "bad_request", ""},
		{"updated_since=2026-01-01T00:00:00Z&from=2026-01-01T00:00:00Z", 200, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := a.as(t, http.MethodGet, "/v1/progress?"+tt.query, "")
			if tt.status == 200 {
				progressAPIRequire(t, rec, 200)
				return
			}
			body := apitest.RequireError(t, rec, tt.status, tt.code)
			if tt.field != "" && (len(body.Error.Details) == 0 || body.Error.Details[0].Field != tt.field) {
				t.Errorf("details = %+v, want field %q", body.Error.Details, tt.field)
			}
		})
	}

	t.Run("a cursor from another sort order does not break the request", func(t *testing.T) {
		rec := a.as(t, http.MethodGet, "/v1/progress?limit=1", "")
		cursor := progressAPIObject(t, rec)["next_cursor"].(string)
		progressAPIRequire(t, a.as(t, http.MethodGet, "/v1/progress?updated_since=1970-01-01T00:00:00Z&cursor="+cursor, ""), 200)
	})

	t.Run("401 and 403", func(t *testing.T) {
		apitest.RequireError(t, a.do(t, uuid.Nil, "", http.MethodGet, "/v1/progress", ""), 401, "unauthorized")
		apitest.RequireError(t, a.do(t, a.admin, domain.RoleAdmin, http.MethodGet, "/v1/progress", ""), 403, "forbidden")
	})
}

// TestProgressAPILargePayload: the largest allowed session (50 exercises of 100
// sets) is saved and read back identically.
func TestProgressAPILargePayload(t *testing.T) {
	a := progressAPINew(t)
	body := a.body(t, "2026-09-19T08:55:02Z", func(r *domain.ProgressSaveRequest) {
		r.Exercises = make([]domain.ProgressExerciseRequest, domain.MaxExercisesPerProgress)
		for i := range r.Exercises {
			sets := make([]domain.ProgressSetRequest, domain.MaxSetsPerExercise)
			for j := range sets {
				sets[j] = domain.ProgressSetRequest{Position: progressAPIPtr(j), Type: string(domain.SetTypes[j%4]), Reps: progressAPIPtr(j),
					Weight: progressAPIDec(fmt.Sprintf("%d.%d", i, j%10)), RPE: progressAPIDec("8.5"), Completed: progressAPIPtr(j%2 == 0)}
			}
			r.Exercises[i] = domain.ProgressExerciseRequest{ExerciseID: a.ex[i%3].String(), Position: progressAPIPtr(i), Sets: sets}
		}
	})
	if len(body) >= domain.MaxBodyBytes {
		t.Fatalf("test payload is %d bytes, over the 1 MiB body limit", len(body))
	}
	t.Logf("payload: %d bytes", len(body))

	id := progressAPINewID(t)
	rec := a.put(t, id, body)
	progressAPIRequire(t, rec, http.StatusCreated)
	if got := a.get(t, id); got.Body.String() != rec.Body.String() {
		t.Error("GET differs from the PUT response")
	}

	sent := progressAPIJSON(t, []byte(body)).(map[string]any)["exercises"].([]any)
	got := progressAPIObject(t, rec)["exercises"].([]any)
	if len(got) != 50 {
		t.Fatalf("%d exercises back, want 50", len(got))
	}
	for i := range got {
		ge, se := got[i].(map[string]any), sent[i].(map[string]any)
		if ge["exercise_id"] != se["exercise_id"] || ge["position"] != se["position"] || !reflect.DeepEqual(ge["sets"], se["sets"]) {
			t.Fatalf("exercise %d differs from what was sent", i)
		}
	}
}

// TestProgressAPIConcurrentPuts: several requests race on one id. There is no
// 500, exactly the documented statuses, and the stored session is consistent.
func TestProgressAPIConcurrentPuts(t *testing.T) {
	t.Run("the same payload: one 201, the rest 200", func(t *testing.T) {
		a := progressAPINew(t)
		id := progressAPINewID(t)
		body := a.body(t, "2026-09-19T08:55:02Z")
		codes := progressAPIRace(12, func(int) int { return a.put(t, id, body).Code })

		created := 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusOK:
			default:
				t.Errorf("status %d, want 201 or 200", c)
			}
		}
		if created != 1 {
			t.Errorf("%d requests answered 201, want exactly 1", created)
		}
		if n := a.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1`, id); n != 3 {
			t.Errorf("%d set rows, want 3", n)
		}
	})

	t.Run("different updated_at: the newest wins, the others are stale", func(t *testing.T) {
		a := progressAPINew(t)
		id := progressAPINewID(t)
		const n = 12
		codes := progressAPIRace(n, func(i int) int {
			body := a.body(t, fmt.Sprintf("2026-09-19T08:56:%02dZ", i), func(r *domain.ProgressSaveRequest) {
				r.Name = fmt.Sprintf("writer %d", i)
				r.Exercises = r.Exercises[:1+i%2]
			})
			return a.put(t, id, body).Code
		})
		for i, c := range codes {
			if c != 200 && c != 201 && c != 409 {
				t.Errorf("writer %d: status %d, want 200, 201 or 409", i, c)
			}
		}
		if codes[n-1] != 200 && codes[n-1] != 201 {
			t.Errorf("the newest writer got %d, want success", codes[n-1])
		}
		got := progressAPIObject(t, a.get(t, id))
		if got["name"] != fmt.Sprintf("writer %d", n-1) || got["updated_at"] != "2026-09-19T08:56:11Z" {
			t.Errorf("stored session = %s, want the newest writer", got["name"])
		}
		if len(got["exercises"].([]any)) != 1+(n-1)%2 {
			t.Errorf("%d exercises, want %d", len(got["exercises"].([]any)), 1+(n-1)%2)
		}
		if c := a.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); c != len(got["exercises"].([]any)) {
			t.Errorf("%d exercise rows for %d exercises", c, len(got["exercises"].([]any)))
		}
	})

	t.Run("save racing a delete: either order is consistent", func(t *testing.T) {
		a := progressAPINew(t)
		id := progressAPINewID(t)
		progressAPIRequire(t, a.put(t, id, a.body(t, "2026-09-19T08:55:00Z")), http.StatusCreated)
		codes := progressAPIRace(8, func(i int) int {
			if i%2 == 0 {
				return a.as(t, http.MethodDelete, "/v1/progress/"+id.String(), "").Code
			}
			return a.put(t, id, a.body(t, fmt.Sprintf("2026-09-19T08:56:%02dZ", i))).Code
		})
		for i, c := range codes {
			if i%2 == 0 && c != 204 || i%2 == 1 && c != 200 && c != 409 {
				t.Errorf("request %d: status %d", i, c)
			}
		}
		apitest.RequireError(t, a.get(t, id), http.StatusNotFound, "not_found")
		apitest.RequireError(t, a.put(t, id, a.body(t, "2026-09-19T09:00:00Z")), http.StatusConflict, "conflict")
	})
}

// progressAPIRace runs fn(i) for i in [0, n) at the same moment and returns the
// results by index.
func progressAPIRace(n int, fn func(i int) int) []int {
	out := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = fn(i)
		}()
	}
	close(start)
	wg.Wait()
	return out
}
