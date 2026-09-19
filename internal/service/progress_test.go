package service_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

// progressSvcEnv is a service on a throwaway database, with a fake clock.
type progressSvcEnv struct {
	svc         *service.Progress
	clock       *clock.Fake
	user, other uuid.UUID
	ex          [2]uuid.UUID
	count       func(t *testing.T, sql string, args ...any) int
}

// progressSvcNow is the fake clock's start.
var progressSvcNow = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

func progressSvcNewEnv(t *testing.T) *progressSvcEnv {
	t.Helper()
	db := testutil.NewDB(t)
	e := &progressSvcEnv{clock: clock.NewFake(progressSvcNow)}
	e.svc = service.NewProgress(service.Deps{DB: db, Clock: e.clock})
	e.user, _ = testutil.SeedUser(t, db, domain.RoleUser)
	e.other, _ = testutil.SeedUser(t, db, domain.RoleUser)
	for i := range e.ex {
		e.ex[i] = testutil.SeedExercise(t, db, e.user)
	}
	e.count = func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	return e
}

// request is a valid save request whose updated_at is the given RFC 3339 time.
func (e *progressSvcEnv) request(t *testing.T, updatedAt string) *domain.ProgressSaveRequest {
	t.Helper()
	body := fmt.Sprintf(`{
		"name": "Push Day A", "notes": "Felt strong",
		"started_at": "2026-09-19T08:00:00Z", "ended_at": "2026-09-19T08:55:00Z",
		"duration_seconds": 3120, "updated_at": %q,
		"exercises": [
			{"exercise_id": %q, "position": 0, "notes": null, "sets": [
				{"position": 0, "type": "warmup", "reps": 12, "weight": 40.0, "rpe": null, "completed": true},
				{"position": 1, "type": "normal", "reps": 8, "weight": 80.25, "rpe": 8.5, "completed": true}]},
			{"exercise_id": %q, "position": 1, "sets": []}
		]}`, updatedAt, e.ex[0], e.ex[1])
	var r domain.ProgressSaveRequest
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func progressSvcJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func progressSvcIssues(t *testing.T, err error) []string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	var out []string
	for _, is := range ve.Issues {
		out = append(out, is.Field+": "+is.Issue)
	}
	return out
}

func TestProgressServiceSave(t *testing.T) {
	e := progressSvcNewEnv(t)
	id := uuid.Must(uuid.NewV7())

	got, created, err := e.svc.Save(t.Context(), e.user, id, e.request(t, "2026-09-19T08:55:02Z"))
	if err != nil || !created {
		t.Fatalf("Save = created %v, err %v, want a create", created, err)
	}
	if got.ID != id || len(got.Exercises) != 2 || got.Exercises[0].Sets[1].Weight == nil || *got.Exercises[0].Sets[1].Weight != "80.25" {
		t.Errorf("session = %s", progressSvcJSON(t, got))
	}

	again, created, err := e.svc.Save(t.Context(), e.user, id, e.request(t, "2026-09-19T08:55:02Z"))
	if err != nil || created || progressSvcJSON(t, again) != progressSvcJSON(t, got) {
		t.Errorf("retry = created %v, err %v: want the same session back and no create", created, err)
	}

	_, _, err = e.svc.Save(t.Context(), e.user, id, e.request(t, "2026-09-19T08:00:00Z"))
	var ce *domain.ConflictError
	if !errors.As(err, &ce) || ce.Issue != domain.IssueStale {
		t.Errorf("older save: %v, want a stale conflict", err)
	}
	if cur, ok := ce.Current.(domain.Progress); !ok || cur.ID != id {
		t.Errorf("stale conflict carries %T, want the stored domain.Progress", ce.Current)
	}

	if _, _, err := e.svc.Save(t.Context(), e.other, id, e.request(t, "2026-09-19T08:59:00Z")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another user's id: %v, want not found", err)
	}
}

func TestProgressServiceSaveValidatesBeforeTouchingTheDatabase(t *testing.T) {
	e := progressSvcNewEnv(t)
	id := uuid.Must(uuid.NewV7())

	req := e.request(t, "2026-09-19T08:55:02Z")
	req.Name = ""
	req.Exercises[0].Sets[0].Type = "bogus"
	// An unknown exercise would be a store-level 422; validation must stop first.
	req.Exercises[1].ExerciseID = uuid.NewString()
	_, _, err := e.svc.Save(t.Context(), e.user, id, req)
	want := []string{"name: required", "exercises[0].sets[0].type: invalid_value"}
	if got := progressSvcIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("issues = %q, want %q", got, want)
	}
	if n := e.count(t, `SELECT count(*) FROM progress`); n != 0 {
		t.Errorf("%d rows written by a rejected request", n)
	}
}

func TestProgressServiceSaveFutureUpdatedAtUsesTheClock(t *testing.T) {
	e := progressSvcNewEnv(t)

	// The clock says 09:00:00, so 09:05:00 is the last accepted updated_at.
	if _, _, err := e.svc.Save(t.Context(), e.user, uuid.Must(uuid.NewV7()), e.request(t, "2026-09-19T09:05:00Z")); err != nil {
		t.Fatalf("exactly 5 minutes ahead: %v", err)
	}
	_, _, err := e.svc.Save(t.Context(), e.user, uuid.Must(uuid.NewV7()), e.request(t, "2026-09-19T09:05:00.000001Z"))
	if got := progressSvcIssues(t, err); !slices.Equal(got, []string{"updated_at: too_far_in_future"}) {
		t.Errorf("issues = %q", got)
	}

	e.clock.Advance(time.Second)
	if _, _, err := e.svc.Save(t.Context(), e.user, uuid.Must(uuid.NewV7()), e.request(t, "2026-09-19T09:05:00.000001Z")); err != nil {
		t.Errorf("after the clock moved on: %v", err)
	}
}

func TestProgressServiceGetAndDelete(t *testing.T) {
	e := progressSvcNewEnv(t)
	id := uuid.Must(uuid.NewV7())
	saved, _, err := e.svc.Save(t.Context(), e.user, id, e.request(t, "2026-09-19T08:55:02Z"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Get(t.Context(), e.user, id)
	if err != nil || progressSvcJSON(t, got) != progressSvcJSON(t, saved) {
		t.Errorf("Get = %v, %v", progressSvcJSON(t, got), err)
	}
	if _, err := e.svc.Get(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get by another user: %v, want not found", err)
	}
	if err := e.svc.Delete(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete by another user: %v, want not found", err)
	}
	if err := e.svc.Delete(t.Context(), e.user, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := e.svc.Delete(t.Context(), e.user, id); err != nil {
		t.Errorf("Delete again: %v, want idempotent success", err)
	}
	if _, err := e.svc.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get after delete: %v, want not found", err)
	}
	_, _, err = e.svc.Save(t.Context(), e.user, id, e.request(t, "2026-09-19T08:59:00Z"))
	var ce *domain.ConflictError
	if !errors.As(err, &ce) || ce.Issue != domain.IssueDeleted {
		t.Errorf("Save after delete: %v, want a deleted conflict", err)
	}
	if err := e.svc.Delete(t.Context(), e.user, uuid.Must(uuid.NewV7())); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete of an unknown id: %v, want not found", err)
	}
}

func TestProgressServiceList(t *testing.T) {
	e := progressSvcNewEnv(t)
	var saved []domain.Progress
	for i := range 5 {
		req := e.request(t, "2026-09-19T08:55:02Z")
		req.StartedAt = fmt.Sprintf("2026-09-%02dT08:00:00Z", 10+i)
		req.EndedAt = fmt.Sprintf("2026-09-%02dT09:00:00Z", 10+i)
		s, _, err := e.svc.Save(t.Context(), e.user, uuid.Must(uuid.NewV7()), req)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, s)
	}
	newestFirst := []uuid.UUID{saved[4].ID, saved[3].ID, saved[2].ID, saved[1].ID, saved[0].ID}

	t.Run("summaries walk the pages through next_cursor", func(t *testing.T) {
		var got []uuid.UUID
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			page, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				got = append(got, it.ID)
				if it.ExerciseCount != 2 || it.SetCount != 2 {
					t.Errorf("counts = %d, %d, want 2, 2", it.ExerciseCount, it.SetCount)
				}
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if !slices.Equal(got, newestFirst) {
			t.Errorf("walk = %v, want %v", got, newestFirst)
		}
	})

	t.Run("expanded walk", func(t *testing.T) {
		var got []uuid.UUID
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			page, err := e.svc.ListExpanded(t.Context(), e.user, domain.ProgressListParams{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				got = append(got, it.ID)
				if len(it.Exercises) != 2 {
					t.Errorf("session %v has %d exercises, want 2", it.ID, len(it.Exercises))
				}
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if !slices.Equal(got, newestFirst) {
			t.Errorf("walk = %v, want %v", got, newestFirst)
		}
	})

	t.Run("the next cursor is the opaque (started_at, id) of the last item", func(t *testing.T) {
		page, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{Limit: 2})
		if err != nil || page.NextCursor == nil {
			t.Fatalf("page = %+v, %v", page, err)
		}
		c, err := domain.DecodeCursor(*page.NextCursor)
		if err != nil || c.ID != saved[3].ID || !c.At.Equal(saved[3].StartedAt) {
			t.Errorf("cursor = %+v, %v, want (started_at, id) of the second item", c, err)
		}
	})

	t.Run("limit 0 is the default page size", func(t *testing.T) {
		page, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{})
		if err != nil || len(page.Items) != 5 || page.NextCursor != nil {
			t.Errorf("page = %d items, cursor %v, %v", len(page.Items), page.NextCursor, err)
		}
	})

	t.Run("limit out of range is a 422 on limit", func(t *testing.T) {
		for _, limit := range []int{-1, 201, 1 << 30} {
			_, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{Limit: limit})
			if got := progressSvcIssues(t, err); !slices.Equal(got, []string{"limit: out_of_range"}) {
				t.Errorf("limit %d: issues = %q", limit, got)
			}
			if _, err := e.svc.ListExpanded(t.Context(), e.user, domain.ProgressListParams{Limit: limit}); !errors.Is(err, domain.ErrValidation) {
				t.Errorf("limit %d expanded: %v", limit, err)
			}
		}
		for _, limit := range []int{1, 200} {
			if _, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{Limit: limit}); err != nil {
				t.Errorf("limit %d: %v", limit, err)
			}
		}
	})

	t.Run("a bad cursor is a 400", func(t *testing.T) {
		for _, c := range []string{"garbage", "AAAA", strings.Repeat("A", 2000)} {
			_, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{Cursor: c})
			if !errors.Is(err, domain.ErrBadRequest) {
				t.Errorf("cursor %.10q: %v, want bad request", c, err)
			}
			if _, err := e.svc.ListExpanded(t.Context(), e.user, domain.ProgressListParams{Cursor: c}); !errors.Is(err, domain.ErrBadRequest) {
				t.Errorf("cursor %.10q expanded: %v, want bad request", c, err)
			}
		}
	})

	t.Run("an empty page has non-nil items", func(t *testing.T) {
		page, err := e.svc.List(t.Context(), e.other, domain.ProgressListParams{})
		if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
			t.Errorf("page = %+v, %v", page, err)
		}
		exp, err := e.svc.ListExpanded(t.Context(), e.other, domain.ProgressListParams{})
		if err != nil || exp.Items == nil || len(exp.Items) != 0 {
			t.Errorf("expanded page = %+v, %v", exp, err)
		}
		if got := progressSvcJSON(t, page); got != `{"items":[],"next_cursor":null}` {
			t.Errorf("json = %s", got)
		}
	})

	t.Run("updated_since is sync mode and includes deleted rows", func(t *testing.T) {
		if err := e.svc.Delete(t.Context(), e.user, saved[2].ID); err != nil {
			t.Fatal(err)
		}
		since := time.Unix(0, 0).UTC()
		page, err := e.svc.List(t.Context(), e.user, domain.ProgressListParams{
			ProgressListFilter: domain.ProgressListFilter{UpdatedSince: &since}})
		if err != nil || len(page.Items) != 5 {
			t.Fatalf("page = %d items, %v, want 5 including the deleted one", len(page.Items), err)
		}
		last := page.Items[4]
		if last.ID != saved[2].ID || last.DeletedAt == nil {
			t.Errorf("last item = %+v, want the deleted session, newest in the feed", last)
		}
	})
}
