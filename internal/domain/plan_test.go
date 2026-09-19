package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// planTestNow is the server time of the validation tests.
var planTestNow = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

const planTestExerciseID = "0195f3a2-bbbb-7000-8000-000000000010"

func planStr(s string) *string { return &s }
func planInt(n int) *int       { return &n }

// planDec is an unvalidated decimal, as the JSON decoder leaves it.
func planDec(lit string) *PlanDecimal { return &PlanDecimal{text: lit} }

// planValidInput is a request that validates: one fully populated exercise.
func planValidInput() PlanInput {
	return PlanInput{
		Name:        planStr("Push"),
		Description: planStr("Chest / shoulders / triceps"),
		UpdatedAt:   planStr("2026-09-19T09:00:00Z"),
		Exercises: []PlanExerciseInput{{
			ExerciseID:            planStr(planTestExerciseID),
			Position:              planInt(0),
			TargetSets:            planInt(4),
			TargetReps:            planInt(8),
			TargetRepsMax:         planInt(10),
			TargetWeight:          planDec("80.0"),
			TargetDurationSeconds: planInt(60),
			TargetDistanceMeters:  planDec("400"),
			RestSeconds:           planInt(120),
			Notes:                 planStr("Pause on chest"),
		}},
	}
}

// planIssues returns the issues of a validation error, and fails on any other
// error.
func planIssues(t *testing.T, err error) []FieldIssue {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
	}
	return ve.Issues
}

func planEx(field string) string { return FieldIndex("exercises", 0) + "." + field }

func TestPlanValidateAcceptsSpecExample(t *testing.T) {
	// The request example of spec 10, verbatim.
	const body = `{
	  "name": "Push",
	  "description": "Chest / shoulders / triceps",
	  "updated_at": "2026-09-19T09:00:00Z",
	  "exercises": [
	    {
	      "exercise_id": "0195f3a2-bbbb-7000-8000-000000000010",
	      "position": 0,
	      "target_sets": 4,
	      "target_reps": 8,
	      "target_reps_max": 10,
	      "target_weight": 80.0,
	      "target_duration_seconds": null,
	      "target_distance_meters": null,
	      "rest_seconds": 120,
	      "notes": "Pause on chest"
	    }
	  ]
	}`
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var in PlanInput
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("decode: %v", err)
	}
	spec, err := in.Validate(planTestNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if spec.Name != "Push" || spec.Description == nil || *spec.Description != "Chest / shoulders / triceps" {
		t.Errorf("name/description = %q, %v", spec.Name, spec.Description)
	}
	if want := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC); !spec.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", spec.UpdatedAt, want)
	}
	if len(spec.Exercises) != 1 {
		t.Fatalf("exercises = %d, want 1", len(spec.Exercises))
	}
	ex := spec.Exercises[0]
	if ex.ExerciseID.String() != planTestExerciseID || ex.Position != 0 || ex.TargetSets != 4 ||
		*ex.TargetReps != 8 || *ex.TargetRepsMax != 10 || *ex.RestSeconds != 120 || *ex.Notes != "Pause on chest" {
		t.Errorf("exercise = %+v", ex)
	}
	if ex.TargetWeight == nil || ex.TargetWeight.String() != "80.00" {
		t.Errorf("TargetWeight = %v, want canonical 80.00", ex.TargetWeight)
	}
	if ex.TargetDurationSeconds != nil || ex.TargetDistanceMeters != nil {
		t.Errorf("null targets must stay nil: %+v", ex)
	}
}

func TestPlanValidateOptionalFieldsMayBeNull(t *testing.T) {
	in := planValidInput()
	in.Description = nil
	in.Exercises = []PlanExerciseInput{{
		ExerciseID: planStr(planTestExerciseID), Position: planInt(0), TargetSets: planInt(1),
	}}
	spec, err := in.Validate(planTestNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if spec.Description != nil {
		t.Errorf("Description = %v, want nil", spec.Description)
	}
	ex := spec.Exercises[0]
	if ex.TargetReps != nil || ex.TargetRepsMax != nil || ex.TargetWeight != nil || ex.TargetDurationSeconds != nil ||
		ex.TargetDistanceMeters != nil || ex.RestSeconds != nil || ex.Notes != nil {
		t.Errorf("optional fields must stay nil: %+v", ex)
	}
}

func TestPlanValidateEmptyExercisesAllowed(t *testing.T) {
	in := planValidInput()
	in.Exercises = []PlanExerciseInput{}
	spec, err := in.Validate(planTestNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if spec.Exercises == nil || len(spec.Exercises) != 0 {
		t.Errorf("Exercises = %#v, want a non-nil empty slice", spec.Exercises)
	}
}

// TestPlanValidateBoundaries covers every rule at and around its limit. Each
// case starts from a valid request, applies mut, and lists the exact issues
// expected (nil: the request must be valid).
func TestPlanValidateBoundaries(t *testing.T) {
	const maxInt = math.MaxInt32
	repeat := func(s string, n int) string { return strings.Repeat(s, n) }
	first := func(in *PlanInput) *PlanExerciseInput { return &in.Exercises[0] }

	tests := []struct {
		name string
		mut  func(*PlanInput)
		want []FieldIssue
	}{
		// name: 1-100 characters, not bytes.
		{"name missing", func(in *PlanInput) { in.Name = nil }, []FieldIssue{{"name", IssueRequired}}},
		{"name empty", func(in *PlanInput) { in.Name = planStr("") }, []FieldIssue{{"name", IssueTooShort}}},
		{"name one char", func(in *PlanInput) { in.Name = planStr("x") }, nil},
		{"name 100 chars", func(in *PlanInput) { in.Name = planStr(repeat("a", 100)) }, nil},
		{"name 101 chars", func(in *PlanInput) { in.Name = planStr(repeat("a", 101)) }, []FieldIssue{{"name", IssueTooLong}}},
		{"name 100 two-byte chars", func(in *PlanInput) { in.Name = planStr(repeat("é", 100)) }, nil},
		{"name 101 two-byte chars", func(in *PlanInput) { in.Name = planStr(repeat("é", 101)) }, []FieldIssue{{"name", IssueTooLong}}},
		{"name 100 four-byte chars", func(in *PlanInput) { in.Name = planStr(repeat("🏋", 100)) }, nil},
		{"name only spaces is a name", func(in *PlanInput) { in.Name = planStr("   ") }, nil},
		{"name with NUL", func(in *PlanInput) { in.Name = planStr("a\x00b") }, []FieldIssue{{"name", IssueInvalidChars}}},
		{"name invalid UTF-8", func(in *PlanInput) { in.Name = planStr("a\xffb") }, []FieldIssue{{"name", IssueInvalidChars}}},

		// description: optional, at most 1000 characters.
		{"description empty", func(in *PlanInput) { in.Description = planStr("") }, nil},
		{"description 1000 chars", func(in *PlanInput) { in.Description = planStr(repeat("d", 1000)) }, nil},
		{"description 1000 multibyte chars", func(in *PlanInput) { in.Description = planStr(repeat("ü", 1000)) }, nil},
		{"description 1001 chars", func(in *PlanInput) { in.Description = planStr(repeat("d", 1001)) }, []FieldIssue{{"description", IssueTooLong}}},
		{"description with NUL", func(in *PlanInput) { in.Description = planStr("\x00") }, []FieldIssue{{"description", IssueInvalidChars}}},

		// updated_at: required RFC 3339, at most 5 minutes ahead of now.
		{"updated_at missing", func(in *PlanInput) { in.UpdatedAt = nil }, []FieldIssue{{"updated_at", IssueRequired}}},
		{"updated_at empty", func(in *PlanInput) { in.UpdatedAt = planStr("") }, []FieldIssue{{"updated_at", IssueInvalidFormat}}},
		{"updated_at date only", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19") }, []FieldIssue{{"updated_at", IssueInvalidFormat}}},
		{"updated_at no zone", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T09:00:00") }, []FieldIssue{{"updated_at", IssueInvalidFormat}}},
		{"updated_at unix number as text", func(in *PlanInput) { in.UpdatedAt = planStr("1789808400") }, []FieldIssue{{"updated_at", IssueInvalidFormat}}},
		{"updated_at exactly 5 minutes ahead", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T09:05:00Z") }, nil},
		{"updated_at 5 minutes and 1 microsecond ahead", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T09:05:00.000001Z") }, []FieldIssue{{"updated_at", IssueTooFarInFuture}}},
		{"updated_at 5 minutes and 1 nanosecond ahead", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T09:05:00.000000001Z") }, nil},
		{"updated_at offset counts as an instant", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T11:05:00+02:00") }, nil},
		{"updated_at offset too far ahead", func(in *PlanInput) { in.UpdatedAt = planStr("2026-09-19T11:05:01+02:00") }, []FieldIssue{{"updated_at", IssueTooFarInFuture}}},
		{"updated_at in the past", func(in *PlanInput) { in.UpdatedAt = planStr("1970-01-01T00:00:00Z") }, nil},
		{"updated_at year 1", func(in *PlanInput) { in.UpdatedAt = planStr("0001-01-01T00:00:00Z") }, nil},

		// exercises: required, at most 50, may be empty.
		{"exercises missing", func(in *PlanInput) { in.Exercises = nil }, []FieldIssue{{"exercises", IssueRequired}}},
		{"exercises empty", func(in *PlanInput) { in.Exercises = []PlanExerciseInput{} }, nil},
		{"exercises 50", func(in *PlanInput) { in.Exercises = planManyExercises(50) }, nil},
		{"exercises 51", func(in *PlanInput) { in.Exercises = planManyExercises(51) }, []FieldIssue{{"exercises", IssueTooMany}}},

		// exercise_id: canonical UUID text.
		{"exercise_id missing", func(in *PlanInput) { first(in).ExerciseID = nil }, []FieldIssue{{planEx("exercise_id"), IssueRequired}}},
		{"exercise_id empty", func(in *PlanInput) { first(in).ExerciseID = planStr("") }, []FieldIssue{{planEx("exercise_id"), IssueInvalidFormat}}},
		{"exercise_id garbage", func(in *PlanInput) { first(in).ExerciseID = planStr("not-a-uuid") }, []FieldIssue{{planEx("exercise_id"), IssueInvalidFormat}}},
		{"exercise_id urn form", func(in *PlanInput) { first(in).ExerciseID = planStr("urn:uuid:" + planTestExerciseID) }, []FieldIssue{{planEx("exercise_id"), IssueInvalidFormat}}},
		{"exercise_id braces", func(in *PlanInput) { first(in).ExerciseID = planStr("{" + planTestExerciseID + "}") }, []FieldIssue{{planEx("exercise_id"), IssueInvalidFormat}}},
		{"exercise_id without hyphens", func(in *PlanInput) { first(in).ExerciseID = planStr(strings.ReplaceAll(planTestExerciseID, "-", "")) }, []FieldIssue{{planEx("exercise_id"), IssueInvalidFormat}}},
		{"exercise_id upper case", func(in *PlanInput) { first(in).ExerciseID = planStr(strings.ToUpper(planTestExerciseID)) }, nil},

		// position: 0 .. int32 max.
		{"position missing", func(in *PlanInput) { first(in).Position = nil }, []FieldIssue{{planEx("position"), IssueRequired}}},
		{"position -1", func(in *PlanInput) { first(in).Position = planInt(-1) }, []FieldIssue{{planEx("position"), IssueOutOfRange}}},
		{"position 0", func(in *PlanInput) { first(in).Position = planInt(0) }, nil},
		{"position int32 max", func(in *PlanInput) { first(in).Position = planInt(maxInt) }, nil},
		{"position over int32 max", func(in *PlanInput) { first(in).Position = planInt(maxInt + 1) }, []FieldIssue{{planEx("position"), IssueOutOfRange}}},

		// target_sets: 1-20.
		{"target_sets missing", func(in *PlanInput) { first(in).TargetSets = nil }, []FieldIssue{{planEx("target_sets"), IssueRequired}}},
		{"target_sets 0", func(in *PlanInput) { first(in).TargetSets = planInt(0) }, []FieldIssue{{planEx("target_sets"), IssueOutOfRange}}},
		{"target_sets 1", func(in *PlanInput) { first(in).TargetSets = planInt(1) }, nil},
		{"target_sets 20", func(in *PlanInput) { first(in).TargetSets = planInt(20) }, nil},
		{"target_sets 21", func(in *PlanInput) { first(in).TargetSets = planInt(21) }, []FieldIssue{{planEx("target_sets"), IssueOutOfRange}}},

		// target_reps: at least 1.
		{"target_reps 0", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = planInt(0), nil }, []FieldIssue{{planEx("target_reps"), IssueOutOfRange}}},
		{"target_reps 1", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = planInt(1), nil }, nil},
		{"target_reps negative", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = planInt(-5), nil }, []FieldIssue{{planEx("target_reps"), IssueOutOfRange}}},
		{"target_reps int32 max", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = planInt(maxInt), nil }, nil},
		{"target_reps over int32 max", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = planInt(maxInt+1), nil }, []FieldIssue{{planEx("target_reps"), IssueOutOfRange}}},

		// target_reps_max: needs target_reps, not below it.
		{"target_reps_max equals target_reps", func(in *PlanInput) { first(in).TargetRepsMax = planInt(8) }, nil},
		{"target_reps_max above target_reps", func(in *PlanInput) { first(in).TargetRepsMax = planInt(9) }, nil},
		{"target_reps_max below target_reps", func(in *PlanInput) { first(in).TargetRepsMax = planInt(7) }, []FieldIssue{{planEx("target_reps_max"), IssueOutOfRange}}},
		{"target_reps_max without target_reps", func(in *PlanInput) { first(in).TargetReps = nil }, []FieldIssue{{planEx("target_reps"), IssueRequired}}},
		{"target_reps_max 0 without target_reps", func(in *PlanInput) { first(in).TargetReps, first(in).TargetRepsMax = nil, planInt(0) }, []FieldIssue{{planEx("target_reps_max"), IssueOutOfRange}}},
		{"target_reps_max over int32 max", func(in *PlanInput) { first(in).TargetRepsMax = planInt(maxInt + 1) }, []FieldIssue{{planEx("target_reps_max"), IssueOutOfRange}}},

		// target_weight: 0 .. 99999.99, at most 2 decimals.
		{"target_weight 0", func(in *PlanInput) { first(in).TargetWeight = planDec("0") }, nil},
		{"target_weight -0", func(in *PlanInput) { first(in).TargetWeight = planDec("-0") }, nil},
		{"target_weight -0.00", func(in *PlanInput) { first(in).TargetWeight = planDec("-0.00") }, nil},
		{"target_weight negative", func(in *PlanInput) { first(in).TargetWeight = planDec("-0.01") }, []FieldIssue{{planEx("target_weight"), IssueOutOfRange}}},
		{"target_weight 2 decimals", func(in *PlanInput) { first(in).TargetWeight = planDec("80.25") }, nil},
		{"target_weight 3 decimals", func(in *PlanInput) { first(in).TargetWeight = planDec("80.251") }, []FieldIssue{{planEx("target_weight"), IssueTooManyDecimals}}},
		{"target_weight 3 decimals ending in zeros", func(in *PlanInput) { first(in).TargetWeight = planDec("80.250") }, nil},
		{"target_weight smallest step", func(in *PlanInput) { first(in).TargetWeight = planDec("0.01") }, nil},
		{"target_weight below the step", func(in *PlanInput) { first(in).TargetWeight = planDec("0.001") }, []FieldIssue{{planEx("target_weight"), IssueTooManyDecimals}}},
		{"target_weight column max", func(in *PlanInput) { first(in).TargetWeight = planDec("99999.99") }, nil},
		{"target_weight over column max", func(in *PlanInput) { first(in).TargetWeight = planDec("100000") }, []FieldIssue{{planEx("target_weight"), IssueOutOfRange}}},
		{"target_weight one step over column max", func(in *PlanInput) { first(in).TargetWeight = planDec("100000.00") }, []FieldIssue{{planEx("target_weight"), IssueOutOfRange}}},
		{"target_weight exponent form", func(in *PlanInput) { first(in).TargetWeight = planDec("8.5e1") }, nil},
		{"target_weight negative exponent", func(in *PlanInput) { first(in).TargetWeight = planDec("25E-1") }, nil},
		{"target_weight negative exponent too fine", func(in *PlanInput) { first(in).TargetWeight = planDec("1e-3") }, []FieldIssue{{planEx("target_weight"), IssueTooManyDecimals}}},
		{"target_weight huge exponent", func(in *PlanInput) { first(in).TargetWeight = planDec("1e999999999999999999999") }, []FieldIssue{{planEx("target_weight"), IssueOutOfRange}}},
		{"target_weight tiny exponent", func(in *PlanInput) { first(in).TargetWeight = planDec("1e-999999999999999999999") }, []FieldIssue{{planEx("target_weight"), IssueTooManyDecimals}}},
		{"target_weight zero with huge exponent", func(in *PlanInput) { first(in).TargetWeight = planDec("0e999999999999") }, nil},
		{"target_weight not a number", func(in *PlanInput) { first(in).TargetWeight = planDec("abc") }, []FieldIssue{{planEx("target_weight"), IssueInvalidFormat}}},

		// target_duration_seconds: at least 0.
		{"target_duration_seconds -1", func(in *PlanInput) { first(in).TargetDurationSeconds = planInt(-1) }, []FieldIssue{{planEx("target_duration_seconds"), IssueOutOfRange}}},
		{"target_duration_seconds 0", func(in *PlanInput) { first(in).TargetDurationSeconds = planInt(0) }, nil},
		{"target_duration_seconds int32 max", func(in *PlanInput) { first(in).TargetDurationSeconds = planInt(maxInt) }, nil},
		{"target_duration_seconds over int32 max", func(in *PlanInput) { first(in).TargetDurationSeconds = planInt(maxInt + 1) }, []FieldIssue{{planEx("target_duration_seconds"), IssueOutOfRange}}},

		// target_distance_meters: 0 .. 9999999.99, at most 2 decimals.
		{"target_distance_meters 0", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("0.0") }, nil},
		{"target_distance_meters negative", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("-1") }, []FieldIssue{{planEx("target_distance_meters"), IssueOutOfRange}}},
		{"target_distance_meters 3 decimals", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("400.125") }, []FieldIssue{{planEx("target_distance_meters"), IssueTooManyDecimals}}},
		{"target_distance_meters column max", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("9999999.99") }, nil},
		{"target_distance_meters over column max", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("10000000") }, []FieldIssue{{planEx("target_distance_meters"), IssueOutOfRange}}},
		{"target_distance_meters weight max is fine", func(in *PlanInput) { first(in).TargetDistanceMeters = planDec("100000") }, nil},

		// rest_seconds: 0-3600.
		{"rest_seconds -1", func(in *PlanInput) { first(in).RestSeconds = planInt(-1) }, []FieldIssue{{planEx("rest_seconds"), IssueOutOfRange}}},
		{"rest_seconds 0", func(in *PlanInput) { first(in).RestSeconds = planInt(0) }, nil},
		{"rest_seconds 3600", func(in *PlanInput) { first(in).RestSeconds = planInt(3600) }, nil},
		{"rest_seconds 3601", func(in *PlanInput) { first(in).RestSeconds = planInt(3601) }, []FieldIssue{{planEx("rest_seconds"), IssueOutOfRange}}},

		// notes: at most 1000 characters.
		{"notes empty", func(in *PlanInput) { first(in).Notes = planStr("") }, nil},
		{"notes 1000 chars", func(in *PlanInput) { first(in).Notes = planStr(repeat("n", 1000)) }, nil},
		{"notes 1001 chars", func(in *PlanInput) { first(in).Notes = planStr(repeat("n", 1001)) }, []FieldIssue{{planEx("notes"), IssueTooLong}}},
		{"notes with NUL", func(in *PlanInput) { first(in).Notes = planStr("x\x00") }, []FieldIssue{{planEx("notes"), IssueInvalidChars}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := planValidInput()
			tt.mut(&in)
			_, err := in.Validate(planTestNow)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate: %v, want valid", err)
				}
				return
			}
			if got := planIssues(t, err); !slices.Equal(got, tt.want) {
				t.Fatalf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

// planManyExercises returns n valid exercises with positions 0..n-1.
func planManyExercises(n int) []PlanExerciseInput {
	out := make([]PlanExerciseInput, n)
	for i := range out {
		out[i] = PlanExerciseInput{ExerciseID: planStr(planTestExerciseID), Position: planInt(i), TargetSets: planInt(3)}
	}
	return out
}

func TestPlanValidateDuplicatePositions(t *testing.T) {
	ex := func(pos int) PlanExerciseInput {
		return PlanExerciseInput{ExerciseID: planStr(planTestExerciseID), Position: planInt(pos), TargetSets: planInt(3)}
	}
	tests := []struct {
		name      string
		positions []int
		want      []FieldIssue
	}{
		{"unique", []int{0, 1, 2}, nil},
		{"unique but not contiguous, not sorted", []int{7, 0, 3}, nil},
		{"pair", []int{0, 0}, []FieldIssue{{"exercises[1].position", IssueDuplicate}}},
		{"later one is flagged", []int{2, 5, 2}, []FieldIssue{{"exercises[2].position", IssueDuplicate}}},
		{"every extra copy", []int{1, 1, 1}, []FieldIssue{{"exercises[1].position", IssueDuplicate}, {"exercises[2].position", IssueDuplicate}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := planValidInput()
			in.Exercises = nil
			for _, p := range tt.positions {
				in.Exercises = append(in.Exercises, ex(p))
			}
			_, err := in.Validate(planTestNow)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if got := planIssues(t, err); !slices.Equal(got, tt.want) {
				t.Fatalf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanValidateSameExerciseMayRepeat(t *testing.T) {
	in := planValidInput()
	in.Exercises = []PlanExerciseInput{
		{ExerciseID: planStr(planTestExerciseID), Position: planInt(0), TargetSets: planInt(3)},
		{ExerciseID: planStr(planTestExerciseID), Position: planInt(1), TargetSets: planInt(3)},
	}
	if _, err := in.Validate(planTestNow); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestPlanValidateCollectsEveryIssue checks that one bad request reports all
// its problems with the right nested paths, not just the first.
func TestPlanValidateCollectsEveryIssue(t *testing.T) {
	in := PlanInput{
		Name:        planStr(""),
		Description: planStr(strings.Repeat("d", 1001)),
		UpdatedAt:   planStr("2026-09-19T10:00:00Z"),
		Exercises: []PlanExerciseInput{
			{ExerciseID: planStr(planTestExerciseID), Position: planInt(0), TargetSets: planInt(3)}, // valid
			{
				ExerciseID:            planStr("nope"),
				Position:              planInt(0), // duplicate of exercises[0]
				TargetSets:            planInt(21),
				TargetReps:            planInt(0),
				TargetWeight:          planDec("1.234"),
				TargetDurationSeconds: planInt(-1),
				TargetDistanceMeters:  planDec("-3"),
				RestSeconds:           planInt(3601),
				Notes:                 planStr(strings.Repeat("n", 1001)),
			},
			{TargetRepsMax: planInt(4)}, // nothing required is there
		},
	}
	_, err := in.Validate(planTestNow)
	want := []FieldIssue{
		{"name", IssueTooShort},
		{"description", IssueTooLong},
		{"updated_at", IssueTooFarInFuture},
		{"exercises[1].exercise_id", IssueInvalidFormat},
		{"exercises[1].position", IssueDuplicate},
		{"exercises[1].target_sets", IssueOutOfRange},
		{"exercises[1].target_reps", IssueOutOfRange},
		{"exercises[1].target_weight", IssueTooManyDecimals},
		{"exercises[1].target_distance_meters", IssueOutOfRange},
		{"exercises[1].target_duration_seconds", IssueOutOfRange},
		{"exercises[1].rest_seconds", IssueOutOfRange},
		{"exercises[1].notes", IssueTooLong},
		{"exercises[2].exercise_id", IssueRequired},
		{"exercises[2].position", IssueRequired},
		{"exercises[2].target_sets", IssueRequired},
		{"exercises[2].target_reps", IssueRequired},
	}
	if got := planIssues(t, err); !slices.Equal(got, want) {
		t.Fatalf("issues:\n got  %v\n want %v", got, want)
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("errors.Is(err, ErrValidation) = false")
	}
}

func TestPlanValidateEmptyRequestReportsRequiredFields(t *testing.T) {
	_, err := PlanInput{}.Validate(planTestNow)
	want := []FieldIssue{{"name", IssueRequired}, {"updated_at", IssueRequired}, {"exercises", IssueRequired}}
	if got := planIssues(t, err); !slices.Equal(got, want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
}

func TestPlanValidateTooManyExercisesDoesNotValidateTheTail(t *testing.T) {
	in := planValidInput()
	in.Exercises = planManyExercises(MaxExercisesPerPlan + 1000)
	for i := MaxExercisesPerPlan; i < len(in.Exercises); i++ {
		in.Exercises[i].TargetSets = planInt(99) // would be one issue each
	}
	_, err := in.Validate(planTestNow)
	want := []FieldIssue{{"exercises", IssueTooMany}}
	if got := planIssues(t, err); !slices.Equal(got, want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
}

func TestPlanValidateNormalisesValues(t *testing.T) {
	in := planValidInput()
	in.UpdatedAt = planStr("2026-09-19T11:00:00.123456789+02:00")
	in.Exercises[0].ExerciseID = planStr(strings.ToUpper(planTestExerciseID))
	in.Exercises[0].TargetWeight = planDec("8.5e1")
	in.Exercises[0].TargetDistanceMeters = planDec("-0")
	spec, err := in.Validate(planTestNow)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if want := time.Date(2026, 9, 19, 9, 0, 0, 123456000, time.UTC); !spec.UpdatedAt.Equal(want) || spec.UpdatedAt.Location() != time.UTC {
		t.Errorf("UpdatedAt = %v (%v), want %v truncated to µs in UTC", spec.UpdatedAt, spec.UpdatedAt.Location(), want)
	}
	ex := spec.Exercises[0]
	if ex.ExerciseID != uuid.MustParse(planTestExerciseID) {
		t.Errorf("ExerciseID = %v", ex.ExerciseID)
	}
	if got := ex.TargetWeight.String(); got != "85.00" {
		t.Errorf("TargetWeight = %q, want 85.00", got)
	}
	if got := ex.TargetDistanceMeters.String(); got != "0.00" {
		t.Errorf("TargetDistanceMeters = %q, want 0.00", got)
	}
}

func TestPlanValidateDoesNotChangeItsInput(t *testing.T) {
	in := planValidInput()
	in.Exercises[0].TargetWeight = planDec("80.0")
	if _, err := in.Validate(planTestNow); err != nil {
		t.Fatal(err)
	}
	if got := in.Exercises[0].TargetWeight.text; got != "80.0" {
		t.Errorf("input decimal changed to %q", got)
	}
}

func TestPlanHundredths(t *testing.T) {
	tests := []struct {
		lit   string
		want  int64
		issue string
	}{
		{"0", 0, ""},
		{"0.0", 0, ""},
		{"-0", 0, ""},
		{"-0.0e5", 0, ""},
		{"1", 100, ""},
		{"80.0", 8000, ""},
		{"80.5", 8050, ""},
		{"80.25", 8025, ""},
		{"0.5", 50, ""},
		{"0.05", 5, ""},
		{"0.01", 1, ""},
		{"99999.99", 9999999, ""},
		{"1e2", 10000, ""},
		{"1E2", 10000, ""},
		{"1e+2", 10000, ""},
		{"1.5e1", 1500, ""},
		{"15e-1", 150, ""},
		{"100e-2", 100, ""},
		{"0.10", 10, ""},
		{"1.000", 100, ""},
		{"1.230", 123, ""},
		{"123456789.99", 12345678999, ""},
		{"0.001", 0, IssueTooManyDecimals},
		{"1.005", 0, IssueTooManyDecimals},
		{"1e-3", 0, IssueTooManyDecimals},
		{"0.000000000000000000000000001", 0, IssueTooManyDecimals},
		{"-1", 0, IssueOutOfRange},
		{"-0.5", 0, IssueOutOfRange},
		{"-1e-9", 0, IssueOutOfRange},
		{"12345678901", 0, IssueOutOfRange},
		{"1e10", 0, IssueOutOfRange},
		{"1e400", 0, IssueOutOfRange},
		{"9" + strings.Repeat("9", 1000), 0, IssueOutOfRange},
		{"1e99999999999999999999999", 0, IssueOutOfRange},
		{"1e-99999999999999999999999", 0, IssueTooManyDecimals},
		{"0e99999999999999999999999", 0, ""},
		{"", 0, IssueInvalidFormat},
		{"abc", 0, IssueInvalidFormat},
		{"1.", 0, IssueInvalidFormat},
		{".5", 0, IssueInvalidFormat},
		{"+1", 0, IssueInvalidFormat},
		{"01", 0, IssueInvalidFormat},
		{"1e", 0, IssueInvalidFormat},
		{"0x10", 0, IssueInvalidFormat},
		{"1_000", 0, IssueInvalidFormat},
		{"NaN", 0, IssueInvalidFormat},
		{"Infinity", 0, IssueInvalidFormat},
		{"1 ", 0, IssueInvalidFormat},
		{`"1"`, 0, IssueInvalidFormat},
		{"null", 0, IssueInvalidFormat},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.40q", tt.lit), func(t *testing.T) {
			got, issue := planHundredths(tt.lit)
			if got != tt.want || issue != tt.issue {
				t.Fatalf("planHundredths(%q) = %d, %q; want %d, %q", tt.lit, got, issue, tt.want, tt.issue)
			}
		})
	}
}

func TestPlanFormatHundredths(t *testing.T) {
	tests := map[int64]string{0: "0.00", 1: "0.01", 10: "0.10", 100: "1.00", 8000: "80.00", 8025: "80.25", 9999999: "99999.99", 999999999: "9999999.99"}
	for h, want := range tests {
		if got := planFormatHundredths(h); got != want {
			t.Errorf("planFormatHundredths(%d) = %q, want %q", h, got, want)
		}
	}
}

func TestParsePlanDecimal(t *testing.T) {
	tests := []struct{ in, want string }{
		{"80.00", "80.00"}, {"0.00", "0.00"}, {"99999.99", "99999.99"}, {"9999999.99", "9999999.99"}, {"7", "7.00"}, {"12.5", "12.50"},
	}
	for _, tt := range tests {
		d, err := ParsePlanDecimal(tt.in)
		if err != nil || d.String() != tt.want {
			t.Errorf("ParsePlanDecimal(%q) = %q, %v; want %q", tt.in, d.String(), err, tt.want)
		}
	}
	for _, bad := range []string{"", "x", "-1", "1.234", "1e50"} {
		if _, err := ParsePlanDecimal(bad); err == nil {
			t.Errorf("ParsePlanDecimal(%q) succeeded, want an error", bad)
		}
	}
}

func TestPlanDecimalMarshalJSON(t *testing.T) {
	tests := []struct{ text, want string }{
		{"80.00", "80.0"},
		{"80.50", "80.5"},
		{"80.25", "80.25"},
		{"80.05", "80.05"},
		{"0.00", "0.0"},
		{"0.10", "0.1"},
		{"100.00", "100.0"},
		{"99999.99", "99999.99"},
		{"9999999.99", "9999999.99"},
		{"80", "80"},   // not canonical, kept as it is
		{"1e2", "1e2"}, // unvalidated client literal, still a JSON number
	}
	for _, tt := range tests {
		got, err := json.Marshal(PlanDecimal{text: tt.text})
		if err != nil || string(got) != tt.want {
			t.Errorf("Marshal(%q) = %s, %v; want %s", tt.text, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "abc", "1."} {
		if _, err := json.Marshal(PlanDecimal{text: bad}); err == nil {
			t.Errorf("Marshal(%q) succeeded, want an error", bad)
		}
	}
}

func TestPlanDecimalUnmarshalJSON(t *testing.T) {
	type holder struct {
		W *PlanDecimal `json:"w"`
	}
	t.Run("number literals are kept as text", func(t *testing.T) {
		for _, lit := range []string{"80", "80.0", "0.1", "-0", "1e2", "12345678901234567890.123456789", "0.30000000000000004"} {
			var h holder
			if err := json.Unmarshal([]byte(`{"w":`+lit+`}`), &h); err != nil {
				t.Fatalf("%s: %v", lit, err)
			}
			if h.W == nil || h.W.text != lit {
				t.Errorf("%s decoded to %v", lit, h.W)
			}
		}
	})
	t.Run("null is nil", func(t *testing.T) {
		h := holder{W: planDec("1")}
		if err := json.Unmarshal([]byte(`{"w":null}`), &h); err != nil {
			t.Fatal(err)
		}
		if h.W != nil {
			t.Errorf("W = %v, want nil", h.W)
		}
	})
	t.Run("other JSON types are a type error", func(t *testing.T) {
		for _, lit := range []string{`"80"`, `"abc"`, `true`, `false`, `[]`, `[1]`, `{}`} {
			var h holder
			err := json.Unmarshal([]byte(`{"w":`+lit+`}`), &h)
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Errorf("%s: err = %v, want *json.UnmarshalTypeError", lit, err)
			}
		}
	})
}

// TestPlanJSONShapesMatchTheSpecs round-trips the examples of specs 06, 07 and
// 10 through the response types: the bytes come out exactly as the spec writes
// them, key order included.
func TestPlanJSONShapesMatchTheSpecs(t *testing.T) {
	compact := func(s string) string {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(s)); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("get-workout-plan (spec 07, also the 200 and 201 body of spec 10)", func(t *testing.T) {
		const example = `{
		  "id": "0195f3a2-aaaa-7000-8000-000000000001",
		  "name": "Push",
		  "description": "Chest / shoulders / triceps",
		  "updated_at": "2026-09-10T07:12:00Z",
		  "created_at": "2026-08-01T10:00:00Z",
		  "server_updated_at": "2026-09-10T07:12:03Z",
		  "deleted_at": null,
		  "exercises": [
		    {
		      "exercise_id": "0195f3a2-bbbb-7000-8000-000000000010",
		      "position": 0,
		      "target_sets": 4,
		      "target_reps": 8,
		      "target_reps_max": 10,
		      "target_weight": 80.0,
		      "target_duration_seconds": null,
		      "target_distance_meters": null,
		      "rest_seconds": 120,
		      "notes": "Pause on chest"
		    }
		  ]
		}`
		var p Plan
		if err := json.Unmarshal([]byte(example), &p); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != compact(example) {
			t.Fatalf("plan JSON:\n got  %s\n want %s", got, compact(example))
		}
	})

	t.Run("a stored plan writes decimals as PostgreSQL gave them", func(t *testing.T) {
		w, _ := ParsePlanDecimal("80.00")
		d, _ := ParsePlanDecimal("1500.50")
		p := Plan{
			ID: uuid.MustParse("0195f3a2-aaaa-7000-8000-000000000001"), Name: "Legs",
			UpdatedAt: time.Date(2026, 9, 10, 7, 12, 0, 500000000, time.UTC),
			CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), ServerUpdatedAt: time.Date(2026, 9, 10, 7, 12, 3, 123456000, time.UTC),
			Exercises: []PlanExercise{{ExerciseID: uuid.MustParse(planTestExerciseID), Position: 2, TargetSets: 3, TargetWeight: &w, TargetDistanceMeters: &d}},
		}
		got, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"id":"0195f3a2-aaaa-7000-8000-000000000001","name":"Legs","description":null,` +
			`"updated_at":"2026-09-10T07:12:00.5Z","created_at":"2026-08-01T10:00:00Z","server_updated_at":"2026-09-10T07:12:03.123456Z","deleted_at":null,` +
			`"exercises":[{"exercise_id":"0195f3a2-bbbb-7000-8000-000000000010","position":2,"target_sets":3,"target_reps":null,"target_reps_max":null,` +
			`"target_weight":80.0,"target_duration_seconds":null,"target_distance_meters":1500.5,"rest_seconds":null,"notes":null}]}`
		if string(got) != want {
			t.Fatalf("plan JSON:\n got  %s\n want %s", got, want)
		}
	})

	t.Run("list without expand (spec 06)", func(t *testing.T) {
		const example = `{
		  "items": [
		    {
		      "id": "0195f3a2-aaaa-7000-8000-000000000001",
		      "name": "Push",
		      "description": "Chest / shoulders / triceps",
		      "exercise_count": 6,
		      "created_at": "2026-08-01T10:00:00Z",
		      "updated_at": "2026-09-10T07:12:00Z",
		      "server_updated_at": "2026-09-10T07:12:03Z",
		      "deleted_at": null
		    }
		  ],
		  "next_cursor": null
		}`
		var page PlanPage[PlanSummary]
		if err := json.Unmarshal([]byte(example), &page); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != compact(example) {
			t.Fatalf("summary page JSON:\n got  %s\n want %s", got, compact(example))
		}
	})

	t.Run("list envelope keeps an empty page an array and a cursor a string", func(t *testing.T) {
		next := "abc"
		got, _ := json.Marshal(PlanPage[Plan]{Items: []Plan{}, NextCursor: &next})
		if want := `{"items":[],"next_cursor":"abc"}`; string(got) != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

// TestPlanResponseKeySets pins the JSON key set of every response shape, so a
// renamed or dropped field fails loudly.
func TestPlanResponseKeySets(t *testing.T) {
	keys := func(v any) []string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	check := func(name string, v any, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := keys(v); !reflect.DeepEqual(got, want) {
			t.Errorf("%s keys = %v, want %v", name, got, want)
		}
	}
	check("Plan", Plan{Exercises: []PlanExercise{}},
		"id", "name", "description", "updated_at", "created_at", "server_updated_at", "deleted_at", "exercises")
	check("PlanExercise", PlanExercise{},
		"exercise_id", "position", "target_sets", "target_reps", "target_reps_max", "target_weight",
		"target_duration_seconds", "target_distance_meters", "rest_seconds", "notes")
	check("PlanSummary", PlanSummary{},
		"id", "name", "description", "exercise_count", "created_at", "updated_at", "server_updated_at", "deleted_at")
	check("PlanPage", PlanPage[Plan]{}, "items", "next_cursor")
}

func TestPlanInputRejectsUnknownFieldsWhenDecodedStrictly(t *testing.T) {
	// The handler decodes with DisallowUnknownFields; PlanInput must not have
	// accepted-but-ignored server fields such as id or created_at.
	for _, body := range []string{
		`{"id":"0195f3a2-aaaa-7000-8000-000000000001"}`,
		`{"created_at":"2026-08-01T10:00:00Z"}`,
		`{"server_updated_at":"2026-08-01T10:00:00Z"}`,
		`{"deleted_at":null}`,
		`{"exercises":[{"name":"Bench"}]}`,
	} {
		dec := json.NewDecoder(strings.NewReader(body))
		dec.DisallowUnknownFields()
		var in PlanInput
		if err := dec.Decode(&in); err == nil {
			t.Errorf("%s decoded without error", body)
		}
	}
}
