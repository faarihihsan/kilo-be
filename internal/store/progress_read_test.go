package store_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

// progressFixedID is a uuid whose order is its number, for deterministic
// tie-breaks.
func progressFixedID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("0195f3a2-cccc-7000-8000-%012d", n))
}

func progressIDs[T domain.ProgressSummary | domain.Progress](items []T) []uuid.UUID {
	out := make([]uuid.UUID, len(items))
	for i, it := range items {
		switch v := any(it).(type) {
		case domain.ProgressSummary:
			out[i] = v.ID
		case domain.Progress:
			out[i] = v.ID
		}
	}
	return out
}

// progressListEnv is a dataset with ties in both sort keys:
//
//	id  started_at  server_updated_at  notes
//	1   +0h         +50m               plan, 2 exercises / 3 sets
//	2   +1h         +10m
//	3   +1h         +10m               same keys as 2: the id decides
//	4   +2h         +30m               deleted, 1 exercise / 1 set
//	5   +3h         +20m               plan, no exercises
//	6   +4h         +60m
//
// plus one session of another user with the newest started_at.
type progressListEnv struct {
	*progressEnv
	plan uuid.UUID
}

func progressNewListEnv(t *testing.T) *progressListEnv {
	t.Helper()
	e := &progressListEnv{progressEnv: progressNewEnv(t)}
	e.plan = testutil.SeedPlan(t, e.db, e.user)
	seed := func(uid uuid.UUID, n int, startH, serverMin int, opts ...testutil.ProgressOption) {
		opts = append([]testutil.ProgressOption{
			testutil.WithProgressID(progressFixedID(n)),
			testutil.WithProgressName(fmt.Sprintf("session %d", n)),
			testutil.WithProgressStartedAt(progressBase.Add(time.Duration(startH) * time.Hour)),
			testutil.WithProgressDuration(1800),
			testutil.WithProgressClientUpdatedAt(progressBase.Add(time.Duration(n) * time.Minute)),
			testutil.WithProgressServerUpdatedAt(progressBase.Add(time.Duration(serverMin) * time.Minute)),
		}, opts...)
		testutil.SeedProgress(t, e.db, uid, opts...)
	}
	seed(e.user, 1, 0, 50, testutil.WithProgressPlan(e.plan),
		testutil.WithProgressExercise(e.ex[0], testutil.WithSets(2)),
		testutil.WithProgressExercise(e.ex[1], testutil.WithSets(1)))
	seed(e.user, 2, 1, 10)
	seed(e.user, 3, 1, 10)
	seed(e.user, 4, 2, 30, testutil.WithProgressDeletedAt(progressBase.Add(30*time.Minute)),
		testutil.WithProgressExercise(e.ex[2], testutil.WithSets(1)))
	seed(e.user, 5, 3, 20, testutil.WithProgressPlan(e.plan))
	seed(e.user, 6, 4, 60)
	seed(e.other, 99, 9, 99, testutil.WithProgressExercise(e.ex[0], testutil.WithSets(1)))
	return e
}

func progressWant(ns ...int) []uuid.UUID {
	out := make([]uuid.UUID, len(ns))
	for i, n := range ns {
		out[i] = progressFixedID(n)
	}
	return out
}

func (e *progressListEnv) summaries(t *testing.T, f domain.ProgressListFilter, after *domain.Cursor, limit int) ([]domain.ProgressSummary, *domain.Cursor) {
	t.Helper()
	items, next, err := e.store.ListSummaries(t.Context(), e.user, f, after, limit)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	return items, next
}

func progressMinutes(n int) *time.Time {
	t := progressBase.Add(time.Duration(n) * time.Minute)
	return &t
}

func TestProgressListDefaultOrder(t *testing.T) {
	e := progressNewListEnv(t)

	t.Run("started_at descending, id ascending, deleted and foreign hidden", func(t *testing.T) {
		items, next := e.summaries(t, domain.ProgressListFilter{}, nil, 50)
		if want := progressWant(6, 5, 2, 3, 1); !slices.Equal(progressIDs(items), want) {
			t.Errorf("ids = %v, want %v", progressIDs(items), want)
		}
		if next != nil {
			t.Errorf("next = %v on the last page", next)
		}
	})

	t.Run("include_deleted", func(t *testing.T) {
		items, _ := e.summaries(t, domain.ProgressListFilter{IncludeDeleted: true}, nil, 50)
		if want := progressWant(6, 5, 4, 2, 3, 1); !slices.Equal(progressIDs(items), want) {
			t.Errorf("ids = %v, want %v", progressIDs(items), want)
		}
	})

	t.Run("cursor walk with a tie across the page boundary", func(t *testing.T) {
		// Pages of 3 and 2 put the 2/3 tie on the boundary of the 2-page walk.
		for _, limit := range []int{1, 2, 3, 4, 5, 6} {
			var got []uuid.UUID
			var after *domain.Cursor
			for pages := 0; ; pages++ {
				if pages > 10 {
					t.Fatal("cursor walk does not end")
				}
				items, next := e.summaries(t, domain.ProgressListFilter{}, after, limit)
				if len(items) > limit {
					t.Fatalf("limit %d: page of %d", limit, len(items))
				}
				got = append(got, progressIDs(items)...)
				if next == nil {
					break
				}
				if len(items) != limit {
					t.Fatalf("limit %d: next cursor on a short page of %d", limit, len(items))
				}
				after = next
			}
			if want := progressWant(6, 5, 2, 3, 1); !slices.Equal(got, want) {
				t.Errorf("limit %d: walk = %v, want %v", limit, got, want)
			}
		}
	})

	t.Run("exactly a full last page has no next cursor", func(t *testing.T) {
		_, next := e.summaries(t, domain.ProgressListFilter{}, nil, 5)
		if next != nil {
			t.Errorf("next = %v, want nil: nothing follows", next)
		}
	})

	t.Run("the cursor is the sort key of the last row", func(t *testing.T) {
		_, next := e.summaries(t, domain.ProgressListFilter{}, nil, 2)
		if next == nil || next.ID != progressFixedID(5) || !next.At.Equal(progressBase.Add(3*time.Hour)) {
			t.Errorf("next = %+v, want (started_at +3h, id 5)", next)
		}
	})
}

func TestProgressListSyncMode(t *testing.T) {
	e := progressNewListEnv(t)
	epoch := time.Unix(0, 0).UTC()

	t.Run("since epoch: everything, deleted included, by (server_updated_at, id)", func(t *testing.T) {
		items, _ := e.summaries(t, domain.ProgressListFilter{UpdatedSince: &epoch}, nil, 50)
		if want := progressWant(2, 3, 5, 4, 1, 6); !slices.Equal(progressIDs(items), want) {
			t.Errorf("ids = %v, want %v", progressIDs(items), want)
		}
		if items[3].DeletedAt == nil || items[3].ID != progressFixedID(4) {
			t.Errorf("the deleted session is missing its deleted_at: %+v", items[3])
		}
	})

	t.Run("since is exclusive", func(t *testing.T) {
		items, _ := e.summaries(t, domain.ProgressListFilter{UpdatedSince: progressMinutes(20)}, nil, 50)
		if want := progressWant(4, 1, 6); !slices.Equal(progressIDs(items), want) {
			t.Errorf("ids = %v, want %v (server_updated_at strictly after +20m)", progressIDs(items), want)
		}
	})

	t.Run("include_deleted=false does not hide deleted rows in sync mode", func(t *testing.T) {
		items, _ := e.summaries(t, domain.ProgressListFilter{UpdatedSince: &epoch, IncludeDeleted: false}, nil, 50)
		if !slices.Contains(progressIDs(items), progressFixedID(4)) {
			t.Error("deleted session missing from the sync feed")
		}
	})

	t.Run("cursor walk", func(t *testing.T) {
		for _, limit := range []int{1, 2, 3, 5} {
			var got []uuid.UUID
			var after *domain.Cursor
			for pages := 0; ; pages++ {
				if pages > 10 {
					t.Fatal("cursor walk does not end")
				}
				items, next := e.summaries(t, domain.ProgressListFilter{UpdatedSince: &epoch}, after, limit)
				got = append(got, progressIDs(items)...)
				if next == nil {
					break
				}
				after = next
			}
			if want := progressWant(2, 3, 5, 4, 1, 6); !slices.Equal(got, want) {
				t.Errorf("limit %d: walk = %v, want %v", limit, got, want)
			}
		}
	})

	t.Run("the cursor is (server_updated_at, id)", func(t *testing.T) {
		_, next := e.summaries(t, domain.ProgressListFilter{UpdatedSince: &epoch}, nil, 1)
		if next == nil || next.ID != progressFixedID(2) || !next.At.Equal(progressBase.Add(10*time.Minute)) {
			t.Errorf("next = %+v, want (+10m, id 2)", next)
		}
	})

	t.Run("a write after the pull shows up in the next pull, and a delete too", func(t *testing.T) {
		items, _ := e.summaries(t, domain.ProgressListFilter{UpdatedSince: &epoch}, nil, 50)
		since := items[len(items)-1].ServerUpdatedAt // the newest server_updated_at seen
		if err := e.store.SoftDelete(t.Context(), e.user, progressFixedID(6)); err != nil {
			t.Fatal(err)
		}
		e.save(t, progressNewID(t), e.input(progressAt(1)))
		items, _ = e.summaries(t, domain.ProgressListFilter{UpdatedSince: &since}, nil, 50)
		if len(items) != 2 || items[0].ID != progressFixedID(6) || items[0].DeletedAt == nil {
			t.Errorf("items = %+v, want the deleted session 6 and the new one", items)
		}
		if !items[0].ServerUpdatedAt.After(since) || !items[1].ServerUpdatedAt.After(items[0].ServerUpdatedAt) {
			t.Error("server_updated_at is not increasing with the writes")
		}
	})
}

func TestProgressListFilters(t *testing.T) {
	e := progressNewListEnv(t)
	epoch := time.Unix(0, 0).UTC()
	at := func(h int) *time.Time { t := progressBase.Add(time.Duration(h) * time.Hour); return &t }

	tests := []struct {
		name string
		f    domain.ProgressListFilter
		want []uuid.UUID
	}{
		{"workout plan", domain.ProgressListFilter{WorkoutPlanID: &e.plan}, progressWant(5, 1)},
		{"workout plan with deleted and sync", domain.ProgressListFilter{WorkoutPlanID: &e.plan, UpdatedSince: &epoch}, progressWant(5, 1)},
		{"unknown plan", domain.ProgressListFilter{WorkoutPlanID: testutil.Ptr(progressNewID(t))}, []uuid.UUID{}},
		{"from is inclusive", domain.ProgressListFilter{From: at(1)}, progressWant(6, 5, 2, 3)},
		{"to is inclusive", domain.ProgressListFilter{To: at(1)}, progressWant(2, 3, 1)},
		{"from and to", domain.ProgressListFilter{From: at(1), To: at(3)}, progressWant(5, 2, 3)},
		{"from after to", domain.ProgressListFilter{From: at(3), To: at(1)}, []uuid.UUID{}},
		{"from with deleted", domain.ProgressListFilter{From: at(2), IncludeDeleted: true}, progressWant(6, 5, 4)},
		{"from in sync mode", domain.ProgressListFilter{From: at(3), UpdatedSince: &epoch}, progressWant(5, 6)},
		{"everything combined", domain.ProgressListFilter{WorkoutPlanID: &e.plan, From: at(1), To: at(3), IncludeDeleted: true}, progressWant(5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, next := e.summaries(t, tt.f, nil, 50)
			if got := progressIDs(items); !slices.Equal(got, tt.want) || next != nil {
				t.Errorf("ids = %v (next %v), want %v", got, next, tt.want)
			}
			if items == nil {
				t.Error("items is nil, want an empty slice for an empty page")
			}
		})
	}
}

func TestProgressListSummaryCounts(t *testing.T) {
	e := progressNewListEnv(t)
	items, _ := e.summaries(t, domain.ProgressListFilter{IncludeDeleted: true}, nil, 50)
	counts := map[uuid.UUID][2]int{}
	for _, it := range items {
		counts[it.ID] = [2]int{it.ExerciseCount, it.SetCount}
	}
	want := map[uuid.UUID][2]int{
		progressFixedID(1): {2, 3},
		progressFixedID(2): {0, 0},
		progressFixedID(3): {0, 0},
		progressFixedID(4): {1, 1}, // children of a deleted session are kept
		progressFixedID(5): {0, 0},
		progressFixedID(6): {0, 0},
	}
	for id, w := range want {
		if counts[id] != w {
			t.Errorf("session %v: (exercises, sets) = %v, want %v", id, counts[id], w)
		}
	}

	// A saved session counts what it stores.
	id := progressNewID(t)
	e.save(t, id, e.input(progressAt(1)))
	items, _ = e.summaries(t, domain.ProgressListFilter{}, nil, 50)
	for _, it := range items {
		if it.ID == id && (it.ExerciseCount != 2 || it.SetCount != 3) {
			t.Errorf("saved session counts = %d, %d, want 2, 3", it.ExerciseCount, it.SetCount)
		}
	}
}

func TestProgressListSummaryFields(t *testing.T) {
	e := progressNewListEnv(t)
	items, _ := e.summaries(t, domain.ProgressListFilter{WorkoutPlanID: &e.plan}, nil, 50)
	first := items[1] // session 1: plan, started +0h
	got := progressJSON(t, first)
	want := fmt.Sprintf(`{"id":%q,"workout_plan_id":%q,"name":"session 1","started_at":"2026-01-15T08:00:00Z",`+
		`"ended_at":"2026-01-15T08:30:00Z","duration_seconds":1800,"exercise_count":2,"set_count":3,`+
		`"updated_at":"2026-01-15T08:01:00Z","server_updated_at":"2026-01-15T08:50:00Z","deleted_at":null}`,
		progressFixedID(1), e.plan)
	if got != want {
		t.Errorf("summary =\n%s\nwant\n%s", got, want)
	}
}

func TestProgressListExpanded(t *testing.T) {
	e := progressNewListEnv(t)
	epoch := time.Unix(0, 0).UTC()

	for name, f := range map[string]domain.ProgressListFilter{
		"default":         {},
		"sync":            {UpdatedSince: &epoch},
		"include deleted": {IncludeDeleted: true},
	} {
		t.Run(name, func(t *testing.T) {
			summaries, _ := e.summaries(t, f, nil, 50)
			full, next, err := e.store.ListExpanded(t.Context(), e.user, f, nil, 50)
			if err != nil {
				t.Fatal(err)
			}
			if next != nil || !slices.Equal(progressIDs(full), progressIDs(summaries)) {
				t.Fatalf("expanded ids = %v (next %v), want the summaries' %v", progressIDs(full), next, progressIDs(summaries))
			}
			for i, s := range full {
				if s.Exercises == nil {
					t.Errorf("session %v: Exercises is nil, want [] at least", s.ID)
				}
				if len(s.Exercises) != summaries[i].ExerciseCount {
					t.Errorf("session %v: %d exercises, summary says %d", s.ID, len(s.Exercises), summaries[i].ExerciseCount)
				}
				sets := 0
				for _, ex := range s.Exercises {
					sets += len(ex.Sets)
				}
				if sets != summaries[i].SetCount {
					t.Errorf("session %v: %d sets, summary says %d", s.ID, sets, summaries[i].SetCount)
				}
				// Every item is exactly what GET returns for a live session.
				if s.DeletedAt == nil {
					got, err := e.store.Get(t.Context(), e.user, s.ID)
					if err != nil || progressJSON(t, got) != progressJSON(t, s) {
						t.Errorf("session %v differs from Get (%v)", s.ID, err)
					}
				}
			}
		})
	}

	t.Run("children of a deleted session are returned in sync mode", func(t *testing.T) {
		full, _, err := e.store.ListExpanded(t.Context(), e.user, domain.ProgressListFilter{UpdatedSince: &epoch}, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range full {
			if s.ID == progressFixedID(4) && (s.DeletedAt == nil || len(s.Exercises) != 1) {
				t.Errorf("deleted session = %s", progressJSON(t, s))
			}
		}
	})

	t.Run("pages carry the same cursor as the summaries", func(t *testing.T) {
		var got []uuid.UUID
		var after *domain.Cursor
		for pages := 0; pages < 10; pages++ {
			full, next, err := e.store.ListExpanded(t.Context(), e.user, domain.ProgressListFilter{}, after, 2)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, progressIDs(full)...)
			if next == nil {
				break
			}
			after = next
		}
		if want := progressWant(6, 5, 2, 3, 1); !slices.Equal(got, want) {
			t.Errorf("walk = %v, want %v", got, want)
		}
	})
}

// TestProgressListCursorStableUnderWrites: rows created or deleted between two
// pages neither repeat nor skip rows of the walk.
func TestProgressListCursorStableUnderWrites(t *testing.T) {
	e := progressNewListEnv(t)
	page1, next := e.summaries(t, domain.ProgressListFilter{}, nil, 2) // 6, 5
	if want := progressWant(6, 5); !slices.Equal(progressIDs(page1), want) || next == nil {
		t.Fatalf("page 1 = %v, next %v", progressIDs(page1), next)
	}

	// A newer session is created (sorts before page 1) and one of the rows
	// already seen is deleted.
	newer := e.input(progressAt(1))
	newer.StartedAt = progressBase.Add(100 * time.Hour)
	newer.EndedAt = newer.StartedAt.Add(time.Hour)
	e.save(t, progressNewID(t), newer)
	if err := e.store.SoftDelete(t.Context(), e.user, progressFixedID(5)); err != nil {
		t.Fatal(err)
	}

	var rest []uuid.UUID
	for after, pages := next, 0; after != nil && pages < 10; pages++ {
		items, n := e.summaries(t, domain.ProgressListFilter{}, after, 2)
		rest = append(rest, progressIDs(items)...)
		after = n
	}
	if want := progressWant(2, 3, 1); !slices.Equal(rest, want) {
		t.Errorf("rest of the walk = %v, want %v", rest, want)
	}
}

func TestProgressGet(t *testing.T) {
	e := progressNewListEnv(t)

	got, err := e.store.Get(t.Context(), e.user, progressFixedID(1))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != progressFixedID(1) || got.WorkoutPlanID == nil || *got.WorkoutPlanID != e.plan ||
		len(got.Exercises) != 2 || len(got.Exercises[0].Sets) != 2 || len(got.Exercises[1].Sets) != 1 {
		t.Errorf("session = %s", progressJSON(t, got))
	}
	if got.Exercises[0].ExerciseID != e.ex[0] || got.Exercises[1].ExerciseID != e.ex[1] {
		t.Error("exercises are not ordered by position")
	}

	for name, tt := range map[string]struct {
		user uuid.UUID
		id   uuid.UUID
	}{
		"unknown id":      {e.user, progressNewID(t)},
		"deleted":         {e.user, progressFixedID(4)},
		"another user's":  {e.other, progressFixedID(1)},
		"foreign deleted": {e.other, progressFixedID(4)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.store.Get(t.Context(), tt.user, tt.id); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("Get = %v, want not found", err)
			}
		})
	}
}

func TestProgressSoftDelete(t *testing.T) {
	e := progressNewEnv(t)
	mine := progressNewID(t)
	kept := progressNewID(t)
	stored, _ := e.save(t, mine, e.input(progressAt(1)))
	e.save(t, kept, e.input(progressAt(1)))

	if err := e.store.SoftDelete(t.Context(), e.user, mine); err != nil {
		t.Fatal(err)
	}
	var deleted, server time.Time
	err := e.db.QueryRow(t.Context(), `SELECT deleted_at, server_updated_at FROM progress WHERE id = $1`, mine).Scan(&deleted, &server)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Equal(server) || !server.After(stored.ServerUpdatedAt) {
		t.Errorf("deleted_at %v, server_updated_at %v: want the same instant, after %v", deleted, server, stored.ServerUpdatedAt)
	}
	if n := e.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, mine); n != 2 {
		t.Errorf("%d exercise rows after delete, want the 2 children kept", n)
	}
	if n := e.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1`, mine); n != 3 {
		t.Errorf("%d set rows after delete, want 3 kept", n)
	}
	if _, err := e.store.Get(t.Context(), e.user, mine); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get after delete = %v, want not found", err)
	}
	list, _, err := e.store.ListSummaries(t.Context(), e.user, domain.ProgressListFilter{}, nil, 50)
	if err != nil || !slices.Equal(progressIDs(list), []uuid.UUID{kept}) {
		t.Errorf("list = %v (%v), want only the live session", progressIDs(list), err)
	}

	t.Run("deleting again succeeds and changes nothing", func(t *testing.T) {
		if err := e.store.SoftDelete(t.Context(), e.user, mine); err != nil {
			t.Fatalf("second delete: %v", err)
		}
		var d2, s2 time.Time
		if err := e.db.QueryRow(t.Context(), `SELECT deleted_at, server_updated_at FROM progress WHERE id = $1`, mine).Scan(&d2, &s2); err != nil {
			t.Fatal(err)
		}
		if !d2.Equal(deleted) || !s2.Equal(server) {
			t.Errorf("a repeated delete moved deleted_at/server_updated_at: %v/%v -> %v/%v", deleted, server, d2, s2)
		}
	})

	t.Run("unknown and foreign ids are not found, and a foreign session is not touched", func(t *testing.T) {
		if err := e.store.SoftDelete(t.Context(), e.user, progressNewID(t)); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("unknown id: %v, want not found", err)
		}
		if err := e.store.SoftDelete(t.Context(), e.other, kept); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("foreign id: %v, want not found", err)
		}
		if n := e.count(t, `SELECT count(*) FROM progress WHERE id = $1 AND deleted_at IS NULL`, kept); n != 1 {
			t.Error("another user's delete marked the session deleted")
		}
	})

	t.Run("a foreign deleted session is not found either", func(t *testing.T) {
		if err := e.store.SoftDelete(t.Context(), e.other, mine); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want not found: deleted-ness must not leak", err)
		}
	})

	t.Run("save after delete is a deleted conflict", func(t *testing.T) {
		_, _, err := e.store.Save(t.Context(), e.user, mine, e.input(progressAt(500)))
		if ce := progressConflict(t, err); ce.Issue != domain.IssueDeleted {
			t.Errorf("issue = %q", ce.Issue)
		}
	})

	t.Run("sync feed carries the deletion", func(t *testing.T) {
		since := stored.ServerUpdatedAt
		items, _, err := e.store.ListSummaries(t.Context(), e.user, domain.ProgressListFilter{UpdatedSince: &since}, nil, 50)
		// `kept` was saved after `mine` and before its deletion, so the deletion is last.
		if err != nil || len(items) != 2 || items[0].ID != kept || items[0].DeletedAt != nil {
			t.Fatalf("items = %v (%v), want kept then the deleted session", progressIDs(items), err)
		}
		if got := items[1]; got.ID != mine || got.DeletedAt == nil || !got.DeletedAt.Equal(got.ServerUpdatedAt) {
			t.Errorf("item = %+v, want the deleted session with deleted_at = server_updated_at", got)
		}
	})
}
