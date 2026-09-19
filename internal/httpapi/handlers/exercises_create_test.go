package handlers_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

// specCreateBody is the request of docs/api/endpoints/09-create-exercise.md.
const specCreateBody = `{
  "id": "0195f3a2-bbbb-7000-8000-000000000011",
  "name": "Incline Dumbbell Press",
  "category": "strength",
  "primary_muscle_group": "chest",
  "secondary_muscle_groups": ["shoulders", "triceps"],
  "equipment": "dumbbell",
  "measurement_type": "reps_weight",
  "instructions": "Set bench to 30 degrees..."
}`

func TestExercisesAPICreate(t *testing.T) {
	t.Run("201 with the exercise in the shape of the list items", func(t *testing.T) {
		a := exerciseNewAPI(t)
		rec := a.asUser().post(specCreateBody).status(http.StatusCreated)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		exerciseJSONEqual(t, rec.Body.String(), `{
		  "id": "0195f3a2-bbbb-7000-8000-000000000011",
		  "name": "Incline Dumbbell Press",
		  "category": "strength",
		  "primary_muscle_group": "chest",
		  "secondary_muscle_groups": ["shoulders", "triceps"],
		  "equipment": "dumbbell",
		  "measurement_type": "reps_weight",
		  "instructions": "Set bench to 30 degrees...",
		  "image_url": null,
		  "created_by": "`+a.user.String()+`",
		  "created_at": "2026-08-01T10:00:00Z",
		  "updated_at": "2026-08-01T10:00:00Z",
		  "deleted_at": null
		}`)
	})

	t.Run("only the required fields: instructions null, secondary empty", func(t *testing.T) {
		a := exerciseNewAPI(t)
		rec := a.asUser().post(`{"name":"Plank","category":"mobility","primary_muscle_group":"abs","equipment":"bodyweight","measurement_type":"duration"}`).
			status(http.StatusCreated)
		got := rec.object()
		if got["instructions"] != nil {
			t.Errorf("instructions = %v, want null", got["instructions"])
		}
		if s, ok := got["secondary_muscle_groups"].([]any); !ok || len(s) != 0 {
			t.Errorf("secondary_muscle_groups = %#v, want []", got["secondary_muscle_groups"])
		}
	})

	t.Run("server generates a UUID v7 when there is no id, and trims the name", func(t *testing.T) {
		a := exerciseNewAPI(t)
		got := a.asUser().post(exerciseBody("   Bench Press \t")).status(http.StatusCreated).object()
		id, err := uuid.Parse(got["id"].(string))
		if err != nil || id.Version() != 7 {
			t.Errorf("id = %v (%v), want a UUID v7", got["id"], err)
		}
		if got["name"] != "Bench Press" {
			t.Errorf("name = %q, want it trimmed", got["name"])
		}
	})

	t.Run("an upper case client id comes back lower case", func(t *testing.T) {
		a := exerciseNewAPI(t)
		body := strings.Replace(specCreateBody, "0195f3a2-bbbb-7000-8000-000000000011", "0195F3A2-BBBB-7000-8000-00000000AAAA", 1)
		got := a.asUser().post(body).status(http.StatusCreated).object()
		if got["id"] != "0195f3a2-bbbb-7000-8000-00000000aaaa" {
			t.Errorf("id = %v", got["id"])
		}
	})

	t.Run("created_by is the caller", func(t *testing.T) {
		a := exerciseNewAPI(t)
		other := a.newUser()
		mine := a.asUser().post(exerciseBody("Mine")).status(http.StatusCreated).object()
		theirs := other.post(exerciseBody("Theirs")).status(http.StatusCreated).object()
		if mine["created_by"] != a.user.String() || theirs["created_by"] != other.user.String() {
			t.Errorf("created_by = %v and %v, want %v and %v", mine["created_by"], theirs["created_by"], a.user, other.user)
		}
	})

	t.Run("the created exercise is in the list", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().post(specCreateBody).status(http.StatusCreated)
		names := a.asUser().get("/v1/exercises").status(http.StatusOK).names()
		if len(names) != 1 || names[0] != "Incline Dumbbell Press" {
			t.Errorf("names = %v", names)
		}
	})

	t.Run("200 for an idempotent retry, even from another user, without changes", func(t *testing.T) {
		a := exerciseNewAPI(t)
		first := a.asUser().post(specCreateBody).status(http.StatusCreated)
		a.clk.Advance(time.Hour)
		retry := a.newUser().post(specCreateBody).status(http.StatusOK)
		exerciseJSONEqual(t, retry.Body.String(), first.Body.String())
	})

	t.Run("a deleted name is free again", func(t *testing.T) {
		a := exerciseNewAPI(t)
		old := a.asUser().create("Bench Press")
		a.asUser().del(old).status(http.StatusNoContent)
		fresh := a.asUser().post(exerciseBody("bench press")).status(http.StatusCreated).object()
		if fresh["id"] == old {
			t.Error("the new exercise reused the old id")
		}
	})
}

func TestExercisesAPICreateErrors(t *testing.T) {
	t.Run("401 without a token", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.anonymous().post(specCreateBody).errorCode(http.StatusUnauthorized, "unauthorized")
	})

	t.Run("403 for an admin, and nothing is created", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asAdmin().post(specCreateBody).errorCode(http.StatusForbidden, "forbidden")
		if names := a.asUser().get("/v1/exercises").names(); len(names) != 0 {
			t.Errorf("an admin created %v", names)
		}
	})

	t.Run("400 bad_request", func(t *testing.T) {
		a := exerciseNewAPI(t)
		tests := []struct{ name, body string }{
			{"malformed JSON", `{"name": "x"`},
			{"not JSON", `name=x`},
			{"empty body", ``},
			{"a JSON array", `[]`},
			{"unknown field", strings.Replace(specCreateBody, `"instructions"`, `"nickname": "x", "instructions"`, 1)},
			{"image_url is not accepted", strings.Replace(specCreateBody, `"instructions"`, `"image_url": null, "instructions"`, 1)},
			{"created_by is not accepted", strings.Replace(specCreateBody, `"instructions"`, `"created_by": "`+uuid.NewString()+`", "instructions"`, 1)},
			{"wrong JSON type", strings.Replace(specCreateBody, `"Incline Dumbbell Press"`, `5`, 1)},
			{"secondary muscle groups not an array", strings.Replace(specCreateBody, `["shoulders", "triceps"]`, `"shoulders"`, 1)},
			{"two JSON values", specCreateBody + specCreateBody},
			{"trailing garbage", specCreateBody + `x`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a.asUser().post(tt.body).errorCode(http.StatusBadRequest, "bad_request")
			})
		}
		if names := a.asUser().get("/v1/exercises").names(); len(names) != 0 {
			t.Errorf("a rejected request created %v", names)
		}
	})

	t.Run("415 for another content type", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().doAs(http.MethodPost, "/v1/exercises", specCreateBody, "text/plain").
			errorCode(http.StatusUnsupportedMediaType, "unsupported_media_type")
	})

	t.Run("413 over 1 MiB", func(t *testing.T) {
		a := exerciseNewAPI(t)
		body := `{"name":"` + strings.Repeat("a", domain.MaxBodyBytes) + `"}`
		a.asUser().post(body).errorCode(http.StatusRequestEntityTooLarge, "payload_too_large")
	})

	t.Run("409 already_exists names the other exercise", func(t *testing.T) {
		a := exerciseNewAPI(t)
		first := a.asUser().create("Bench Press")
		body := a.asUser().post(exerciseBody("  BENCH press ")).errorCode(http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 {
			t.Fatalf("details = %+v, want one entry", body.Error.Details)
		}
		d := body.Error.Details[0]
		if d.Issue != "already_exists" || d.Field != "name" || d.ExistingID != first {
			t.Errorf("details[0] = %+v, want already_exists on name with existing_id %s", d, first)
		}
	})

	t.Run("409 id_taken when the id exists with different content", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().post(specCreateBody).status(http.StatusCreated)
		changed := strings.Replace(specCreateBody, `"dumbbell"`, `"barbell"`, 1)
		body := a.asUser().post(changed).errorCode(http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "id_taken" || body.Error.Details[0].ExistingID != "" {
			t.Errorf("details = %+v, want id_taken", body.Error.Details)
		}
	})

	t.Run("409 deleted when the id was soft-deleted", func(t *testing.T) {
		a := exerciseNewAPI(t)
		a.asUser().post(specCreateBody).status(http.StatusCreated)
		a.asUser().del("0195f3a2-bbbb-7000-8000-000000000011").status(http.StatusNoContent)
		body := a.asUser().post(specCreateBody).errorCode(http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "deleted" {
			t.Errorf("details = %+v, want deleted", body.Error.Details)
		}
	})

	t.Run("422 validation_failed lists every issue", func(t *testing.T) {
		a := exerciseNewAPI(t)
		tests := []struct {
			name string
			body string
			want []string
		}{
			{"nothing valid", `{}`, []string{"name:required", "category:required", "primary_muscle_group:required", "equipment:required", "measurement_type:required"}},
			{"unknown enum values", `{"name":"x","category":"yoga","primary_muscle_group":"wings","equipment":"trampoline","measurement_type":"vibes"}`,
				[]string{"category:invalid_value", "primary_muscle_group:invalid_value", "equipment:invalid_value", "measurement_type:invalid_value"}},
			{"name too long", strings.Replace(specCreateBody, "Incline Dumbbell Press", strings.Repeat("a", 101), 1), []string{"name:too_long"}},
			{"blank name", strings.Replace(specCreateBody, "Incline Dumbbell Press", "   ", 1), []string{"name:required"}},
			{"name with NUL", strings.Replace(specCreateBody, "Incline Dumbbell Press", `a\u0000b`, 1), []string{"name:invalid_chars"}},
			{"instructions too long", strings.Replace(specCreateBody, "Set bench to 30 degrees...", strings.Repeat("a", 4001), 1), []string{"instructions:too_long"}},
			{"instructions with NUL", strings.Replace(specCreateBody, "Set bench to 30 degrees...", `a\u0000`, 1), []string{"instructions:invalid_chars"}},
			{"secondary: too many", strings.Replace(specCreateBody, `["shoulders", "triceps"]`, `["shoulders","triceps","biceps","abs","lats","traps"]`, 1), []string{"secondary_muscle_groups:too_many"}},
			{"secondary: duplicate", strings.Replace(specCreateBody, `["shoulders", "triceps"]`, `["shoulders","shoulders"]`, 1), []string{"secondary_muscle_groups[1]:duplicate"}},
			{"secondary: contains the primary", strings.Replace(specCreateBody, `["shoulders", "triceps"]`, `["shoulders","chest"]`, 1), []string{"secondary_muscle_groups[1]:contains_primary"}},
			{"secondary: unknown value", strings.Replace(specCreateBody, `["shoulders", "triceps"]`, `["shoulders","wings"]`, 1), []string{"secondary_muscle_groups[1]:invalid_value"}},
			{"id that is not a uuid", strings.Replace(specCreateBody, "0195f3a2-bbbb-7000-8000-000000000011", "bench-press", 1), []string{"id:invalid_format"}},
			{"the nil uuid", strings.Replace(specCreateBody, "0195f3a2-bbbb-7000-8000-000000000011", uuid.Nil.String(), 1), []string{"id:invalid_format"}},
			{"several problems at once", `{"id":"x","name":"","category":"yoga","primary_muscle_group":"chest","secondary_muscle_groups":["chest"],"equipment":"barbell","measurement_type":"reps"}`,
				[]string{"id:invalid_format", "name:required", "category:invalid_value", "secondary_muscle_groups[0]:contains_primary"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				a.asUser().post(tt.body).issues(tt.want...)
			})
		}
		if names := a.asUser().get("/v1/exercises").names(); len(names) != 0 {
			t.Errorf("a rejected request created %v", names)
		}
	})

	t.Run("boundaries are accepted", func(t *testing.T) {
		a := exerciseNewAPI(t)
		name := strings.Repeat("é", 100)
		body := strings.Replace(specCreateBody, "Incline Dumbbell Press", name, 1)
		body = strings.Replace(body, "Set bench to 30 degrees...", strings.Repeat("é", 4000), 1)
		body = strings.Replace(body, `["shoulders", "triceps"]`, `["shoulders","triceps","biceps","abs","lats"]`, 1)
		a.asUser().post(body).status(http.StatusCreated)
	})
}

// Two phones creating the same exercise at once: one creates it, the other is
// told (never a 500).
func TestExercisesAPICreateConcurrently(t *testing.T) {
	const workers = 10

	t.Run("same name, different ids", func(t *testing.T) {
		a := exerciseNewAPI(t)
		codes := make([]int, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = a.asUser().post(exerciseBody("Deadlift")).Code
			}()
		}
		wg.Wait()
		created, conflicts := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflicts++
			}
		}
		if created != 1 || conflicts != workers-1 {
			t.Errorf("status codes = %v, want one 201 and %d 409", codes, workers-1)
		}
	})

	t.Run("same id and content", func(t *testing.T) {
		a := exerciseNewAPI(t)
		codes := make([]int, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = a.asUser().post(specCreateBody).Code
			}()
		}
		wg.Wait()
		created, ok := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusOK:
				ok++
			}
		}
		if created != 1 || ok != workers-1 {
			t.Errorf("status codes = %v, want one 201 and %d 200", codes, workers-1)
		}
	})
}
