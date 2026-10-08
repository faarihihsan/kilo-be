package handlers_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

// seed inserts an exercise straight into the database, created by the
// API's default user, and returns its id.
func (a *exerciseAPI) seed(name string, opts ...testutil.ExerciseOption) uuid.UUID {
	a.t.Helper()
	opts = append([]testutil.ExerciseOption{testutil.WithExerciseName(name)}, opts...)
	return testutil.SeedExercise(a.t, a.deps.DB, a.user, opts...)
}

// exerciseQuery builds a query string from key/value pairs.
func exerciseQuery(kv ...string) string {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return "/v1/exercises?" + v.Encode()
}

// TestExercisesAPIListGolden compares the response with the example of
// docs/api/endpoints/08-get-list-exercise.md.
func TestExercisesAPIListGolden(t *testing.T) {
	a := exerciseNewAPI(t)
	creator := uuid.MustParse("0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90")
	a.exec(`INSERT INTO users (id, username, password_hash, role) VALUES ($1, 'golden', $2, 'user')`, creator, testutil.DummyPasswordHash)
	testutil.SeedExercise(t, a.deps.DB, creator,
		testutil.WithExerciseID(uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010")),
		testutil.WithExerciseName("Bench Press"),
		testutil.WithSecondaryMuscleGroups(domain.MuscleGroupTriceps, domain.MuscleGroupShoulders),
		testutil.WithInstructions("Lie on bench..."),
		testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtWebP, 1234),
		testutil.WithExerciseCreatedAt(exerciseAPIT0),
		testutil.WithExerciseUpdatedAt(exerciseAPIT0))

	rec := a.asUser().get("/v1/exercises").status(http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	exerciseJSONEqual(t, rec.Body.String(), `{
	  "items": [
	    {
	      "id": "0195f3a2-bbbb-7000-8000-000000000010",
	      "name": "Bench Press",
	      "category": "strength",
	      "primary_muscle_group": "chest",
	      "secondary_muscle_groups": ["triceps", "shoulders"],
	      "equipment": "barbell",
	      "measurement_type": "reps_weight",
	      "instructions": "Lie on bench...",
	      "image_url": "https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp",
	      "created_by": "0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90",
	      "created_at": "2026-08-01T10:00:00Z",
	      "updated_at": "2026-08-01T10:00:00Z",
	      "deleted_at": null
	    }
	  ],
	  "next_cursor": null
	}`)
}

func TestExercisesAPIList(t *testing.T) {
	t.Run("an empty catalog is an empty array", func(t *testing.T) {
		a := exerciseNewAPI(t)
		exerciseJSONEqual(t, a.asUser().get("/v1/exercises").status(http.StatusOK).Body.String(), `{"items":[],"next_cursor":null}`)
	})

	t.Run("any user sees every exercise, whoever created it", func(t *testing.T) {
		a := exerciseNewAPI(t)
		other := a.newUser()
		other.create("Made by someone else")
		a.asUser().create("Made by me")
		if got := a.asUser().get("/v1/exercises").names(); len(got) != 2 {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("case-insensitive name order, deleted hidden by default", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("cherry")
		a.seed("Banana")
		a.seed("apple")
		a.seed("Date", testutil.WithExerciseDeletedAt(exerciseAPIT0))
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"apple", "Banana", "cherry"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("include_deleted=true lists deleted rows with all their fields", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("Live")
		a.seed("Old", testutil.WithInstructions("Keep me."), testutil.WithExerciseDeletedAt(exerciseAPIT0),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtPNG, 1))
		items, _ := a.asUser().get("/v1/exercises?include_deleted=true").status(http.StatusOK).items()
		if len(items) != 2 || items[0]["name"] != "Live" || items[1]["name"] != "Old" {
			t.Fatalf("items = %v", items)
		}
		old := items[1]
		if old["deleted_at"] != "2026-08-01T10:00:00Z" || old["instructions"] != "Keep me." || old["image_url"] == nil {
			t.Errorf("deleted item = %v, want deleted_at set and every field kept", old)
		}
		if items[0]["deleted_at"] != nil {
			t.Errorf("live item deleted_at = %v", items[0]["deleted_at"])
		}
	})

	t.Run("include_deleted=false is the default", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("Live")
		a.seed("Old", testutil.WithExerciseDeletedAt(exerciseAPIT0))
		if got := a.asUser().get("/v1/exercises?include_deleted=false").names(); !slices.Equal(got, []string{"Live"}) {
			t.Errorf("names = %v", got)
		}
	})
}

func TestExercisesAPIListFilters(t *testing.T) {
	a := exerciseNewAPI(t)
	a.seed("Bench Press", testutil.WithPrimaryMuscleGroup(domain.MuscleGroupChest), testutil.WithEquipment(domain.EquipmentBarbell))
	a.seed("Incline Bench", testutil.WithPrimaryMuscleGroup(domain.MuscleGroupChest), testutil.WithEquipment(domain.EquipmentDumbbell))
	a.seed("Pull Up", testutil.WithPrimaryMuscleGroup(domain.MuscleGroupLats), testutil.WithEquipment(domain.EquipmentBodyweight))
	a.seed("Running", testutil.WithExerciseCategory(domain.CategoryCardio), testutil.WithPrimaryMuscleGroup(domain.MuscleGroupFullBody),
		testutil.WithEquipment(domain.EquipmentCardioMachine), testutil.WithMeasurementType(domain.MeasurementTypeDistanceDuration))
	a.seed("100% Effort", testutil.WithPrimaryMuscleGroup(domain.MuscleGroupFullBody))
	a.seed("Old Bench", testutil.WithPrimaryMuscleGroup(domain.MuscleGroupChest), testutil.WithExerciseDeletedAt(exerciseAPIT0))

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"category", exerciseQuery("category", "cardio"), []string{"Running"}},
		{"primary_muscle_group", exerciseQuery("primary_muscle_group", "chest"), []string{"Bench Press", "Incline Bench"}},
		{"equipment", exerciseQuery("equipment", "bodyweight"), []string{"Pull Up"}},
		{"filters combine", exerciseQuery("primary_muscle_group", "chest", "equipment", "dumbbell"), []string{"Incline Bench"}},
		{"q is a case-insensitive substring", exerciseQuery("q", "BENCH"), []string{"Bench Press", "Incline Bench"}},
		{"q in the middle of a word", exerciseQuery("q", "ncline"), []string{"Incline Bench"}},
		{"q with a space", exerciseQuery("q", "ch Pre"), []string{"Bench Press"}},
		{"q is trimmed", exerciseQuery("q", "  running "), []string{"Running"}},
		{"q percent is literal", exerciseQuery("q", "%"), []string{"100% Effort"}},
		{"q underscore is literal", exerciseQuery("q", "_"), []string{}},
		{"q combines with filters", exerciseQuery("q", "bench", "equipment", "barbell"), []string{"Bench Press"}},
		{"empty q is no search", exerciseQuery("q", ""), []string{"100% Effort", "Bench Press", "Incline Bench", "Pull Up", "Running"}},
		{"q does not find deleted rows", exerciseQuery("q", "old bench"), []string{}},
		{"no match", exerciseQuery("q", "deadlift"), []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.asUser().get(tt.query).status(http.StatusOK).names()
			slices.Sort(got)
			want := slices.Sorted(slices.Values(tt.want))
			if !slices.Equal(got, want) {
				t.Errorf("names = %v, want %v", got, want)
			}
		})
	}

	t.Run("include_deleted applies to a search too", func(t *testing.T) {
		got := a.asUser().get(exerciseQuery("q", "old bench", "include_deleted", "true")).names()
		if !slices.Equal(got, []string{"Old Bench"}) {
			t.Errorf("names = %v", got)
		}
	})
}

func TestExercisesAPIListSyncFeed(t *testing.T) {
	at := func(sec int) testutil.ExerciseOption {
		return testutil.WithExerciseUpdatedAt(exerciseAPIT0.Add(time.Duration(sec) * time.Second))
	}

	t.Run("updated_since includes soft-deleted rows, strictly after, ordered by updated_at", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("Third", at(3))
		a.seed("First", at(1))
		a.seed("Gone", at(2), testutil.WithExerciseDeletedAt(exerciseAPIT0.Add(2*time.Second)))
		a.seed("Fourth", at(4))

		since := exerciseAPIT0.Add(time.Second).Format(time.RFC3339)
		items, next := a.asUser().get(exerciseQuery("updated_since", since)).status(http.StatusOK).items()
		var names []string
		for _, it := range items {
			names = append(names, it["name"].(string))
		}
		if !slices.Equal(names, []string{"Gone", "Third", "Fourth"}) {
			t.Errorf("names = %v, want [Gone Third Fourth] (First is not after %s)", names, since)
		}
		if items[0]["deleted_at"] == nil {
			t.Error("the soft-deleted row has no deleted_at")
		}
		if next != nil {
			t.Errorf("next_cursor = %v", next)
		}
	})

	t.Run("include_deleted=false does not hide deleted rows in sync mode", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("Gone", at(2), testutil.WithExerciseDeletedAt(exerciseAPIT0))
		got := a.asUser().get("/v1/exercises?updated_since=1970-01-01T00:00:00Z&include_deleted=false").status(http.StatusOK).names()
		if !slices.Equal(got, []string{"Gone"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("an offset in the timestamp", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("One", at(1))
		// 12:00:00+02:00 is 10:00:00Z, before the row.
		got := a.asUser().get(exerciseQuery("updated_since", "2026-08-01T12:00:00+02:00")).status(http.StatusOK).names()
		if !slices.Equal(got, []string{"One"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("a bare date is midnight UTC of that day", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.seed("One", at(1)) // 2026-08-01T10:00:01Z
		got := a.asUser().get(exerciseQuery("updated_since", "2026-08-01")).status(http.StatusOK).names()
		if !slices.Equal(got, []string{"One"}) {
			t.Errorf("since the day itself: names = %v, want [One]", got)
		}
		got = a.asUser().get(exerciseQuery("updated_since", "2026-08-02")).status(http.StatusOK).names()
		if len(got) != 0 {
			t.Errorf("since the next day: names = %v, want none", got)
		}
	})

	t.Run("edits and deletes reach the feed", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		since := exerciseAPIT0.Format(time.RFC3339)
		feed := func() []map[string]any {
			items, _ := a.asUser().get(exerciseQuery("updated_since", since)).status(http.StatusOK).items()
			return items
		}
		if got := feed(); len(got) != 0 {
			t.Fatalf("nothing changed since creation, feed = %v", got)
		}

		a.clk.Advance(time.Hour)
		a.asUser().put(id, exerciseBody("Back Squat")).status(http.StatusOK)
		got := feed()
		if len(got) != 1 || got[0]["name"] != "Back Squat" || got[0]["updated_at"] != "2026-08-01T11:00:00Z" {
			t.Fatalf("after an edit, feed = %v", got)
		}
		since = got[0]["updated_at"].(string)
		if got := feed(); len(got) != 0 {
			t.Fatalf("feed after catching up = %v", got)
		}

		a.clk.Advance(time.Hour)
		a.asUser().del(id).status(http.StatusNoContent)
		got = feed()
		if len(got) != 1 || got[0]["deleted_at"] != "2026-08-01T12:00:00Z" || got[0]["name"] != "Back Squat" {
			t.Fatalf("after a delete, feed = %v", got)
		}
	})

	t.Run("follow next_cursor to pull everything, then poll with the max updated_at", func(t *testing.T) {
		a := exerciseNewAPI(t)
		for i := 1; i <= 5; i++ {
			a.seed(fmt.Sprintf("ex%d", i), at(i))
		}
		var (
			names     []string
			maxUpdate string
			cursor    string
			pages     int
		)
		for {
			q := []string{"updated_since", "1970-01-01T00:00:00Z", "limit", "2"}
			if cursor != "" {
				q = append(q, "cursor", cursor)
			}
			items, next := a.asUser().get(exerciseQuery(q...)).status(http.StatusOK).items()
			pages++
			for _, it := range items {
				names = append(names, it["name"].(string))
				maxUpdate = it["updated_at"].(string)
			}
			if next == nil {
				break
			}
			cursor = next.(string)
			if pages > 10 {
				t.Fatal("next_cursor never ended")
			}
		}
		if !slices.Equal(names, []string{"ex1", "ex2", "ex3", "ex4", "ex5"}) || pages != 3 {
			t.Errorf("pulled %v in %d pages", names, pages)
		}

		if got := a.asUser().get(exerciseQuery("updated_since", maxUpdate)).names(); len(got) != 0 {
			t.Errorf("polling with the max updated_at returned %v", got)
		}
		a.clk.Advance(24 * time.Hour)
		a.asUser().create("New one")
		if got := a.asUser().get(exerciseQuery("updated_since", maxUpdate)).names(); !slices.Equal(got, []string{"New one"}) {
			t.Errorf("after a new exercise, polling returned %v", got)
		}
	})
}

func TestExercisesAPIListPagination(t *testing.T) {
	a := exerciseNewAPI(t)
	for i := 1; i <= 9; i++ {
		a.seed(fmt.Sprintf("ex%d", i))
	}

	collect := func(query ...string) (pages [][]string, cursors []any) {
		cursor := ""
		for range 10 {
			q := append([]string{"limit", "3"}, query...)
			if cursor != "" {
				q = append(q, "cursor", cursor)
			}
			rec := a.asUser().get(exerciseQuery(q...)).status(http.StatusOK)
			pages = append(pages, rec.names())
			_, next := rec.items()
			cursors = append(cursors, next)
			if next == nil {
				return
			}
			cursor = next.(string)
		}
		t.Fatal("next_cursor never ended")
		return
	}

	t.Run("pages of three, the last page has a null cursor", func(t *testing.T) {
		pages, cursors := collect()
		want := fmt.Sprint([][]string{{"ex1", "ex2", "ex3"}, {"ex4", "ex5", "ex6"}, {"ex7", "ex8", "ex9"}})
		if fmt.Sprint(pages) != want {
			t.Errorf("pages = %v, want %s", pages, want)
		}
		if cursors[0] == nil || cursors[1] == nil || cursors[2] != nil {
			t.Errorf("cursors = %v, want strings then null", cursors)
		}
	})

	t.Run("a row inserted between two requests does not shift the next page", func(t *testing.T) {
		a := exerciseNewAPI(t)
		for i := 1; i <= 6; i++ {
			a.seed(fmt.Sprintf("ex%d", i))
		}
		first := a.asUser().get("/v1/exercises?limit=3").status(http.StatusOK)
		_, next := first.items()

		// Rows before and after the cursor position, and a delete ahead of it.
		a.seed("ex0")
		a.seed("ex25")
		a.seed("ex45")
		ex5, _ := a.asUser().get("/v1/exercises?q=ex5").status(http.StatusOK).items()
		a.asUser().del(ex5[0]["id"].(string)).status(http.StatusNoContent)

		second := a.asUser().get(exerciseQuery("limit", "10", "cursor", next.(string))).status(http.StatusOK)
		if got := second.names(); !slices.Equal(got, []string{"ex4", "ex45", "ex6"}) {
			t.Errorf("second page = %v, want [ex4 ex45 ex6]", got)
		}
	})
}

func TestExercisesAPIListLimit(t *testing.T) {
	a := exerciseNewAPI(t)
	a.seed("one")
	a.seed("two")
	for _, tt := range []struct {
		limit string
		want  int
	}{{"1", 1}, {"2", 2}, {"200", 2}} {
		if got := a.asUser().get("/v1/exercises?limit=" + tt.limit).status(http.StatusOK).names(); len(got) != tt.want {
			t.Errorf("limit=%s returned %d items, want %d", tt.limit, len(got), tt.want)
		}
	}
}

func TestExercisesAPIListErrors(t *testing.T) {
	a := exerciseNewAPI(t)
	a.seed("ex1")

	t.Run("401 without a token", func(t *testing.T) {
		a.anonymous().get("/v1/exercises").errorCode(http.StatusUnauthorized, "unauthorized")
	})
	t.Run("403 for an admin", func(t *testing.T) {
		a.asAdmin().get("/v1/exercises").errorCode(http.StatusForbidden, "forbidden")
		a.asAdmin().get("/v1/exercises?updated_since=1970-01-01T00:00:00Z").errorCode(http.StatusForbidden, "forbidden")
	})

	nulCursor := domain.EncodeKeyCursor("a\x00b", uuid.NewString())
	badUTF8Cursor := domain.EncodeKeyCursor("a\xffb", uuid.NewString())
	for _, tt := range []struct{ name, query string }{
		{"a cursor that is not base64", exerciseQuery("cursor", "not a cursor!")},
		{"a cursor of the wrong shape", exerciseQuery("cursor", domain.EncodeKeyCursor("only-one-part"))},
		{"a cursor with a NUL", exerciseQuery("cursor", nulCursor)},
		{"a cursor with invalid UTF-8", exerciseQuery("cursor", badUTF8Cursor)},
		{"a cursor with a bad id", exerciseQuery("cursor", domain.EncodeKeyCursor("ex1", "nope"))},
		{"a name cursor in sync mode", exerciseQuery("updated_since", "1970-01-01T00:00:00Z", "cursor", domain.EncodeKeyCursor("ex1", uuid.NewString()))},
		{"updated_since: not a timestamp", exerciseQuery("updated_since", "yesterday")},
		{"updated_since: an impossible date", exerciseQuery("updated_since", "2026-02-30")},
		{"updated_since: empty", exerciseQuery("updated_since", "")},
		{"updated_since: a plus that became a space", "/v1/exercises?updated_since=2026-08-01T12:00:00+02:00"},
	} {
		t.Run("400 "+tt.name, func(t *testing.T) {
			a.asUser().get(tt.query).errorCode(http.StatusBadRequest, "bad_request")
		})
	}

	for _, tt := range []struct {
		name  string
		query string
		want  []string
	}{
		{"limit 0", exerciseQuery("limit", "0"), []string{"limit:out_of_range"}},
		{"limit 201", exerciseQuery("limit", "201"), []string{"limit:out_of_range"}},
		{"negative limit", exerciseQuery("limit", "-5"), []string{"limit:out_of_range"}},
		{"limit not a number", exerciseQuery("limit", "abc"), []string{"limit:invalid_format"}},
		{"empty limit", exerciseQuery("limit", ""), []string{"limit:invalid_format"}},
		{"include_deleted not a bool", exerciseQuery("include_deleted", "maybe"), []string{"include_deleted:invalid_format"}},
		{"include_deleted 1", exerciseQuery("include_deleted", "1"), []string{"include_deleted:invalid_format"}},
		{"unknown category", exerciseQuery("category", "yoga"), []string{"category:invalid_value"}},
		{"category in the wrong case", exerciseQuery("category", "Strength"), []string{"category:invalid_value"}},
		{"empty category", exerciseQuery("category", ""), []string{"category:invalid_value"}},
		{"unknown muscle group", exerciseQuery("primary_muscle_group", "wings"), []string{"primary_muscle_group:invalid_value"}},
		{"unknown equipment", exerciseQuery("equipment", "trampoline"), []string{"equipment:invalid_value"}},
		{"every filter invalid", exerciseQuery("category", "x", "primary_muscle_group", "y", "equipment", "z"),
			[]string{"category:invalid_value", "primary_muscle_group:invalid_value", "equipment:invalid_value"}},
		{"q too long", exerciseQuery("q", strings.Repeat("a", 101)), []string{"q:too_long"}},
		{"q with a NUL", "/v1/exercises?q=a%00b", []string{"q:invalid_chars"}},
		{"q with invalid UTF-8", "/v1/exercises?q=a%ffb", []string{"q:invalid_chars"}},
	} {
		t.Run("422 "+tt.name, func(t *testing.T) {
			a.asUser().get(tt.query).issues(tt.want...)
		})
	}

	t.Run("extreme timestamps are answered, not a 500", func(t *testing.T) {
		for _, since := range []string{"0000-01-01T00:00:00Z", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59Z", "1969-12-31T23:59:59.999999999Z"} {
			a.asUser().get(exerciseQuery("updated_since", since)).status(http.StatusOK)
		}
	})
	t.Run("unknown query parameters are ignored", func(t *testing.T) {
		a.asUser().get("/v1/exercises?colour=red").status(http.StatusOK)
	})
	t.Run("a 400 wins over a 422 for updated_since", func(t *testing.T) {
		a.asUser().get(exerciseQuery("updated_since", "x", "limit", "0")).errorCode(http.StatusBadRequest, "bad_request")
	})
}
