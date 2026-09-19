package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Workout plans (specs 06, 07, 10): the response shapes, the request shape and
// its validation. Every response shape is one type, with JSON tags in the key
// order of the spec examples:
//
//	Plan                 spec 07, the body of GET, PUT and of `current` in a stale conflict
//	PlanSummary          spec 06 items without expand
//	PlanPage[Plan]       spec 06 with expand=exercises
//	PlanPage[PlanSummary] spec 06 without expand

// PlanExercise is one exercise row of a plan. It is the item of `exercises` in
// responses and, once validated, in a save. There is no exercise name: the app
// resolves exercise_id through the exercise master (spec 07).
type PlanExercise struct {
	ExerciseID            uuid.UUID    `json:"exercise_id"`
	Position              int          `json:"position"`
	TargetSets            int          `json:"target_sets"`
	TargetReps            *int         `json:"target_reps"`
	TargetRepsMax         *int         `json:"target_reps_max"`
	TargetWeight          *PlanDecimal `json:"target_weight"`
	TargetDurationSeconds *int         `json:"target_duration_seconds"`
	TargetDistanceMeters  *PlanDecimal `json:"target_distance_meters"`
	RestSeconds           *int         `json:"rest_seconds"`
	Notes                 *string      `json:"notes"`
}

// Plan is a saved workout plan with all its exercises (spec 07). UpdatedAt is
// the client's last-modified time (the conflict rule input, column
// client_updated_at), ServerUpdatedAt the server time of the last accepted
// write or delete (the sync cursor). All times are UTC. Exercises is never nil.
type Plan struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	Description     *string        `json:"description"`
	UpdatedAt       time.Time      `json:"updated_at"`
	CreatedAt       time.Time      `json:"created_at"`
	ServerUpdatedAt time.Time      `json:"server_updated_at"`
	DeletedAt       *time.Time     `json:"deleted_at"`
	Exercises       []PlanExercise `json:"exercises"`
}

// PlanSummary is a plan in the list without expand (spec 06).
type PlanSummary struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Description     *string    `json:"description"`
	ExerciseCount   int        `json:"exercise_count"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ServerUpdatedAt time.Time  `json:"server_updated_at"`
	DeletedAt       *time.Time `json:"deleted_at"`
}

// PlanPage is the list envelope (conventions: Pagination). Items is never nil.
type PlanPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// PlanListParams are the parsed query parameters of GET /v1/workout-plans.
type PlanListParams struct {
	// Limit is 1..MaxPageLimit; the caller validates it.
	Limit int
	// Cursor is the opaque next_cursor of a previous page, or "".
	Cursor string
	// UpdatedSince switches to sync mode: rows with server_updated_at after it,
	// including soft-deleted ones, ordered by (server_updated_at, id).
	UpdatedSince *time.Time
	// IncludeDeleted lists soft-deleted plans in the default order. It is
	// implied by UpdatedSince.
	IncludeDeleted bool
	// Expand asks for full plans (expand=exercises) instead of summaries.
	Expand bool
}

// PlanInput is the request body of PUT /v1/workout-plans/{id}, as decoded.
// Fields are pointers so a missing value can be told from a zero one, and
// updated_at and exercise_id stay text so that a malformed value is a 422 with
// a field path rather than a 400. Validate turns it into a PlanSpec.
type PlanInput struct {
	Name        *string             `json:"name"`
	Description *string             `json:"description"`
	UpdatedAt   *string             `json:"updated_at"`
	Exercises   []PlanExerciseInput `json:"exercises"`
}

// PlanExerciseInput is one item of PlanInput.Exercises.
type PlanExerciseInput struct {
	ExerciseID            *string      `json:"exercise_id"`
	Position              *int         `json:"position"`
	TargetSets            *int         `json:"target_sets"`
	TargetReps            *int         `json:"target_reps"`
	TargetRepsMax         *int         `json:"target_reps_max"`
	TargetWeight          *PlanDecimal `json:"target_weight"`
	TargetDurationSeconds *int         `json:"target_duration_seconds"`
	TargetDistanceMeters  *PlanDecimal `json:"target_distance_meters"`
	RestSeconds           *int         `json:"rest_seconds"`
	Notes                 *string      `json:"notes"`
}

// PlanSpec is a validated save request: the full state of the plan. UpdatedAt
// is UTC at microsecond precision. Exercises is never nil and has unique
// positions; the exercise ids are not yet checked against the exercise master.
type PlanSpec struct {
	Name        string
	Description *string
	UpdatedAt   time.Time
	Exercises   []PlanExercise
}

// Validate checks the request and returns every problem at once as a
// *ValidationError (422), with JSON field paths such as
// exercises[2].target_reps_max. now is the server time for the 5 minute
// future rule.
func (in PlanInput) Validate(now time.Time) (PlanSpec, error) {
	var v ValidationError
	spec := PlanSpec{Exercises: []PlanExercise{}}

	if in.Name == nil {
		v.Add("name", IssueRequired)
	} else {
		spec.Name = *in.Name
		planCheckText(&v, "name", spec.Name, PlanNameMinLen, PlanNameMaxLen)
	}

	if in.Description != nil {
		planCheckText(&v, "description", *in.Description, 0, PlanDescriptionMaxLen)
		spec.Description = in.Description
	}

	switch {
	case in.UpdatedAt == nil:
		v.Add("updated_at", IssueRequired)
	default:
		t, err := time.Parse(time.RFC3339, *in.UpdatedAt)
		if err != nil {
			v.Add("updated_at", IssueInvalidFormat)
			break
		}
		spec.UpdatedAt = t.UTC().Truncate(time.Microsecond)
		var future *ValidationError
		if errors.As(CheckNotFuture(spec.UpdatedAt, now), &future) {
			v.Issues = append(v.Issues, future.Issues...)
		}
	}

	if in.Exercises == nil {
		v.Add("exercises", IssueRequired)
	} else {
		items := in.Exercises
		if len(items) > MaxExercisesPerPlan {
			v.Add("exercises", IssueTooMany)
			items = items[:MaxExercisesPerPlan] // do not validate an unbounded tail
		}
		positions := make(map[int]struct{}, len(items))
		for i, item := range items {
			spec.Exercises = append(spec.Exercises, item.validate(&v, i, positions))
		}
	}

	if err := v.Err(); err != nil {
		return PlanSpec{}, err
	}
	return spec, nil
}

// validate checks item i of exercises and returns its validated form (only
// meaningful when v stays empty). positions holds the positions seen so far.
func (in PlanExerciseInput) validate(v *ValidationError, i int, positions map[int]struct{}) PlanExercise {
	field := func(name string) string { return FieldIndex("exercises", i) + "." + name }
	var ex PlanExercise

	switch {
	case in.ExerciseID == nil:
		v.Add(field("exercise_id"), IssueRequired)
	default:
		// Same rule as a path id: the canonical hyphenated form only.
		id, err := uuid.Parse(*in.ExerciseID)
		if err != nil || len(*in.ExerciseID) != 36 {
			v.Add(field("exercise_id"), IssueInvalidFormat)
			break
		}
		ex.ExerciseID = id
	}

	switch {
	case in.Position == nil:
		v.Add(field("position"), IssueRequired)
	case planCheckInt(v, field("position"), *in.Position, 0, MaxIntColumn):
		ex.Position = *in.Position
		if _, dup := positions[ex.Position]; dup {
			v.Add(field("position"), IssueDuplicate)
		}
		positions[ex.Position] = struct{}{}
	}

	switch {
	case in.TargetSets == nil:
		v.Add(field("target_sets"), IssueRequired)
	case planCheckInt(v, field("target_sets"), *in.TargetSets, PlanTargetSetsMin, PlanTargetSetsMax):
		ex.TargetSets = *in.TargetSets
	}

	if in.TargetReps != nil && planCheckInt(v, field("target_reps"), *in.TargetReps, PlanTargetRepsMin, MaxIntColumn) {
		ex.TargetReps = in.TargetReps
	}
	if in.TargetRepsMax != nil {
		switch {
		case !planCheckInt(v, field("target_reps_max"), *in.TargetRepsMax, PlanTargetRepsMin, MaxIntColumn):
		case in.TargetReps == nil:
			// The range needs its lower end; report the missing value.
			v.Add(field("target_reps"), IssueRequired)
		case *in.TargetRepsMax < *in.TargetReps:
			v.Add(field("target_reps_max"), IssueOutOfRange)
		default:
			ex.TargetRepsMax = in.TargetRepsMax
		}
	}

	ex.TargetWeight = planCheckDecimal(v, field("target_weight"), in.TargetWeight, planMaxWeightHundredths)
	ex.TargetDistanceMeters = planCheckDecimal(v, field("target_distance_meters"), in.TargetDistanceMeters, planMaxDistanceHundredths)

	if in.TargetDurationSeconds != nil && planCheckInt(v, field("target_duration_seconds"), *in.TargetDurationSeconds, 0, MaxIntColumn) {
		ex.TargetDurationSeconds = in.TargetDurationSeconds
	}
	if in.RestSeconds != nil && planCheckInt(v, field("rest_seconds"), *in.RestSeconds, PlanRestSecondsMin, PlanRestSecondsMax) {
		ex.RestSeconds = in.RestSeconds
	}
	if in.Notes != nil {
		planCheckText(v, field("notes"), *in.Notes, 0, PlanExerciseNotesMaxLen)
		ex.Notes = in.Notes
	}
	return ex
}

// planCheckText validates a string by length in characters (runes, not
// bytes). NUL and invalid UTF-8 are rejected because PostgreSQL text cannot
// store them.
func planCheckText(v *ValidationError, field, s string, minLen, maxLen int) {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		v.Add(field, IssueInvalidChars)
		return
	}
	switch n := utf8.RuneCountInString(s); {
	case n < minLen:
		v.Add(field, IssueTooShort)
	case n > maxLen:
		v.Add(field, IssueTooLong)
	}
}

// planCheckInt reports whether n is within [lo, hi] and records out_of_range
// when it is not.
func planCheckInt(v *ValidationError, field string, n, lo, hi int) bool {
	if n < lo || n > hi {
		v.Add(field, IssueOutOfRange)
		return false
	}
	return true
}

// planCheckDecimal validates an optional decimal against [0, max hundredths]
// and returns it in canonical form, or nil when absent or invalid.
func planCheckDecimal(v *ValidationError, field string, d *PlanDecimal, maxHundredths int64) *PlanDecimal {
	if d == nil {
		return nil
	}
	h, issue := planHundredths(d.text)
	if issue == "" && h > maxHundredths {
		issue = IssueOutOfRange
	}
	if issue != "" {
		v.Add(field, issue)
		return nil
	}
	c := PlanDecimal{text: planFormatHundredths(h)}
	return &c
}

// Column maxima in hundredths: numeric(7,2) and numeric(9,2).
const (
	planMaxWeightHundredths   = int64(MaxWeightKg * 100)
	planMaxDistanceHundredths = int64(MaxDistanceMeters * 100)
)

// PlanDecimal is a weight (kg) or distance (m) that is kept as decimal text
// from the request to the database and back, and never becomes a float, so
// 0.1 stays 0.1 and "more than 2 decimals" can be judged on what the client
// wrote. Decoded from a JSON number only (a string or bool is a wrong-type
// 400); PlanInput.Validate then checks it and replaces it with the canonical
// text "<int>.<two decimals>". It is written as a JSON number with trailing
// zeros trimmed but one decimal kept: 80.0, 80.5, 0.25.
type PlanDecimal struct{ text string }

// ParsePlanDecimal reads a decimal from text such as the "80.00" PostgreSQL
// gives for a numeric(7,2), or a JSON number literal. It fails when the text
// is not a non-negative number with at most 2 decimals.
func ParsePlanDecimal(s string) (PlanDecimal, error) {
	h, issue := planHundredths(s)
	if issue != "" {
		return PlanDecimal{}, fmt.Errorf("domain: invalid plan decimal %q: %s", s, issue)
	}
	return PlanDecimal{text: planFormatHundredths(h)}, nil
}

// String is the decimal text as held: canonical after Validate or
// ParsePlanDecimal ("80.00"), the literal as sent before.
func (d PlanDecimal) String() string { return d.text }

// UnmarshalJSON keeps the literal of a JSON number. Checking the value is
// PlanInput.Validate's job, so that all problems are reported together.
func (d *PlanDecimal) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return &json.UnmarshalTypeError{Value: "empty", Type: reflect.TypeFor[PlanDecimal]()}
	}
	switch c := b[0]; {
	case c == '-' || c >= '0' && c <= '9':
		d.text = string(b)
		return nil
	case bytes.Equal(b, []byte("null")):
		return nil // by convention null is a no-op
	default:
		return &json.UnmarshalTypeError{Value: planJSONKind(c), Type: reflect.TypeFor[PlanDecimal]()}
	}
}

// planJSONKind names the JSON value that starts with c, as encoding/json does
// in its type errors.
func planJSONKind(c byte) string {
	switch c {
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case '{':
		return "object"
	case '[':
		return "array"
	}
	return "value"
}

// MarshalJSON writes the decimal as a JSON number.
func (d PlanDecimal) MarshalJSON() ([]byte, error) {
	if !planIsNumber(d.text) {
		return nil, fmt.Errorf("domain: PlanDecimal %q is not a JSON number", d.text)
	}
	text := d.text
	if !strings.ContainsAny(text, "eE") && strings.Contains(text, ".") {
		text = strings.TrimRight(text, "0")
		if strings.HasSuffix(text, ".") {
			text += "0"
		}
	}
	return []byte(text), nil
}

// planIsNumber reports whether s is a JSON number literal. json.Valid also
// accepts other values and surrounding white space, so the first and last byte
// are checked too: a number starts with '-' or a digit and ends with a digit.
func planIsNumber(s string) bool {
	if s == "" || s[0] != '-' && (s[0] < '0' || s[0] > '9') || s[len(s)-1] < '0' || s[len(s)-1] > '9' {
		return false
	}
	return json.Valid([]byte(s))
}

// planExponentLimit bounds how far an exponent may shift the decimal point
// before the value is out of range (or has too many decimals) whatever its
// digits are, so the arithmetic below stays small for any input.
const planExponentLimit = 1 << 20

// planHundredths converts a JSON number literal to hundredths of a unit
// without floats. The issue is "" on success, IssueInvalidFormat for text that
// is not a number, IssueOutOfRange for a negative or absurdly large value
// (larger than 10 integer digits; the column limits are checked by the
// caller) and IssueTooManyDecimals for more than 2 decimals. -0 is 0.
func planHundredths(lit string) (h int64, issue string) {
	if !planIsNumber(lit) {
		return 0, IssueInvalidFormat
	}
	neg := strings.HasPrefix(lit, "-")
	s := strings.TrimPrefix(lit, "-")

	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		// The grammar is valid, so an error can only be ErrRange, and Atoi
		// then returns the clamped extreme with the right sign.
		n, _ := strconv.Atoi(s[i+1:])
		exp = max(-planExponentLimit, min(planExponentLimit, n))
		s = s[:i]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	digits := intPart + frac
	point := len(intPart) + exp // the decimal point sits after digits[:point]

	// Drop leading zeros (they only move the point) and trailing zeros.
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return 0, "" // zero, also -0
	}
	point -= len(digits) - len(trimmed)
	digits = strings.TrimRight(trimmed, "0")

	switch decimals := len(digits) - point; {
	case neg:
		return 0, IssueOutOfRange
	case point > 10:
		return 0, IssueOutOfRange
	case decimals > DecimalPlaces:
		return 0, IssueTooManyDecimals
	}
	// value = digits * 10^(point-len(digits)); hundredths shift it by 2 more.
	h, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, IssueOutOfRange
	}
	for shift := point - len(digits) + DecimalPlaces; shift > 0; shift-- {
		h *= 10
	}
	return h, ""
}

// planFormatHundredths is the canonical text of a value in hundredths.
func planFormatHundredths(h int64) string {
	return strconv.FormatInt(h/100, 10) + "." + fmt.Sprintf("%02d", h%100)
}

// --- list cursors ------------------------------------------------------------

// planNameCursorTag is the first part of a default-order plan cursor. It keeps
// a name cursor from being decoded as a sync cursor and vice versa.
const planNameCursorTag = "name"

// EncodePlanNameCursor is the opaque keyset cursor for the default plan order
// (lower(name), id). lowerName is the value of lower(name) as the database
// returned it for the last row of the page.
func EncodePlanNameCursor(lowerName string, id uuid.UUID) string {
	return EncodeKeyCursor(planNameCursorTag, lowerName, id.String())
}

// DecodePlanNameCursor is the strict inverse of EncodePlanNameCursor: it
// returns the lower(name) and id it carries, or a *BadRequestError for any
// malformed, tampered or foreign cursor.
func DecodePlanNameCursor(s string) (lowerName string, id uuid.UUID, err error) {
	parts, err := DecodeKeyCursor(s, 3)
	if err != nil {
		return "", uuid.Nil, err
	}
	if parts[0] != planNameCursorTag {
		return "", uuid.Nil, errInvalidCursor()
	}
	id, err = uuid.Parse(parts[2])
	if err != nil {
		return "", uuid.Nil, errInvalidCursor()
	}
	// Canonical form only: no uppercase uuids, no redundant encodings.
	if EncodePlanNameCursor(parts[1], id) != s {
		return "", uuid.Nil, errInvalidCursor()
	}
	return parts[1], id, nil
}
