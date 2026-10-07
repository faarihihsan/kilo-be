package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// The exercises store tests run against a real PostgreSQL through
// testutil.NewDB. Helpers are prefixed exercise so they cannot collide with
// those of the other store test files.

var exerciseT0 = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

func exerciseInput(name string) domain.ExerciseInput {
	return domain.ExerciseInput{
		Name:                  name,
		Category:              domain.CategoryStrength,
		PrimaryMuscleGroup:    domain.MuscleGroupChest,
		SecondaryMuscleGroups: []domain.MuscleGroup{domain.MuscleGroupShoulders, domain.MuscleGroupTriceps},
		Equipment:             domain.EquipmentDumbbell,
		MeasurementType:       domain.MeasurementTypeRepsWeight,
		Instructions:          testutil.Ptr("Set bench to 30 degrees..."),
	}
}

func exerciseNewID(t testing.TB) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// exerciseSetup returns a fresh database, the exercises store and a user.
func exerciseSetup(t *testing.T) (*store.DB, *store.Exercises, uuid.UUID) {
	t.Helper()
	db := testutil.NewDB(t)
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)
	return db, store.NewExercises(db), user
}

// exerciseCreate creates an exercise with the given name and fails the test on
// any problem.
func exerciseCreate(t *testing.T, s *store.Exercises, user uuid.UUID, name string) domain.Exercise {
	t.Helper()
	e, created, err := s.Create(t.Context(), exerciseNewID(t), user, exerciseInput(name), exerciseT0)
	if err != nil || !created {
		t.Fatalf("Create %q: created=%v err=%v", name, created, err)
	}
	return e
}

func exerciseRequireConflict(t *testing.T, err error, issue string) *domain.ConflictError {
	t.Helper()
	var c *domain.ConflictError
	if !errors.As(err, &c) {
		t.Fatalf("err = %v (%T), want a conflict", err, err)
	}
	if c.Issue != issue {
		t.Fatalf("conflict issue = %q, want %q", c.Issue, issue)
	}
	return c
}

// exerciseRequireSame compares two exercises deeply (pointers by value).
func exerciseRequireSame(t *testing.T, got, want domain.Exercise) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %s\nwant %s", exerciseDump(got), exerciseDump(want))
	}
}

func exerciseDump(e domain.Exercise) string {
	b, _ := json.Marshal(e)
	img := "none"
	if e.Image != nil {
		img = fmt.Sprintf("%+v", *e.Image)
	}
	return fmt.Sprintf("%s image=%s", b, img)
}

func exerciseRequireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestExercisesCreate(t *testing.T) {
	_, s, user := exerciseSetup(t)
	id := exerciseNewID(t)
	in := exerciseInput("Incline Dumbbell Press")

	e, created, err := s.Create(t.Context(), id, user, in, exerciseT0)
	if err != nil || !created {
		t.Fatalf("Create: created=%v err=%v", created, err)
	}
	if e.ID != id || e.Name != in.Name || e.Category != in.Category ||
		e.PrimaryMuscleGroup != in.PrimaryMuscleGroup || e.Equipment != in.Equipment ||
		e.MeasurementType != in.MeasurementType {
		t.Errorf("stored %+v, want the content of %+v", e, in)
	}
	if !slices.Equal(e.SecondaryMuscleGroups, in.SecondaryMuscleGroups) {
		t.Errorf("secondary = %v, want %v", e.SecondaryMuscleGroups, in.SecondaryMuscleGroups)
	}
	if e.Instructions == nil || *e.Instructions != *in.Instructions {
		t.Errorf("instructions = %v", e.Instructions)
	}
	if e.CreatedBy != user {
		t.Errorf("created_by = %v, want %v", e.CreatedBy, user)
	}
	if !e.CreatedAt.Equal(exerciseT0) || !e.UpdatedAt.Equal(exerciseT0) {
		t.Errorf("created_at = %v, updated_at = %v, want %v", e.CreatedAt, e.UpdatedAt, exerciseT0)
	}
	if e.CreatedAt.Location() != time.UTC || e.UpdatedAt.Location() != time.UTC {
		t.Error("times are not in UTC")
	}
	if e.DeletedAt != nil || e.Image != nil || e.ImageURL != nil {
		t.Errorf("deleted_at = %v, image = %v, image_url = %v, want none", e.DeletedAt, e.Image, e.ImageURL)
	}

	got, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	exerciseRequireSame(t, got, e)
}

func TestExercisesCreateWithoutOptionalContent(t *testing.T) {
	_, s, user := exerciseSetup(t)
	in := exerciseInput("Plank")
	in.SecondaryMuscleGroups = nil
	in.Instructions = nil
	e, _, err := s.Create(t.Context(), exerciseNewID(t), user, in, exerciseT0)
	if err != nil {
		t.Fatal(err)
	}
	if e.Instructions != nil {
		t.Errorf("instructions = %q, want nil", *e.Instructions)
	}
	if e.SecondaryMuscleGroups == nil || len(e.SecondaryMuscleGroups) != 0 {
		t.Errorf("secondary = %#v, want empty non-nil", e.SecondaryMuscleGroups)
	}
}

func TestExercisesCreateConflicts(t *testing.T) {
	t.Run("idempotent retry of the same id returns the row unchanged", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := exerciseNewID(t)
		first, _, err := s.Create(t.Context(), id, user, exerciseInput("Squat"), exerciseT0)
		if err != nil {
			t.Fatal(err)
		}
		// Another user, a later clock: nothing may change.
		other, _ := testutil.SeedUser(t, db, domain.RoleUser)
		again, created, err := s.Create(t.Context(), id, other, exerciseInput("Squat"), exerciseT0.Add(time.Hour))
		if err != nil || created {
			t.Fatalf("retry: created=%v err=%v, want created=false", created, err)
		}
		exerciseRequireSame(t, again, first)
	})

	t.Run("same id with different content is id_taken", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		id := exerciseNewID(t)
		if _, _, err := s.Create(t.Context(), id, user, exerciseInput("Squat"), exerciseT0); err != nil {
			t.Fatal(err)
		}
		in := exerciseInput("Squat")
		in.Equipment = domain.EquipmentBarbell
		_, _, err := s.Create(t.Context(), id, user, in, exerciseT0)
		c := exerciseRequireConflict(t, err, domain.IssueIDTaken)
		if c.ExistingID != uuid.Nil {
			t.Errorf("ExistingID = %v, want none", c.ExistingID)
		}
	})

	t.Run("same id of a deleted exercise is deleted, even with identical content", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		id := exerciseNewID(t)
		if _, _, err := s.Create(t.Context(), id, user, exerciseInput("Squat"), exerciseT0); err != nil {
			t.Fatal(err)
		}
		if err := s.SoftDelete(t.Context(), id, exerciseT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.Create(t.Context(), id, user, exerciseInput("Squat"), exerciseT0.Add(time.Hour))
		_ = exerciseRequireConflict(t, err, domain.IssueDeleted)
	})

	t.Run("name taken is already_exists with the other id, ignoring case", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		first := exerciseCreate(t, s, user, "Bench Press")
		_, _, err := s.Create(t.Context(), exerciseNewID(t), user, exerciseInput("bench PRESS"), exerciseT0)
		c := exerciseRequireConflict(t, err, domain.IssueAlreadyExists)
		if c.ExistingID != first.ID || c.Field != "name" {
			t.Errorf("ExistingID = %v, Field = %q, want %v and name", c.ExistingID, c.Field, first.ID)
		}
	})

	t.Run("the id is examined before the name", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		a := exerciseCreate(t, s, user, "Alpha")
		exerciseCreate(t, s, user, "Beta")
		// Same id as Alpha, different content, and a name that Beta holds.
		_, _, err := s.Create(t.Context(), a.ID, user, exerciseInput("Beta"), exerciseT0)
		_ = exerciseRequireConflict(t, err, domain.IssueIDTaken)
	})

	t.Run("a deleted name can be reused", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		old := exerciseCreate(t, s, user, "Bench Press")
		if err := s.SoftDelete(t.Context(), old.ID, exerciseT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		fresh, created, err := s.Create(t.Context(), exerciseNewID(t), user, exerciseInput("bench press"), exerciseT0.Add(time.Hour))
		if err != nil || !created {
			t.Fatalf("Create: created=%v err=%v", created, err)
		}
		if fresh.ID == old.ID {
			t.Error("the new exercise reused the old id")
		}
	})

	t.Run("an unknown creator is not a client error", func(t *testing.T) {
		_, s, _ := exerciseSetup(t)
		_, _, err := s.Create(t.Context(), exerciseNewID(t), uuid.New(), exerciseInput("Squat"), exerciseT0)
		if err == nil {
			t.Fatal("Create with an unknown created_by succeeded")
		}
		for _, sentinel := range []error{domain.ErrConflict, domain.ErrNotFound, domain.ErrValidation} {
			if errors.Is(err, sentinel) {
				t.Errorf("err = %v, must be an internal error", err)
			}
		}
	})
}

func TestExercisesCreateConcurrently(t *testing.T) {
	const workers = 12

	t.Run("one name wins, the others get already_exists", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			created []uuid.UUID
			errs    []error
		)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := exerciseNewID(t)
				_, ok, err := s.Create(t.Context(), id, user, exerciseInput("Deadlift"), exerciseT0)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
				} else if ok {
					created = append(created, id)
				}
			}()
		}
		wg.Wait()
		if len(created) != 1 {
			t.Fatalf("%d exercises created, want exactly 1 (errors: %v)", len(created), errs)
		}
		for _, err := range errs {
			c := exerciseRequireConflict(t, err, domain.IssueAlreadyExists)
			if c.ExistingID != created[0] {
				t.Errorf("ExistingID = %v, want the winner %v", c.ExistingID, created[0])
			}
		}
		if len(errs) != workers-1 {
			t.Errorf("%d conflicts, want %d", len(errs), workers-1)
		}
	})

	t.Run("one id created once, retries are idempotent", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		id := exerciseNewID(t)
		var (
			wg           sync.WaitGroup
			mu           sync.Mutex
			createdCount int
		)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, ok, err := s.Create(t.Context(), id, user, exerciseInput("Deadlift"), exerciseT0)
				if err != nil {
					t.Errorf("Create: %v", err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if ok {
					createdCount++
				}
			}()
		}
		wg.Wait()
		if createdCount != 1 {
			t.Errorf("created %d times, want 1", createdCount)
		}
	})
}

func TestExercisesGet(t *testing.T) {
	db, s, user := exerciseSetup(t)

	t.Run("not found", func(t *testing.T) {
		_, err := s.Get(t.Context(), uuid.New())
		exerciseRequireNotFound(t, err)
	})

	t.Run("returns a deleted exercise with all its fields and its image", func(t *testing.T) {
		id := testutil.SeedExercise(t, db, user,
			testutil.WithExerciseName("Old One"),
			testutil.WithSecondaryMuscleGroups(domain.MuscleGroupLats, domain.MuscleGroupBiceps),
			testutil.WithInstructions("Pull."),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtWebP, 1234),
			testutil.WithExerciseCreatedAt(exerciseT0),
			testutil.WithExerciseUpdatedAt(exerciseT0.Add(time.Hour)),
			testutil.WithExerciseDeletedAt(exerciseT0.Add(time.Hour)))
		e, err := s.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Name != "Old One" || e.DeletedAt == nil || !e.DeletedAt.Equal(exerciseT0.Add(time.Hour)) {
			t.Errorf("got %+v", e)
		}
		if e.Image == nil || *e.Image != (domain.ExerciseImage{Hash: "9f2c4e1ab37d05c6", Ext: domain.ImageExtWebP, SizeBytes: 1234}) {
			t.Errorf("image = %+v", e.Image)
		}
		if e.ImageURL != nil {
			t.Error("the store must not fill image_url")
		}
		if !slices.Equal(e.SecondaryMuscleGroups, []domain.MuscleGroup{domain.MuscleGroupLats, domain.MuscleGroupBiceps}) {
			t.Errorf("secondary = %v", e.SecondaryMuscleGroups)
		}
	})
}

func TestExercisesUpdate(t *testing.T) {
	t.Run("replaces the editable fields and bumps updated_at", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := testutil.SeedExercise(t, db, user,
			testutil.WithExerciseName("Bench Press"),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtPNG, 10),
			testutil.WithExerciseCreatedAt(exerciseT0),
			testutil.WithExerciseUpdatedAt(exerciseT0))

		in := domain.ExerciseInput{
			Name:               "Barbell Bench Press",
			Category:           domain.CategoryCardio,
			PrimaryMuscleGroup: domain.MuscleGroupLats,
			Equipment:          domain.EquipmentCable,
			MeasurementType:    domain.MeasurementTypeDuration,
		}
		now := exerciseT0.Add(2 * time.Hour)
		e, err := s.Update(t.Context(), id, in, now)
		if err != nil {
			t.Fatal(err)
		}
		if e.Name != in.Name || e.Category != in.Category || e.PrimaryMuscleGroup != in.PrimaryMuscleGroup ||
			e.Equipment != in.Equipment || e.MeasurementType != in.MeasurementType {
			t.Errorf("updated = %+v, want the content of %+v", e, in)
		}
		if len(e.SecondaryMuscleGroups) != 0 || e.Instructions != nil {
			t.Errorf("secondary = %v, instructions = %v: a full replace clears what the input omits", e.SecondaryMuscleGroups, e.Instructions)
		}
		if !e.UpdatedAt.Equal(now) || !e.CreatedAt.Equal(exerciseT0) || e.CreatedBy != user {
			t.Errorf("updated_at = %v created_at = %v created_by = %v", e.UpdatedAt, e.CreatedAt, e.CreatedBy)
		}
		if e.Image == nil || e.Image.Hash != "9f2c4e1ab37d05c6" || e.Image.Ext != domain.ImageExtPNG {
			t.Errorf("the image was touched: %+v", e.Image)
		}
		got, _ := s.Get(t.Context(), id)
		exerciseRequireSame(t, got, e)
	})

	t.Run("identical content writes nothing", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		first := exerciseCreate(t, s, user, "Squat")
		e, err := s.Update(t.Context(), first.ID, exerciseInput("Squat"), exerciseT0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !e.UpdatedAt.Equal(exerciseT0) {
			t.Errorf("updated_at = %v, want %v (unchanged)", e.UpdatedAt, exerciseT0)
		}
	})

	t.Run("a change of case only is an update", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		first := exerciseCreate(t, s, user, "squat")
		e, err := s.Update(t.Context(), first.ID, exerciseInput("Squat"), exerciseT0.Add(time.Hour))
		if err != nil {
			t.Fatalf("renaming to another case of its own name failed: %v", err)
		}
		if e.Name != "Squat" || !e.UpdatedAt.Equal(exerciseT0.Add(time.Hour)) {
			t.Errorf("got %+v", e)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, s, _ := exerciseSetup(t)
		_, err := s.Update(t.Context(), uuid.New(), exerciseInput("Squat"), exerciseT0)
		exerciseRequireNotFound(t, err)
	})

	t.Run("a deleted exercise is a conflict, before the name is looked at", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		gone := exerciseCreate(t, s, user, "Gone")
		exerciseCreate(t, s, user, "Taken")
		if err := s.SoftDelete(t.Context(), gone.ID, exerciseT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		_, err := s.Update(t.Context(), gone.ID, exerciseInput("Taken"), exerciseT0.Add(time.Hour))
		_ = exerciseRequireConflict(t, err, domain.IssueDeleted)
	})

	t.Run("a name held by another live exercise is already_exists", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		a := exerciseCreate(t, s, user, "Alpha")
		b := exerciseCreate(t, s, user, "Beta")
		_, err := s.Update(t.Context(), b.ID, exerciseInput("ALPHA"), exerciseT0.Add(time.Hour))
		c := exerciseRequireConflict(t, err, domain.IssueAlreadyExists)
		if c.ExistingID != a.ID || c.Field != "name" {
			t.Errorf("ExistingID = %v Field = %q, want %v and name", c.ExistingID, c.Field, a.ID)
		}
		// The failed update changed nothing.
		got, _ := s.Get(t.Context(), b.ID)
		if got.Name != "Beta" || !got.UpdatedAt.Equal(exerciseT0) {
			t.Errorf("after a conflict: %+v", got)
		}
	})

	t.Run("the name of a deleted exercise is free", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		a := exerciseCreate(t, s, user, "Alpha")
		b := exerciseCreate(t, s, user, "Beta")
		if err := s.SoftDelete(t.Context(), a.ID, exerciseT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		e, err := s.Update(t.Context(), b.ID, exerciseInput("Alpha"), exerciseT0.Add(time.Hour))
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if e.Name != "Alpha" {
			t.Errorf("name = %q", e.Name)
		}
	})

	t.Run("concurrent renames to one name: one wins", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		const workers = 6
		ids := make([]uuid.UUID, workers)
		for i := range ids {
			ids[i] = exerciseCreate(t, s, user, fmt.Sprintf("Original %d", i)).ID
		}
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			won  int
			errs []error
		)
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.Update(t.Context(), id, exerciseInput("Contested"), exerciseT0.Add(time.Hour))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					won++
				} else {
					errs = append(errs, err)
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Fatalf("%d renames succeeded, want 1 (errors: %v)", won, errs)
		}
		for _, err := range errs {
			_ = exerciseRequireConflict(t, err, domain.IssueAlreadyExists)
		}
	})
}

func TestExercisesSoftDelete(t *testing.T) {
	t.Run("sets deleted_at and updated_at, keeps everything else", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := testutil.SeedExercise(t, db, user,
			testutil.WithExerciseName("Bench Press"),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtJPG, 10),
			testutil.WithExerciseUpdatedAt(exerciseT0))
		now := exerciseT0.Add(time.Hour)
		if err := s.SoftDelete(t.Context(), id, now); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(t.Context(), id)
		if e.DeletedAt == nil || !e.DeletedAt.Equal(now) || !e.UpdatedAt.Equal(now) {
			t.Errorf("deleted_at = %v updated_at = %v, want both %v", e.DeletedAt, e.UpdatedAt, now)
		}
		if e.Name != "Bench Press" || e.Image == nil {
			t.Errorf("a deleted exercise must keep its fields and image: %+v", e)
		}
	})

	t.Run("deleting twice is a no-op the second time", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		first := exerciseT0.Add(time.Hour)
		if err := s.SoftDelete(t.Context(), e.ID, first); err != nil {
			t.Fatal(err)
		}
		if err := s.SoftDelete(t.Context(), e.ID, first.Add(time.Hour)); err != nil {
			t.Fatalf("second delete: %v", err)
		}
		got, _ := s.Get(t.Context(), e.ID)
		if !got.DeletedAt.Equal(first) || !got.UpdatedAt.Equal(first) {
			t.Errorf("deleted_at = %v updated_at = %v, want both %v (unchanged)", got.DeletedAt, got.UpdatedAt, first)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, s, _ := exerciseSetup(t)
		exerciseRequireNotFound(t, s.SoftDelete(t.Context(), uuid.New(), exerciseT0))
	})
}
