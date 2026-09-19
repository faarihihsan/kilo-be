package domain

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The exercise resource (specs 08, 09, 18). This file has its JSON shape, the
// request bodies and their validation. Persistence is in store.Exercises, the
// rules around it in service.Exercises.

// ExerciseImage references the stored image file of an exercise: the
// image_hash, image_ext and image_size_bytes columns, which are all set or all
// NULL. The file lives at media.Path(exerciseID, Hash, Ext).
type ExerciseImage struct {
	Hash      string
	Ext       ImageExt
	SizeBytes int64
}

// Exercise is one item of the exercise catalogue in its response shape (specs
// 08, 09, 18 and 20). The JSON has exactly these keys; a deleted exercise keeps
// all its fields.
//
// The store fills Image, the reference to the stored file, and leaves ImageURL
// nil. The service turns Image into ImageURL (the public URL needs
// MEDIA_BASE_URL) before anything is encoded; see newExerciseResponse in
// package service. Nothing outside the service layer should encode an Exercise
// straight from the store.
type Exercise struct {
	ID                    uuid.UUID       `json:"id"`
	Name                  string          `json:"name"`
	Category              Category        `json:"category"`
	PrimaryMuscleGroup    MuscleGroup     `json:"primary_muscle_group"`
	SecondaryMuscleGroups []MuscleGroup   `json:"secondary_muscle_groups"`
	Equipment             Equipment       `json:"equipment"`
	MeasurementType       MeasurementType `json:"measurement_type"`
	Instructions          *string         `json:"instructions"`
	ImageURL              *string         `json:"image_url"`
	CreatedBy             uuid.UUID       `json:"created_by"`
	CreatedAt             time.Time       `json:"created_at"`
	// UpdatedAt is set by the server on every edit, image change and delete;
	// it is the sync cursor of the list endpoint.
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`

	// Image is the stored image file, nil when there is none. It is not part
	// of the JSON.
	Image *ExerciseImage `json:"-"`
}

// ExercisePage is the response of the list endpoint (spec 08).
type ExercisePage struct {
	Items []Exercise `json:"items"`
	// NextCursor is nil on the last page.
	NextCursor *string `json:"next_cursor"`
}

// ExerciseInput is the editable content of an exercise: the body of
// PUT /v1/exercises/{id} (spec 18) and, with an id, of POST /v1/exercises
// (spec 09, ExerciseCreateInput). Call Validate before using it.
type ExerciseInput struct {
	Name                  string          `json:"name"`
	Category              Category        `json:"category"`
	PrimaryMuscleGroup    MuscleGroup     `json:"primary_muscle_group"`
	SecondaryMuscleGroups []MuscleGroup   `json:"secondary_muscle_groups"`
	Equipment             Equipment       `json:"equipment"`
	MeasurementType       MeasurementType `json:"measurement_type"`
	Instructions          *string         `json:"instructions"`
}

// ExerciseCreateInput is the body of POST /v1/exercises. ID is the optional
// client-generated id; it is a string so a malformed one is a 422 on field
// "id" rather than a JSON decoding error.
type ExerciseCreateInput struct {
	ID *string `json:"id"`
	ExerciseInput
}

// Validate checks the input and returns its normalised form: the name
// trimmed, an empty instructions text as nil (no instructions), and a nil
// secondary list as empty. It reports every problem at once as one
// *ValidationError (422):
//
//	name                     required (empty after trimming), too_long, invalid_chars
//	category, equipment,
//	measurement_type,
//	primary_muscle_group     required (empty), invalid_value (not in the enum list)
//	secondary_muscle_groups  too_many (more than 5)
//	secondary_muscle_groups[i]
//	                         invalid_value, duplicate (repeats an earlier
//	                         entry), contains_primary
//	instructions             too_long, invalid_chars
//
// invalid_chars is a NUL character, which PostgreSQL text cannot hold. Lengths
// count characters, not bytes.
func (in ExerciseInput) Validate() (ExerciseInput, error) {
	var v ValidationError
	out := in.validate(&v)
	if err := v.Err(); err != nil {
		return ExerciseInput{}, err
	}
	return out, nil
}

// Validate is ExerciseInput.Validate plus the id: it returns the normalised
// content and the client-supplied id, or uuid.Nil when the client sent none
// and the server must generate one. A malformed id (not the hyphenated UUID
// form, or the nil UUID) is reported as invalid_format on field "id" together
// with the other issues.
func (in ExerciseCreateInput) Validate() (ExerciseInput, uuid.UUID, error) {
	var v ValidationError
	id := uuid.Nil
	if in.ID != nil {
		if parsed, ok := exerciseParseID(*in.ID); ok {
			id = parsed
		} else {
			v.Add("id", IssueInvalidFormat)
		}
	}
	out := in.ExerciseInput.validate(&v)
	if err := v.Err(); err != nil {
		return ExerciseInput{}, uuid.Nil, err
	}
	return out, id, nil
}

// Matches reports whether e has exactly the content of in, which must already
// be normalised by Validate. It is the "identical content" test of an
// idempotent create retry (spec 09) and of a no-op update (spec 18). The
// secondary muscle groups are compared in order.
func (in ExerciseInput) Matches(e Exercise) bool {
	return in.Name == e.Name &&
		in.Category == e.Category &&
		in.PrimaryMuscleGroup == e.PrimaryMuscleGroup &&
		slices.Equal(in.SecondaryMuscleGroups, e.SecondaryMuscleGroups) &&
		in.Equipment == e.Equipment &&
		in.MeasurementType == e.MeasurementType &&
		exerciseTextEqual(in.Instructions, e.Instructions)
}

func (in ExerciseInput) validate(v *ValidationError) ExerciseInput {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	switch {
	case out.Name == "":
		v.Add("name", IssueRequired)
	case utf8.RuneCountInString(out.Name) > ExerciseNameMaxLen:
		v.Add("name", IssueTooLong)
	case strings.ContainsRune(out.Name, 0):
		v.Add("name", IssueInvalidChars)
	}

	exerciseEnumIssue(v, "category", string(in.Category), in.Category.IsValid())
	exerciseEnumIssue(v, "primary_muscle_group", string(in.PrimaryMuscleGroup), in.PrimaryMuscleGroup.IsValid())

	out.SecondaryMuscleGroups = exerciseValidateSecondary(v, in.SecondaryMuscleGroups, in.PrimaryMuscleGroup)

	exerciseEnumIssue(v, "equipment", string(in.Equipment), in.Equipment.IsValid())
	exerciseEnumIssue(v, "measurement_type", string(in.MeasurementType), in.MeasurementType.IsValid())

	out.Instructions = nil
	if in.Instructions != nil {
		text := *in.Instructions
		switch {
		case utf8.RuneCountInString(text) > ExerciseInstructionsMaxLen:
			v.Add("instructions", IssueTooLong)
		case strings.ContainsRune(text, 0):
			v.Add("instructions", IssueInvalidChars)
		}
		if text != "" {
			out.Instructions = &text
		}
	}
	return out
}

// exerciseEnumIssue reports an empty enum value as required and any other
// value outside its list as invalid_value.
func exerciseEnumIssue(v *ValidationError, field, value string, valid bool) {
	switch {
	case value == "":
		v.Add(field, IssueRequired)
	case !valid:
		v.Add(field, IssueInvalidValue)
	}
}

// exerciseValidateSecondary checks the secondary muscle groups and returns a
// non-nil copy. An entry is reported at most once: invalid, else duplicate,
// else contains_primary.
func exerciseValidateSecondary(v *ValidationError, groups []MuscleGroup, primary MuscleGroup) []MuscleGroup {
	const field = "secondary_muscle_groups"
	if len(groups) > MaxSecondaryMuscleGroups {
		v.Add(field, IssueTooMany)
	}
	seen := make(map[MuscleGroup]bool, len(groups))
	for i, g := range groups {
		switch {
		case !g.IsValid():
			v.Add(FieldIndex(field, i), IssueInvalidValue)
			continue
		case seen[g]:
			v.Add(FieldIndex(field, i), IssueDuplicate)
		case g == primary:
			v.Add(FieldIndex(field, i), IssueContainsPrimary)
		}
		seen[g] = true
	}
	return append([]MuscleGroup{}, groups...)
}

// exerciseParseID accepts the canonical hyphenated UUID (any case) and
// rejects the nil UUID, which would look like "no id" to the callers.
func exerciseParseID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

func exerciseTextEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
