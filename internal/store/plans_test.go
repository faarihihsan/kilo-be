package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// planEnv is a migrated database with two users and a few exercises.
type planEnv struct {
	db        *store.DB
	plans     *store.Plans
	user      uuid.UUID
	other     uuid.UUID
	exercises []uuid.UUID
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	db := testutil.NewDB(t)
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)
	other, _ := testutil.SeedUser(t, db, domain.RoleUser)
	e := &planEnv{db: db, plans: store.NewPlans(db), user: user, other: other}
	for range 4 {
		e.exercises = append(e.exercises, testutil.SeedExercise(t, db, user))
	}
	return e
}

// planT0 is the base of the client timestamps in these tests.
var planT0 = time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC)

func planAt(sec int) time.Time { return planT0.Add(time.Duration(sec) * time.Second) }

// planFuture is a client timestamp newer than anything seeded with the
// default (now) client time.
func planFuture() time.Time { return time.Now().UTC().Add(time.Hour) }

// ex is an exercise row on exercise number i of the env at a position.
func (e *planEnv) ex(i, position int) domain.PlanExercise {
	return domain.PlanExercise{ExerciseID: e.exercises[i], Position: position, TargetSets: 3}
}

func planSpec(name string, at time.Time, exercises ...domain.PlanExercise) domain.PlanSpec {
	if exercises == nil {
		exercises = []domain.PlanExercise{}
	}
	return domain.PlanSpec{Name: name, UpdatedAt: at, Exercises: exercises}
}

func planDecimal(t testing.TB, s string) *domain.PlanDecimal {
	t.Helper()
	d, err := domain.ParsePlanDecimal(s)
	if err != nil {
		t.Fatalf("ParsePlanDecimal(%q): %v", s, err)
	}
	return &d
}

func planPtr[T any](v T) *T { return &v }

// save saves spec as the user and fails the test on an error.
func (e *planEnv) save(t *testing.T, id uuid.UUID, spec domain.PlanSpec) (domain.Plan, bool) {
	t.Helper()
	plan, created, err := e.plans.Save(t.Context(), e.user, id, spec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return plan, created
}

func (e *planEnv) get(t *testing.T, id uuid.UUID) domain.Plan {
	t.Helper()
	plan, err := e.plans.Get(t.Context(), e.user, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return plan
}

func planNewID(t testing.TB) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// count runs a SELECT count(*) with args.
func (e *planEnv) count(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\nSQL: %s", err, sql)
	}
	return n
}

func planRequireNotFound(t *testing.T, err error) {
	t.Helper()
	var nf *domain.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v (%T), want *domain.NotFoundError", err, err)
	}
}

func planRequireConflict(t *testing.T, err error, issue string) *domain.ConflictError {
	t.Helper()
	var c *domain.ConflictError
	if !errors.As(err, &c) {
		t.Fatalf("error = %v (%T), want *domain.ConflictError", err, err)
	}
	if c.Issue != issue {
		t.Fatalf("conflict issue = %q, want %q", c.Issue, issue)
	}
	return c
}

func planIssues(t *testing.T, err error) []domain.FieldIssue {
	t.Helper()
	var v *domain.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v (%T), want *domain.ValidationError", err, err)
	}
	return v.Issues
}

func planJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlansSaveInsert(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)
	spec := domain.PlanSpec{
		Name:        "Push",
		Description: planPtr("Chest / shoulders / triceps"),
		UpdatedAt:   planAt(0).Add(123456 * time.Microsecond),
		Exercises: []domain.PlanExercise{{
			ExerciseID:            e.exercises[0],
			Position:              0,
			TargetSets:            4,
			TargetReps:            planPtr(8),
			TargetRepsMax:         planPtr(10),
			TargetWeight:          planDecimal(t, "80.00"),
			TargetDurationSeconds: planPtr(45),
			TargetDistanceMeters:  planDecimal(t, "1234567.89"),
			RestSeconds:           planPtr(120),
			Notes:                 planPtr("Pause on chest"),
		}},
	}
	plan, created := e.save(t, id, spec)
	if !created {
		t.Error("created = false, want true")
	}
	if plan.ID != id || plan.Name != "Push" || plan.Description == nil || *plan.Description != "Chest / shoulders / triceps" {
		t.Errorf("plan = %+v", plan)
	}
	if !plan.UpdatedAt.Equal(spec.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v (the client's value)", plan.UpdatedAt, spec.UpdatedAt)
	}
	for name, ts := range map[string]time.Time{"UpdatedAt": plan.UpdatedAt, "CreatedAt": plan.CreatedAt, "ServerUpdatedAt": plan.ServerUpdatedAt} {
		if ts.Location() != time.UTC {
			t.Errorf("%s is in %v, want UTC", name, ts.Location())
		}
	}
	// created_at and server_updated_at are the server's, not the client's.
	if !plan.CreatedAt.After(spec.UpdatedAt) || plan.ServerUpdatedAt.Before(plan.CreatedAt) {
		t.Errorf("CreatedAt %v / ServerUpdatedAt %v are not server times", plan.CreatedAt, plan.ServerUpdatedAt)
	}
	if plan.DeletedAt != nil {
		t.Errorf("DeletedAt = %v", plan.DeletedAt)
	}
	if len(plan.Exercises) != 1 {
		t.Fatalf("exercises = %d, want 1", len(plan.Exercises))
	}
	want := spec.Exercises[0]
	got := plan.Exercises[0]
	if got.ExerciseID != want.ExerciseID || got.Position != 0 || got.TargetSets != 4 || *got.TargetReps != 8 ||
		*got.TargetRepsMax != 10 || *got.TargetDurationSeconds != 45 || *got.RestSeconds != 120 || *got.Notes != "Pause on chest" {
		t.Errorf("exercise = %+v", got)
	}
	if got.TargetWeight.String() != "80.00" || got.TargetDistanceMeters.String() != "1234567.89" {
		t.Errorf("decimals = %v, %v", got.TargetWeight, got.TargetDistanceMeters)
	}
	if again := e.get(t, id); planJSON(t, again) != planJSON(t, plan) {
		t.Errorf("Get differs from what Save returned:\n%s\n%s", planJSON(t, again), planJSON(t, plan))
	}
}

func TestPlansSaveInsertMinimal(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)

	plan, created := e.save(t, id, planSpec("Draft", planAt(0)))
	if !created {
		t.Error("created = false")
	}
	if plan.Description != nil {
		t.Errorf("Description = %q, want nil", *plan.Description)
	}
	if plan.Exercises == nil || len(plan.Exercises) != 0 {
		t.Errorf("Exercises = %#v, want an empty, non-nil slice (JSON [])", plan.Exercises)
	}
	if got := planJSONField(t, planJSON(t, plan), "exercises"); got != "[]" {
		t.Errorf("exercises JSON = %s, want []", got)
	}

	// An empty description is kept as empty, not turned into NULL.
	spec := planSpec("Draft", planAt(1))
	spec.Description = planPtr("")
	plan, created = e.save(t, id, spec)
	if created {
		t.Error("created = true for an update")
	}
	if plan.Description == nil || *plan.Description != "" {
		t.Errorf("Description = %v, want empty string", plan.Description)
	}
}

func planJSONField(t testing.TB, doc, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return string(m[key])
}

// TestPlansSaveDecimalsAreExact writes weights and distances through the store
// and reads them back as text from the column itself, so a float anywhere in
// the path would show up as a changed digit.
func TestPlansSaveDecimalsAreExact(t *testing.T) {
	e := newPlanEnv(t)
	weights := []string{"0.00", "0.01", "0.07", "0.10", "0.30", "1.15", "33.33", "80.00", "80.25", "1234.56", "99999.99"}
	distances := []string{"0.00", "0.01", "0.29", "400.00", "5000.50", "42195.00", "9999999.99"}

	var rows []domain.PlanExercise
	for i, w := range weights {
		row := e.ex(0, i)
		row.TargetWeight = planDecimal(t, w)
		row.TargetDistanceMeters = planDecimal(t, distances[i%len(distances)])
		rows = append(rows, row)
	}
	id := planNewID(t)
	plan, _ := e.save(t, id, planSpec("Decimals", planAt(0), rows...))

	for i, w := range weights {
		var wText, dText string
		err := e.db.QueryRow(t.Context(), `
			SELECT target_weight::text, target_distance_meters::text FROM workout_plan_exercises
			WHERE workout_plan_id = $1 AND position = $2`, id, i).Scan(&wText, &dText)
		if err != nil {
			t.Fatal(err)
		}
		if wText != w || dText != distances[i%len(distances)] {
			t.Errorf("row %d stored %s / %s, want %s / %s", i, wText, dText, w, distances[i%len(distances)])
		}
		if got := plan.Exercises[i].TargetWeight.String(); got != w {
			t.Errorf("row %d weight read back as %s, want %s", i, got, w)
		}
	}
	// And as JSON numbers: trailing zeros trimmed, one decimal kept.
	body := planJSON(t, plan.Exercises[0])
	if want := `"target_weight":0.0,"target_duration_seconds":null,"target_distance_meters":0.0,`; !strings.Contains(body, want) {
		t.Errorf("exercise JSON = %s, want it to contain %s", body, want)
	}
	if body := planJSON(t, plan.Exercises[2]); !strings.Contains(body, `"target_weight":0.07,`) {
		t.Errorf("exercise JSON = %s, want weight 0.07", body)
	}
}

func TestPlansSaveReturnsExercisesInPositionOrder(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)
	// Given out of order and with gaps; the same exercise may repeat.
	plan, _ := e.save(t, id, planSpec("Order", planAt(0), e.ex(1, 30), e.ex(0, 0), e.ex(1, 7), e.ex(2, 12)))
	var positions []int
	var ids []uuid.UUID
	for _, ex := range plan.Exercises {
		positions = append(positions, ex.Position)
		ids = append(ids, ex.ExerciseID)
	}
	if want := []int{0, 7, 12, 30}; !slices.Equal(positions, want) {
		t.Errorf("positions = %v, want %v", positions, want)
	}
	if want := []uuid.UUID{e.exercises[0], e.exercises[1], e.exercises[2], e.exercises[1]}; !slices.Equal(ids, want) {
		t.Errorf("exercise ids = %v, want %v", ids, want)
	}
}

// TestPlansSaveConflictRule is the conflict table of spec 03 at the store.
func TestPlansSaveConflictRule(t *testing.T) {
	setup := func(t *testing.T) (*planEnv, uuid.UUID, domain.Plan) {
		e := newPlanEnv(t)
		id := planNewID(t)
		stored, created := e.save(t, id, planSpec("Stored", planAt(100), e.ex(0, 0), e.ex(1, 1)))
		if !created {
			t.Fatal("setup: not created")
		}
		return e, id, stored
	}

	t.Run("no row creates", func(t *testing.T) {
		e := newPlanEnv(t)
		if _, created := e.save(t, planNewID(t), planSpec("New", planAt(5))); !created {
			t.Error("created = false")
		}
	})

	t.Run("newer replaces the plan and every exercise", func(t *testing.T) {
		e, id, stored := setup(t)
		spec := planSpec("Renamed", planAt(101), e.ex(2, 0))
		spec.Description = planPtr("now with a description")
		plan, created := e.save(t, id, spec)
		if created {
			t.Error("created = true for an update")
		}
		if plan.Name != "Renamed" || plan.Description == nil || !plan.UpdatedAt.Equal(planAt(101)) {
			t.Errorf("plan = %+v", plan)
		}
		if len(plan.Exercises) != 1 || plan.Exercises[0].ExerciseID != e.exercises[2] {
			t.Errorf("exercises = %+v, want only the new one", plan.Exercises)
		}
		if !plan.ServerUpdatedAt.After(stored.ServerUpdatedAt) {
			t.Errorf("server_updated_at %v did not advance past %v", plan.ServerUpdatedAt, stored.ServerUpdatedAt)
		}
		if !plan.CreatedAt.Equal(stored.CreatedAt) {
			t.Errorf("created_at changed from %v to %v", stored.CreatedAt, plan.CreatedAt)
		}
		if n := e.count(t, `SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, id); n != 1 {
			t.Errorf("%d exercise rows in the table, want 1 (old ones deleted)", n)
		}
		if again := e.get(t, id); planJSON(t, again) != planJSON(t, plan) {
			t.Error("Get differs from what Save returned")
		}
	})

	t.Run("newer by one microsecond still replaces", func(t *testing.T) {
		e, id, _ := setup(t)
		plan, _ := e.save(t, id, planSpec("Micro", planAt(100).Add(time.Microsecond)))
		if plan.Name != "Micro" {
			t.Errorf("Name = %q, want the update to win", plan.Name)
		}
	})

	t.Run("equal is a no-op that returns the stored copy", func(t *testing.T) {
		e, id, stored := setup(t)
		plan, created := e.save(t, id, planSpec("Different name, same time", planAt(100), e.ex(3, 0)))
		if created {
			t.Error("created = true for a no-op")
		}
		if planJSON(t, plan) != planJSON(t, stored) {
			t.Errorf("no-op returned %s, want the stored %s", planJSON(t, plan), planJSON(t, stored))
		}
		if again := e.get(t, id); planJSON(t, again) != planJSON(t, stored) {
			t.Error("a no-op changed the stored plan")
		}
	})

	t.Run("equal within the same microsecond is a no-op", func(t *testing.T) {
		e, id, stored := setup(t)
		// The API accepts nanoseconds; the column keeps microseconds.
		plan, _ := e.save(t, id, planSpec("Other", planAt(100).Add(999*time.Nanosecond)))
		if planJSON(t, plan) != planJSON(t, stored) {
			t.Errorf("sub-microsecond difference was not a no-op: %s", planJSON(t, plan))
		}
	})

	t.Run("older is a stale conflict carrying the stored plan", func(t *testing.T) {
		e, id, stored := setup(t)
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Old edit", planAt(99), e.ex(3, 0)))
		c := planRequireConflict(t, err, domain.IssueStale)
		current, ok := c.Current.(domain.Plan)
		if !ok {
			t.Fatalf("Current = %T, want domain.Plan (the GET shape)", c.Current)
		}
		if planJSON(t, current) != planJSON(t, stored) {
			t.Errorf("current = %s, want %s", planJSON(t, current), planJSON(t, stored))
		}
		if again := e.get(t, id); planJSON(t, again) != planJSON(t, stored) {
			t.Error("a stale write changed the stored plan")
		}
	})

	t.Run("older by one microsecond is stale", func(t *testing.T) {
		e, id, _ := setup(t)
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("x", planAt(100).Add(-time.Microsecond)))
		planRequireConflict(t, err, domain.IssueStale)
	})

	t.Run("soft-deleted is a deleted conflict, even with a newer updated_at", func(t *testing.T) {
		e, id, _ := setup(t)
		if err := e.plans.SoftDelete(t.Context(), e.user, id); err != nil {
			t.Fatal(err)
		}
		for _, at := range []time.Time{planAt(50), planAt(100), planAt(9999)} {
			_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Back again", at))
			c := planRequireConflict(t, err, domain.IssueDeleted)
			if c.Current != nil {
				t.Errorf("Current = %v, want none for a deleted conflict", c.Current)
			}
		}
	})

	t.Run("a rejected save changes nothing", func(t *testing.T) {
		e, id, stored := setup(t)
		bad := planSpec("Bad", planAt(200), domain.PlanExercise{ExerciseID: uuid.New(), Position: 0, TargetSets: 3})
		if _, _, err := e.plans.Save(t.Context(), e.user, id, bad); err == nil {
			t.Fatal("Save with an unknown exercise succeeded")
		}
		if again := e.get(t, id); planJSON(t, again) != planJSON(t, stored) {
			t.Errorf("a failed save changed the plan: %s", planJSON(t, again))
		}
	})
}

func TestPlansSaveOwnership(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)
	mine, _ := e.save(t, id, planSpec("Mine", planAt(10), e.ex(0, 0)))

	// The other user hits a plan they do not own: NotFound, whatever the
	// request would otherwise have done (newer, older, equal, invalid
	// exercise) and without touching the row.
	unknown := domain.PlanExercise{ExerciseID: uuid.New(), Position: 0, TargetSets: 3}
	for name, spec := range map[string]domain.PlanSpec{
		"newer":             planSpec("Stolen", planAt(20)),
		"older":             planSpec("Stolen", planAt(1)),
		"equal":             planSpec("Stolen", planAt(10)),
		"unknown exercise":  planSpec("Stolen", planAt(20), unknown),
		"with own exercise": planSpec("Stolen", planAt(20), e.ex(1, 0)),
	} {
		_, _, err := e.plans.Save(t.Context(), e.other, id, spec)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Save (%s) by another user: %v, want NotFound", name, err)
		}
	}
	if _, err := e.plans.Get(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get by another user: %v, want NotFound", err)
	}
	if err := e.plans.SoftDelete(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SoftDelete by another user: %v, want NotFound", err)
	}
	if again := e.get(t, id); planJSON(t, again) != planJSON(t, mine) {
		t.Errorf("the owner's plan changed: %s", planJSON(t, again))
	}

	// A soft-deleted plan of another user is NotFound too, not a conflict.
	if err := e.plans.SoftDelete(t.Context(), e.user, id); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.plans.Save(t.Context(), e.other, id, planSpec("x", planAt(30)))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Save on another user's deleted plan: %v, want NotFound", err)
	}
	if err := e.plans.SoftDelete(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SoftDelete on another user's deleted plan: %v, want NotFound", err)
	}
}

func TestPlansSaveUnknownExercise(t *testing.T) {
	e := newPlanEnv(t)
	ghost1, ghost2 := uuid.New(), uuid.New()
	row := func(id uuid.UUID, pos int) domain.PlanExercise {
		return domain.PlanExercise{ExerciseID: id, Position: pos, TargetSets: 3}
	}

	t.Run("insert names every unknown exercise by path and writes nothing", func(t *testing.T) {
		id := planNewID(t)
		spec := planSpec("Bad", planAt(0), e.ex(0, 0), row(ghost1, 1), e.ex(1, 2), row(ghost2, 3), row(ghost1, 4))
		_, _, err := e.plans.Save(t.Context(), e.user, id, spec)
		want := []domain.FieldIssue{
			{Field: "exercises[1].exercise_id", Issue: domain.IssueUnknownReference},
			{Field: "exercises[3].exercise_id", Issue: domain.IssueUnknownReference},
			{Field: "exercises[4].exercise_id", Issue: domain.IssueUnknownReference},
		}
		if got := planIssues(t, err); !slices.Equal(got, want) {
			t.Errorf("issues = %v, want %v", got, want)
		}
		if _, err := e.plans.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the rejected plan exists: %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM workout_plans WHERE id = $1`, id); n != 0 {
			t.Errorf("%d plan rows written", n)
		}
	})

	t.Run("update is rejected the same way and the old state stays", func(t *testing.T) {
		id := planNewID(t)
		stored, _ := e.save(t, id, planSpec("Good", planAt(0), e.ex(0, 0)))
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Bad", planAt(1), row(ghost1, 0)))
		if got := planIssues(t, err); !slices.Equal(got, []domain.FieldIssue{{Field: "exercises[0].exercise_id", Issue: domain.IssueUnknownReference}}) {
			t.Errorf("issues = %v", got)
		}
		if again := e.get(t, id); planJSON(t, again) != planJSON(t, stored) {
			t.Errorf("plan changed: %s", planJSON(t, again))
		}
	})

	t.Run("soft-deleted exercises are accepted", func(t *testing.T) {
		gone := testutil.SeedExercise(t, e.db, e.user, testutil.WithExerciseDeletedAt(planAt(0)))
		plan, _ := e.save(t, planNewID(t), planSpec("Old exercise", planAt(0), row(gone, 0)))
		if len(plan.Exercises) != 1 || plan.Exercises[0].ExerciseID != gone {
			t.Errorf("exercises = %+v", plan.Exercises)
		}
	})

	t.Run("an exercise of another user is fine, the master is shared", func(t *testing.T) {
		theirs := testutil.SeedExercise(t, e.db, e.other)
		e.save(t, planNewID(t), planSpec("Shared master", planAt(0), row(theirs, 0)))
	})

	t.Run("a stale or deleted write reports the conflict before unknown exercises", func(t *testing.T) {
		id := planNewID(t)
		e.save(t, id, planSpec("Stored", planAt(50)))
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Old", planAt(10), row(ghost1, 0)))
		planRequireConflict(t, err, domain.IssueStale)
		if err := e.plans.SoftDelete(t.Context(), e.user, id); err != nil {
			t.Fatal(err)
		}
		_, _, err = e.plans.Save(t.Context(), e.user, id, planSpec("New", planAt(99), row(ghost1, 0)))
		planRequireConflict(t, err, domain.IssueDeleted)
	})

	t.Run("a no-op retry does not look at the exercises", func(t *testing.T) {
		id := planNewID(t)
		stored, _ := e.save(t, id, planSpec("Stored", planAt(50), e.ex(0, 0)))
		plan, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Stored", planAt(50), row(ghost1, 0)))
		if err != nil || planJSON(t, plan) != planJSON(t, stored) {
			t.Errorf("no-op = %s, %v", planJSON(t, plan), err)
		}
	})
}

func TestPlansSaveExerciseRowIDsAreServerGeneratedV7(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)
	e.save(t, id, planSpec("Ids", planAt(0), e.ex(0, 0), e.ex(1, 1), e.ex(2, 2)))
	first := e.exerciseRowIDs(t, id)
	e.save(t, id, planSpec("Ids", planAt(1), e.ex(0, 0), e.ex(1, 1), e.ex(2, 2)))
	second := e.exerciseRowIDs(t, id)

	for _, ids := range [][]uuid.UUID{first, second} {
		if len(ids) != 3 {
			t.Fatalf("%d rows, want 3", len(ids))
		}
		for _, rowID := range ids {
			if rowID.Version() != 7 {
				t.Errorf("row id %v is version %d, want 7", rowID, rowID.Version())
			}
		}
	}
	for _, a := range first {
		if slices.Contains(second, a) {
			t.Errorf("row id %v survived the replace; rows are deleted and re-inserted", a)
		}
	}
}

func (e *planEnv) exerciseRowIDs(t *testing.T, planID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := e.db.Query(t.Context(), `SELECT id FROM workout_plan_exercises WHERE workout_plan_id = $1 ORDER BY position`, planID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestPlansSavePlanLimit(t *testing.T) {
	seedActive := func(e *planEnv, userID uuid.UUID, n int) []uuid.UUID {
		var ids []uuid.UUID
		for range n {
			ids = append(ids, testutil.SeedPlan(t, e.db, userID))
		}
		return ids
	}
	limitIssue := []domain.FieldIssue{{Issue: domain.IssuePlanLimit}}

	t.Run("the 101st plan is refused, updates are not", func(t *testing.T) {
		e := newPlanEnv(t)
		ids := seedActive(e, e.user, domain.MaxActivePlansPerUser-1)

		if _, created := e.save(t, planNewID(t), planSpec("Number 100", planAt(0))); !created {
			t.Fatal("the 100th plan was refused")
		}
		_, _, err := e.plans.Save(t.Context(), e.user, planNewID(t), planSpec("Number 101", planAt(0)))
		if got := planIssues(t, err); !slices.Equal(got, limitIssue) {
			t.Fatalf("issues = %v, want %v", got, limitIssue)
		}
		// An update of an existing plan at the cap works.
		future := planFuture()
		plan, created := e.save(t, ids[0], planSpec("Edited at the cap", future))
		if created || plan.Name != "Edited at the cap" {
			t.Errorf("update at the cap: created=%v plan=%+v", created, plan)
		}
		// So does a no-op retry of an existing one.
		e.save(t, ids[0], planSpec("Edited at the cap", future))
	})

	t.Run("soft-deleted plans do not count and freeing one makes room", func(t *testing.T) {
		e := newPlanEnv(t)
		for range 20 {
			testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanDeletedAt(planAt(0)))
		}
		ids := seedActive(e, e.user, domain.MaxActivePlansPerUser)
		_, _, err := e.plans.Save(t.Context(), e.user, planNewID(t), planSpec("Over", planAt(0)))
		if got := planIssues(t, err); !slices.Equal(got, limitIssue) {
			t.Fatalf("issues = %v, want %v", got, limitIssue)
		}
		if err := e.plans.SoftDelete(t.Context(), e.user, ids[7]); err != nil {
			t.Fatal(err)
		}
		if _, created := e.save(t, planNewID(t), planSpec("Fits now", planAt(0))); !created {
			t.Error("not created after freeing a slot")
		}
	})

	t.Run("the cap is per user", func(t *testing.T) {
		e := newPlanEnv(t)
		seedActive(e, e.other, domain.MaxActivePlansPerUser)
		if _, created := e.save(t, planNewID(t), planSpec("Mine", planAt(0))); !created {
			t.Error("another user's plans counted against this user")
		}
	})

	t.Run("a refused insert writes nothing, and reports the cap before unknown exercises", func(t *testing.T) {
		e := newPlanEnv(t)
		seedActive(e, e.user, domain.MaxActivePlansPerUser)
		id := planNewID(t)
		ghost := domain.PlanExercise{ExerciseID: uuid.New(), Position: 0, TargetSets: 3}
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Over", planAt(0), ghost))
		if got := planIssues(t, err); !slices.Equal(got, limitIssue) {
			t.Fatalf("issues = %v, want just plan_limit", got)
		}
		if n := e.count(t, `SELECT count(*) FROM workout_plans WHERE id = $1`, id); n != 0 {
			t.Errorf("%d rows written", n)
		}
	})
}

func TestPlansGet(t *testing.T) {
	e := newPlanEnv(t)
	id := testutil.SeedPlan(t, e.db, e.user,
		testutil.WithPlanName("Seeded"),
		testutil.WithPlanDescription("d"),
		testutil.WithPlanClientUpdatedAt(planAt(5)),
		testutil.WithPlanExercise(e.exercises[2], testutil.WithPlanExercisePosition(5), testutil.WithTargetWeight(62.5)),
		testutil.WithPlanExercise(e.exercises[0], testutil.WithPlanExercisePosition(1), testutil.WithTargetDistance(1500.25), testutil.WithRestSeconds(90)),
	)

	plan := e.get(t, id)
	if plan.ID != id || plan.Name != "Seeded" || *plan.Description != "d" || !plan.UpdatedAt.Equal(planAt(5)) {
		t.Errorf("plan = %+v", plan)
	}
	if len(plan.Exercises) != 2 || plan.Exercises[0].Position != 1 || plan.Exercises[1].Position != 5 {
		t.Fatalf("exercises = %+v, want positions 1 then 5", plan.Exercises)
	}
	if got := plan.Exercises[0]; got.TargetDistanceMeters.String() != "1500.25" || got.TargetWeight != nil || *got.RestSeconds != 90 {
		t.Errorf("first exercise = %+v", got)
	}
	if got := plan.Exercises[1]; got.TargetWeight.String() != "62.50" || got.TargetDistanceMeters != nil {
		t.Errorf("second exercise = %+v", got)
	}

	t.Run("missing", func(t *testing.T) {
		_, err := e.plans.Get(t.Context(), e.user, uuid.New())
		planRequireNotFound(t, err)
	})
	t.Run("another user's", func(t *testing.T) {
		_, err := e.plans.Get(t.Context(), e.other, id)
		planRequireNotFound(t, err)
	})
	t.Run("soft-deleted", func(t *testing.T) {
		gone := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanDeletedAt(planAt(0)))
		_, err := e.plans.Get(t.Context(), e.user, gone)
		planRequireNotFound(t, err)
	})
	t.Run("a cancelled context is an error, not a panic or a leaked connection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := e.plans.Get(ctx, e.user, id); err == nil {
			t.Fatal("Get with a cancelled context succeeded")
		}
		planNoLeakedConns(t, e.db)
	})
}

func planNoLeakedConns(t *testing.T, db *store.DB) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for db.Pool().Stat().AcquiredConns() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d connection(s) still acquired", db.Pool().Stat().AcquiredConns())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPlansSoftDelete(t *testing.T) {
	e := newPlanEnv(t)
	id := planNewID(t)
	stored, _ := e.save(t, id, planSpec("Doomed", planAt(0), e.ex(0, 0), e.ex(1, 1)))

	if err := e.plans.SoftDelete(t.Context(), e.user, id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	var deletedAt, serverUpdatedAt time.Time
	var clientUpdatedAt time.Time
	err := e.db.QueryRow(t.Context(),
		`SELECT deleted_at, server_updated_at, client_updated_at FROM workout_plans WHERE id = $1`, id).
		Scan(&deletedAt, &serverUpdatedAt, &clientUpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !deletedAt.Equal(serverUpdatedAt) {
		t.Errorf("deleted_at %v and server_updated_at %v differ", deletedAt, serverUpdatedAt)
	}
	if !serverUpdatedAt.After(stored.ServerUpdatedAt) {
		t.Errorf("server_updated_at %v did not advance past %v (the sync feed would miss the delete)", serverUpdatedAt, stored.ServerUpdatedAt)
	}
	if !clientUpdatedAt.Equal(stored.UpdatedAt) {
		t.Errorf("client_updated_at changed to %v", clientUpdatedAt)
	}
	if n := e.count(t, `SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, id); n != 2 {
		t.Errorf("%d exercise rows after delete, want the 2 kept", n)
	}
	if _, err := e.plans.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get after delete: %v, want NotFound", err)
	}

	t.Run("deleting again succeeds and changes nothing", func(t *testing.T) {
		if err := e.plans.SoftDelete(t.Context(), e.user, id); err != nil {
			t.Fatalf("second SoftDelete: %v", err)
		}
		var again time.Time
		if err := e.db.QueryRow(t.Context(), `SELECT server_updated_at FROM workout_plans WHERE id = $1`, id).Scan(&again); err != nil {
			t.Fatal(err)
		}
		if !again.Equal(serverUpdatedAt) {
			t.Errorf("server_updated_at moved from %v to %v on a repeated delete", serverUpdatedAt, again)
		}
	})
	t.Run("a later save is a deleted conflict", func(t *testing.T) {
		_, _, err := e.plans.Save(t.Context(), e.user, id, planSpec("Undo", planAt(1000)))
		planRequireConflict(t, err, domain.IssueDeleted)
	})
	t.Run("never existed", func(t *testing.T) {
		planRequireNotFound(t, e.plans.SoftDelete(t.Context(), e.user, uuid.New()))
	})
	t.Run("other plans are untouched", func(t *testing.T) {
		keep := planNewID(t)
		e.save(t, keep, planSpec("Keep", planAt(0)))
		other := planNewID(t)
		e.save(t, other, planSpec("Other", planAt(0)))
		if err := e.plans.SoftDelete(t.Context(), e.user, other); err != nil {
			t.Fatal(err)
		}
		e.get(t, keep)
	})
}
