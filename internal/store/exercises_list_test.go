package store_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

func exerciseNames(items []domain.Exercise) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.Name
	}
	return out
}

func exerciseList(t *testing.T, s *store.Exercises, f store.ExerciseFilter) ([]domain.Exercise, string) {
	t.Helper()
	items, next, err := s.List(t.Context(), f)
	if err != nil {
		t.Fatalf("List(%+v): %v", f, err)
	}
	return items, next
}

func exerciseRequireNames(t *testing.T, items []domain.Exercise, want ...string) {
	t.Helper()
	if got := exerciseNames(items); !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

// exerciseRequireSameNames compares names ignoring order. Search tests use it
// because the order of names with spaces or punctuation depends on the
// database's collation.
func exerciseRequireSameNames(t *testing.T, items []domain.Exercise, want ...string) {
	t.Helper()
	got := exerciseNames(items)
	slices.Sort(got)
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

func TestExercisesListDefaultOrderAndDeleted(t *testing.T) {
	db, s, user := exerciseSetup(t)
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("cherry"))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("Banana"))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("apple"))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("Date"), testutil.WithExerciseDeletedAt(exerciseT0))

	t.Run("case-insensitive name order, deleted hidden", func(t *testing.T) {
		items, next := exerciseList(t, s, store.ExerciseFilter{})
		exerciseRequireNames(t, items, "apple", "Banana", "cherry")
		if next != "" {
			t.Errorf("next cursor = %q, want none", next)
		}
	})

	t.Run("include_deleted lists deleted rows in the same order", func(t *testing.T) {
		items, _ := exerciseList(t, s, store.ExerciseFilter{IncludeDeleted: true})
		exerciseRequireNames(t, items, "apple", "Banana", "cherry", "Date")
		if items[3].DeletedAt == nil {
			t.Error("the deleted row has no deleted_at")
		}
	})
}

func TestExercisesListTieBreaksOnID(t *testing.T) {
	db, s, user := exerciseSetup(t)
	low := uuid.MustParse("00000000-0000-7000-8000-000000000001")
	high := uuid.MustParse("00000000-0000-7000-8000-000000000002")
	// Same lower(name): only possible when one of them is deleted.
	testutil.SeedExercise(t, db, user, testutil.WithExerciseID(high), testutil.WithExerciseName("Bench"))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseID(low), testutil.WithExerciseName("bench"),
		testutil.WithExerciseDeletedAt(exerciseT0))

	items, _ := exerciseList(t, s, store.ExerciseFilter{IncludeDeleted: true})
	if len(items) != 2 || items[0].ID != low || items[1].ID != high {
		t.Errorf("order = %v, want ids %v then %v", exerciseNames(items), low, high)
	}

	// Paging one by one across the tie must not skip or repeat.
	first, next := exerciseList(t, s, store.ExerciseFilter{IncludeDeleted: true, Limit: 1})
	second, _ := exerciseList(t, s, store.ExerciseFilter{IncludeDeleted: true, Limit: 1, Cursor: next})
	if len(first) != 1 || len(second) != 1 || first[0].ID != low || second[0].ID != high {
		t.Errorf("pages = %v, %v", exerciseNames(first), exerciseNames(second))
	}
}

func TestExercisesListFilters(t *testing.T) {
	db, s, user := exerciseSetup(t)
	seed := func(name string, cat domain.Category, primary domain.MuscleGroup, eq domain.Equipment, opts ...testutil.ExerciseOption) {
		opts = append(opts, testutil.WithExerciseName(name), testutil.WithExerciseCategory(cat),
			testutil.WithPrimaryMuscleGroup(primary), testutil.WithEquipment(eq))
		testutil.SeedExercise(t, db, user, opts...)
	}
	seed("a", domain.CategoryStrength, domain.MuscleGroupChest, domain.EquipmentBarbell)
	seed("b", domain.CategoryStrength, domain.MuscleGroupLats, domain.EquipmentBarbell)
	seed("c", domain.CategoryCardio, domain.MuscleGroupFullBody, domain.EquipmentCardioMachine)
	seed("d", domain.CategoryStrength, domain.MuscleGroupChest, domain.EquipmentDumbbell)
	seed("e", domain.CategoryStrength, domain.MuscleGroupChest, domain.EquipmentBarbell,
		testutil.WithExerciseDeletedAt(exerciseT0))
	// A secondary muscle group is not a match for primary_muscle_group.
	seed("f", domain.CategoryMobility, domain.MuscleGroupAbs, domain.EquipmentOther,
		testutil.WithSecondaryMuscleGroups(domain.MuscleGroupChest))

	tests := []struct {
		name string
		f    store.ExerciseFilter
		want []string
	}{
		{"category", store.ExerciseFilter{Category: domain.CategoryStrength}, []string{"a", "b", "d"}},
		{"primary muscle group", store.ExerciseFilter{PrimaryMuscleGroup: domain.MuscleGroupChest}, []string{"a", "d"}},
		{"equipment", store.ExerciseFilter{Equipment: domain.EquipmentBarbell}, []string{"a", "b"}},
		{"all three combine with AND", store.ExerciseFilter{
			Category: domain.CategoryStrength, PrimaryMuscleGroup: domain.MuscleGroupChest, Equipment: domain.EquipmentBarbell,
		}, []string{"a"}},
		{"filters apply to deleted rows too", store.ExerciseFilter{
			PrimaryMuscleGroup: domain.MuscleGroupChest, IncludeDeleted: true,
		}, []string{"a", "d", "e"}},
		{"no match", store.ExerciseFilter{Category: domain.CategoryMobility, Equipment: domain.EquipmentBarbell}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, _ := exerciseList(t, s, tt.f)
			exerciseRequireNames(t, items, tt.want...)
		})
	}
}

func TestExercisesListSearch(t *testing.T) {
	db, s, user := exerciseSetup(t)
	for _, name := range []string{
		"Bench Press", "Incline Bench", "Benchmark 100%", "Squat", "Leg_Press", "Back Squat", "O'Neil\\Row",
	} {
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName(name))
	}
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("Deleted Bench"), testutil.WithExerciseDeletedAt(exerciseT0))

	tests := []struct {
		name string
		q    string
		want []string
	}{
		{"substring in the middle", "nch pre", []string{"Bench Press"}},
		{"lower case finds mixed case", "bench", []string{"Bench Press", "Benchmark 100%", "Incline Bench"}},
		{"upper case finds mixed case", "SQUAT", []string{"Back Squat", "Squat"}},
		{"percent is literal", "100%", []string{"Benchmark 100%"}},
		{"a lone percent matches only names containing it", "%", []string{"Benchmark 100%"}},
		{"underscore is literal", "g_p", []string{"Leg_Press"}},
		{"a lone underscore matches only names containing it", "_", []string{"Leg_Press"}},
		{"backslash is literal", `\r`, []string{`O'Neil\Row`}},
		{"quote is plain text", "o'n", []string{`O'Neil\Row`}},
		{"no match", "deadlift", []string{}},
		{"deleted rows are hidden", "deleted", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, _ := exerciseList(t, s, store.ExerciseFilter{Query: tt.q})
			exerciseRequireSameNames(t, items, tt.want...)
		})
	}

	t.Run("include_deleted searches deleted rows", func(t *testing.T) {
		items, _ := exerciseList(t, s, store.ExerciseFilter{Query: "bench", IncludeDeleted: true})
		exerciseRequireSameNames(t, items, "Bench Press", "Benchmark 100%", "Deleted Bench", "Incline Bench")
	})
	t.Run("combines with a filter", func(t *testing.T) {
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("Bench Dip"), testutil.WithEquipment(domain.EquipmentBodyweight))
		items, _ := exerciseList(t, s, store.ExerciseFilter{Query: "bench", Equipment: domain.EquipmentBodyweight})
		exerciseRequireSameNames(t, items, "Bench Dip")
	})
}

func TestExercisesListUpdatedSince(t *testing.T) {
	db, s, user := exerciseSetup(t)
	at := func(sec int) testutil.ExerciseOption {
		return testutil.WithExerciseUpdatedAt(exerciseT0.Add(time.Duration(sec) * time.Second))
	}
	idLow := uuid.MustParse("00000000-0000-7000-8000-000000000001")
	idHigh := uuid.MustParse("00000000-0000-7000-8000-000000000002")
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("A"), at(1))
	// Two rows with the same updated_at: ordered by id, not by name.
	testutil.SeedExercise(t, db, user, testutil.WithExerciseID(idHigh), testutil.WithExerciseName("B"), at(2))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseID(idLow), testutil.WithExerciseName("Z"), at(2),
		testutil.WithExerciseDeletedAt(exerciseT0.Add(2*time.Second)))
	testutil.SeedExercise(t, db, user, testutil.WithExerciseName("D"), at(3))

	since := func(sec int) *time.Time { v := exerciseT0.Add(time.Duration(sec) * time.Second); return &v }

	t.Run("strictly after, deleted rows included, ordered by updated_at then id", func(t *testing.T) {
		items, _ := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: since(1)})
		exerciseRequireNames(t, items, "Z", "B", "D")
		if items[0].DeletedAt == nil {
			t.Error("the soft-deleted row has no deleted_at")
		}
	})
	t.Run("the epoch returns everything", func(t *testing.T) {
		epoch := time.Unix(0, 0).UTC()
		items, _ := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: &epoch})
		exerciseRequireNames(t, items, "A", "Z", "B", "D")
	})
	t.Run("nothing newer", func(t *testing.T) {
		items, next := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: since(3)})
		exerciseRequireNames(t, items)
		if next != "" {
			t.Errorf("next = %q", next)
		}
	})
	t.Run("filters apply in sync mode", func(t *testing.T) {
		items, _ := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: since(0), Query: "b"})
		exerciseRequireNames(t, items, "B")
	})
	t.Run("a non-UTC offset means the same instant", func(t *testing.T) {
		zone := time.FixedZone("x", 5*3600)
		v := since(1).In(zone)
		items, _ := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: &v})
		exerciseRequireNames(t, items, "Z", "B", "D")
	})
	t.Run("paging one at a time visits each row once, across equal timestamps", func(t *testing.T) {
		var got []string
		cursor := ""
		for range 10 {
			items, next := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: since(0), Limit: 1, Cursor: cursor})
			got = append(got, exerciseNames(items)...)
			if next == "" {
				break
			}
			cursor = next
		}
		if want := []string{"A", "Z", "B", "D"}; !slices.Equal(got, want) {
			t.Errorf("visited %v, want %v", got, want)
		}
	})
}

func TestExercisesListPagination(t *testing.T) {
	db, s, user := exerciseSetup(t)
	for i := 1; i <= 7; i++ {
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName(fmt.Sprintf("ex%d", i)),
			testutil.WithExerciseUpdatedAt(exerciseT0.Add(time.Duration(i)*time.Second)))
	}
	epoch := time.Unix(0, 0).UTC()

	for _, mode := range []struct {
		name string
		f    store.ExerciseFilter
	}{
		{"by name", store.ExerciseFilter{}},
		{"sync", store.ExerciseFilter{UpdatedSince: &epoch}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			var (
				pages  [][]string
				cursor string
				nexts  []string
			)
			for range 10 {
				f := mode.f
				f.Limit, f.Cursor = 3, cursor
				items, next := exerciseList(t, s, f)
				pages = append(pages, exerciseNames(items))
				nexts = append(nexts, next)
				if next == "" {
					break
				}
				cursor = next
			}
			want := [][]string{{"ex1", "ex2", "ex3"}, {"ex4", "ex5", "ex6"}, {"ex7"}}
			if fmt.Sprint(pages) != fmt.Sprint(want) {
				t.Errorf("pages = %v, want %v", pages, want)
			}
			if nexts[0] == "" || nexts[1] == "" || nexts[2] != "" {
				t.Errorf("next cursors = %q, want set, set, empty", nexts)
			}
		})

		t.Run(mode.name+": a full last page has no next cursor", func(t *testing.T) {
			f := mode.f
			f.Limit = 7
			items, next := exerciseList(t, s, f)
			if len(items) != 7 || next != "" {
				t.Errorf("got %d items and next %q, want 7 and none", len(items), next)
			}
			f.Limit = 6
			items, next = exerciseList(t, s, f)
			if len(items) != 6 || next == "" {
				t.Errorf("got %d items and next %q, want 6 and a cursor", len(items), next)
			}
		})
	}
}

func TestExercisesListLimitBounds(t *testing.T) {
	db, s, user := exerciseSetup(t)
	for i := range domain.MaxPageLimit + 5 {
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName(fmt.Sprintf("ex%03d", i)))
	}
	tests := []struct {
		limit, want int
	}{
		{0, domain.DefaultPageLimit},
		{-3, domain.DefaultPageLimit},
		{1, 1},
		{domain.MaxPageLimit, domain.MaxPageLimit},
		{domain.MaxPageLimit + 5, domain.MaxPageLimit},
	}
	for _, tt := range tests {
		items, _ := exerciseList(t, s, store.ExerciseFilter{Limit: tt.limit})
		if len(items) != tt.want {
			t.Errorf("limit %d returned %d items, want %d", tt.limit, len(items), tt.want)
		}
	}
}

// TestExercisesListPagesAreStableUnderConcurrentWrites: rows added, deleted or
// edited between two requests never make a row repeat or vanish from the pages
// that follow.
func TestExercisesListPagesAreStableUnderConcurrentWrites(t *testing.T) {
	t.Run("by name", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		for i := 1; i <= 9; i++ {
			testutil.SeedExercise(t, db, user, testutil.WithExerciseName(fmt.Sprintf("ex%d", i)))
		}
		page1, next := exerciseList(t, s, store.ExerciseFilter{Limit: 3})
		exerciseRequireNames(t, page1, "ex1", "ex2", "ex3")

		// Between the pages: rows before and after the cursor position appear,
		// and a row that is still to come is deleted.
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("ex0"))
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("ex25"))
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("ex35"))
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("ex95"))
		if err := s.SoftDelete(t.Context(), exerciseMustID(t, s, "ex4"), exerciseT0); err != nil {
			t.Fatal(err)
		}

		var rest []string
		for range 10 {
			items, n := exerciseList(t, s, store.ExerciseFilter{Limit: 3, Cursor: next})
			rest = append(rest, exerciseNames(items)...)
			if n == "" {
				break
			}
			next = n
		}
		want := []string{"ex35", "ex5", "ex6", "ex7", "ex8", "ex9", "ex95"}
		if !slices.Equal(rest, want) {
			t.Errorf("remaining pages = %v, want %v", rest, want)
		}
	})

	t.Run("sync feed", func(t *testing.T) {
		db, s, user := exerciseSetup(t)
		at := func(sec int) time.Time { return exerciseT0.Add(time.Duration(sec) * time.Second) }
		var ids []uuid.UUID
		for i := 1; i <= 6; i++ {
			ids = append(ids, testutil.SeedExercise(t, db, user, testutil.WithExerciseName(fmt.Sprintf("ex%d", i)),
				testutil.WithExerciseUpdatedAt(at(i))))
		}
		epoch := time.Unix(0, 0).UTC()
		page1, next := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: &epoch, Limit: 2})
		exerciseRequireNames(t, page1, "ex1", "ex2")

		// A new row, an edit of a row that was already delivered and the
		// deletion of one still to come: all show up after the cursor, once.
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName("new"), testutil.WithExerciseUpdatedAt(at(10)))
		if _, err := s.Update(t.Context(), ids[0], exerciseInput("ex1 edited"), at(20)); err != nil {
			t.Fatal(err)
		}
		if err := s.SoftDelete(t.Context(), ids[3], at(30)); err != nil {
			t.Fatal(err)
		}

		var rest []string
		for range 10 {
			items, n := exerciseList(t, s, store.ExerciseFilter{UpdatedSince: &epoch, Limit: 2, Cursor: next})
			rest = append(rest, exerciseNames(items)...)
			if n == "" {
				break
			}
			next = n
		}
		// ex4 was deleted at +30 s, so it is delivered (once, as deleted) at the end.
		want := []string{"ex3", "ex5", "ex6", "new", "ex1 edited", "ex4"}
		if !slices.Equal(rest, want) {
			t.Errorf("remaining pages = %v, want %v", rest, want)
		}
	})
}

func exerciseMustID(t *testing.T, s *store.Exercises, name string) uuid.UUID {
	t.Helper()
	items, _, err := s.List(t.Context(), store.ExerciseFilter{Query: name})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range items {
		if e.Name == name {
			return e.ID
		}
	}
	t.Fatalf("no exercise named %q", name)
	return uuid.Nil
}

func TestExercisesListRejectsBadCursors(t *testing.T) {
	db, s, user := exerciseSetup(t)
	for i := 1; i <= 3; i++ {
		testutil.SeedExercise(t, db, user, testutil.WithExerciseName(fmt.Sprintf("ex%d", i)))
	}
	epoch := time.Unix(0, 0).UTC()
	_, nameCursor := exerciseList(t, s, store.ExerciseFilter{Limit: 1})
	validID := uuid.NewString()

	tests := []struct {
		name   string
		filter store.ExerciseFilter
	}{
		{"garbage", store.ExerciseFilter{Cursor: "not-a-cursor!"}},
		{"garbage in sync mode", store.ExerciseFilter{UpdatedSince: &epoch, Cursor: "not-a-cursor!"}},
		{"a name cursor in sync mode", store.ExerciseFilter{UpdatedSince: &epoch, Cursor: nameCursor}},
		{"three parts", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("a", validID, "c")}},
		{"one part", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("a")}},
		{"NUL in the name part", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("a\x00b", validID)}},
		{"invalid UTF-8 in the name part", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("a\xffb", validID)}},
		{"id that is not a uuid", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("ex1", "nope")}},
		{"upper case uuid", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("ex1", strings.ToUpper(validID))}},
		{"uuid without hyphens", store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("ex1", strings.ReplaceAll(validID, "-", ""))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := s.List(t.Context(), tt.filter)
			var br *domain.BadRequestError
			if !errors.As(err, &br) {
				t.Errorf("err = %v (%T), want a bad request", err, err)
			}
		})
	}

	t.Run("a valid cursor of a name that no longer exists still works", func(t *testing.T) {
		items, _ := exerciseList(t, s, store.ExerciseFilter{Cursor: domain.EncodeKeyCursor("ex15", validID)})
		exerciseRequireNames(t, items, "ex2", "ex3")
	})
}
