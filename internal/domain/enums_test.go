package domain

import (
	"slices"
	"testing"
)

// enumCase describes one enum by its string values, so a single test can cover
// all of them. The expected lists are copied from docs/api/enums.md and
// docs/data-model.md on purpose: they must not be derived from the code.
type enumCase struct {
	name    string
	want    []string
	got     []string
	isValid func(string) bool
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func enumCases() []enumCase {
	return []enumCase{
		{
			name:    "Role",
			want:    []string{"user", "admin"},
			got:     toStrings(Roles),
			isValid: func(s string) bool { return Role(s).IsValid() },
		},
		{
			name:    "Category",
			want:    []string{"strength", "cardio", "mobility"},
			got:     toStrings(Categories),
			isValid: func(s string) bool { return Category(s).IsValid() },
		},
		{
			name: "MuscleGroup",
			want: []string{
				"chest", "upper_back", "lats", "lower_back", "traps", "shoulders",
				"biceps", "triceps", "forearms", "abs", "obliques", "glutes", "quads",
				"hamstrings", "calves", "adductors", "abductors", "full_body",
			},
			got:     toStrings(MuscleGroups),
			isValid: func(s string) bool { return MuscleGroup(s).IsValid() },
		},
		{
			name: "Equipment",
			want: []string{
				"barbell", "dumbbell", "kettlebell", "machine", "cable", "smith_machine",
				"bodyweight", "resistance_band", "cardio_machine", "other",
			},
			got:     toStrings(EquipmentList),
			isValid: func(s string) bool { return Equipment(s).IsValid() },
		},
		{
			name:    "MeasurementType",
			want:    []string{"reps_weight", "reps", "duration", "distance_duration"},
			got:     toStrings(MeasurementTypes),
			isValid: func(s string) bool { return MeasurementType(s).IsValid() },
		},
		{
			name:    "SetType",
			want:    []string{"warmup", "normal", "drop", "failure"},
			got:     toStrings(SetTypes),
			isValid: func(s string) bool { return SetType(s).IsValid() },
		},
		{
			name:    "ImageExt",
			want:    []string{"jpg", "png", "webp"},
			got:     toStrings(ImageExts),
			isValid: func(s string) bool { return ImageExt(s).IsValid() },
		},
	}
}

func TestEnumListsMatchSpec(t *testing.T) {
	for _, tc := range enumCases() {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Errorf("values = %v\nwant     %v", tc.got, tc.want)
			}
		})
	}
}

func TestEnumIsValid(t *testing.T) {
	for _, tc := range enumCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range tc.want {
				if !tc.isValid(v) {
					t.Errorf("IsValid(%q) = false, want true", v)
				}
			}
			// Matching is exact: no case folding, trimming or partial values.
			invalid := []string{"", " ", "unknown", "STRENGTH", "Admin", tc.want[0] + " ", " " + tc.want[0], tc.want[0] + "x", "user,admin"}
			for _, v := range invalid {
				if slices.Contains(tc.want, v) {
					continue
				}
				if tc.isValid(v) {
					t.Errorf("IsValid(%q) = true, want false", v)
				}
			}
		})
	}
}

func TestEnumValuesDoNotOverlapAcrossTypes(t *testing.T) {
	// A value of one enum used as another must be rejected, for example a
	// muscle group passed as a category.
	if Category("chest").IsValid() {
		t.Error(`Category("chest") should be invalid`)
	}
	if MuscleGroup("strength").IsValid() {
		t.Error(`MuscleGroup("strength") should be invalid`)
	}
	if SetType("reps").IsValid() {
		t.Error(`SetType("reps") should be invalid`)
	}
	if MeasurementType("normal").IsValid() {
		t.Error(`MeasurementType("normal") should be invalid`)
	}
}

func TestEnumListsHaveNoDuplicates(t *testing.T) {
	for _, tc := range enumCases() {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, v := range tc.got {
				if seen[v] {
					t.Errorf("duplicate value %q", v)
				}
				seen[v] = true
			}
		})
	}
}

func TestImageExtContentType(t *testing.T) {
	tests := []struct {
		ext  ImageExt
		want string
	}{
		{ImageExtJPG, "image/jpeg"},
		{ImageExtPNG, "image/png"},
		{ImageExtWebP, "image/webp"},
		{ImageExt("gif"), ""},
		{ImageExt(""), ""},
	}
	for _, tc := range tests {
		if got := tc.ext.ContentType(); got != tc.want {
			t.Errorf("ImageExt(%q).ContentType() = %q, want %q", tc.ext, got, tc.want)
		}
	}
	for _, ext := range ImageExts {
		if ext.ContentType() == "" {
			t.Errorf("valid ImageExt %q has no content type", ext)
		}
	}
}
