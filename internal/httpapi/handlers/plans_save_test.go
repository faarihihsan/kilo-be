package handlers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/testutil"
)

// TestPlansHTTPSaveStatusTable walks every status and error code of the
// response table of spec 10 (and the decoding errors of the conventions).
func TestPlansHTTPSaveStatusTable(t *testing.T) {
	a := newPlanAPI(t)
	ex0, ex1 := a.exercises[0], a.exercises[1]
	valid := planBody("Push", planTime(-time.Hour), planExercise(ex0, 0, ""))
	ghost := uuid.New()

	many := make([]string, domain.MaxExercisesPerPlan+1)
	for i := range many {
		many[i] = planExercise(ex0, i, "")
	}
	fifty := many[:domain.MaxExercisesPerPlan]
	longDescription := strings.Repeat("a", domain.MaxBodyBytes+1024)

	tests := []struct {
		name        string
		who         planCaller
		path        string // empty: a new id
		body        string
		contentType string // empty: JSON
		status      int
		code        string
		issues      []string // "field:issue", in order; nil: not checked
	}{
		{name: "new plan", who: a.user, body: valid, status: http.StatusCreated},
		{name: "50 exercises are fine", who: a.user, body: planBody("Big", planTime(0), fifty...), status: http.StatusCreated},
		{name: "empty exercises are fine", who: a.user, body: planBody("Draft", planTime(0)), status: http.StatusCreated},

		{name: "no token", who: planAnon, body: valid, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "admin is forbidden", who: a.admin, body: valid, status: http.StatusForbidden, code: "forbidden"},

		{name: "bad path id", who: a.user, path: "/v1/workout-plans/not-a-uuid", body: valid, status: http.StatusBadRequest, code: "bad_request"},
		{name: "path id without hyphens", who: a.user, path: "/v1/workout-plans/" + strings.ReplaceAll(uuid.NewString(), "-", ""), body: valid, status: http.StatusBadRequest, code: "bad_request"},
		{name: "empty body", who: a.user, body: "", status: http.StatusBadRequest, code: "bad_request"},
		{name: "malformed JSON", who: a.user, body: `{"name":`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "not an object", who: a.user, body: `[]`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "two JSON values", who: a.user, body: valid + valid, status: http.StatusBadRequest, code: "bad_request"},
		{name: "unknown field", who: a.user, body: `{"name":"x","updated_at":"` + planTime(0) + `","exercises":[],"color":"red"}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "unknown field in an exercise", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"exercise_name":"Bench"`)), status: http.StatusBadRequest, code: "bad_request"},
		{name: "server-owned field", who: a.user, body: `{"id":"` + uuid.NewString() + `","name":"x","updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "server timestamps in the body", who: a.user, body: `{"name":"x","updated_at":"` + planTime(0) + `","server_updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "name of the wrong type", who: a.user, body: `{"name":5,"updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "target_reps of the wrong type", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_reps":"8"`)), status: http.StatusBadRequest, code: "bad_request"},
		{name: "fractional position", who: a.user, body: planBody("x", planTime(0), `{"exercise_id":"`+ex0.String()+`","position":1.5,"target_sets":3}`), status: http.StatusBadRequest, code: "bad_request"},
		{name: "weight as a string", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_weight":"80"`)), status: http.StatusBadRequest, code: "bad_request"},
		{name: "weight as a bool", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_weight":true`)), status: http.StatusBadRequest, code: "bad_request"},
		{name: "exercises as an object", who: a.user, body: `{"name":"x","updated_at":"` + planTime(0) + `","exercises":{}}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "not JSON", who: a.user, body: valid, contentType: "text/plain", status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "body over 1 MiB", who: a.user, body: `{"name":"x","description":"` + longDescription + `","updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusRequestEntityTooLarge, code: "payload_too_large"},

		{name: "several problems at once", who: a.user,
			body:   `{"name":"","updated_at":"` + planTime(10*time.Minute) + `","exercises":[` + planExercise(ex0, 0, `,"target_reps":0`) + `,` + planExercise(ex0, 0, `,"rest_seconds":4000`) + `]}`,
			status: http.StatusUnprocessableEntity, code: "validation_failed",
			issues: []string{"name:too_short", "updated_at:too_far_in_future", "exercises[0].target_reps:out_of_range", "exercises[1].position:duplicate", "exercises[1].rest_seconds:out_of_range"}},
		{name: "name null", who: a.user, body: `{"name":null,"updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"name:required"}},
		{name: "everything missing", who: a.user, body: `{}`, status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"name:required", "updated_at:required", "exercises:required"}},
		{name: "exercises null", who: a.user, body: `{"name":"x","updated_at":"` + planTime(0) + `","exercises":null}`, status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises:required"}},
		{name: "name with NUL", who: a.user, body: `{"name":"a\u0000b","updated_at":"` + planTime(0) + `","exercises":[]}`, status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"name:invalid_chars"}},
		{name: "name of 101 characters", who: a.user, body: planBody(strings.Repeat("é", 101), planTime(0)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"name:too_long"}},
		{name: "future updated_at", who: a.user, body: planBody("x", planTime(5*time.Minute+time.Second)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"updated_at:too_far_in_future"}},
		{name: "updated_at not a timestamp", who: a.user, body: planBody("x", "yesterday"), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"updated_at:invalid_format"}},
		{name: "exercise_id not a uuid", who: a.user, body: planBody("x", planTime(0), `{"exercise_id":"bench","position":0,"target_sets":3}`), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].exercise_id:invalid_format"}},
		{name: "51 exercises", who: a.user, body: planBody("Too big", planTime(0), many...), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises:too_many"}},
		{name: "duplicate positions", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 4, ""), planExercise(ex1, 4, "")), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[1].position:duplicate"}},
		{name: "unknown exercise", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, ""), planExercise(ghost, 1, "")), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[1].exercise_id:unknown_reference"}},
		{name: "target_reps_max without target_reps", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_reps_max":10`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_reps:required"}},
		{name: "target_reps_max below target_reps", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_reps":8,"target_reps_max":6`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_reps_max:out_of_range"}},
		{name: "weight with 3 decimals", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_weight":80.125`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_weight:too_many_decimals"}},
		{name: "negative weight", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_weight":-1`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_weight:out_of_range"}},
		{name: "weight beyond the column", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_weight":100000`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_weight:out_of_range"}},
		{name: "distance beyond the column", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_distance_meters":1e7`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_distance_meters:out_of_range"}},
		{name: "int beyond the column", who: a.user, body: planBody("x", planTime(0), planExercise(ex0, 0, `,"target_duration_seconds":2147483648`)), status: http.StatusUnprocessableEntity, code: "validation_failed", issues: []string{"exercises[0].target_duration_seconds:out_of_range"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if path == "" {
				path = "/v1/workout-plans/" + uuid.NewString()
			}
			contentType := tt.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			rec := a.doType(http.MethodPut, path, tt.who, tt.body, contentType)
			if tt.code == "" {
				planRequireOK(t, rec, tt.status)
				return
			}
			apitest.RequireError(t, rec, tt.status, tt.code)
			if tt.issues != nil {
				if got := planIssueList(t, rec); !slices.Equal(got, tt.issues) {
					t.Errorf("details = %v, want %v", got, tt.issues)
				}
			}
		})
	}
	a.planRequireNoServerErrors()
}

func TestPlansHTTPUnauthorizedHasChallenge(t *testing.T) {
	a := newPlanAPI(t)
	rec := a.put(planAnon, uuid.New(), planBody("x", planTime(0)))
	apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
}

// TestPlansHTTPNothingIsStoredByARejectedSave: every 4xx leaves no plan behind.
func TestPlansHTTPNothingIsStoredByARejectedSave(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	for _, body := range []string{
		`{"name":"","updated_at":"` + planTime(0) + `","exercises":[]}`,
		planBody("x", planTime(0), planExercise(uuid.New(), 0, "")),
		planBody("x", planTime(0), planExercise(a.exercises[0], 0, ""), planExercise(a.exercises[0], 0, "")),
	} {
		if rec := a.put(a.user, id, body); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; %s", rec.Code, rec.Body.String())
		}
		apitest.RequireError(t, a.get(a.user, id), http.StatusNotFound, "not_found")
	}
	if rec := a.put(a.admin, id, planBody("x", planTime(0))); rec.Code != http.StatusForbidden {
		t.Fatalf("admin status = %d", rec.Code)
	}
	apitest.RequireError(t, a.get(a.user, id), http.StatusNotFound, "not_found")
}

// TestPlansHTTPConflictRule is the conflict table of spec 03 (used by spec 10),
// end to end.
func TestPlansHTTPConflictRule(t *testing.T) {
	a := newPlanAPI(t)
	ex := a.exercises
	id := uuid.New()
	at := func(minutes int) string { return planTime(time.Duration(minutes) * time.Minute) }

	// No row: create, 201.
	first := a.put(a.user, id, planBody("v1", at(-60), planExercise(ex[0], 0, ""), planExercise(ex[1], 1, "")))
	created := planRequireOK(t, first, http.StatusCreated)

	// Equal updated_at: 200, no change, the stored copy, even though the body
	// says something else. This is what makes a retry after a lost response safe.
	retry := a.put(a.user, id, planBody("not applied", at(-60), planExercise(ex[2], 0, "")))
	planRequireOK(t, retry, http.StatusOK)
	if retry.Body.String() != first.Body.String() {
		t.Errorf("no-op response differs from the stored copy:\n%s\n%s", retry.Body.String(), first.Body.String())
	}
	if got := planRequireOK(t, a.get(a.user, id), http.StatusOK); !reflect.DeepEqual(got, created) {
		t.Errorf("a no-op changed the plan: %v", got)
	}
	if created["server_updated_at"] != planJSONOf(t, retry)["server_updated_at"] {
		t.Error("a no-op moved server_updated_at")
	}

	// Older updated_at: 409 stale, with the server copy in details[0].current.
	stale := a.put(a.user, id, planBody("old edit", at(-61), planExercise(ex[2], 0, "")))
	body := apitest.RequireError(t, stale, http.StatusConflict, "conflict")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != domain.IssueStale || body.Error.Details[0].Field != "" {
		t.Fatalf("details = %+v, want exactly [{issue: stale}] plus current", body.Error.Details)
	}
	current, _ := planDetails(t, stale)[0]["current"].(map[string]any)
	if !reflect.DeepEqual(current, created) {
		t.Errorf("details[0].current differs from the GET shape:\n%v\n%v", current, created)
	}
	if got := planRequireOK(t, a.get(a.user, id), http.StatusOK); !reflect.DeepEqual(got, created) {
		t.Errorf("a stale write changed the plan: %v", got)
	}

	// Newer updated_at: 200, saved, server_updated_at advanced, children replaced.
	newer := a.put(a.user, id, planBody("v2", at(-30), planExercise(ex[2], 5, `,"target_reps":12`)))
	updated := planRequireOK(t, newer, http.StatusOK)
	if updated["name"] != "v2" || updated["updated_at"] != planHTTPNow.Add(-30*time.Minute).Format(time.RFC3339) {
		t.Errorf("updated plan = %v", updated)
	}
	if updated["created_at"] != created["created_at"] {
		t.Errorf("created_at changed: %v -> %v", created["created_at"], updated["created_at"])
	}
	before, _ := time.Parse(time.RFC3339Nano, created["server_updated_at"].(string))
	after, _ := time.Parse(time.RFC3339Nano, updated["server_updated_at"].(string))
	if !after.After(before) {
		t.Errorf("server_updated_at did not advance: %v -> %v", before, after)
	}
	exercises, _ := updated["exercises"].([]any)
	if len(exercises) != 1 {
		t.Fatalf("exercises = %v, want only the new one", exercises)
	}
	if row, _ := exercises[0].(map[string]any); row["exercise_id"] != ex[2].String() || fmt.Sprint(row["position"]) != "5" || fmt.Sprint(row["target_reps"]) != "12" {
		t.Errorf("exercise = %v", exercises[0])
	}
	if got := planRequireOK(t, a.get(a.user, id), http.StatusOK); !reflect.DeepEqual(got, updated) {
		t.Errorf("GET differs from the PUT response:\n%v\n%v", got, updated)
	}

	// The old copy is now stale.
	apitest.RequireError(t, a.put(a.user, id, planBody("v1 again", at(-60))), http.StatusConflict, "conflict")

	// Deleted: 409 deleted, whatever the timestamp; no copy of the row.
	if rec := a.del(a.user, id); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	for _, minutes := range []int{-30, -29, 4} {
		rec := a.put(a.user, id, planBody("resurrect", at(minutes)))
		body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != domain.IssueDeleted || body.Error.Details[0].Current != nil {
			t.Errorf("at %d min: details = %+v, want [{issue: deleted}] without current", minutes, body.Error.Details)
		}
	}
	apitest.RequireError(t, a.get(a.user, id), http.StatusNotFound, "not_found")
	a.planRequireNoServerErrors()
}

// TestPlansHTTPRetryAfterLostResponse: the same request twice gives 201, then
// 200 with the same body.
func TestPlansHTTPRetryAfterLostResponse(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	body := planBody("Push", planTime(-time.Minute), planExercise(a.exercises[0], 0, `,"target_weight":80.5`))
	first := a.put(a.user, id, body)
	planRequireOK(t, first, http.StatusCreated)
	second := a.put(a.user, id, body)
	planRequireOK(t, second, http.StatusOK)
	if first.Body.String() != second.Body.String() {
		t.Errorf("retry body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
}

// TestPlansHTTPOwnership: another user's plan is 404 on every route, and a save
// never touches it.
func TestPlansHTTPOwnership(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	planRequireOK(t, a.put(a.user, id, planBody("Mine", planTime(-time.Hour), planExercise(a.exercises[0], 0, ""))), http.StatusCreated)
	mine := planRequireOK(t, a.get(a.user, id), http.StatusOK)

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"get":               a.get(a.other, id),
		"put newer":         a.put(a.other, id, planBody("Stolen", planTime(0))),
		"put older":         a.put(a.other, id, planBody("Stolen", planTime(-2*time.Hour))),
		"put equal":         a.put(a.other, id, planBody("Stolen", planTime(-time.Hour))),
		"put with ghost id": a.put(a.other, id, planBody("Stolen", planTime(0), planExercise(uuid.New(), 0, ""))),
		"delete":            a.del(a.other, id),
	} {
		t.Run(name, func(t *testing.T) {
			body := apitest.RequireError(t, rec, http.StatusNotFound, "not_found")
			if strings.Contains(rec.Body.String(), "Mine") || len(body.Error.Details) != 0 {
				t.Errorf("the 404 leaks something: %s", rec.Body.String())
			}
		})
	}
	if got := planRequireOK(t, a.get(a.user, id), http.StatusOK); !reflect.DeepEqual(got, mine) {
		t.Errorf("the owner's plan changed: %v", got)
	}
	// The same 404 whether the plan is missing or someone else's, in the body too.
	missing := a.get(a.other, uuid.New())
	theirs := a.get(a.other, id)
	if missing.Body.String() != theirs.Body.String() {
		t.Errorf("missing and foreign plans are distinguishable:\n%s\n%s", missing.Body.String(), theirs.Body.String())
	}
}

// TestPlansHTTPPlanLimit: 100 active plans per user; only creations count.
func TestPlansHTTPPlanLimit(t *testing.T) {
	a := newPlanAPI(t)
	var ids []uuid.UUID
	for range domain.MaxActivePlansPerUser {
		ids = append(ids, a.seedPlan(a.user.id))
	}
	// Soft-deleted plans do not count, and neither do another user's.
	for range 30 {
		a.seedPlan(a.user.id, testutil.WithPlanDeletedAt(planHTTPNow.Add(-time.Hour)))
		a.seedPlan(a.other.id)
	}

	rec := a.put(a.user, uuid.New(), planBody("Number 101", planTime(0)))
	apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	details := planDetails(t, rec)
	if len(details) != 1 || details[0]["issue"] != "plan_limit" {
		t.Errorf("details = %v, want [{issue: plan_limit}]", details)
	}

	// Updating an existing plan and a no-op retry are not affected.
	planRequireOK(t, a.put(a.user, ids[0], planBody("Edited at the cap", planTime(0))), http.StatusOK)
	planRequireOK(t, a.put(a.user, ids[0], planBody("Edited at the cap", planTime(0))), http.StatusOK)

	// Deleting one frees a slot; exactly one.
	if rec := a.del(a.user, ids[1]); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	planRequireOK(t, a.put(a.user, uuid.New(), planBody("Fits", planTime(0))), http.StatusCreated)
	apitest.RequireError(t, a.put(a.user, uuid.New(), planBody("Over again", planTime(0))), http.StatusUnprocessableEntity, "validation_failed")

	// The other user is unaffected.
	planRequireOK(t, a.put(a.other, uuid.New(), planBody("Theirs", planTime(0))), http.StatusCreated)
}

// TestPlansHTTPDecimalsRoundTrip: weights and distances go in and out as exact
// decimals, never through a float.
func TestPlansHTTPDecimalsRoundTrip(t *testing.T) {
	a := newPlanAPI(t)
	tests := []struct{ in, out string }{
		{"80", "80.0"}, {"80.0", "80.0"}, {"80.5", "80.5"}, {"80.50", "80.5"}, {"0.1", "0.1"}, {"0.07", "0.07"},
		{"0", "0.0"}, {"-0", "0.0"}, {"100e-1", "10.0"}, {"1E2", "100.0"}, {"33.33", "33.33"}, {"99999.99", "99999.99"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			id := uuid.New()
			rec := a.put(a.user, id, planBody("D", planTime(0), planExercise(a.exercises[0], 0, `,"target_weight":`+tt.in+`,"target_distance_meters":`+tt.in)))
			planRequireStatus(t, rec, http.StatusCreated)
			want := fmt.Sprintf(`"target_weight":%s,"target_duration_seconds":null,"target_distance_meters":%s,`, tt.out, tt.out)
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("PUT body has no %s:\n%s", want, rec.Body.String())
			}
			if got := a.get(a.user, id); !strings.Contains(got.Body.String(), want) {
				t.Errorf("GET body has no %s:\n%s", want, got.Body.String())
			}
		})
	}
	t.Run("distance with 2 decimals and a large value", func(t *testing.T) {
		id := uuid.New()
		rec := a.put(a.user, id, planBody("D", planTime(0), planExercise(a.exercises[0], 0, `,"target_distance_meters":9999999.99`)))
		planRequireStatus(t, rec, http.StatusCreated)
		if !strings.Contains(rec.Body.String(), `"target_distance_meters":9999999.99,`) {
			t.Errorf("body = %s", rec.Body.String())
		}
	})
}

// TestPlansHTTPTimestampsAreUTCMicroseconds: an offset in updated_at is
// converted, nanoseconds are cut to what PostgreSQL stores, and a retry of the
// same instant in any spelling is a no-op.
func TestPlansHTTPTimestampsAreUTCMicroseconds(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	rec := a.put(a.user, id, planBody("T", "2026-09-19T10:30:00.123456789+02:00"))
	got := planRequireOK(t, rec, http.StatusCreated)
	if got["updated_at"] != "2026-09-19T08:30:00.123456Z" {
		t.Errorf("updated_at = %v, want 2026-09-19T08:30:00.123456Z", got["updated_at"])
	}
	for _, spelling := range []string{"2026-09-19T08:30:00.123456Z", "2026-09-19T08:30:00.1234561Z", "2026-09-19T10:30:00.123456+02:00"} {
		retry := planRequireOK(t, a.put(a.user, id, planBody("other name", spelling)), http.StatusOK)
		if retry["name"] != "T" {
			t.Errorf("%s was not a no-op: name = %v", spelling, retry["name"])
		}
	}
	apitest.RequireError(t, a.put(a.user, id, planBody("older", "2026-09-19T08:30:00.123455Z")), http.StatusConflict, "conflict")
}

func TestPlansHTTPExercisesComeBackInPositionOrder(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	ex := a.exercises
	rec := a.put(a.user, id, planBody("Order", planTime(0),
		planExercise(ex[2], 20, ""), planExercise(ex[0], 3, ""), planExercise(ex[0], 7, ""), planExercise(ex[1], 0, "")))
	got := planRequireOK(t, rec, http.StatusCreated)
	var positions []string
	for _, e := range got["exercises"].([]any) {
		positions = append(positions, fmt.Sprint(e.(map[string]any)["position"]))
	}
	if !slices.Equal(positions, []string{"0", "3", "7", "20"}) {
		t.Errorf("positions = %v, want ascending", positions)
	}
}

// TestPlansHTTPExtremeTimestamps: the oldest and newest RFC 3339 instants are
// answered normally, never with a 500 from the database.
func TestPlansHTTPExtremeTimestamps(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	planRequireOK(t, a.put(a.user, id, planBody("Old", "0000-01-01T00:00:00Z")), http.StatusCreated)
	planRequireOK(t, a.put(a.user, id, planBody("Newer", "1970-01-01T00:00:00Z")), http.StatusOK)
	apitest.RequireError(t, a.put(a.user, id, planBody("Far future", "9999-12-31T23:59:59Z")), http.StatusUnprocessableEntity, "validation_failed")

	for _, since := range []string{"0000-01-01T00:00:00Z", "0001-01-01T00:00:00Z", "1970-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z"} {
		rec := a.list(a.user, "updated_since="+since+"&limit=1")
		planRequireStatus(t, rec, http.StatusOK)
		// Every page of the feed hands out a cursor that works.
		if next := planNextCursor(t, rec); next != "" {
			planRequireStatus(t, a.list(a.user, "updated_since="+since+"&cursor="+next), http.StatusOK)
		}
	}
	a.planRequireNoServerErrors()
}
