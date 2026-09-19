package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// T7.2 cross-user ownership (docs/tasks.md, docs/implementation-plan.md
// section 9): user B must never see or touch user A's plans or progress. The
// whole stack runs for real: router, authenticator, handlers, services and
// PostgreSQL. B always gets 404, never 403, so a probing client cannot even
// learn that the resource exists.

// e2eOwnFixture is one app with two real users (their bearer tokens are
// seeded) and one shared exercise.
type e2eOwnFixture struct {
	t          *testing.T
	h          http.Handler
	db         *store.DB
	aliceID    uuid.UUID
	bobID      uuid.UUID
	aliceToken string
	bobToken   string
	exerciseID uuid.UUID
}

func e2eOwnSetup(t *testing.T) *e2eOwnFixture {
	t.Helper()
	a, db := newTestApp(t)
	// Real tokens are used, so the real authenticator stays in place. The
	// per-IP limiter would throttle this many requests from one address.
	a.router.RateLimit = nil
	h := a.Handler()

	aliceID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	bobID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	aliceToken, _ := testutil.SeedToken(t, db, aliceID)
	bobToken, _ := testutil.SeedToken(t, db, bobID)
	exerciseID := testutil.SeedExercise(t, db, aliceID)

	return &e2eOwnFixture{
		t: t, h: h, db: db,
		aliceID: aliceID, bobID: bobID,
		aliceToken: aliceToken, bobToken: bobToken,
		exerciseID: exerciseID,
	}
}

// do sends one authenticated JSON request through the real router. body may be
// nil for GET and DELETE.
func (f *e2eOwnFixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal request body: %v", err)
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// progressBody is a valid spec-03 save body: one exercise from the shared
// master with one completed set. planID may be nil (freestyle) or another
// user's plan id to exercise the reference check.
func (f *e2eOwnFixture) progressBody(name string, planID *uuid.UUID, updatedAt time.Time) map[string]any {
	f.t.Helper()
	body := map[string]any{
		"name":             name,
		"notes":            nil,
		"started_at":       updatedAt.Add(-time.Hour).Format(time.RFC3339),
		"ended_at":         updatedAt.Add(-time.Minute).Format(time.RFC3339),
		"duration_seconds": 3540,
		"updated_at":       updatedAt.Format(time.RFC3339),
		"exercises": []any{
			map[string]any{
				"exercise_id": f.exerciseID.String(),
				"position":    0,
				"notes":       nil,
				"sets": []any{
					map[string]any{
						"position":         0,
						"type":             "normal",
						"reps":             8,
						"weight":           80.0,
						"duration_seconds": nil,
						"distance_meters":  nil,
						"rpe":              8.5,
						"completed":        true,
					},
				},
			},
		},
	}
	if planID != nil {
		body["workout_plan_id"] = planID.String()
	} else {
		body["workout_plan_id"] = nil
	}
	return body
}

// planBody is a valid spec-10 save body with one exercise from the shared
// master.
func (f *e2eOwnFixture) planBody(name string, updatedAt time.Time) map[string]any {
	f.t.Helper()
	return map[string]any{
		"name":        name,
		"description": nil,
		"updated_at":  updatedAt.Format(time.RFC3339),
		"exercises": []any{
			map[string]any{
				"exercise_id":             f.exerciseID.String(),
				"position":                0,
				"target_sets":             3,
				"target_reps":             10,
				"target_reps_max":         nil,
				"target_weight":           nil,
				"target_duration_seconds": nil,
				"target_distance_meters":  nil,
				"rest_seconds":            nil,
				"notes":                   nil,
			},
		},
	}
}

// ownE2EMustCreate fails the test unless the request created the resource.
func ownE2EMustCreate(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status = %d, want 201; body: %s", what, rec.Code, rec.Body)
	}
}

// ownE2ERequire404 asserts a generic not_found and returns the body text.
func ownE2ERequire404(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	apitest.RequireError(t, rec, http.StatusNotFound, "not_found")
	return rec.Body.String()
}

func TestE2EOwnership(t *testing.T) {
	f := e2eOwnSetup(t)

	progressID := uuid.New()
	planID := uuid.New()
	bobProgressID := uuid.New()
	bobPlanID := uuid.New()
	at := time.Now().UTC().Truncate(time.Second)

	ownE2EMustCreate(t, f.do(http.MethodPut, "/v1/progress/"+progressID.String(), f.aliceToken,
		f.progressBody("Alice session", nil, at)), "A's progress")
	ownE2EMustCreate(t, f.do(http.MethodPut, "/v1/workout-plans/"+planID.String(), f.aliceToken,
		f.planBody("Alice plan", at)), "A's plan")
	// B owns data too, so the list checks below are not trivially empty.
	ownE2EMustCreate(t, f.do(http.MethodPut, "/v1/progress/"+bobProgressID.String(), f.bobToken,
		f.progressBody("Bob session", nil, at)), "B's progress")
	ownE2EMustCreate(t, f.do(http.MethodPut, "/v1/workout-plans/"+bobPlanID.String(), f.bobToken,
		f.planBody("Bob plan", at)), "B's plan")

	progressPath := "/v1/progress/" + progressID.String()
	planPath := "/v1/workout-plans/" + planID.String()

	t.Run("progress is invisible and immutable to another user", func(t *testing.T) {
		before := f.do(http.MethodGet, progressPath, f.aliceToken, nil)
		if before.Code != http.StatusOK {
			t.Fatalf("A's GET: status = %d, want 200; body: %s", before.Code, before.Body)
		}

		// A random id and A's foreign id answer with the exact same body: the
		// 404 does not reveal that the resource exists.
		missing := f.do(http.MethodGet, "/v1/progress/"+uuid.NewString(), f.bobToken, nil)
		foreign := f.do(http.MethodGet, progressPath, f.bobToken, nil)
		if got, want := ownE2ERequire404(t, foreign), ownE2ERequire404(t, missing); got != want {
			t.Errorf("foreign 404 body differs from missing 404 body:\n foreign: %s\n missing: %s", got, want)
		}

		// B's PUT (a rename) must not overwrite A's session.
		ownE2ERequire404(t, f.do(http.MethodPut, progressPath, f.bobToken,
			f.progressBody("Bob hijack", nil, at.Add(time.Second))))
		if got := f.do(http.MethodGet, progressPath, f.aliceToken, nil).Body.String(); got != before.Body.String() {
			t.Errorf("A's session changed after B's PUT:\n before: %s\n after:  %s", before.Body, got)
		}

		// B's DELETE must not remove it.
		ownE2ERequire404(t, f.do(http.MethodDelete, progressPath, f.bobToken, nil))
		after := f.do(http.MethodGet, progressPath, f.aliceToken, nil)
		if after.Code != http.StatusOK {
			t.Fatalf("A's GET after B's DELETE: status = %d, want 200; body: %s", after.Code, after.Body)
		}
		if after.Body.String() != before.Body.String() {
			t.Errorf("A's session changed after B's DELETE:\n before: %s\n after:  %s", before.Body, after.Body)
		}
	})

	t.Run("plan is invisible and immutable to another user", func(t *testing.T) {
		before := f.do(http.MethodGet, planPath, f.aliceToken, nil)
		if before.Code != http.StatusOK {
			t.Fatalf("A's GET: status = %d, want 200; body: %s", before.Code, before.Body)
		}

		missing := f.do(http.MethodGet, "/v1/workout-plans/"+uuid.NewString(), f.bobToken, nil)
		foreign := f.do(http.MethodGet, planPath, f.bobToken, nil)
		if got, want := ownE2ERequire404(t, foreign), ownE2ERequire404(t, missing); got != want {
			t.Errorf("foreign 404 body differs from missing 404 body:\n foreign: %s\n missing: %s", got, want)
		}

		ownE2ERequire404(t, f.do(http.MethodPut, planPath, f.bobToken,
			f.planBody("Bob hijack", at.Add(time.Second))))
		if got := f.do(http.MethodGet, planPath, f.aliceToken, nil).Body.String(); got != before.Body.String() {
			t.Errorf("A's plan changed after B's PUT:\n before: %s\n after:  %s", before.Body, got)
		}

		ownE2ERequire404(t, f.do(http.MethodDelete, planPath, f.bobToken, nil))
		after := f.do(http.MethodGet, planPath, f.aliceToken, nil)
		if after.Code != http.StatusOK {
			t.Fatalf("A's GET after B's DELETE: status = %d, want 200; body: %s", after.Code, after.Body)
		}
		if after.Body.String() != before.Body.String() {
			t.Errorf("A's plan changed after B's DELETE:\n before: %s\n after:  %s", before.Body, after.Body)
		}
	})

	t.Run("lists do not leak another user's ids", func(t *testing.T) {
		check := func(listPath, ownID, foreignID string) {
			t.Helper()
			rec := f.do(http.MethodGet, listPath, f.bobToken, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200; body: %s", listPath, rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if !strings.Contains(body, ownID) {
				t.Errorf("GET %s: B's own id %s missing from the list: %s", listPath, ownID, body)
			}
			if strings.Contains(body, foreignID) {
				t.Errorf("GET %s: A's id %s leaked into B's list: %s", listPath, foreignID, body)
			}
		}
		// Default list and sync pull (updated_since includes soft-deleted rows).
		check("/v1/progress", bobProgressID.String(), progressID.String())
		check("/v1/progress?updated_since=1970-01-01T00:00:00Z", bobProgressID.String(), progressID.String())
		check("/v1/workout-plans", bobPlanID.String(), planID.String())
		check("/v1/workout-plans?updated_since=1970-01-01T00:00:00Z", bobPlanID.String(), planID.String())
	})

	t.Run("progress cannot reference another user's plan", func(t *testing.T) {
		rec := f.do(http.MethodPut, "/v1/progress/"+uuid.NewString(), f.bobToken,
			f.progressBody("Bob attaches to Alice's plan", &planID, at))
		body := apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		found := false
		for _, d := range body.Error.Details {
			if d.Field == "workout_plan_id" && d.Issue == "unknown_reference" {
				found = true
			}
		}
		if !found {
			t.Errorf("details = %+v, want workout_plan_id unknown_reference", body.Error.Details)
		}
	})
}
