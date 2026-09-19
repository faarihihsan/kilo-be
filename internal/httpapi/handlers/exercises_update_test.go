package handlers_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

// specUpdateBody is the request of docs/api/endpoints/18-update-exercise.md.
const specUpdateBody = `{
  "name": "Incline Dumbbell Press",
  "category": "strength",
  "primary_muscle_group": "chest",
  "secondary_muscle_groups": ["shoulders", "triceps"],
  "equipment": "dumbbell",
  "measurement_type": "reps_weight",
  "instructions": "Set bench to 30 degrees, elbows at 45..."
}`

func TestExercisesAPIUpdate(t *testing.T) {
	t.Run("200 with the updated exercise: updated_at is now, created_by and the image are kept", func(t *testing.T) {
		a := exerciseNewAPI(t)
		creator := a.newUser()
		id := testutil.SeedExercise(t, a.deps.DB, creator.user,
			testutil.WithExerciseName("Incline Press"),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtWebP, 1234),
			testutil.WithExerciseCreatedAt(exerciseAPIT0),
			testutil.WithExerciseUpdatedAt(exerciseAPIT0)).String()
		a.clk.Advance(2 * time.Hour)

		// A different user edits: anyone may.
		rec := a.asUser().put(id, specUpdateBody).status(http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		exerciseJSONEqual(t, rec.Body.String(), `{
		  "id": "`+id+`",
		  "name": "Incline Dumbbell Press",
		  "category": "strength",
		  "primary_muscle_group": "chest",
		  "secondary_muscle_groups": ["shoulders", "triceps"],
		  "equipment": "dumbbell",
		  "measurement_type": "reps_weight",
		  "instructions": "Set bench to 30 degrees, elbows at 45...",
		  "image_url": "https://api.example.com/media/exercises/`+id+`/9f2c4e1ab37d05c6.webp",
		  "created_by": "`+creator.user.String()+`",
		  "created_at": "2026-08-01T10:00:00Z",
		  "updated_at": "2026-08-01T12:00:00Z",
		  "deleted_at": null
		}`)
	})

	t.Run("it is a full replace: what the body omits is cleared", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		got := a.asUser().put(id, `{"name":"Squat","category":"strength","primary_muscle_group":"quads","equipment":"barbell","measurement_type":"reps_weight"}`).
			status(http.StatusOK).object()
		if got["instructions"] != nil {
			t.Errorf("instructions = %v, want null", got["instructions"])
		}
		if s, ok := got["secondary_muscle_groups"].([]any); !ok || len(s) != 0 {
			t.Errorf("secondary_muscle_groups = %#v, want []", got["secondary_muscle_groups"])
		}
		if got["primary_muscle_group"] != "quads" {
			t.Errorf("primary_muscle_group = %v", got["primary_muscle_group"])
		}
	})

	t.Run("identical content is a 200 that changes nothing", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.clk.Advance(time.Hour)
		got := a.asUser().put(id, exerciseBody("Squat")).status(http.StatusOK).object()
		if got["updated_at"] != "2026-08-01T10:00:00Z" {
			t.Errorf("updated_at = %v, want it unchanged", got["updated_at"])
		}
		// Whitespace around the name is not a change either.
		got = a.asUser().put(id, exerciseBody("  Squat  ")).status(http.StatusOK).object()
		if got["updated_at"] != "2026-08-01T10:00:00Z" {
			t.Errorf("updated_at = %v after padding the name", got["updated_at"])
		}
	})

	t.Run("a change of case only renames it", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("squat")
		got := a.asUser().put(id, exerciseBody("Squat")).status(http.StatusOK).object()
		if got["name"] != "Squat" {
			t.Errorf("name = %v", got["name"])
		}
	})

	t.Run("a name that only a deleted exercise had is free", func(t *testing.T) {
		a := exerciseNewAPI(t)
		old := a.asUser().create("Alpha")
		id := a.asUser().create("Beta")
		a.asUser().del(old).status(http.StatusNoContent)
		a.asUser().put(id, exerciseBody("Alpha")).status(http.StatusOK)
	})

	t.Run("the edit is in the list and the sync feed", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.clk.Advance(time.Hour)
		a.asUser().put(id, exerciseBody("Back Squat")).status(http.StatusOK)
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Back Squat"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("an upper case id in the path", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asUser().put(strings.ToUpper(id), exerciseBody("Back Squat")).status(http.StatusOK)
	})
}

func TestExercisesAPIUpdateErrors(t *testing.T) {
	const missing = "0195f3a2-bbbb-7000-8000-0000000000ff"

	t.Run("401 without a token", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.anonymous().put(missing, specUpdateBody).errorCode(http.StatusUnauthorized, "unauthorized")
	})

	t.Run("403 for an admin, and nothing changes", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asAdmin().put(id, exerciseBody("Hacked")).errorCode(http.StatusForbidden, "forbidden")
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Squat"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("404 for an unknown id", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().put(missing, specUpdateBody).errorCode(http.StatusNotFound, "not_found")
	})

	t.Run("400 bad_request", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		tests := []struct{ name, id, body string }{
			{"path id is not a uuid", "not-a-uuid", specUpdateBody},
			{"path id without hyphens", strings.ReplaceAll(id, "-", ""), specUpdateBody},
			{"malformed JSON", id, `{"name":`},
			{"empty body", id, ``},
			{"unknown field", id, strings.Replace(specUpdateBody, `"name"`, `"nickname": "x", "name"`, 1)},
			{"id in the body", id, strings.Replace(specUpdateBody, `"name"`, `"id": "`+id+`", "name"`, 1)},
			{"image_url in the body", id, strings.Replace(specUpdateBody, `"name"`, `"image_url": null, "name"`, 1)},
			{"wrong JSON type", id, strings.Replace(specUpdateBody, `"Incline Dumbbell Press"`, `["x"]`, 1)},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a.asUser().put(tt.id, tt.body).errorCode(http.StatusBadRequest, "bad_request")
			})
		}
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Squat"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("415 for another content type", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asUser().doAs(http.MethodPut, "/v1/exercises/"+id, specUpdateBody, "text/plain").
			errorCode(http.StatusUnsupportedMediaType, "unsupported_media_type")
	})

	t.Run("409 already_exists names the exercise that has the name", func(t *testing.T) {
		a := exerciseNewAPI(t)
		alpha := a.asUser().create("Alpha")
		beta := a.asUser().create("Beta")
		body := a.asUser().put(beta, exerciseBody("alpha")).errorCode(http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 {
			t.Fatalf("details = %+v, want one entry", body.Error.Details)
		}
		d := body.Error.Details[0]
		if d.Issue != "already_exists" || d.Field != "name" || d.ExistingID != alpha {
			t.Errorf("details[0] = %+v, want already_exists on name with existing_id %s", d, alpha)
		}
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Alpha", "Beta"}) {
			t.Errorf("names = %v: a failed update must change nothing", got)
		}
	})

	t.Run("409 deleted when the exercise was soft-deleted", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asUser().del(id).status(http.StatusNoContent)
		body := a.asUser().put(id, exerciseBody("Squat")).errorCode(http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "deleted" {
			t.Errorf("details = %+v, want deleted", body.Error.Details)
		}
	})

	t.Run("422 validation_failed lists every issue", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		tests := []struct {
			name string
			body string
			want []string
		}{
			{"nothing valid", `{}`, []string{"name:required", "category:required", "primary_muscle_group:required", "equipment:required", "measurement_type:required"}},
			{"unknown enum values", `{"name":"x","category":"yoga","primary_muscle_group":"wings","equipment":"trampoline","measurement_type":"vibes"}`,
				[]string{"category:invalid_value", "primary_muscle_group:invalid_value", "equipment:invalid_value", "measurement_type:invalid_value"}},
			{"name too long", strings.Replace(specUpdateBody, "Incline Dumbbell Press", strings.Repeat("a", 101), 1), []string{"name:too_long"}},
			{"instructions too long", strings.Replace(specUpdateBody, "Set bench to 30 degrees, elbows at 45...", strings.Repeat("a", 4001), 1), []string{"instructions:too_long"}},
			{"secondary: contains the primary and a duplicate", strings.Replace(specUpdateBody, `["shoulders", "triceps"]`, `["chest","shoulders","shoulders"]`, 1),
				[]string{"secondary_muscle_groups[0]:contains_primary", "secondary_muscle_groups[2]:duplicate"}},
			{"secondary: too many", strings.Replace(specUpdateBody, `["shoulders", "triceps"]`, `["shoulders","triceps","biceps","abs","lats","traps"]`, 1), []string{"secondary_muscle_groups:too_many"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a.asUser().put(id, tt.body).issues(tt.want...)
			})
		}
	})

	t.Run("validation is checked before the exercise is looked up", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().put(missing, `{}`).errorCode(http.StatusUnprocessableEntity, "validation_failed")
	})

	t.Run("a bad path id is a 400 even with an invalid body", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().put("nope", `{}`).errorCode(http.StatusBadRequest, "bad_request")
	})
}

// Two renames to the same name at once: exactly one wins, never a 500.
func TestExercisesAPIUpdateConcurrently(t *testing.T) {
	a := exerciseNewAPI(t)
	const workers = 6
	ids := make([]string, workers)
	for i := range ids {
		ids[i] = a.asUser().create("Original " + string(rune('A'+i)))
	}
	codes := make([]int, workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = a.asUser().put(id, exerciseBody("Contested")).Code
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != workers-1 {
		t.Errorf("status codes = %v, want one 200 and %d 409", codes, workers-1)
	}
}

func TestExercisesAPIDelete(t *testing.T) {
	t.Run("204 with no body; the exercise leaves the list but stays in the feed with all its fields", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := testutil.SeedExercise(t, a.deps.DB, a.user,
			testutil.WithExerciseName("Bench Press"),
			testutil.WithInstructions("Lie on bench..."),
			testutil.WithSecondaryMuscleGroups(domain.MuscleGroupTriceps),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtWebP, 1234),
			testutil.WithExerciseCreatedAt(exerciseAPIT0),
			testutil.WithExerciseUpdatedAt(exerciseAPIT0)).String()
		a.clk.Advance(3 * time.Hour)

		rec := a.asUser().del(id).status(http.StatusNoContent)
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
		if got := a.asUser().get("/v1/exercises").names(); len(got) != 0 {
			t.Errorf("the default list still has %v", got)
		}

		want := `{
		  "id": "` + id + `",
		  "name": "Bench Press",
		  "category": "strength",
		  "primary_muscle_group": "chest",
		  "secondary_muscle_groups": ["triceps"],
		  "equipment": "barbell",
		  "measurement_type": "reps_weight",
		  "instructions": "Lie on bench...",
		  "image_url": "https://api.example.com/media/exercises/` + id + `/9f2c4e1ab37d05c6.webp",
		  "created_by": "` + a.user.String() + `",
		  "created_at": "2026-08-01T10:00:00Z",
		  "updated_at": "2026-08-01T13:00:00Z",
		  "deleted_at": "2026-08-01T13:00:00Z"
		}`
		for _, q := range []string{"include_deleted=true", "updated_since=2026-08-01T10:00:00Z"} {
			items, _ := a.asUser().get("/v1/exercises?" + q).status(http.StatusOK).items()
			if len(items) != 1 {
				t.Fatalf("?%s: %d items, want 1", q, len(items))
			}
			b, _ := json.Marshal(items[0])
			exerciseJSONEqual(t, string(b), want)
		}
	})

	t.Run("deleting again is a 204 that changes nothing", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.clk.Advance(time.Hour)
		a.asUser().del(id).status(http.StatusNoContent)
		a.clk.Advance(time.Hour)
		a.asUser().del(id).status(http.StatusNoContent)

		items, _ := a.asUser().get("/v1/exercises?include_deleted=true").items()
		if len(items) != 1 || items[0]["deleted_at"] != "2026-08-01T11:00:00Z" || items[0]["updated_at"] != "2026-08-01T11:00:00Z" {
			t.Errorf("items = %v, want the first delete's time", items)
		}
	})

	t.Run("any user can delete any exercise", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.newUser().create("Squat")
		a.asUser().del(id).status(http.StatusNoContent)
	})

	t.Run("the name can be used again", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asUser().del(id).status(http.StatusNoContent)
		a.asUser().create("Squat")
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Squat"}) {
			t.Errorf("names = %v", got)
		}
	})

	t.Run("an upper case id in the path", func(t *testing.T) {
		a := exerciseNewAPI(t)
		id := a.asUser().create("Squat")
		a.asUser().del(strings.ToUpper(id)).status(http.StatusNoContent)
	})
}

func TestExercisesAPIDeleteErrors(t *testing.T) {
	a := exerciseNewAPI(t)
	id := a.asUser().create("Squat")

	t.Run("401 without a token", func(t *testing.T) {
		a.anonymous().del(id).errorCode(http.StatusUnauthorized, "unauthorized")
	})
	t.Run("403 for an admin, and nothing is deleted", func(t *testing.T) {
		a.asAdmin().del(id).errorCode(http.StatusForbidden, "forbidden")
		if got := a.asUser().get("/v1/exercises").names(); !slices.Equal(got, []string{"Squat"}) {
			t.Errorf("names = %v", got)
		}
	})
	t.Run("404 for an id that never existed", func(t *testing.T) {
		a.asUser().del("0195f3a2-bbbb-7000-8000-0000000000ff").errorCode(http.StatusNotFound, "not_found")
	})
	t.Run("400 for a bad path id", func(t *testing.T) {
		a.asUser().del("not-a-uuid").errorCode(http.StatusBadRequest, "bad_request")
	})
}
