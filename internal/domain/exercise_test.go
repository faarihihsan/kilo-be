package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func exerciseValidInput() ExerciseInput {
	return ExerciseInput{
		Name:                  "Incline Dumbbell Press",
		Category:              CategoryStrength,
		PrimaryMuscleGroup:    MuscleGroupChest,
		SecondaryMuscleGroups: []MuscleGroup{MuscleGroupShoulders, MuscleGroupTriceps},
		Equipment:             EquipmentDumbbell,
		MeasurementType:       MeasurementTypeRepsWeight,
		Instructions:          exerciseStr("Set bench to 30 degrees..."),
	}
}

func exerciseStr(s string) *string { return &s }

// exerciseIssues returns the validation issues of err as "field:issue" strings,
// failing the test when err is not a *ValidationError.
func exerciseIssues(t *testing.T, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v (%T), want *ValidationError", err, err)
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Field + ":" + is.Issue
	}
	return out
}

func TestExerciseInputValidateAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExerciseInput)
		check  func(t *testing.T, got ExerciseInput)
	}{
		{"spec example", func(*ExerciseInput) {}, func(t *testing.T, got ExerciseInput) {
			if !reflect.DeepEqual(got, exerciseValidInput()) {
				t.Errorf("got %+v, want the input unchanged", got)
			}
		}},
		{"name is trimmed", func(in *ExerciseInput) { in.Name = " \t Squat \n" }, func(t *testing.T, got ExerciseInput) {
			if got.Name != "Squat" {
				t.Errorf("Name = %q, want Squat", got.Name)
			}
		}},
		{"name of one character", func(in *ExerciseInput) { in.Name = "x" }, nil},
		{"name of 100 characters", func(in *ExerciseInput) { in.Name = strings.Repeat("a", 100) }, nil},
		{"name of 100 multi-byte characters", func(in *ExerciseInput) { in.Name = strings.Repeat("é", 100) }, nil},
		{"trimmed name of 100 characters with padding", func(in *ExerciseInput) { in.Name = "  " + strings.Repeat("a", 100) + "  " }, nil},
		{"nil secondary becomes empty", func(in *ExerciseInput) { in.SecondaryMuscleGroups = nil }, func(t *testing.T, got ExerciseInput) {
			if got.SecondaryMuscleGroups == nil || len(got.SecondaryMuscleGroups) != 0 {
				t.Errorf("SecondaryMuscleGroups = %#v, want empty non-nil", got.SecondaryMuscleGroups)
			}
		}},
		{"five secondary groups", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders, MuscleGroupTriceps, MuscleGroupBiceps, MuscleGroupAbs, MuscleGroupLats}
		}, nil},
		{"nil instructions", func(in *ExerciseInput) { in.Instructions = nil }, func(t *testing.T, got ExerciseInput) {
			if got.Instructions != nil {
				t.Errorf("Instructions = %q, want nil", *got.Instructions)
			}
		}},
		{"empty instructions become nil", func(in *ExerciseInput) { in.Instructions = exerciseStr("") }, func(t *testing.T, got ExerciseInput) {
			if got.Instructions != nil {
				t.Errorf("Instructions = %q, want nil", *got.Instructions)
			}
		}},
		{"instructions are not trimmed", func(in *ExerciseInput) { in.Instructions = exerciseStr("  x \n") }, func(t *testing.T, got ExerciseInput) {
			if got.Instructions == nil || *got.Instructions != "  x \n" {
				t.Errorf("Instructions = %v, want the text unchanged", got.Instructions)
			}
		}},
		{"instructions of 4000 characters", func(in *ExerciseInput) { in.Instructions = exerciseStr(strings.Repeat("é", 4000)) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := exerciseValidInput()
			tt.mutate(&in)
			got, err := in.Validate()
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestExerciseInputValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExerciseInput)
		want   []string
	}{
		{"empty name", func(in *ExerciseInput) { in.Name = "" }, []string{"name:required"}},
		{"blank name", func(in *ExerciseInput) { in.Name = " \t\n " }, []string{"name:required"}},
		{"name of 101 characters", func(in *ExerciseInput) { in.Name = strings.Repeat("a", 101) }, []string{"name:too_long"}},
		{"name of 101 multi-byte characters", func(in *ExerciseInput) { in.Name = strings.Repeat("é", 101) }, []string{"name:too_long"}},
		{"name with NUL", func(in *ExerciseInput) { in.Name = "a\x00b" }, []string{"name:invalid_chars"}},

		{"missing category", func(in *ExerciseInput) { in.Category = "" }, []string{"category:required"}},
		{"unknown category", func(in *ExerciseInput) { in.Category = "yoga" }, []string{"category:invalid_value"}},
		{"category in wrong case", func(in *ExerciseInput) { in.Category = "Strength" }, []string{"category:invalid_value"}},
		{"missing primary", func(in *ExerciseInput) { in.PrimaryMuscleGroup = "" }, []string{"primary_muscle_group:required"}},
		{"unknown primary", func(in *ExerciseInput) { in.PrimaryMuscleGroup = "wings" }, []string{"primary_muscle_group:invalid_value"}},
		{"missing equipment", func(in *ExerciseInput) { in.Equipment = "" }, []string{"equipment:required"}},
		{"unknown equipment", func(in *ExerciseInput) { in.Equipment = "trampoline" }, []string{"equipment:invalid_value"}},
		{"missing measurement type", func(in *ExerciseInput) { in.MeasurementType = "" }, []string{"measurement_type:required"}},
		{"unknown measurement type", func(in *ExerciseInput) { in.MeasurementType = "vibes" }, []string{"measurement_type:invalid_value"}},

		{"six secondary groups", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders, MuscleGroupTriceps, MuscleGroupBiceps, MuscleGroupAbs, MuscleGroupLats, MuscleGroupTraps}
		}, []string{"secondary_muscle_groups:too_many"}},
		{"invalid secondary group", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders, "wings"}
		}, []string{"secondary_muscle_groups[1]:invalid_value"}},
		{"empty secondary group", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{""}
		}, []string{"secondary_muscle_groups[0]:invalid_value"}},
		{"duplicate secondary group", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders, MuscleGroupTriceps, MuscleGroupShoulders}
		}, []string{"secondary_muscle_groups[2]:duplicate"}},
		{"secondary contains primary", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders, MuscleGroupChest}
		}, []string{"secondary_muscle_groups[1]:contains_primary"}},
		{"primary listed twice as secondary", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupChest, MuscleGroupChest}
		}, []string{"secondary_muscle_groups[0]:contains_primary", "secondary_muscle_groups[1]:duplicate"}},
		{"invalid secondary equal to an invalid primary is only invalid", func(in *ExerciseInput) {
			in.PrimaryMuscleGroup = "wings"
			in.SecondaryMuscleGroups = []MuscleGroup{"wings"}
		}, []string{"primary_muscle_group:invalid_value", "secondary_muscle_groups[0]:invalid_value"}},

		{"instructions of 4001 characters", func(in *ExerciseInput) { in.Instructions = exerciseStr(strings.Repeat("a", 4001)) }, []string{"instructions:too_long"}},
		{"instructions of 4001 multi-byte characters", func(in *ExerciseInput) { in.Instructions = exerciseStr(strings.Repeat("é", 4001)) }, []string{"instructions:too_long"}},
		{"instructions with NUL", func(in *ExerciseInput) { in.Instructions = exerciseStr("a\x00") }, []string{"instructions:invalid_chars"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := exerciseValidInput()
			tt.mutate(&in)
			_, err := in.Validate()
			if got := exerciseIssues(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExerciseInputValidateCollectsAllIssues(t *testing.T) {
	in := ExerciseInput{
		Name:                  strings.Repeat("a", 101),
		Category:              "yoga",
		PrimaryMuscleGroup:    MuscleGroupChest,
		SecondaryMuscleGroups: []MuscleGroup{MuscleGroupChest, "wings", MuscleGroupAbs, MuscleGroupAbs},
		Equipment:             "",
		MeasurementType:       "vibes",
		Instructions:          exerciseStr(strings.Repeat("a", 4001)),
	}
	_, err := in.Validate()
	want := []string{
		"name:too_long",
		"category:invalid_value",
		"secondary_muscle_groups[0]:contains_primary",
		"secondary_muscle_groups[1]:invalid_value",
		"secondary_muscle_groups[3]:duplicate",
		"equipment:required",
		"measurement_type:invalid_value",
		"instructions:too_long",
	}
	if got := exerciseIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
}

func TestExerciseInputValidateAllZeroValueReportsEveryRequiredField(t *testing.T) {
	_, err := ExerciseInput{}.Validate()
	want := []string{
		"name:required", "category:required", "primary_muscle_group:required",
		"equipment:required", "measurement_type:required",
	}
	if got := exerciseIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
}

func TestExerciseInputValidateDoesNotModifyTheReceiver(t *testing.T) {
	in := exerciseValidInput()
	in.Name = "  Squat  "
	in.Instructions = exerciseStr("")
	in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders}
	got, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "  Squat  " || in.Instructions == nil {
		t.Errorf("Validate changed its receiver: %+v", in)
	}
	got.SecondaryMuscleGroups[0] = MuscleGroupAbs
	if in.SecondaryMuscleGroups[0] != MuscleGroupShoulders {
		t.Error("the returned secondary list shares memory with the input")
	}
}

func TestExerciseEveryEnumValueIsAccepted(t *testing.T) {
	for _, c := range Categories {
		in := exerciseValidInput()
		in.Category = c
		if _, err := in.Validate(); err != nil {
			t.Errorf("category %q: %v", c, err)
		}
	}
	for _, e := range EquipmentList {
		in := exerciseValidInput()
		in.Equipment = e
		if _, err := in.Validate(); err != nil {
			t.Errorf("equipment %q: %v", e, err)
		}
	}
	for _, m := range MeasurementTypes {
		in := exerciseValidInput()
		in.MeasurementType = m
		if _, err := in.Validate(); err != nil {
			t.Errorf("measurement_type %q: %v", m, err)
		}
	}
	for _, g := range MuscleGroups {
		in := exerciseValidInput()
		in.PrimaryMuscleGroup = g
		in.SecondaryMuscleGroups = nil
		if _, err := in.Validate(); err != nil {
			t.Errorf("primary %q: %v", g, err)
		}
	}
}

func TestExerciseCreateInputValidateID(t *testing.T) {
	const canonical = "0195f3a2-bbbb-7000-8000-000000000011"
	tests := []struct {
		name    string
		id      *string
		want    uuid.UUID
		wantErr []string
	}{
		{"omitted", nil, uuid.Nil, nil},
		{"canonical", exerciseStr(canonical), uuid.MustParse(canonical), nil},
		{"upper case is folded", exerciseStr(strings.ToUpper(canonical)), uuid.MustParse(canonical), nil},
		{"empty", exerciseStr(""), uuid.Nil, []string{"id:invalid_format"}},
		{"not a uuid", exerciseStr("bench-press"), uuid.Nil, []string{"id:invalid_format"}},
		{"without hyphens", exerciseStr(strings.ReplaceAll(canonical, "-", "")), uuid.Nil, []string{"id:invalid_format"}},
		{"urn form", exerciseStr("urn:uuid:" + canonical), uuid.Nil, []string{"id:invalid_format"}},
		{"braced", exerciseStr("{" + canonical + "}"), uuid.Nil, []string{"id:invalid_format"}},
		{"nil uuid", exerciseStr(uuid.Nil.String()), uuid.Nil, []string{"id:invalid_format"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := ExerciseCreateInput{ID: tt.id, ExerciseInput: exerciseValidInput()}
			content, id, err := in.Validate()
			if tt.wantErr != nil {
				if got := exerciseIssues(t, err); !slices.Equal(got, tt.wantErr) {
					t.Errorf("issues = %v, want %v", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if id != tt.want {
				t.Errorf("id = %v, want %v", id, tt.want)
			}
			if content.Name != "Incline Dumbbell Press" {
				t.Errorf("content = %+v", content)
			}
		})
	}
}

func TestExerciseCreateInputValidateReportsIDFirstWithTheOtherIssues(t *testing.T) {
	in := ExerciseCreateInput{ID: exerciseStr("nope"), ExerciseInput: ExerciseInput{Name: "x"}}
	_, _, err := in.Validate()
	want := []string{"id:invalid_format", "category:required", "primary_muscle_group:required", "equipment:required", "measurement_type:required"}
	if got := exerciseIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
}

func TestExerciseCreateInputJSON(t *testing.T) {
	body := `{
		"id": "0195f3a2-bbbb-7000-8000-000000000011",
		"name": "Incline Dumbbell Press",
		"category": "strength",
		"primary_muscle_group": "chest",
		"secondary_muscle_groups": ["shoulders", "triceps"],
		"equipment": "dumbbell",
		"measurement_type": "reps_weight",
		"instructions": "Set bench to 30 degrees..."
	}`
	var in ExerciseCreateInput
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content, id, err := in.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.String() != "0195f3a2-bbbb-7000-8000-000000000011" || !reflect.DeepEqual(content, exerciseValidInput()) {
		t.Errorf("id = %v, content = %+v", id, content)
	}
}

func TestExerciseInputMatches(t *testing.T) {
	stored := Exercise{
		Name:                  "Incline Dumbbell Press",
		Category:              CategoryStrength,
		PrimaryMuscleGroup:    MuscleGroupChest,
		SecondaryMuscleGroups: []MuscleGroup{MuscleGroupShoulders, MuscleGroupTriceps},
		Equipment:             EquipmentDumbbell,
		MeasurementType:       MeasurementTypeRepsWeight,
		Instructions:          exerciseStr("Set bench to 30 degrees..."),
	}
	tests := []struct {
		name   string
		mutate func(*ExerciseInput)
		want   bool
	}{
		{"identical", func(*ExerciseInput) {}, true},
		{"name case differs", func(in *ExerciseInput) { in.Name = "incline dumbbell press" }, false},
		{"category differs", func(in *ExerciseInput) { in.Category = CategoryMobility }, false},
		{"primary differs", func(in *ExerciseInput) { in.PrimaryMuscleGroup = MuscleGroupLats }, false},
		{"secondary order differs", func(in *ExerciseInput) {
			in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupTriceps, MuscleGroupShoulders}
		}, false},
		{"secondary shorter", func(in *ExerciseInput) { in.SecondaryMuscleGroups = []MuscleGroup{MuscleGroupShoulders} }, false},
		{"equipment differs", func(in *ExerciseInput) { in.Equipment = EquipmentBarbell }, false},
		{"measurement type differs", func(in *ExerciseInput) { in.MeasurementType = MeasurementTypeReps }, false},
		{"instructions differ", func(in *ExerciseInput) { in.Instructions = exerciseStr("other") }, false},
		{"instructions removed", func(in *ExerciseInput) { in.Instructions = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := exerciseValidInput()
			tt.mutate(&in)
			if got := in.Matches(stored); got != tt.want {
				t.Errorf("Matches = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("no instructions on both sides", func(t *testing.T) {
		in := exerciseValidInput()
		in.Instructions = nil
		s := stored
		s.Instructions = nil
		if !in.Matches(s) {
			t.Error("Matches = false, want true")
		}
	})
	t.Run("nil and empty secondary lists are the same", func(t *testing.T) {
		in := exerciseValidInput()
		in.SecondaryMuscleGroups = []MuscleGroup{}
		s := stored
		s.SecondaryMuscleGroups = nil
		if !in.Matches(s) {
			t.Error("Matches = false, want true")
		}
	})
}

// TestExerciseJSONKeys pins the response shape of specs 08 and 09: exactly
// these keys, in this order, with null for what is absent.
func TestExerciseJSONKeys(t *testing.T) {
	created := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	e := Exercise{
		ID:                    uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010"),
		Name:                  "Bench Press",
		Category:              CategoryStrength,
		PrimaryMuscleGroup:    MuscleGroupChest,
		SecondaryMuscleGroups: []MuscleGroup{MuscleGroupTriceps, MuscleGroupShoulders},
		Equipment:             EquipmentBarbell,
		MeasurementType:       MeasurementTypeRepsWeight,
		CreatedBy:             uuid.MustParse("0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90"),
		CreatedAt:             created,
		UpdatedAt:             created,
		// The stored image reference is not part of the JSON.
		Image: &ExerciseImage{Hash: "9f2c4e1ab37d05c6", Ext: ImageExtWebP, SizeBytes: 100},
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"0195f3a2-bbbb-7000-8000-000000000010","name":"Bench Press","category":"strength",` +
		`"primary_muscle_group":"chest","secondary_muscle_groups":["triceps","shoulders"],"equipment":"barbell",` +
		`"measurement_type":"reps_weight","instructions":null,"image_url":null,` +
		`"created_by":"0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90","created_at":"2026-08-01T10:00:00Z",` +
		`"updated_at":"2026-08-01T10:00:00Z","deleted_at":null}`
	if string(got) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestExercisePageJSON(t *testing.T) {
	got, err := json.Marshal(ExercisePage{Items: []Exercise{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"items":[],"next_cursor":null}` {
		t.Errorf("JSON = %s", got)
	}
	next := "abc"
	got, _ = json.Marshal(ExercisePage{Items: []Exercise{}, NextCursor: &next})
	if string(got) != `{"items":[],"next_cursor":"abc"}` {
		t.Errorf("JSON = %s", got)
	}
}
