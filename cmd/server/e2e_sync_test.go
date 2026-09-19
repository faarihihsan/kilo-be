package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/testutil"
)

// T7.3: two-phone sync scenarios against the full stack (real router, real
// Postgres). One user has two real tokens, standing in for two phones. The
// tests assert that the sync feed is driven by server_updated_at and never by
// the client's updated_at.

// e2eSyncApp wires the application on a throwaway database, turns the per-IP
// rate limiter off (the tests send many requests from one address) and seeds
// one user with two tokens, one per phone.
func e2eSyncApp(t *testing.T) (h http.Handler, phone1, phone2 string) {
	t.Helper()
	a, db := newTestApp(t)
	a.router.RateLimit = nil
	userID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	phone1, _ = testutil.SeedToken(t, db, userID)
	phone2, _ = testutil.SeedToken(t, db, userID)
	return a.Handler(), phone1, phone2
}

// e2eSyncDo runs one authenticated request. body is JSON already, or "" for a
// request without one.
func e2eSyncDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func e2eSyncDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v\nstatus: %d\nbody: %s", err, rec.Code, rec.Body.String())
	}
	return out
}

func e2eSyncRequireStatus(t *testing.T, what string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status = %d, want %d; body: %s", what, rec.Code, want, rec.Body.String())
	}
}

func e2eSyncNewID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

func e2eSyncStamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// e2eSyncPull renders a sync pull query: updated_since drives the feed and
// expand=exercises returns full objects.
func e2eSyncPull(since time.Time, cursor string, limit int) string {
	q := url.Values{}
	q.Set("updated_since", e2eSyncStamp(since))
	q.Set("expand", "exercises")
	q.Set("limit", strconv.Itoa(limit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return "?" + q.Encode()
}

// e2eSyncPage is e2eSyncPull with limit=1, to force paging.
func e2eSyncPage(since time.Time, cursor string) string { return e2eSyncPull(since, cursor, 1) }

// e2eSyncCurrent decodes a conflict's details[0].current (any after JSON
// decoding) into the resource's response type.
func e2eSyncCurrent[T any](t *testing.T, current any) T {
	t.Helper()
	b, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal conflict current: %v", err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode conflict current: %v\njson: %s", err, b)
	}
	return out
}

func e2eSyncRequireStale[T any](t *testing.T, what string, rec *httptest.ResponseRecorder, want T, same func(T, T) bool, describe func(T) string) {
	t.Helper()
	body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "stale" {
		t.Fatalf("%s: conflict details = %+v, want one issue stale", what, body.Error.Details)
	}
	current := e2eSyncCurrent[T](t, body.Error.Details[0].Current)
	if !same(current, want) {
		t.Fatalf("%s: current = %s, want the server's copy %s", what, describe(current), describe(want))
	}
}

func e2eSyncRequireDeleted(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "deleted" {
		t.Fatalf("%s: conflict details = %+v, want one issue deleted", what, body.Error.Details)
	}
}

// --- request bodies ---------------------------------------------------------

type e2eSyncSet struct {
	Position  int      `json:"position"`
	Type      string   `json:"type"`
	Reps      *int     `json:"reps"`
	Weight    *float64 `json:"weight"`
	Completed bool     `json:"completed"`
}

type e2eSyncProgressExercise struct {
	ExerciseID string       `json:"exercise_id"`
	Position   int          `json:"position"`
	Sets       []e2eSyncSet `json:"sets"`
}

type e2eSyncProgressBody struct {
	Name            string                    `json:"name"`
	StartedAt       string                    `json:"started_at"`
	EndedAt         string                    `json:"ended_at"`
	DurationSeconds int                       `json:"duration_seconds"`
	UpdatedAt       string                    `json:"updated_at"`
	Exercises       []e2eSyncProgressExercise `json:"exercises"`
}

func e2eSyncProgressJSON(name, updatedAt string, exercises ...e2eSyncProgressExercise) string {
	b, err := json.Marshal(e2eSyncProgressBody{
		Name:            name,
		StartedAt:       "2026-09-19T08:00:00Z",
		EndedAt:         "2026-09-19T08:55:00Z",
		DurationSeconds: 3120,
		UpdatedAt:       updatedAt,
		Exercises:       append([]e2eSyncProgressExercise{}, exercises...),
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

type e2eSyncPlanBody struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
	Exercises []any  `json:"exercises"`
}

func e2eSyncPlanJSON(name, updatedAt string) string {
	b, err := json.Marshal(e2eSyncPlanBody{Name: name, UpdatedAt: updatedAt, Exercises: []any{}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// --- progress: endpoints 3, 5, 16 ------------------------------------------

func TestE2ESyncProgressTwoPhones(t *testing.T) {
	h, phone1, phone2 := e2eSyncApp(t)

	// Phone 1 uploads a session while offline: an old client updated_at is
	// stored anyway, and server_updated_at is the server's now.
	oldClient := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	body1 := e2eSyncProgressJSON("Offline Push A", e2eSyncStamp(oldClient))

	id1 := e2eSyncNewID(t)
	before := time.Now().UTC()
	rec1 := e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+id1.String(), phone1, body1)
	after := time.Now().UTC()
	e2eSyncRequireStatus(t, "phone 1 offline upload", rec1, http.StatusCreated)
	saved1 := e2eSyncDecode[domain.Progress](t, rec1)

	if saved1.ID != id1 {
		t.Errorf("id = %s, want %s", saved1.ID, id1)
	}
	if !saved1.UpdatedAt.Equal(oldClient) {
		t.Errorf("updated_at = %s, want the client's old %s", saved1.UpdatedAt, oldClient)
	}
	if !saved1.ServerUpdatedAt.After(oldClient) {
		t.Errorf("server_updated_at = %s, want it after the client's %s", saved1.ServerUpdatedAt, oldClient)
	}
	if saved1.ServerUpdatedAt.Before(before.Add(-time.Second)) || saved1.ServerUpdatedAt.After(after.Add(time.Second)) {
		t.Errorf("server_updated_at = %s, want the server time (%s..%s)", saved1.ServerUpdatedAt, before, after)
	}
	if saved1.ServerUpdatedAt.Equal(saved1.UpdatedAt) {
		t.Error("server_updated_at equals the client updated_at")
	}

	// A second offline upload, so the pull has two rows to page through.
	id2 := e2eSyncNewID(t)
	rec2 := e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+id2.String(), phone1,
		e2eSyncProgressJSON("Offline Push B", e2eSyncStamp(oldClient)))
	e2eSyncRequireStatus(t, "phone 1 second offline upload", rec2, http.StatusCreated)
	saved2 := e2eSyncDecode[domain.Progress](t, rec2)

	// A retry after a lost response is a no-op: same updated_at, 200, the same
	// stored copy and an unchanged server_updated_at.
	retryRec := e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+id1.String(), phone1, body1)
	e2eSyncRequireStatus(t, "phone 1 retry", retryRec, http.StatusOK)
	retried := e2eSyncDecode[domain.Progress](t, retryRec)
	if retried.Name != saved1.Name || !retried.UpdatedAt.Equal(saved1.UpdatedAt) ||
		!retried.CreatedAt.Equal(saved1.CreatedAt) || !retried.ServerUpdatedAt.Equal(saved1.ServerUpdatedAt) {
		t.Errorf("retry changed the stored session:\n got %+v\nwant %+v", retried, saved1)
	}

	// Phone 2 pulls with updated_since set to the client's old updated_at.
	// Because the feed uses server_updated_at, the rows are visible even though
	// their client time is not after the bound.
	page1 := e2eSyncDecode[domain.ProgressPage[domain.Progress]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/progress"+e2eSyncPage(oldClient, ""), phone2, ""))
	if len(page1.Items) != 1 || page1.Items[0].ID != id1 || page1.NextCursor == nil {
		t.Fatalf("pull page 1 = %d items, next %v; want [%s] and a cursor", len(page1.Items), page1.NextCursor, id1)
	}
	if page1.Items[0].Exercises == nil {
		t.Error("pull page 1 is a summary, want the expand=exercises full object")
	}
	page2 := e2eSyncDecode[domain.ProgressPage[domain.Progress]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/progress"+e2eSyncPage(oldClient, *page1.NextCursor), phone2, ""))
	if len(page2.Items) != 1 || page2.Items[0].ID != id2 || page2.NextCursor != nil {
		t.Fatalf("pull page 2 = %d items, next %v; want [%s] and no cursor", len(page2.Items), page2.NextCursor, id2)
	}

	// Pulling from the newest server_updated_at returns nothing: the bound is a
	// strict comparison on the server cursor.
	empty := e2eSyncDecode[domain.ProgressPage[domain.Progress]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/progress"+e2eSyncPage(saved2.ServerUpdatedAt, ""), phone2, ""))
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("pull after newest = %d items, next %v; want empty", len(empty.Items), empty.NextCursor)
	}

	// Phone 2 writes an older updated_at: 409 stale with the server's copy.
	staleRec := e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+id1.String(), phone2,
		e2eSyncProgressJSON("Stale Push", e2eSyncStamp(oldClient.Add(-time.Hour))))
	e2eSyncRequireStale(t, "phone 2 stale write", staleRec, saved1,
		func(a, b domain.Progress) bool {
			return a.ID == b.ID && a.UpdatedAt.Equal(b.UpdatedAt) && a.ServerUpdatedAt.Equal(b.ServerUpdatedAt)
		},
		func(p domain.Progress) string { return p.ID.String() + " updated_at " + p.UpdatedAt.String() })

	// Phone 1 deletes the session; phone 2 sees the tombstone on its pull.
	e2eSyncRequireStatus(t, "phone 1 delete", e2eSyncDo(t, h, http.MethodDelete, "/v1/progress/"+id1.String(), phone1, ""), http.StatusNoContent)

	pull := e2eSyncDecode[domain.ProgressPage[domain.Progress]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/progress"+e2eSyncPull(saved1.ServerUpdatedAt, "", 50), phone2, ""))
	var tombstone *domain.Progress
	for i := range pull.Items {
		if pull.Items[i].ID == id1 {
			tombstone = &pull.Items[i]
		}
	}
	if tombstone == nil || tombstone.DeletedAt == nil {
		t.Fatalf("phone 2 pull after delete did not show %s as deleted: %+v", id1, pull.Items)
	}

	// A later save of the same id loses to the deletion, even with a newer
	// client time.
	e2eSyncRequireDeleted(t, "phone 2 save after delete", e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+id1.String(), phone2,
		e2eSyncProgressJSON("Undo attempt", e2eSyncStamp(time.Now().UTC().Truncate(time.Microsecond)))))

	// The default list hides the deleted session and keeps the live one.
	list := e2eSyncDecode[domain.ProgressPage[domain.ProgressSummary]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/progress", phone2, ""))
	if len(list.Items) != 1 || list.Items[0].ID != id2 {
		t.Fatalf("default list = %+v, want only %s", list.Items, id2)
	}
}

// --- workout plans: endpoints 10, 6, 17 ------------------------------------

func TestE2ESyncWorkoutPlansTwoPhones(t *testing.T) {
	h, phone1, phone2 := e2eSyncApp(t)

	oldClient := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	body1 := e2eSyncPlanJSON("Offline Plan A", e2eSyncStamp(oldClient))

	id1 := e2eSyncNewID(t)
	before := time.Now().UTC()
	rec1 := e2eSyncDo(t, h, http.MethodPut, "/v1/workout-plans/"+id1.String(), phone1, body1)
	after := time.Now().UTC()
	e2eSyncRequireStatus(t, "phone 1 offline plan upload", rec1, http.StatusCreated)
	saved1 := e2eSyncDecode[domain.Plan](t, rec1)

	if saved1.ID != id1 {
		t.Errorf("id = %s, want %s", saved1.ID, id1)
	}
	if !saved1.UpdatedAt.Equal(oldClient) {
		t.Errorf("updated_at = %s, want the client's old %s", saved1.UpdatedAt, oldClient)
	}
	if !saved1.ServerUpdatedAt.After(oldClient) {
		t.Errorf("server_updated_at = %s, want it after the client's %s", saved1.ServerUpdatedAt, oldClient)
	}
	if saved1.ServerUpdatedAt.Before(before.Add(-time.Second)) || saved1.ServerUpdatedAt.After(after.Add(time.Second)) {
		t.Errorf("server_updated_at = %s, want the server time (%s..%s)", saved1.ServerUpdatedAt, before, after)
	}

	id2 := e2eSyncNewID(t)
	rec2 := e2eSyncDo(t, h, http.MethodPut, "/v1/workout-plans/"+id2.String(), phone1,
		e2eSyncPlanJSON("Offline Plan B", e2eSyncStamp(oldClient)))
	e2eSyncRequireStatus(t, "phone 1 second offline plan upload", rec2, http.StatusCreated)
	saved2 := e2eSyncDecode[domain.Plan](t, rec2)

	// Retry after a lost response: same updated_at, 200 no-op, same row.
	retryRec := e2eSyncDo(t, h, http.MethodPut, "/v1/workout-plans/"+id1.String(), phone1, body1)
	e2eSyncRequireStatus(t, "phone 1 plan retry", retryRec, http.StatusOK)
	retried := e2eSyncDecode[domain.Plan](t, retryRec)
	if retried.Name != saved1.Name || !retried.UpdatedAt.Equal(saved1.UpdatedAt) ||
		!retried.CreatedAt.Equal(saved1.CreatedAt) || !retried.ServerUpdatedAt.Equal(saved1.ServerUpdatedAt) {
		t.Errorf("retry changed the stored plan:\n got %+v\nwant %+v", retried, saved1)
	}

	// Phone 2 pages through the sync feed.
	page1 := e2eSyncDecode[domain.PlanPage[domain.Plan]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/workout-plans"+e2eSyncPage(oldClient, ""), phone2, ""))
	if len(page1.Items) != 1 || page1.Items[0].ID != id1 || page1.NextCursor == nil {
		t.Fatalf("pull page 1 = %d items, next %v; want [%s] and a cursor", len(page1.Items), page1.NextCursor, id1)
	}
	if page1.Items[0].Exercises == nil {
		t.Error("pull page 1 is a summary, want the expand=exercises full object")
	}
	page2 := e2eSyncDecode[domain.PlanPage[domain.Plan]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/workout-plans"+e2eSyncPage(oldClient, *page1.NextCursor), phone2, ""))
	if len(page2.Items) != 1 || page2.Items[0].ID != id2 || page2.NextCursor != nil {
		t.Fatalf("pull page 2 = %d items, next %v; want [%s] and no cursor", len(page2.Items), page2.NextCursor, id2)
	}

	empty := e2eSyncDecode[domain.PlanPage[domain.Plan]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/workout-plans"+e2eSyncPage(saved2.ServerUpdatedAt, ""), phone2, ""))
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("pull after newest = %d items, next %v; want empty", len(empty.Items), empty.NextCursor)
	}

	// Stale write from the other phone: 409 with the server's current plan.
	e2eSyncRequireStale(t, "phone 2 stale plan write",
		e2eSyncDo(t, h, http.MethodPut, "/v1/workout-plans/"+id1.String(), phone2,
			e2eSyncPlanJSON("Stale Plan", e2eSyncStamp(oldClient.Add(-time.Hour)))),
		saved1,
		func(a, b domain.Plan) bool {
			return a.ID == b.ID && a.UpdatedAt.Equal(b.UpdatedAt) && a.ServerUpdatedAt.Equal(b.ServerUpdatedAt)
		},
		func(p domain.Plan) string { return p.ID.String() + " updated_at " + p.UpdatedAt.String() })

	// Delete on phone 1, tombstone on phone 2's pull.
	e2eSyncRequireStatus(t, "phone 1 delete plan",
		e2eSyncDo(t, h, http.MethodDelete, "/v1/workout-plans/"+id1.String(), phone1, ""), http.StatusNoContent)

	pull := e2eSyncDecode[domain.PlanPage[domain.Plan]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/workout-plans"+e2eSyncPull(saved1.ServerUpdatedAt, "", 50), phone2, ""))
	var tombstone *domain.Plan
	for i := range pull.Items {
		if pull.Items[i].ID == id1 {
			tombstone = &pull.Items[i]
		}
	}
	if tombstone == nil || tombstone.DeletedAt == nil {
		t.Fatalf("phone 2 pull after delete did not show %s as deleted: %+v", id1, pull.Items)
	}

	// A later save of the deleted id is refused.
	e2eSyncRequireDeleted(t, "phone 2 save after delete", e2eSyncDo(t, h, http.MethodPut, "/v1/workout-plans/"+id1.String(), phone2,
		e2eSyncPlanJSON("Undo attempt", e2eSyncStamp(time.Now().UTC().Truncate(time.Microsecond)))))

	// The default list hides the deleted plan.
	list := e2eSyncDecode[domain.PlanPage[domain.PlanSummary]](t,
		e2eSyncDo(t, h, http.MethodGet, "/v1/workout-plans", phone2, ""))
	if len(list.Items) != 1 || list.Items[0].ID != id2 {
		t.Fatalf("default list = %+v, want only %s", list.Items, id2)
	}
}

// --- deleted exercise referenced by an offline save -------------------------

const e2eSyncExerciseBody = `{"name":"Offline Deleted Exercise","category":"strength",` +
	`"primary_muscle_group":"chest","equipment":"barbell","measurement_type":"reps_weight"}`

func TestE2ESyncDeletedExerciseReferencedByOfflineSave(t *testing.T) {
	h, phone1, _ := e2eSyncApp(t)

	createRec := e2eSyncDo(t, h, http.MethodPost, "/v1/exercises", phone1, e2eSyncExerciseBody)
	e2eSyncRequireStatus(t, "create exercise", createRec, http.StatusCreated)
	ex := e2eSyncDecode[domain.Exercise](t, createRec)

	e2eSyncRequireStatus(t, "delete exercise",
		e2eSyncDo(t, h, http.MethodDelete, "/v1/exercises/"+ex.ID.String(), phone1, ""), http.StatusNoContent)

	list := e2eSyncDecode[domain.ExercisePage](t, e2eSyncDo(t, h, http.MethodGet, "/v1/exercises", phone1, ""))
	for _, e := range list.Items {
		if e.ID == ex.ID {
			t.Fatalf("soft-deleted exercise %s is still in the default list", ex.ID)
		}
	}

	// An offline save made before the phone learned of the deletion still
	// references the exercise; a soft-deleted reference is accepted.
	sessionID := e2eSyncNewID(t)
	saveRec := e2eSyncDo(t, h, http.MethodPut, "/v1/progress/"+sessionID.String(), phone1,
		e2eSyncProgressJSON("Saved after exercise delete",
			e2eSyncStamp(time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)),
			e2eSyncProgressExercise{
				ExerciseID: ex.ID.String(),
				Position:   0,
				Sets: []e2eSyncSet{{
					Position:  0,
					Type:      "normal",
					Reps:      testutil.Ptr(10),
					Weight:    testutil.Ptr(50.0),
					Completed: true,
				}},
			}))
	e2eSyncRequireStatus(t, "save referencing deleted exercise", saveRec, http.StatusCreated)
	saved := e2eSyncDecode[domain.Progress](t, saveRec)
	if len(saved.Exercises) != 1 || saved.Exercises[0].ExerciseID != ex.ID {
		t.Fatalf("saved session exercises = %+v, want the deleted exercise %s", saved.Exercises, ex.ID)
	}
}
