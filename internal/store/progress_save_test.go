package store_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

func TestProgressSaveInsert(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	before := time.Now().Add(-time.Second)

	out, created := e.save(t, id, e.input(progressAt(5)))
	if !created {
		t.Fatal("created = false for a new id")
	}

	if out.ID != id || out.Name != "Push Day A" || *out.Notes != "Felt strong" || out.WorkoutPlanID != nil {
		t.Errorf("session = %+v", out)
	}
	if !out.StartedAt.Equal(progressBase) || !out.EndedAt.Equal(progressBase.Add(55*time.Minute)) || out.DurationSeconds != 3120 {
		t.Errorf("times/duration = %v %v %d", out.StartedAt, out.EndedAt, out.DurationSeconds)
	}
	if !out.UpdatedAt.Equal(progressAt(5)) {
		t.Errorf("UpdatedAt = %v, want the client's %v", out.UpdatedAt, progressAt(5))
	}
	if !out.CreatedAt.Equal(out.ServerUpdatedAt) || out.CreatedAt.Before(before) || out.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("CreatedAt = %v, ServerUpdatedAt = %v, want both now", out.CreatedAt, out.ServerUpdatedAt)
	}
	if out.DeletedAt != nil {
		t.Errorf("DeletedAt = %v, want nil", out.DeletedAt)
	}
	for _, ts := range []time.Time{out.StartedAt, out.EndedAt, out.UpdatedAt, out.CreatedAt, out.ServerUpdatedAt} {
		if ts.Location() != time.UTC {
			t.Errorf("time %v is not in UTC", ts)
		}
	}

	// Children: the exact JSON the API sends, decimals as text.
	want := fmt.Sprintf(`[{"exercise_id":%q,"position":0,"notes":null,"sets":[`+
		`{"position":0,"type":"warmup","reps":12,"weight":40.0,"duration_seconds":null,"distance_meters":null,"rpe":null,"completed":true},`+
		`{"position":1,"type":"normal","reps":8,"weight":80.5,"duration_seconds":null,"distance_meters":null,"rpe":8.5,"completed":true}]},`+
		`{"exercise_id":%q,"position":1,"notes":"plank","sets":[`+
		`{"position":0,"type":"failure","reps":null,"weight":null,"duration_seconds":90,"distance_meters":null,"rpe":null,"completed":false}]}]`,
		e.ex[0], e.ex[1])
	if got := progressJSON(t, out.Exercises); got != want {
		t.Errorf("exercises =\n%s\nwant\n%s", got, want)
	}

	// What is stored: server-generated v7 ids, numerics at their column scale.
	if n := e.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1 AND uuid_extract_version(id) = 7`, id); n != 2 {
		t.Errorf("%d exercise rows with a v7 id, want 2", n)
	}
	if n := e.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id
		WHERE x.progress_id = $1 AND uuid_extract_version(s.id) = 7`, id); n != 3 {
		t.Errorf("%d set rows with a v7 id, want 3", n)
	}
	var weight, rpe string
	err := e.db.QueryRow(t.Context(), `SELECT s.weight::text, s.rpe::text FROM progress_sets s
		JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1 AND x.position = 0 AND s.position = 1`, id).Scan(&weight, &rpe)
	if err != nil || weight != "80.50" || rpe != "8.5" {
		t.Errorf("stored weight, rpe = %q, %q (%v), want 80.50, 8.5", weight, rpe, err)
	}

	got, err := e.store.Get(t.Context(), e.user, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a, b := progressJSON(t, got), progressJSON(t, out); a != b {
		t.Errorf("Get =\n%s\nSave returned\n%s", a, b)
	}
}

func TestProgressSaveEmptySession(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	in := e.input(progressAt(0))
	in.Exercises = []domain.ProgressExercise{
		{ExerciseID: e.ex[0], Position: 0, Sets: []domain.ProgressSet{}},
	}
	out, _ := e.save(t, id, in)
	if len(out.Exercises) != 1 || out.Exercises[0].Sets == nil || len(out.Exercises[0].Sets) != 0 {
		t.Errorf("exercises = %+v, want one exercise with an empty, non-nil sets slice", out.Exercises)
	}

	in.Exercises = []domain.ProgressExercise{}
	in.UpdatedAt = progressAt(1)
	out, created, err := e.store.Save(t.Context(), e.user, id, in)
	if err != nil || created {
		t.Fatalf("Save(no exercises) = created %v, err %v", created, err)
	}
	if out.Exercises == nil || len(out.Exercises) != 0 {
		t.Errorf("Exercises = %v, want an empty, non-nil slice", out.Exercises)
	}
	if s := progressJSON(t, out); !strings.Contains(s, `"exercises":[]`) {
		t.Errorf("json = %s, want exercises:[]", s)
	}
}

// TestProgressSaveConflictRule walks the table of spec 03 on one session.
func TestProgressSaveConflictRule(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)

	first, created := e.save(t, id, e.input(progressAt(100)))
	if !created {
		t.Fatal("missing row: created = false, want an insert")
	}
	rowIDs := func() []uuid.UUID {
		rows, err := e.db.Query(t.Context(), `SELECT id FROM progress_exercises WHERE progress_id = $1 ORDER BY position`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}
	idsBefore := rowIDs()

	t.Run("equal updated_at is a no-op that returns the stored copy", func(t *testing.T) {
		in := e.input(progressAt(100))
		in.Name = "Changed name, same updated_at"
		in.Exercises = in.Exercises[:1]
		out, created, err := e.store.Save(t.Context(), e.user, id, in)
		if err != nil || created {
			t.Fatalf("Save = created %v, err %v, want a 200 no-op", created, err)
		}
		if a, b := progressJSON(t, out), progressJSON(t, first); a != b {
			t.Errorf("no-op returned\n%s\nwant the stored copy\n%s", a, b)
		}
		if !slices.Equal(rowIDs(), idsBefore) {
			t.Error("a no-op replaced the child rows")
		}
	})

	t.Run("older updated_at is stale and carries the stored session", func(t *testing.T) {
		in := e.input(progressAt(99))
		in.Name = "Older"
		_, _, err := e.store.Save(t.Context(), e.user, id, in)
		ce := progressConflict(t, err)
		if ce.Issue != domain.IssueStale {
			t.Fatalf("issue = %q, want stale", ce.Issue)
		}
		got, err := e.store.Get(t.Context(), e.user, id)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := progressJSON(t, ce.Current), progressJSON(t, got); a != b {
			t.Errorf("Current =\n%s\nwant the GET shape\n%s", a, b)
		}
		if got.Name != "Push Day A" || !slices.Equal(rowIDs(), idsBefore) {
			t.Error("a stale write changed the row")
		}
	})

	t.Run("newer updated_at replaces the session and its children", func(t *testing.T) {
		in := e.input(progressAt(101))
		in.Name = "Newer"
		in.Notes = nil
		in.Exercises = []domain.ProgressExercise{{
			ExerciseID: e.ex[2], Position: 4, Sets: []domain.ProgressSet{progressSet(7, domain.SetTypeDrop, 5, "20", "")},
		}}
		out, created, err := e.store.Save(t.Context(), e.user, id, in)
		if err != nil || created {
			t.Fatalf("Save = created %v, err %v, want a 200 update", created, err)
		}
		if out.Name != "Newer" || out.Notes != nil || !out.UpdatedAt.Equal(progressAt(101)) {
			t.Errorf("session = %+v", out)
		}
		if !out.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("CreatedAt changed: %v -> %v", first.CreatedAt, out.CreatedAt)
		}
		if !out.ServerUpdatedAt.After(first.ServerUpdatedAt) {
			t.Errorf("ServerUpdatedAt = %v, want after %v", out.ServerUpdatedAt, first.ServerUpdatedAt)
		}
		if len(out.Exercises) != 1 || out.Exercises[0].ExerciseID != e.ex[2] || out.Exercises[0].Position != 4 ||
			len(out.Exercises[0].Sets) != 1 || out.Exercises[0].Sets[0].Position != 7 {
			t.Errorf("exercises = %s", progressJSON(t, out.Exercises))
		}
		if n := e.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); n != 1 {
			t.Errorf("%d exercise rows, want the old ones gone and 1 new", n)
		}
		if n := e.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1`, id); n != 1 {
			t.Errorf("%d set rows, want 1", n)
		}
	})

	t.Run("deleted wins even against a newer updated_at", func(t *testing.T) {
		if err := e.store.SoftDelete(t.Context(), e.user, id); err != nil {
			t.Fatal(err)
		}
		for _, at := range []int{50, 101, 1000} {
			_, _, err := e.store.Save(t.Context(), e.user, id, e.input(progressAt(at)))
			ce := progressConflict(t, err)
			if ce.Issue != domain.IssueDeleted || ce.Current != nil {
				t.Errorf("updated_at +%ds: conflict = %+v, want deleted without current", at, ce)
			}
		}
	})
}

func TestProgressSaveSubMicrosecondRetryIsNoop(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	at := progressAt(3).Add(123456789 * time.Nanosecond)

	first, _ := e.save(t, id, e.input(at))
	if want := progressAt(3).Add(123456 * time.Microsecond); !first.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v (microsecond precision)", first.UpdatedAt, want)
	}
	again, created, err := e.store.Save(t.Context(), e.user, id, e.input(at))
	if err != nil || created || !again.ServerUpdatedAt.Equal(first.ServerUpdatedAt) {
		t.Errorf("retry = created %v, err %v, server_updated_at %v -> %v, want an untouched no-op",
			created, err, first.ServerUpdatedAt, again.ServerUpdatedAt)
	}
}

func TestProgressSaveOwnership(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	mine, _ := e.save(t, id, e.input(progressAt(10)))

	for _, name := range []string{"newer", "older", "equal"} {
		t.Run(name, func(t *testing.T) {
			at := map[string]int{"newer": 20, "older": 1, "equal": 10}[name]
			in := e.input(progressAt(at))
			in.Name = "hijacked"
			_, _, err := e.store.Save(t.Context(), e.other, id, in)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("Save by another user = %v, want not found", err)
			}
			got, err := e.store.Get(t.Context(), e.user, id)
			if err != nil || progressJSON(t, got) != progressJSON(t, mine) {
				t.Errorf("the owner's session changed: %v", err)
			}
		})
	}

	t.Run("a deleted session of another user is still not found, not a conflict", func(t *testing.T) {
		if err := e.store.SoftDelete(t.Context(), e.user, id); err != nil {
			t.Fatal(err)
		}
		_, _, err := e.store.Save(t.Context(), e.other, id, e.input(progressAt(30)))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
	})
}

func TestProgressSaveReferences(t *testing.T) {
	e := progressNewEnv(t)
	unknown := progressNewID(t)
	deletedEx := testutil.SeedExercise(t, e.db, e.user, testutil.WithExerciseDeletedAt(progressBase))
	plan := testutil.SeedPlan(t, e.db, e.user)
	deletedPlan := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanDeletedAt(progressBase))
	otherPlan := testutil.SeedPlan(t, e.db, e.other)

	t.Run("soft-deleted exercise and soft-deleted plan are accepted", func(t *testing.T) {
		in := e.input(progressAt(1))
		in.WorkoutPlanID = &deletedPlan
		in.Exercises[1].ExerciseID = deletedEx
		out, _ := e.save(t, progressNewID(t), in)
		if out.WorkoutPlanID == nil || *out.WorkoutPlanID != deletedPlan || out.Exercises[1].ExerciseID != deletedEx {
			t.Errorf("session = %s", progressJSON(t, out))
		}
	})

	t.Run("own plan is accepted", func(t *testing.T) {
		in := e.input(progressAt(1))
		in.WorkoutPlanID = &plan
		if out, _ := e.save(t, progressNewID(t), in); *out.WorkoutPlanID != plan {
			t.Errorf("WorkoutPlanID = %v, want %v", out.WorkoutPlanID, plan)
		}
	})

	t.Run("unknown and foreign references are 422 with their paths, all at once, and write nothing", func(t *testing.T) {
		id := progressNewID(t)
		in := e.input(progressAt(1))
		in.WorkoutPlanID = &otherPlan
		in.Exercises[0].ExerciseID = unknown
		in.Exercises[1].ExerciseID = unknown
		_, _, err := e.store.Save(t.Context(), e.user, id, in)
		want := []string{
			"workout_plan_id: unknown_reference",
			"exercises[0].exercise_id: unknown_reference",
			"exercises[1].exercise_id: unknown_reference",
		}
		if got := progressIssues(t, err); !slices.Equal(got, want) {
			t.Errorf("issues = %q, want %q", got, want)
		}
		if n := e.count(t, `SELECT count(*) FROM progress WHERE id = $1`, id); n != 0 {
			t.Errorf("%d rows written by a failed save", n)
		}
	})

	t.Run("an unknown plan id", func(t *testing.T) {
		in := e.input(progressAt(1))
		in.WorkoutPlanID = &unknown
		_, _, err := e.store.Save(t.Context(), e.user, progressNewID(t), in)
		if got := progressIssues(t, err); !slices.Equal(got, []string{"workout_plan_id: unknown_reference"}) {
			t.Errorf("issues = %q", got)
		}
	})

	t.Run("a failed update leaves the stored session untouched", func(t *testing.T) {
		id := progressNewID(t)
		stored, _ := e.save(t, id, e.input(progressAt(1)))
		in := e.input(progressAt(2))
		in.Exercises[0].ExerciseID = unknown
		if _, _, err := e.store.Save(t.Context(), e.user, id, in); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("err = %v, want a validation error", err)
		}
		got, err := e.store.Get(t.Context(), e.user, id)
		if err != nil || progressJSON(t, got) != progressJSON(t, stored) {
			t.Errorf("stored session changed after a failed update (%v)", err)
		}
	})

	t.Run("stale and deleted are decided before the references are checked", func(t *testing.T) {
		id := progressNewID(t)
		e.save(t, id, e.input(progressAt(10)))
		in := e.input(progressAt(5))
		in.Exercises[0].ExerciseID = unknown
		_, _, err := e.store.Save(t.Context(), e.user, id, in)
		if ce := progressConflict(t, err); ce.Issue != domain.IssueStale {
			t.Errorf("issue = %q, want stale", ce.Issue)
		}
	})
}

// TestProgressSaveDecimalsRoundTrip: the boundary values of each numeric
// column are stored and read back digit for digit.
func TestProgressSaveDecimalsRoundTrip(t *testing.T) {
	e := progressNewEnv(t)
	in := e.input(progressAt(1))
	set := func(pos int, w, d, r string) domain.ProgressSet {
		s := domain.ProgressSet{Position: pos, Type: domain.SetTypeNormal, Completed: true}
		for _, f := range []struct {
			dst **domain.ProgressDecimal
			v   string
		}{{&s.Weight, w}, {&s.DistanceMeters, d}, {&s.RPE, r}} {
			if f.v != "" {
				*f.dst = testutil.Ptr(domain.ProgressDecimal(f.v))
			}
		}
		return s
	}
	in.Exercises = []domain.ProgressExercise{{ExerciseID: e.ex[0], Position: 0, Sets: []domain.ProgressSet{
		set(0, "0", "0", "1"),
		set(1, "0.01", "0.01", "1.5"),
		set(2, "99999.99", "9999999.99", "10"),
		set(3, "0.1", "12345.6", "9.5"),
		set(4, "100", "5000", "5"),
	}}}
	id := progressNewID(t)
	out, _ := e.save(t, id, in)

	want := []string{
		`"weight":0.0,"duration_seconds":null,"distance_meters":0.0,"rpe":1.0`,
		`"weight":0.01,"duration_seconds":null,"distance_meters":0.01,"rpe":1.5`,
		`"weight":99999.99,"duration_seconds":null,"distance_meters":9999999.99,"rpe":10.0`,
		`"weight":0.1,"duration_seconds":null,"distance_meters":12345.6,"rpe":9.5`,
		`"weight":100.0,"duration_seconds":null,"distance_meters":5000.0,"rpe":5.0`,
	}
	for i, s := range out.Exercises[0].Sets {
		if got := progressJSON(t, s); !strings.Contains(got, want[i]) {
			t.Errorf("set %d = %s, want it to contain %s", i, got, want[i])
		}
	}
}

func TestProgressSaveLargePayloadReadsBackIdentically(t *testing.T) {
	e := progressNewEnv(t)
	in := e.input(progressAt(1))
	in.Exercises = make([]domain.ProgressExercise, domain.MaxExercisesPerProgress)
	types := domain.SetTypes
	for i := range in.Exercises {
		sets := make([]domain.ProgressSet, domain.MaxSetsPerExercise)
		for j := range sets {
			sets[j] = progressSet(j, types[j%len(types)], j, fmt.Sprintf("%d.%02d", i*2+j, (i*7+j)%100), "")
			sets[j].Completed = j%3 != 0
			if j%4 == 0 {
				sets[j].RPE = testutil.Ptr(domain.ProgressDecimal("7.5"))
			}
			if j%5 == 0 {
				sets[j].DistanceMeters = testutil.Ptr(domain.ProgressDecimal(fmt.Sprintf("%d.5", j*100)))
				sets[j].DurationSeconds = testutil.Ptr(j * 3)
				sets[j].Reps = nil
			}
		}
		// Positions are not contiguous and not in insertion order.
		in.Exercises[i] = domain.ProgressExercise{ExerciseID: e.ex[i%3], Position: (i * 7) % 50 * 2, Sets: sets}
		if i%2 == 0 {
			in.Exercises[i].Notes = testutil.Ptr(fmt.Sprintf("exercise %d", i))
		}
	}
	// Exercises come back ordered by position, like the API promises.
	want := slices.Clone(in.Exercises)
	slices.SortFunc(want, func(a, b domain.ProgressExercise) int { return a.Position - b.Position })

	id := progressNewID(t)
	start := time.Now()
	out, _ := e.save(t, id, in)
	t.Logf("saved %d exercises x %d sets in %v", len(want), domain.MaxSetsPerExercise, time.Since(start))

	if n := e.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1`, id); n != 5000 {
		t.Fatalf("%d set rows, want 5000", n)
	}
	got, err := e.store.Get(t.Context(), e.user, id)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := progressJSON(t, got.Exercises), progressJSON(t, out.Exercises); a != b {
		t.Error("Get differs from what Save returned")
	}
	// Compare with the input, ignoring only the decimal spelling (40 -> 40.0).
	if a, b := progressJSON(t, got.Exercises), progressJSON(t, want); a != b {
		t.Errorf("stored exercises differ from the input\n got: %.300s\nwant: %.300s", a, b)
	}
}

func TestProgressSaveConcurrentSameIDInsert(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	const n = 12

	type result struct {
		created bool
		err     error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, created, err := e.store.Save(t.Context(), e.user, id, e.input(progressAt(1)))
			results[i] = result{created, err}
		}()
	}
	close(start)
	wg.Wait()

	creates := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("request %d: %v", i, r.err)
		}
		if r.created {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("%d requests created the session, want exactly 1 (the rest are no-ops)", creates)
	}
	if got := e.count(t, `SELECT count(*) FROM progress WHERE id = $1`, id); got != 1 {
		t.Errorf("%d progress rows", got)
	}
	if got := e.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); got != 2 {
		t.Errorf("%d exercise rows, want 2", got)
	}
	if got := e.count(t, `SELECT count(*) FROM progress_sets s JOIN progress_exercises x ON x.id = s.progress_exercise_id WHERE x.progress_id = $1`, id); got != 3 {
		t.Errorf("%d set rows, want 3", got)
	}
}

// TestProgressSaveConcurrentWritersEndConsistent: writers with different
// updated_at race on one id, first as inserts and then as updates. Every result
// is a success or a stale conflict, and the newest updated_at wins.
func TestProgressSaveConcurrentWritersEndConsistent(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			e := progressNewEnv(t)
			id := progressNewID(t)
			if existing {
				e.save(t, id, e.input(progressAt(0)))
			}
			const n = 12
			errs := make([]error, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					in := e.input(progressAt(i + 1))
					in.Name = fmt.Sprintf("writer %d", i)
					// Each writer has a different number of exercises.
					in.Exercises = in.Exercises[:1]
					for k := 1; k <= i%3; k++ {
						in.Exercises = append(in.Exercises, domain.ProgressExercise{
							ExerciseID: e.ex[k], Position: k, Sets: []domain.ProgressSet{progressSet(0, domain.SetTypeNormal, k, "10", "")}})
					}
					<-start
					_, _, errs[i] = e.store.Save(t.Context(), e.user, id, in)
				}()
			}
			close(start)
			wg.Wait()

			for i, err := range errs {
				if err == nil {
					continue
				}
				if ce := (*domain.ConflictError)(nil); !errors.As(err, &ce) || ce.Issue != domain.IssueStale {
					t.Errorf("writer %d: %v, want success or stale", i, err)
				}
			}
			if errs[n-1] != nil {
				t.Errorf("the newest writer must always win, got %v", errs[n-1])
			}
			got, err := e.store.Get(t.Context(), e.user, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != fmt.Sprintf("writer %d", n-1) || !got.UpdatedAt.Equal(progressAt(n)) {
				t.Errorf("stored %q at %v, want the newest writer", got.Name, got.UpdatedAt)
			}
			if len(got.Exercises) != 1+(n-1)%3 {
				t.Errorf("%d exercises, want %d: children of a lost writer leaked or vanished", len(got.Exercises), 1+(n-1)%3)
			}
			if c := e.count(t, `SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, id); c != len(got.Exercises) {
				t.Errorf("%d exercise rows, want %d", c, len(got.Exercises))
			}
		})
	}
}

// TestProgressSaveColumnBoundaries: the extremes the validation lets through
// fit their columns, so none of them can surface as a database error.
func TestProgressSaveColumnBoundaries(t *testing.T) {
	e := progressNewEnv(t)
	for name, at := range map[string]time.Time{
		"year 0":    time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		"year 1":    time.Date(1, 1, 1, 0, 0, 1, 0, time.UTC),
		"year 1900": time.Date(1900, 1, 1, 0, 0, 0, 1000, time.UTC),
		"year 9999": time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			in := e.input(at)
			in.StartedAt, in.EndedAt = at, at
			out, _ := e.save(t, progressNewID(t), in)
			if !out.StartedAt.Equal(at) || !out.UpdatedAt.Equal(at) {
				t.Errorf("stored %v / %v, want %v", out.StartedAt, out.UpdatedAt, at)
			}
		})
	}

	t.Run("largest integers and text", func(t *testing.T) {
		in := e.input(progressAt(0))
		in.Name = strings.Repeat("\U0001F600", domain.ProgressNameMaxLen) // 100 characters, 400 bytes
		in.Notes = testutil.Ptr(strings.Repeat("é", domain.ProgressNotesMaxLen))
		in.Exercises[0].Position = domain.MaxIntColumn
		in.Exercises[0].Notes = testutil.Ptr(strings.Repeat("n", domain.ProgressExerciseNotesMaxLen))
		in.Exercises[0].Sets[0].Position = domain.MaxIntColumn
		in.Exercises[0].Sets[0].Reps = testutil.Ptr(domain.MaxIntColumn)
		in.Exercises[0].Sets[0].DurationSeconds = testutil.Ptr(domain.MaxIntColumn)
		out, _ := e.save(t, progressNewID(t), in)
		// Ordered by position, so the exercise at the largest position comes last.
		if out.Exercises[0].Position != 1 || out.Exercises[1].Position != domain.MaxIntColumn {
			t.Errorf("positions = %d, %d", out.Exercises[0].Position, out.Exercises[1].Position)
		}
		if utf8Len := len([]rune(out.Name)); utf8Len != domain.ProgressNameMaxLen {
			t.Errorf("name has %d characters back", utf8Len)
		}
	})
}

// TestProgressSaveConcurrentFirstInsertByTwoUsers: two users race to create the
// same new id. One wins; the other only ever gets not found, never a 500.
func TestProgressSaveConcurrentFirstInsertByTwoUsers(t *testing.T) {
	e := progressNewEnv(t)
	id := progressNewID(t)
	const n = 12
	userOf := func(i int) uuid.UUID { return []uuid.UUID{e.user, e.other}[i%2] }

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, errs[i] = e.store.Save(t.Context(), userOf(i), id, e.input(progressAt(1)))
		}()
	}
	close(start)
	wg.Wait()

	var owner uuid.UUID
	if err := e.db.QueryRow(t.Context(), `SELECT user_id FROM progress WHERE id = $1`, id).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	for i, err := range errs {
		switch {
		case userOf(i) == owner && err != nil:
			t.Errorf("request %d by the owner: %v", i, err)
		case userOf(i) != owner && !errors.Is(err, domain.ErrNotFound):
			t.Errorf("request %d by the other user: %v, want not found", i, err)
		}
	}
}
