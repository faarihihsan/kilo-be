package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// progressBase is the reference time of the progress tests. It is fixed and in
// the past, so rows the store stamps with the database clock sort after rows
// seeded relative to it.
var progressBase = time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)

// progressEnv is a migrated throwaway database with two users and three
// exercises of the first one.
type progressEnv struct {
	db    *store.DB
	store *store.Progress
	user  uuid.UUID // the caller of the tests
	other uuid.UUID // another user
	ex    [3]uuid.UUID
}

func progressNewEnv(t *testing.T) *progressEnv {
	t.Helper()
	db := testutil.NewDB(t)
	e := &progressEnv{db: db, store: store.NewProgress(db)}
	e.user, _ = testutil.SeedUser(t, db, domain.RoleUser)
	e.other, _ = testutil.SeedUser(t, db, domain.RoleUser)
	for i := range e.ex {
		e.ex[i] = testutil.SeedExercise(t, db, e.user)
	}
	return e
}

// progressSet builds a set with the decimals as text (nil when empty).
func progressSet(pos int, typ domain.SetType, reps int, weight, rpe string) domain.ProgressSet {
	s := domain.ProgressSet{Position: pos, Type: typ, Reps: testutil.Ptr(reps), Completed: true}
	if weight != "" {
		s.Weight = testutil.Ptr(domain.ProgressDecimal(weight))
	}
	if rpe != "" {
		s.RPE = testutil.Ptr(domain.ProgressDecimal(rpe))
	}
	return s
}

// input is a valid two-exercise save with the given client updated_at. The
// first exercise has a warmup and a normal set, the second one timed set.
func (e *progressEnv) input(updatedAt time.Time) domain.ProgressSave {
	return domain.ProgressSave{
		Name:            "Push Day A",
		Notes:           testutil.Ptr("Felt strong"),
		StartedAt:       progressBase,
		EndedAt:         progressBase.Add(55 * time.Minute),
		DurationSeconds: 3120,
		UpdatedAt:       updatedAt,
		Exercises: []domain.ProgressExercise{
			{
				ExerciseID: e.ex[0], Position: 0, Sets: []domain.ProgressSet{
					progressSet(0, domain.SetTypeWarmup, 12, "40", ""),
					progressSet(1, domain.SetTypeNormal, 8, "80.5", "8.5"),
				},
			},
			{
				ExerciseID: e.ex[1], Position: 1, Notes: testutil.Ptr("plank"), Sets: []domain.ProgressSet{
					{Position: 0, Type: domain.SetTypeFailure, DurationSeconds: testutil.Ptr(90), Completed: false},
				},
			},
		},
	}
}

// at is progressBase plus n seconds: a client updated_at.
func progressAt(n int) time.Time { return progressBase.Add(time.Duration(n) * time.Second) }

// save runs Save as the caller and fails the test on any error.
func (e *progressEnv) save(t *testing.T, id uuid.UUID, in domain.ProgressSave) (domain.Progress, bool) {
	t.Helper()
	out, created, err := e.store.Save(t.Context(), e.user, id, in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return out, created
}

func progressNewID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// progressJSON is v as compact JSON, the form in which sessions are compared:
// it pins the field names and the decimal text along with the values.
func progressJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func (e *progressEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// progressIssues returns the issues of a validation error as "field: issue".
func progressIssues(t *testing.T, err error) []string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *domain.ValidationError", err)
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Field + ": " + is.Issue
	}
	return out
}

// progressConflict returns the conflict error inside err, failing the test if
// there is none.
func progressConflict(t *testing.T, err error) *domain.ConflictError {
	t.Helper()
	var ce *domain.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a *domain.ConflictError", err)
	}
	return ce
}
