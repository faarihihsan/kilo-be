package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

const (
	exerciseHashA = "9f2c4e1ab37d05c6"
	exerciseHashB = "0123456789abcdef"
	exerciseHashC = "fedcba9876543210"
)

func exerciseImage(hash string, ext domain.ImageExt) domain.ExerciseImage {
	return domain.ExerciseImage{Hash: hash, Ext: ext, SizeBytes: 1500}
}

func TestExercisesSetImage(t *testing.T) {
	t.Run("first image: nothing to delete, updated_at bumped", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		now := exerciseT0.Add(time.Hour)
		img := exerciseImage(exerciseHashA, domain.ImageExtWebP)

		got, old, err := s.SetImage(t.Context(), e.ID, img, now)
		if err != nil {
			t.Fatal(err)
		}
		if old != nil {
			t.Errorf("old = %+v, want nil", old)
		}
		if got.Image == nil || *got.Image != img {
			t.Errorf("image = %+v, want %+v", got.Image, img)
		}
		if !got.UpdatedAt.Equal(now) || !got.CreatedAt.Equal(exerciseT0) || got.Name != "Squat" {
			t.Errorf("updated_at = %v, created_at = %v, name = %q", got.UpdatedAt, got.CreatedAt, got.Name)
		}
		stored, _ := s.Get(t.Context(), e.ID)
		exerciseRequireSame(t, stored, got)
	})

	t.Run("replacing returns the previous image", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		first := exerciseImage(exerciseHashA, domain.ImageExtWebP)
		second := domain.ExerciseImage{Hash: exerciseHashB, Ext: domain.ImageExtJPG, SizeBytes: 99}
		if _, _, err := s.SetImage(t.Context(), e.ID, first, exerciseT0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, old, err := s.SetImage(t.Context(), e.ID, second, exerciseT0.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if old == nil || *old != first {
			t.Errorf("old = %+v, want %+v", old, first)
		}
		if *got.Image != second || !got.UpdatedAt.Equal(exerciseT0.Add(2*time.Hour)) {
			t.Errorf("got image %+v updated_at %v", got.Image, got.UpdatedAt)
		}
	})

	t.Run("the same file again is a no-op and old stays nil, so it is not deleted", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		img := exerciseImage(exerciseHashA, domain.ImageExtPNG)
		first, _, err := s.SetImage(t.Context(), e.ID, img, exerciseT0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		again, old, err := s.SetImage(t.Context(), e.ID, img, exerciseT0.Add(5*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if old != nil {
			t.Errorf("old = %+v, want nil: the file is still referenced", old)
		}
		exerciseRequireSame(t, again, first)
	})

	t.Run("the same hash with another extension is a different file", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		png := exerciseImage(exerciseHashA, domain.ImageExtPNG)
		jpg := exerciseImage(exerciseHashA, domain.ImageExtJPG)
		if _, _, err := s.SetImage(t.Context(), e.ID, png, exerciseT0); err != nil {
			t.Fatal(err)
		}
		_, old, err := s.SetImage(t.Context(), e.ID, jpg, exerciseT0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if old == nil || *old != png {
			t.Errorf("old = %+v, want %+v", old, png)
		}
	})

	t.Run("a deleted exercise is a conflict", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		if err := s.SoftDelete(t.Context(), e.ID, exerciseT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.SetImage(t.Context(), e.ID, exerciseImage(exerciseHashA, domain.ImageExtPNG), exerciseT0.Add(time.Hour))
		exerciseRequireConflict(t, err, domain.IssueDeleted)
		got, _ := s.Get(t.Context(), e.ID)
		if got.Image != nil {
			t.Error("a deleted exercise got an image")
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, s, _ := exerciseSetup(t)
		_, _, err := s.SetImage(t.Context(), uuid.New(), exerciseImage(exerciseHashA, domain.ImageExtPNG), exerciseT0)
		exerciseRequireNotFound(t, err)
	})

	t.Run("concurrent uploads never lose track of a file", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		imgs := []domain.ExerciseImage{
			exerciseImage(exerciseHashA, domain.ImageExtPNG),
			exerciseImage(exerciseHashB, domain.ImageExtPNG),
			exerciseImage(exerciseHashC, domain.ImageExtPNG),
		}
		type result struct {
			img domain.ExerciseImage
			old *domain.ExerciseImage
		}
		results := make([]result, len(imgs))
		var wg sync.WaitGroup
		for i, img := range imgs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, old, err := s.SetImage(t.Context(), e.ID, img, exerciseT0.Add(time.Duration(i+1)*time.Minute))
				if err != nil {
					t.Errorf("SetImage: %v", err)
				}
				results[i] = result{img, old}
			}()
		}
		wg.Wait()

		// Every upload replaced exactly one predecessor, so the files that were
		// reported as replaced plus the one that remains are all three, once.
		final, _ := s.Get(t.Context(), e.ID)
		files := map[domain.ExerciseImage]int{*final.Image: 1}
		for _, r := range results {
			if r.old != nil {
				files[*r.old]++
			}
		}
		if len(files) != 3 {
			t.Errorf("files accounted for: %v, want all three, each once", files)
		}
		for f, n := range files {
			if n != 1 {
				t.Errorf("file %+v accounted %d times", f, n)
			}
		}
	})
}

func TestExercisesClearImage(t *testing.T) {
	t.Run("returns the removed image and bumps updated_at", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := testutil.SeedExercise(t, db, user,
			testutil.WithExerciseImage(exerciseHashA, domain.ImageExtWebP, 1500),
			testutil.WithExerciseUpdatedAt(exerciseT0))
		now := exerciseT0.Add(time.Hour)
		old, err := s.ClearImage(t.Context(), id, now)
		if err != nil {
			t.Fatal(err)
		}
		if old == nil || *old != exerciseImage(exerciseHashA, domain.ImageExtWebP) {
			t.Errorf("old = %+v", old)
		}
		got, _ := s.Get(t.Context(), id)
		if got.Image != nil || !got.UpdatedAt.Equal(now) {
			t.Errorf("image = %+v updated_at = %v", got.Image, got.UpdatedAt)
		}
	})

	t.Run("no image is a no-op", func(t *testing.T) {
		_, s, user := exerciseSetup(t)
		e := exerciseCreate(t, s, user, "Squat")
		old, err := s.ClearImage(t.Context(), e.ID, exerciseT0.Add(time.Hour))
		if err != nil || old != nil {
			t.Fatalf("old = %+v err = %v, want nil, nil", old, err)
		}
		got, _ := s.Get(t.Context(), e.ID)
		if !got.UpdatedAt.Equal(exerciseT0) {
			t.Errorf("updated_at = %v, want unchanged", got.UpdatedAt)
		}
	})

	t.Run("clearing twice is a no-op the second time", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := testutil.SeedExercise(t, db, user, testutil.WithExerciseImage(exerciseHashA, domain.ImageExtJPG, 10))
		if _, err := s.ClearImage(t.Context(), id, exerciseT0); err != nil {
			t.Fatal(err)
		}
		old, err := s.ClearImage(t.Context(), id, exerciseT0.Add(time.Hour))
		if err != nil || old != nil {
			t.Fatalf("second clear: old = %+v err = %v", old, err)
		}
		got, _ := s.Get(t.Context(), id)
		if !got.UpdatedAt.Equal(exerciseT0) {
			t.Errorf("updated_at = %v, want the first clear's time", got.UpdatedAt)
		}
	})

	t.Run("a deleted exercise keeps its image and nothing is reported", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		id := testutil.SeedExercise(t, db, user,
			testutil.WithExerciseImage(exerciseHashA, domain.ImageExtJPG, 10),
			testutil.WithExerciseUpdatedAt(exerciseT0), testutil.WithExerciseDeletedAt(exerciseT0))
		old, err := s.ClearImage(t.Context(), id, exerciseT0.Add(time.Hour))
		if err != nil || old != nil {
			t.Fatalf("old = %+v err = %v, want nil, nil", old, err)
		}
		got, _ := s.Get(t.Context(), id)
		if got.Image == nil || !got.UpdatedAt.Equal(exerciseT0) {
			t.Errorf("image = %+v updated_at = %v, want untouched", got.Image, got.UpdatedAt)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, s, _ := exerciseSetup(t)
		_, err := s.ClearImage(t.Context(), uuid.New(), exerciseT0)
		exerciseRequireNotFound(t, err)
	})
}

func TestExercisesImageRefs(t *testing.T) {
	db, s, user := exerciseSetup(t)
	live := testutil.SeedExercise(t, db, user, testutil.WithExerciseImage(exerciseHashA, domain.ImageExtWebP, 10))
	deleted := testutil.SeedExercise(t, db, user, testutil.WithExerciseImage(exerciseHashB, domain.ImageExtPNG, 20),
		testutil.WithExerciseDeletedAt(exerciseT0))
	testutil.SeedExercise(t, db, user) // no image

	got := map[uuid.UUID]domain.ExerciseImage{}
	err := s.ImageRefs(t.Context(), func(id uuid.UUID, img domain.ExerciseImage) error {
		got[id] = img
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]domain.ExerciseImage{
		live:    {Hash: exerciseHashA, Ext: domain.ImageExtWebP, SizeBytes: 10},
		deleted: {Hash: exerciseHashB, Ext: domain.ImageExtPNG, SizeBytes: 20},
	}
	if len(got) != len(want) || got[live] != want[live] || got[deleted] != want[deleted] {
		t.Errorf("refs = %+v, want %+v (deleted exercises keep their files)", got, want)
	}

	t.Run("an error from the callback stops the scan and is returned", func(t *testing.T) {
		stop := errors.New("stop")
		calls := 0
		err := s.ImageRefs(t.Context(), func(uuid.UUID, domain.ExerciseImage) error {
			calls++
			return stop
		})
		if !errors.Is(err, stop) || calls != 1 {
			t.Errorf("err = %v after %d calls, want stop after 1", err, calls)
		}
		noLeakedConns(t, db)
	})

	t.Run("no images", func(t *testing.T) {
		_, empty, _ := exerciseSetup(t)
		n := 0
		if err := empty.ImageRefs(t.Context(), func(uuid.UUID, domain.ExerciseImage) error { n++; return nil }); err != nil || n != 0 {
			t.Errorf("n = %d err = %v", n, err)
		}
	})
}

// TestExercisesWritesBumpUpdatedAtForTheSyncFeed: every kind of write makes the
// exercise appear in updated_since, so other phones learn about it.
func TestExercisesWritesAppearInTheSyncFeed(t *testing.T) {
	_, s, user := exerciseSetup(t)
	e := exerciseCreate(t, s, user, "Squat")
	since := exerciseT0
	step := 0
	next := func() time.Time { step++; return exerciseT0.Add(time.Duration(step) * time.Hour) }
	appears := func(what string) {
		t.Helper()
		items, _ := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: &since})
		if len(items) != 1 || items[0].ID != e.ID {
			t.Fatalf("after %s: sync feed = %v, want the exercise", what, exerciseNames(items))
		}
		since = items[0].UpdatedAt
	}

	if _, err := s.Update(t.Context(), e.ID, exerciseInput("Back Squat"), next()); err != nil {
		t.Fatal(err)
	}
	appears("update")
	if _, _, err := s.SetImage(t.Context(), e.ID, exerciseImage(exerciseHashA, domain.ImageExtPNG), next()); err != nil {
		t.Fatal(err)
	}
	appears("set image")
	if _, err := s.ClearImage(t.Context(), e.ID, next()); err != nil {
		t.Fatal(err)
	}
	appears("clear image")
	if err := s.SoftDelete(t.Context(), e.ID, next()); err != nil {
		t.Fatal(err)
	}
	appears("delete")
}
