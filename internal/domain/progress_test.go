package domain

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// progressSpecRequest is the request example of docs/api/endpoints/03-save-progress.md.
const progressSpecRequest = `{
  "workout_plan_id": "0195f3a2-aaaa-7000-8000-000000000001",
  "name": "Push Day A",
  "notes": "Felt strong",
  "started_at": "2026-09-19T08:00:00Z",
  "ended_at": "2026-09-19T08:55:00Z",
  "duration_seconds": 3120,
  "updated_at": "2026-09-19T08:55:02Z",
  "exercises": [
    {
      "exercise_id": "0195f3a2-bbbb-7000-8000-000000000010",
      "position": 0,
      "notes": null,
      "sets": [
        {"position": 0, "type": "warmup", "reps": 12, "weight": 40.0, "duration_seconds": null,
         "distance_meters": null, "rpe": null, "completed": true},
        {"position": 1, "type": "normal", "reps": 8, "weight": 80.0, "duration_seconds": null,
         "distance_meters": null, "rpe": 8.5, "completed": true}
      ]
    }
  ]
}`

// progressNow is the server time of the validation tests, a little after the
// spec example's updated_at.
var progressNow = time.Date(2026, 9, 19, 8, 56, 0, 0, time.UTC)

// progressDecode decodes a request the way the handler does: strictly.
func progressDecode(t testing.TB, body string) ProgressSaveRequest {
	t.Helper()
	var r ProgressSaveRequest
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

func progressValidRequest(t testing.TB) ProgressSaveRequest {
	t.Helper()
	return progressDecode(t, progressSpecRequest)
}

// progressIssues returns the issues of err as "field: issue" strings.
func progressIssues(t testing.TB, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *ValidationError", err)
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Field + ": " + is.Issue
	}
	return out
}

func progressPtr[T any](v T) *T { return &v }

func progressDec(s string) *ProgressDecimal { return progressPtr(ProgressDecimal(s)) }

func TestProgressValidateSpecExample(t *testing.T) {
	req := progressValidRequest(t)
	got, err := req.Validate(progressNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	plan := uuid.MustParse("0195f3a2-aaaa-7000-8000-000000000001")
	if got.WorkoutPlanID == nil || *got.WorkoutPlanID != plan || got.Name != "Push Day A" || *got.Notes != "Felt strong" {
		t.Errorf("header = %+v", got)
	}
	if want := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC); !got.StartedAt.Equal(want) || got.StartedAt.Location() != time.UTC {
		t.Errorf("StartedAt = %v", got.StartedAt)
	}
	if want := time.Date(2026, 9, 19, 8, 55, 2, 0, time.UTC); !got.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, want)
	}
	if got.DurationSeconds != 3120 || len(got.Exercises) != 1 || len(got.Exercises[0].Sets) != 2 {
		t.Fatalf("session = %+v", got)
	}
	s := got.Exercises[0].Sets[1]
	if s.Type != SetTypeNormal || *s.Reps != 8 || *s.Weight != "80" || *s.RPE != "8.5" || !s.Completed || s.DistanceMeters != nil {
		t.Errorf("set = %+v", s)
	}
}

func TestProgressValidateOptionalFieldsMayBeNullOrAbsent(t *testing.T) {
	req := progressDecode(t, `{"name":"x","started_at":"2026-09-19T08:00:00Z","ended_at":"2026-09-19T08:00:00Z",
		"duration_seconds":0,"updated_at":"2026-09-19T08:00:00Z","exercises":[
		{"exercise_id":"0195f3a2-bbbb-7000-8000-000000000010","position":0,"sets":[{"position":0,"type":"normal","completed":false}]}]}`)
	got, err := req.Validate(progressNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	s := got.Exercises[0].Sets[0]
	if got.WorkoutPlanID != nil || got.Notes != nil || got.Exercises[0].Notes != nil ||
		s.Reps != nil || s.Weight != nil || s.DurationSeconds != nil || s.DistanceMeters != nil || s.RPE != nil || s.Completed {
		t.Errorf("optional fields were not left null: %+v", got)
	}
}

// TestProgressValidateFields breaks one field of a valid request at a time and
// expects exactly the listed issues.
func TestProgressValidateFields(t *testing.T) {
	const ex = "exercises[0]"
	const set0 = "exercises[0].sets[0]"
	long := func(n int) string { return strings.Repeat("a", n) }
	maxInt := int(math.MaxInt32)

	tests := []struct {
		name   string
		mutate func(r *ProgressSaveRequest)
		want   []string // "field: issue"; nil means valid
	}{
		{"valid", func(r *ProgressSaveRequest) {}, nil},

		// workout_plan_id
		{"plan id null", func(r *ProgressSaveRequest) { r.WorkoutPlanID = nil }, nil},
		{"plan id upper case", func(r *ProgressSaveRequest) {
			r.WorkoutPlanID = progressPtr("0195F3A2-AAAA-7000-8000-000000000001")
		}, nil},
		{"plan id malformed", func(r *ProgressSaveRequest) { r.WorkoutPlanID = progressPtr("nope") }, []string{"workout_plan_id: invalid_format"}},
		{"plan id empty", func(r *ProgressSaveRequest) { r.WorkoutPlanID = progressPtr("") }, []string{"workout_plan_id: invalid_format"}},
		{"plan id in braces", func(r *ProgressSaveRequest) {
			r.WorkoutPlanID = progressPtr("{0195f3a2-aaaa-7000-8000-000000000001}")
		}, []string{"workout_plan_id: invalid_format"}},
		{"plan id urn", func(r *ProgressSaveRequest) {
			r.WorkoutPlanID = progressPtr("urn:uuid:0195f3a2-aaaa-7000-8000-000000000001")
		}, []string{"workout_plan_id: invalid_format"}},
		{"plan id without hyphens", func(r *ProgressSaveRequest) {
			r.WorkoutPlanID = progressPtr("0195f3a2aaaa70008000000000000001")
		}, []string{"workout_plan_id: invalid_format"}},

		// name: 1-100 characters
		{"name missing", func(r *ProgressSaveRequest) { r.Name = "" }, []string{"name: required"}},
		{"name one char", func(r *ProgressSaveRequest) { r.Name = "x" }, nil},
		{"name 100 chars", func(r *ProgressSaveRequest) { r.Name = long(100) }, nil},
		{"name 101 chars", func(r *ProgressSaveRequest) { r.Name = long(101) }, []string{"name: too_long"}},
		{"name 100 multibyte chars", func(r *ProgressSaveRequest) { r.Name = strings.Repeat("é", 100) }, nil},
		{"name 101 multibyte chars", func(r *ProgressSaveRequest) { r.Name = strings.Repeat("é", 101) }, []string{"name: too_long"}},
		{"name with NUL", func(r *ProgressSaveRequest) { r.Name = "a\x00b" }, []string{"name: invalid_chars"}},

		// notes: max 2000
		{"notes empty", func(r *ProgressSaveRequest) { r.Notes = progressPtr("") }, nil},
		{"notes 2000", func(r *ProgressSaveRequest) { r.Notes = progressPtr(long(2000)) }, nil},
		{"notes 2001", func(r *ProgressSaveRequest) { r.Notes = progressPtr(long(2001)) }, []string{"notes: too_long"}},
		{"notes with NUL", func(r *ProgressSaveRequest) { r.Notes = progressPtr("\x00") }, []string{"notes: invalid_chars"}},

		// started_at / ended_at
		{"started_at missing", func(r *ProgressSaveRequest) { r.StartedAt = "" }, []string{"started_at: required"}},
		{"started_at malformed", func(r *ProgressSaveRequest) { r.StartedAt = "yesterday" }, []string{"started_at: invalid_format"}},
		{"started_at without time zone", func(r *ProgressSaveRequest) { r.StartedAt = "2026-09-19T08:00:00" }, []string{"started_at: invalid_format"}},
		{"started_at date only", func(r *ProgressSaveRequest) { r.StartedAt = "2026-09-19" }, []string{"started_at: invalid_format"}},
		{"started_at with offset", func(r *ProgressSaveRequest) { r.StartedAt = "2026-09-19T10:00:00+02:00" }, nil},
		{"started_at with fraction", func(r *ProgressSaveRequest) { r.StartedAt = "2026-09-19T08:00:00.123456789Z" }, nil},
		{"ended_at missing", func(r *ProgressSaveRequest) { r.EndedAt = "" }, []string{"ended_at: required"}},
		{"ended_at malformed", func(r *ProgressSaveRequest) { r.EndedAt = "soon" }, []string{"ended_at: invalid_format"}},
		{"ended_at equals started_at", func(r *ProgressSaveRequest) { r.EndedAt = r.StartedAt }, nil},
		{"ended_at one second before started_at", func(r *ProgressSaveRequest) { r.EndedAt = "2026-09-19T07:59:59Z" }, []string{"ended_at: out_of_range"}},
		{"ended_at before started_at by a microsecond", func(r *ProgressSaveRequest) {
			r.StartedAt, r.EndedAt = "2026-09-19T08:00:00.000001Z", "2026-09-19T08:00:00Z"
		}, []string{"ended_at: out_of_range"}},
		{"ended_at before started_at across offsets", func(r *ProgressSaveRequest) {
			r.StartedAt, r.EndedAt = "2026-09-19T10:00:00+02:00", "2026-09-19T07:59:00Z"
		}, []string{"ended_at: out_of_range"}},
		{"started_at malformed does not also flag the order", func(r *ProgressSaveRequest) {
			r.StartedAt, r.EndedAt = "?", "2000-01-01T00:00:00Z"
		}, []string{"started_at: invalid_format"}},

		// duration_seconds: 0-86400
		{"duration missing", func(r *ProgressSaveRequest) { r.DurationSeconds = nil }, []string{"duration_seconds: required"}},
		{"duration -1", func(r *ProgressSaveRequest) { r.DurationSeconds = progressPtr(-1) }, []string{"duration_seconds: out_of_range"}},
		{"duration 0", func(r *ProgressSaveRequest) { r.DurationSeconds = progressPtr(0) }, nil},
		{"duration 86400", func(r *ProgressSaveRequest) { r.DurationSeconds = progressPtr(86400) }, nil},
		{"duration 86401", func(r *ProgressSaveRequest) { r.DurationSeconds = progressPtr(86401) }, []string{"duration_seconds: out_of_range"}},

		// updated_at: required, RFC 3339, at most 5 minutes ahead
		{"updated_at missing", func(r *ProgressSaveRequest) { r.UpdatedAt = "" }, []string{"updated_at: required"}},
		{"updated_at malformed", func(r *ProgressSaveRequest) { r.UpdatedAt = "1758268502" }, []string{"updated_at: invalid_format"}},
		{"updated_at long ago", func(r *ProgressSaveRequest) { r.UpdatedAt = "1999-01-01T00:00:00Z" }, nil},
		{"updated_at exactly 5 minutes ahead", func(r *ProgressSaveRequest) { r.UpdatedAt = "2026-09-19T09:01:00Z" }, nil},
		{"updated_at 5 minutes and 1 microsecond ahead", func(r *ProgressSaveRequest) { r.UpdatedAt = "2026-09-19T09:01:00.000001Z" }, []string{"updated_at: too_far_in_future"}},
		{"updated_at a day ahead", func(r *ProgressSaveRequest) { r.UpdatedAt = "2026-09-20T08:56:00Z" }, []string{"updated_at: too_far_in_future"}},

		// exercises: required, max 50
		{"exercises missing", func(r *ProgressSaveRequest) { r.Exercises = nil }, []string{"exercises: required"}},
		{"exercises empty", func(r *ProgressSaveRequest) { r.Exercises = []ProgressExerciseRequest{} }, nil},
		{"exercises 50", func(r *ProgressSaveRequest) { progressManyExercises(r, 50) }, nil},
		{"exercises 51", func(r *ProgressSaveRequest) { progressManyExercises(r, 51) }, []string{"exercises: too_many"}},

		// exercise_id
		{"exercise_id missing", func(r *ProgressSaveRequest) { r.Exercises[0].ExerciseID = "" }, []string{ex + ".exercise_id: required"}},
		{"exercise_id malformed", func(r *ProgressSaveRequest) { r.Exercises[0].ExerciseID = "12345" }, []string{ex + ".exercise_id: invalid_format"}},

		// exercise position
		{"exercise position missing", func(r *ProgressSaveRequest) { r.Exercises[0].Position = nil }, []string{ex + ".position: required"}},
		{"exercise position -1", func(r *ProgressSaveRequest) { r.Exercises[0].Position = progressPtr(-1) }, []string{ex + ".position: out_of_range"}},
		{"exercise position 0", func(r *ProgressSaveRequest) { r.Exercises[0].Position = progressPtr(0) }, nil},
		{"exercise position max int32", func(r *ProgressSaveRequest) { r.Exercises[0].Position = progressPtr(maxInt) }, nil},
		{"exercise position over max int32", func(r *ProgressSaveRequest) { r.Exercises[0].Position = progressPtr(maxInt + 1) }, []string{ex + ".position: out_of_range"}},
		{"exercise position duplicated", func(r *ProgressSaveRequest) {
			r.Exercises = append(r.Exercises, r.Exercises[0])
		}, []string{"exercises[1].position: duplicate"}},
		{"exercise position gaps and order are fine", func(r *ProgressSaveRequest) {
			second := r.Exercises[0]
			second.Position = progressPtr(9)
			r.Exercises[0].Position = progressPtr(40)
			r.Exercises = append(r.Exercises, second)
		}, nil},
		{"same exercise twice at different positions is fine", func(r *ProgressSaveRequest) {
			second := r.Exercises[0]
			second.Position = progressPtr(1)
			r.Exercises = append(r.Exercises, second)
		}, nil},

		// exercise notes: max 1000
		{"exercise notes 1000", func(r *ProgressSaveRequest) { r.Exercises[0].Notes = progressPtr(long(1000)) }, nil},
		{"exercise notes 1001", func(r *ProgressSaveRequest) { r.Exercises[0].Notes = progressPtr(long(1001)) }, []string{ex + ".notes: too_long"}},
		{"exercise notes NUL", func(r *ProgressSaveRequest) { r.Exercises[0].Notes = progressPtr("\x00") }, []string{ex + ".notes: invalid_chars"}},

		// sets: required, max 100
		{"sets missing", func(r *ProgressSaveRequest) { r.Exercises[0].Sets = nil }, []string{ex + ".sets: required"}},
		{"sets empty", func(r *ProgressSaveRequest) { r.Exercises[0].Sets = []ProgressSetRequest{} }, nil},
		{"sets 100", func(r *ProgressSaveRequest) { progressManySets(r, 100) }, nil},
		{"sets 101", func(r *ProgressSaveRequest) { progressManySets(r, 101) }, []string{ex + ".sets: too_many"}},

		// set position
		{"set position missing", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Position = nil }, []string{set0 + ".position: required"}},
		{"set position -1", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Position = progressPtr(-1) }, []string{set0 + ".position: out_of_range"}},
		{"set position duplicated", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[1].Position = progressPtr(0) }, []string{ex + ".sets[1].position: duplicate"}},
		{"set positions may repeat across exercises", func(r *ProgressSaveRequest) {
			second := r.Exercises[0]
			second.Position = progressPtr(1)
			r.Exercises = append(r.Exercises, second)
		}, nil},

		// set type
		{"type missing", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "" }, []string{set0 + ".type: required"}},
		{"type unknown", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "superset" }, []string{set0 + ".type: invalid_value"}},
		{"type is case sensitive", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "Warmup" }, []string{set0 + ".type: invalid_value"}},
		{"type warmup", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "warmup" }, nil},
		{"type normal", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "normal" }, nil},
		{"type drop", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "drop" }, nil},
		{"type failure", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Type = "failure" }, nil},

		// reps and set duration: 0 or more, within the column
		{"reps -1", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Reps = progressPtr(-1) }, []string{set0 + ".reps: out_of_range"}},
		{"reps 0", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Reps = progressPtr(0) }, nil},
		{"reps max int32", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Reps = progressPtr(maxInt) }, nil},
		{"reps over max int32", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Reps = progressPtr(maxInt + 1) }, []string{set0 + ".reps: out_of_range"}},
		{"set duration -1", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].DurationSeconds = progressPtr(-1) }, []string{set0 + ".duration_seconds: out_of_range"}},
		{"set duration 0", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].DurationSeconds = progressPtr(0) }, nil},
		{"set duration over max int32", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].DurationSeconds = progressPtr(maxInt + 1) }, []string{set0 + ".duration_seconds: out_of_range"}},

		// completed: required, false is a value
		{"completed missing", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Completed = nil }, []string{set0 + ".completed: required"}},
		{"completed false", func(r *ProgressSaveRequest) { r.Exercises[0].Sets[0].Completed = progressPtr(false) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := progressValidRequest(t)
			tt.mutate(&req)
			_, err := req.Validate(progressNow)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate = %v, want valid", err)
				}
				return
			}
			got := progressIssues(t, err)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("issues = %q, want %q", got, tt.want)
			}
		})
	}
}

func progressManyExercises(r *ProgressSaveRequest, n int) {
	r.Exercises = make([]ProgressExerciseRequest, n)
	for i := range r.Exercises {
		r.Exercises[i] = ProgressExerciseRequest{
			ExerciseID: "0195f3a2-bbbb-7000-8000-000000000010", Position: progressPtr(i), Sets: []ProgressSetRequest{},
		}
	}
}

func progressManySets(r *ProgressSaveRequest, n int) {
	sets := make([]ProgressSetRequest, n)
	for i := range sets {
		sets[i] = ProgressSetRequest{Position: progressPtr(i), Type: "normal", Completed: progressPtr(true)}
	}
	r.Exercises[0].Sets = sets
}

// TestProgressValidateDecimals covers weight, distance_meters and rpe, which
// arrive as JSON number text and never pass through a float.
func TestProgressValidateDecimals(t *testing.T) {
	// want is the canonical text, or the issue when it starts with "!".
	tests := []struct {
		field string
		in    string
		want  string
	}{
		// weight numeric(7,2): >= 0, at most 2 decimals, at most 99999.99
		{"weight", "0", "0"},
		{"weight", "0.0", "0"},
		{"weight", "-0", "0"},
		{"weight", "-0.00", "0"},
		{"weight", "0.01", "0.01"},
		{"weight", "80", "80"},
		{"weight", "80.0", "80"},
		{"weight", "80.50", "80.5"},
		{"weight", "80.500", "80.5"},
		{"weight", "80.55", "80.55"},
		{"weight", "007.10", "7.1"},
		{"weight", "99999.99", "99999.99"},
		{"weight", "99999.990", "99999.99"},
		{"weight", "100000", "!out_of_range"},
		{"weight", "100000.001", "!out_of_range"},
		{"weight", "99999.991", "!too_many_decimals"},
		{"weight", "80.555", "!too_many_decimals"},
		{"weight", "0.001", "!too_many_decimals"},
		{"weight", "-0.01", "!out_of_range"},
		{"weight", "-80", "!out_of_range"},
		{"weight", "-80.555", "!out_of_range"},
		{"weight", "999999999999999999999999", "!out_of_range"},
		{"weight", "1e2", "!invalid_format"},
		{"weight", "1E2", "!invalid_format"},
		{"weight", "8.5e1", "!invalid_format"},
		{"weight", "abc", "!invalid_format"},
		{"weight", "", "!invalid_format"},
		{"weight", "-", "!invalid_format"},
		{"weight", "+5", "!invalid_format"},
		{"weight", ".5", "!invalid_format"},
		{"weight", "5.", "!invalid_format"},
		{"weight", "5.5.5", "!invalid_format"},
		{"weight", "1 0", "!invalid_format"},
		{"weight", "NaN", "!invalid_format"},

		// distance_meters numeric(9,2): at most 9999999.99
		{"distance_meters", "0", "0"},
		{"distance_meters", "5000", "5000"},
		{"distance_meters", "5000.25", "5000.25"},
		{"distance_meters", "9999999.99", "9999999.99"},
		{"distance_meters", "10000000", "!out_of_range"},
		{"distance_meters", "9999999.999", "!too_many_decimals"},
		{"distance_meters", "-1", "!out_of_range"},
		{"distance_meters", "0.005", "!too_many_decimals"},

		// rpe numeric(3,1): 1 to 10 in steps of 0.5
		{"rpe", "1", "1"},
		{"rpe", "1.0", "1"},
		{"rpe", "1.5", "1.5"},
		{"rpe", "1.50", "1.5"},
		{"rpe", "8", "8"},
		{"rpe", "8.5", "8.5"},
		{"rpe", "9.5", "9.5"},
		{"rpe", "10", "10"},
		{"rpe", "10.0", "10"},
		{"rpe", "10.00", "10"},
		{"rpe", "0", "!out_of_range"},
		{"rpe", "0.5", "!out_of_range"},
		{"rpe", "0.99", "!out_of_range"},
		{"rpe", "-1", "!out_of_range"},
		{"rpe", "-0.5", "!out_of_range"},
		{"rpe", "10.5", "!out_of_range"},
		{"rpe", "10.001", "!out_of_range"},
		{"rpe", "11", "!out_of_range"},
		{"rpe", "1000", "!out_of_range"},
		{"rpe", "8.25", "!invalid_value"},
		{"rpe", "8.51", "!invalid_value"},
		{"rpe", "8.1", "!invalid_value"},
		{"rpe", "8.75", "!invalid_value"},
		{"rpe", "1.05", "!invalid_value"},
		{"rpe", "9.999", "!invalid_value"},
		{"rpe", "2e0", "!invalid_format"},
		{"rpe", "x", "!invalid_format"},
	}
	for _, tt := range tests {
		t.Run(tt.field+" "+tt.in, func(t *testing.T) {
			req := progressValidRequest(t)
			s := &req.Exercises[0].Sets[0]
			switch tt.field {
			case "weight":
				s.Weight = progressDec(tt.in)
			case "distance_meters":
				s.DistanceMeters = progressDec(tt.in)
			case "rpe":
				s.RPE = progressDec(tt.in)
			}
			got, err := req.Validate(progressNow)

			if issue, bad := strings.CutPrefix(tt.want, "!"); bad {
				want := "exercises[0].sets[0]." + tt.field + ": " + issue
				if issues := progressIssues(t, err); len(issues) != 1 || issues[0] != want {
					t.Errorf("issues = %q, want [%q]", issues, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate = %v, want valid", err)
			}
			set := got.Exercises[0].Sets[0]
			d := map[string]*ProgressDecimal{"weight": set.Weight, "distance_meters": set.DistanceMeters, "rpe": set.RPE}[tt.field]
			if d == nil || string(*d) != tt.want {
				t.Errorf("canonical = %v, want %q", d, tt.want)
			}
		})
	}
}

func TestProgressValidateCollectsEveryIssueWithPaths(t *testing.T) {
	req := ProgressSaveRequest{
		WorkoutPlanID:   progressPtr("bad"),
		Name:            strings.Repeat("n", 101),
		Notes:           progressPtr(strings.Repeat("n", 2001)),
		StartedAt:       "2026-09-19T09:00:00Z",
		EndedAt:         "2026-09-19T08:00:00Z",
		DurationSeconds: progressPtr(86401),
		UpdatedAt:       "2027-01-01T00:00:00Z",
		Exercises: []ProgressExerciseRequest{
			{ExerciseID: "0195f3a2-bbbb-7000-8000-000000000010", Position: progressPtr(0), Sets: []ProgressSetRequest{
				{Position: progressPtr(0), Type: "normal", Completed: progressPtr(true)},
			}},
			{ExerciseID: "x", Position: progressPtr(0), Notes: progressPtr(strings.Repeat("n", 1001)), Sets: nil},
			{ExerciseID: "0195f3a2-bbbb-7000-8000-000000000010", Position: progressPtr(2), Sets: []ProgressSetRequest{
				{Position: progressPtr(0), Type: "normal", Completed: progressPtr(true)},
				{Position: progressPtr(0), Type: "bogus", Reps: progressPtr(-1), Weight: progressDec("1.234"),
					DurationSeconds: progressPtr(-5), DistanceMeters: progressDec("-1"), RPE: progressDec("8.25")},
				{Position: progressPtr(5), Type: "normal", Completed: progressPtr(false)},
				{Position: nil, Type: "", Completed: nil},
			}},
		},
	}
	_, err := req.Validate(progressNow)
	want := []string{
		"workout_plan_id: invalid_format",
		"name: too_long",
		"notes: too_long",
		"ended_at: out_of_range",
		"duration_seconds: out_of_range",
		"updated_at: too_far_in_future",
		"exercises[1].exercise_id: invalid_format",
		"exercises[1].position: duplicate",
		"exercises[1].notes: too_long",
		"exercises[1].sets: required",
		"exercises[2].sets[1].position: duplicate",
		"exercises[2].sets[1].type: invalid_value",
		"exercises[2].sets[1].reps: out_of_range",
		"exercises[2].sets[1].duration_seconds: out_of_range",
		"exercises[2].sets[1].weight: too_many_decimals",
		"exercises[2].sets[1].distance_meters: out_of_range",
		"exercises[2].sets[1].rpe: invalid_value",
		"exercises[2].sets[1].completed: required",
		"exercises[2].sets[3].position: required",
		"exercises[2].sets[3].type: required",
		"exercises[2].sets[3].completed: required",
	}
	if got := progressIssues(t, err); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("issues =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProgressValidateEmptyRequest(t *testing.T) {
	var req ProgressSaveRequest
	_, err := req.Validate(progressNow)
	want := []string{
		"name: required", "started_at: required", "ended_at: required",
		"duration_seconds: required", "updated_at: required", "exercises: required",
	}
	if got := progressIssues(t, err); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("issues = %q, want %q", got, want)
	}
}

func TestProgressValidateNormalisesTimes(t *testing.T) {
	req := progressValidRequest(t)
	req.StartedAt = "2026-09-19T10:00:00.123456789+02:00"
	req.EndedAt = "2026-09-19T08:30:00Z"
	req.UpdatedAt = "2026-09-19T08:55:02.9999999Z"
	got, err := req.Validate(progressNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 19, 8, 0, 0, 123456000, time.UTC); !got.StartedAt.Equal(want) || got.StartedAt.Location() != time.UTC {
		t.Errorf("StartedAt = %v, want %v truncated to the microsecond, in UTC", got.StartedAt, want)
	}
	if want := time.Date(2026, 9, 19, 8, 55, 2, 999999000, time.UTC); !got.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, want)
	}
}

func TestProgressDecimalUnmarshal(t *testing.T) {
	type holder struct {
		V *ProgressDecimal `json:"v"`
	}
	ok := map[string]string{ // JSON value -> stored text
		`80`: "80", `80.0`: "80.0", `-0.5`: "-0.5", `8.50`: "8.50", `1e2`: "1e2", `0`: "0",
	}
	for in, want := range ok {
		var h holder
		if err := json.Unmarshal([]byte(`{"v":`+in+`}`), &h); err != nil || h.V == nil || string(*h.V) != want {
			t.Errorf("Unmarshal(%s) = %v, %v, want %q", in, h.V, err, want)
		}
	}

	var h holder
	if err := json.Unmarshal([]byte(`{"v":null}`), &h); err != nil || h.V != nil {
		t.Errorf("null = %v, %v, want nil", h.V, err)
	}
	if err := json.Unmarshal([]byte(`{}`), &h); err != nil || h.V != nil {
		t.Errorf("absent = %v, %v, want nil", h.V, err)
	}

	// Anything but a number is a JSON type error, which the API answers with a 400.
	for _, in := range []string{`"80"`, `""`, `true`, `false`, `{}`, `[]`, `[80]`} {
		var h holder
		err := json.Unmarshal([]byte(`{"v":`+in+`}`), &h)
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Errorf("Unmarshal(%s) = %v, want a *json.UnmarshalTypeError", in, err)
		}
	}
}

func TestProgressDecimalMarshal(t *testing.T) {
	tests := []struct{ in, want string }{
		{"80.00", "80.0"}, // numeric(7,2) text from the database
		{"80.50", "80.5"},
		{"8.5", "8.5"},
		{"0.00", "0.0"},
		{"0.01", "0.01"},
		{"99999.99", "99999.99"},
		{"9999999.99", "9999999.99"},
		{"10.0", "10.0"},
		{"10", "10.0"},
		{"007.100", "7.1"},
		{"-0.00", "0.0"},
		{"-1.5", "-1.5"},
	}
	for _, tt := range tests {
		b, err := json.Marshal(ProgressDecimal(tt.in))
		if err != nil || string(b) != tt.want {
			t.Errorf("Marshal(%q) = %s, %v, want %s", tt.in, b, err, tt.want)
		}
	}
	for _, bad := range []string{"", "abc", "1e5", "1.2.3"} {
		if b, err := json.Marshal(ProgressDecimal(bad)); err == nil {
			t.Errorf("Marshal(%q) = %s, want an error", bad, b)
		}
	}
	// A nil pointer is null.
	b, _ := json.Marshal(struct {
		V *ProgressDecimal `json:"v"`
	}{})
	if string(b) != `{"v":null}` {
		t.Errorf("nil decimal = %s", b)
	}
}

// TestProgressDecimalNeverUsesFloats: a value that a float64 cannot hold
// survives the whole trip byte for byte.
func TestProgressDecimalNeverUsesFloats(t *testing.T) {
	const exact = "99999.99" // 99999.99 is not exactly representable, 0.1+0.2 style traps
	req := progressValidRequest(t)
	req.Exercises[0].Sets[0].Weight = progressDec(exact)
	req.Exercises[0].Sets[0].DistanceMeters = progressDec("9999999.99")
	got, err := req.Validate(progressNow)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.Exercises[0].Sets[0])
	if !strings.Contains(string(b), `"weight":99999.99`) || !strings.Contains(string(b), `"distance_meters":9999999.99`) {
		t.Errorf("set = %s", b)
	}
}

func TestProgressRequestDecodingIsStrict(t *testing.T) {
	bad := map[string]string{
		"unknown field":            `{"name":"x","bogus":1}`,
		"unknown field in set":     `{"exercises":[{"sets":[{"nope":1}]}]}`,
		"weight as string":         `{"exercises":[{"sets":[{"weight":"80"}]}]}`,
		"reps as float":            `{"exercises":[{"sets":[{"reps":8.5}]}]}`,
		"position as string":       `{"exercises":[{"position":"1"}]}`,
		"completed as string":      `{"exercises":[{"sets":[{"completed":"true"}]}]}`,
		"name as number":           `{"name":5}`,
		"exercises as object":      `{"exercises":{}}`,
		"duration_seconds as text": `{"duration_seconds":"60"}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			var r ProgressSaveRequest
			dec := json.NewDecoder(strings.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err == nil {
				t.Errorf("decoded %s without error", body)
			}
		})
	}

	// completed: false is a value, absent is not.
	r := progressDecode(t, `{"exercises":[{"sets":[{"completed":false},{}]}]}`)
	sets := r.Exercises[0].Sets
	if sets[0].Completed == nil || *sets[0].Completed || sets[1].Completed != nil {
		t.Errorf("completed = %v, %v; want false and absent to differ", sets[0].Completed, sets[1].Completed)
	}
}

// TestProgressJSONShapes pins the response key sets and their order to the
// examples of specs 04 and 05.
func TestProgressJSONShapes(t *testing.T) {
	at := time.Date(2026, 9, 19, 8, 55, 4, 0, time.UTC)
	plan := uuid.MustParse("0195f3a2-aaaa-7000-8000-000000000001")
	id := uuid.MustParse("0195f3a2-cccc-7000-8000-000000000100")

	full := Progress{
		ID: id, WorkoutPlanID: &plan, Name: "Push Day A", Notes: progressPtr("Felt strong"),
		StartedAt: at.Add(-55 * time.Minute), EndedAt: at.Add(-4 * time.Second), DurationSeconds: 3120,
		UpdatedAt: at.Add(-2 * time.Second), CreatedAt: at, ServerUpdatedAt: at,
		Exercises: []ProgressExercise{{
			ExerciseID: uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010"), Position: 0,
			Sets: []ProgressSet{{Position: 0, Type: SetTypeNormal, Reps: progressPtr(8), Weight: progressDec("80.00"),
				RPE: progressDec("8.5"), Completed: true}},
		}},
	}
	wantFull := `{"id":"0195f3a2-cccc-7000-8000-000000000100","workout_plan_id":"0195f3a2-aaaa-7000-8000-000000000001",` +
		`"name":"Push Day A","notes":"Felt strong","started_at":"2026-09-19T08:00:04Z","ended_at":"2026-09-19T08:55:00Z",` +
		`"duration_seconds":3120,"updated_at":"2026-09-19T08:55:02Z","created_at":"2026-09-19T08:55:04Z",` +
		`"server_updated_at":"2026-09-19T08:55:04Z","deleted_at":null,` +
		`"exercises":[{"exercise_id":"0195f3a2-bbbb-7000-8000-000000000010","position":0,"notes":null,"sets":[` +
		`{"position":0,"type":"normal","reps":8,"weight":80.0,"duration_seconds":null,"distance_meters":null,"rpe":8.5,"completed":true}]}]}`
	if got := progressMarshal(t, full); got != wantFull {
		t.Errorf("Progress =\n%s\nwant\n%s", got, wantFull)
	}

	summary := ProgressSummary{
		ID: id, WorkoutPlanID: &plan, Name: "Push Day A", StartedAt: at.Add(-55 * time.Minute), EndedAt: at.Add(-4 * time.Second),
		DurationSeconds: 3120, ExerciseCount: 5, SetCount: 18, UpdatedAt: at.Add(-2 * time.Second), ServerUpdatedAt: at,
	}
	wantSummary := `{"id":"0195f3a2-cccc-7000-8000-000000000100","workout_plan_id":"0195f3a2-aaaa-7000-8000-000000000001",` +
		`"name":"Push Day A","started_at":"2026-09-19T08:00:04Z","ended_at":"2026-09-19T08:55:00Z","duration_seconds":3120,` +
		`"exercise_count":5,"set_count":18,"updated_at":"2026-09-19T08:55:02Z","server_updated_at":"2026-09-19T08:55:04Z","deleted_at":null}`
	if got := progressMarshal(t, summary); got != wantSummary {
		t.Errorf("ProgressSummary =\n%s\nwant\n%s", got, wantSummary)
	}

	// An empty page is [] and a last page has next_cursor null.
	empty := ProgressPage[ProgressSummary]{Items: []ProgressSummary{}}
	if got := progressMarshal(t, empty); got != `{"items":[],"next_cursor":null}` {
		t.Errorf("empty page = %s", got)
	}
	next := "abc"
	if got := progressMarshal(t, ProgressPage[Progress]{Items: []Progress{}, NextCursor: &next}); got != `{"items":[],"next_cursor":"abc"}` {
		t.Errorf("page with cursor = %s", got)
	}
}

func progressMarshal(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestProgressColumnMaxima: the integer part of the largest weight and
// distance the numeric columns hold (limits.go has the specs' own limits).
func TestProgressColumnMaxima(t *testing.T) {
	if progressMaxWeightInt != 99999 || progressMaxDistanceInt != 9999999 {
		t.Errorf("column maxima = %d, %d, want 99999, 9999999", progressMaxWeightInt, progressMaxDistanceInt)
	}
}
