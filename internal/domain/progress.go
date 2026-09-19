package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Progress sessions (specs 03, 04, 05, 16): the response shapes, the request
// and its validation, and the list parameters.
//
// One Go type per response shape:
//
//	Progress         a full session (specs 03, 04, and 05 with expand=exercises)
//	ProgressSummary  a list item without expand (spec 05)
//	ProgressPage[T]  the list envelope around either
//
// A save request goes through two types. ProgressSaveRequest is exactly what
// the client sent: strings for uuids and timestamps and pointers for required
// numbers, so that a missing field, a malformed value and a wrong value each
// give their own 422 issue instead of a bare 400. Validate turns it into a
// ProgressSave, which is typed and ready for the store.

// ProgressDecimal is a decimal number (weight, distance, rpe) carried as its
// text, so it never goes through a float. It decodes from a JSON number only
// and encodes as a JSON number with trailing zeros trimmed but at least one
// decimal kept, so numeric "80.00" is written as 80.0 like in the specs.
//
// The value read from the database is the numeric's own text (::text); the
// value in a validated request is canonical (no sign, no exponent, no
// leading or trailing zeros).
type ProgressDecimal string

// UnmarshalJSON accepts a JSON number and keeps its literal text. Any other
// JSON type is a type error (a 400 in the API), including a quoted number.
func (d *ProgressDecimal) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		kind := "string"
		switch {
		case len(b) > 0 && (b[0] == 't' || b[0] == 'f'):
			kind = "bool"
		case len(b) > 0 && b[0] == '{':
			kind = "object"
		case len(b) > 0 && b[0] == '[':
			kind = "array"
		}
		return &json.UnmarshalTypeError{Value: kind, Type: reflect.TypeFor[ProgressDecimal]()}
	}
	*d = ProgressDecimal(b)
	return nil
}

// MarshalJSON writes the number without any float conversion.
func (d ProgressDecimal) MarshalJSON() ([]byte, error) {
	n, ok := parseProgressNumber(string(d))
	if !ok {
		return nil, fmt.Errorf("domain: %q is not a plain decimal number", string(d))
	}
	out := n.ip + "." + n.frac
	if n.frac == "" {
		out += "0"
	}
	if n.neg {
		out = "-" + out
	}
	return []byte(out), nil
}

// progressNumber is a parsed plain decimal: sign, integer digits without
// leading zeros ("0" for zero) and fraction digits without trailing zeros.
type progressNumber struct {
	neg  bool // never set for zero
	ip   string
	frac string
}

// parseProgressNumber parses -?digits[.digits]. Exponent notation is not
// accepted (ok is false): no client writes a weight as 1e2.
func parseProgressNumber(s string) (n progressNumber, ok bool) {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	ip, frac, hasPoint := strings.Cut(s, ".")
	if !progressAllDigits(ip) || (hasPoint && !progressAllDigits(frac)) {
		return progressNumber{}, false
	}
	n.ip = strings.TrimLeft(ip, "0")
	if n.ip == "" {
		n.ip = "0"
	}
	n.frac = strings.TrimRight(frac, "0")
	n.neg = neg && (n.ip != "0" || n.frac != "")
	return n, true
}

func progressAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// canonical is the text stored in ProgressSave: unsigned, no zero padding.
func (n progressNumber) canonical() ProgressDecimal {
	if n.frac == "" {
		return ProgressDecimal(n.ip)
	}
	return ProgressDecimal(n.ip + "." + n.frac)
}

// intAbove reports whether the integer part is greater than max. Integer
// parts too long for an int64 are above every limit used here.
func (n progressNumber) intAbove(max int64) bool {
	if len(n.ip) > 18 {
		return true
	}
	v, err := strconv.ParseInt(n.ip, 10, 64)
	return err != nil || v > max
}

// The integer part of the largest value each column holds. With at most
// DecimalPlaces decimals, a value fits exactly when its integer part does.
var (
	progressMaxWeightInt   = int64(math.Trunc(MaxWeightKg))
	progressMaxDistanceInt = int64(math.Trunc(MaxDistanceMeters))
)

// progressMeasure checks a weight or a distance: not negative, at most
// DecimalPlaces decimals, within the column's range. It returns the canonical
// value, or the issue.
func progressMeasure(d ProgressDecimal, maxInt int64) (ProgressDecimal, string) {
	n, ok := parseProgressNumber(string(d))
	switch {
	case !ok:
		return "", IssueInvalidFormat
	case n.neg, n.intAbove(maxInt):
		return "", IssueOutOfRange
	case len(n.frac) > DecimalPlaces:
		return "", IssueTooManyDecimals
	}
	return n.canonical(), ""
}

// progressRPE checks an rpe: 1 to 10 in steps of RPEStep (0.5).
func progressRPE(d ProgressDecimal) (ProgressDecimal, string) {
	n, ok := parseProgressNumber(string(d))
	switch {
	case !ok:
		return "", IssueInvalidFormat
	case n.neg, n.ip == "0", n.intAbove(RPEMax), n.ip == "10" && n.frac != "":
		return "", IssueOutOfRange
	case n.frac != "" && n.frac != "5": // the only fraction that is a multiple of 0.5
		return "", IssueInvalidValue
	}
	return n.canonical(), ""
}

// --- responses ---------------------------------------------------------------

// ProgressSet is one set of an exercise in a session. In a validated request
// Weight, DistanceMeters and RPE hold canonical decimals.
type ProgressSet struct {
	Position        int              `json:"position"`
	Type            SetType          `json:"type"`
	Reps            *int             `json:"reps"`
	Weight          *ProgressDecimal `json:"weight"`
	DurationSeconds *int             `json:"duration_seconds"`
	DistanceMeters  *ProgressDecimal `json:"distance_meters"`
	RPE             *ProgressDecimal `json:"rpe"`
	Completed       bool             `json:"completed"`
}

// ProgressExercise is one exercise of a session with its sets. There is no
// exercise name: the app resolves exercise_id through the exercise master.
type ProgressExercise struct {
	ExerciseID uuid.UUID     `json:"exercise_id"`
	Position   int           `json:"position"`
	Notes      *string       `json:"notes"`
	Sets       []ProgressSet `json:"sets"`
}

// Progress is a full session, the response of endpoints 3 and 4 and the item
// of endpoint 5 with expand=exercises. UpdatedAt is the client's last-modified
// time (the client_updated_at column); ServerUpdatedAt is the sync cursor.
type Progress struct {
	ID              uuid.UUID          `json:"id"`
	WorkoutPlanID   *uuid.UUID         `json:"workout_plan_id"`
	Name            string             `json:"name"`
	Notes           *string            `json:"notes"`
	StartedAt       time.Time          `json:"started_at"`
	EndedAt         time.Time          `json:"ended_at"`
	DurationSeconds int                `json:"duration_seconds"`
	UpdatedAt       time.Time          `json:"updated_at"`
	CreatedAt       time.Time          `json:"created_at"`
	ServerUpdatedAt time.Time          `json:"server_updated_at"`
	DeletedAt       *time.Time         `json:"deleted_at"`
	Exercises       []ProgressExercise `json:"exercises"`
}

// ProgressSummary is a list item of endpoint 5 without expand.
type ProgressSummary struct {
	ID              uuid.UUID  `json:"id"`
	WorkoutPlanID   *uuid.UUID `json:"workout_plan_id"`
	Name            string     `json:"name"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         time.Time  `json:"ended_at"`
	DurationSeconds int        `json:"duration_seconds"`
	ExerciseCount   int        `json:"exercise_count"`
	SetCount        int        `json:"set_count"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ServerUpdatedAt time.Time  `json:"server_updated_at"`
	DeletedAt       *time.Time `json:"deleted_at"`
}

// ProgressPage is the list envelope (conventions: pagination). Items is never
// nil, so an empty page encodes as [] and not null.
type ProgressPage[T ProgressSummary | Progress] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// --- list parameters ---------------------------------------------------------

// ProgressListFilter is what the list endpoint filters on (spec 05).
type ProgressListFilter struct {
	// UpdatedSince switches to sync mode: rows with server_updated_at after
	// it, deleted ones included, ordered by (server_updated_at, id).
	UpdatedSince  *time.Time
	WorkoutPlanID *uuid.UUID
	// From and To bound started_at, both inclusive.
	From, To *time.Time
	// IncludeDeleted shows soft-deleted rows; UpdatedSince implies it.
	IncludeDeleted bool
}

// ProgressListParams is the parsed query of the list endpoint as the service
// receives it. Limit 0 means DefaultPageLimit. Cursor is the opaque string
// from a previous page.
type ProgressListParams struct {
	ProgressListFilter
	Limit  int
	Cursor string
}

// --- save request ------------------------------------------------------------

// ProgressSaveRequest is the body of PUT /v1/progress/{id} exactly as decoded
// from JSON (spec 03). Decode it with unknown fields disallowed, then call
// Validate.
type ProgressSaveRequest struct {
	WorkoutPlanID   *string                   `json:"workout_plan_id"`
	Name            string                    `json:"name"`
	Notes           *string                   `json:"notes"`
	StartedAt       string                    `json:"started_at"`
	EndedAt         string                    `json:"ended_at"`
	DurationSeconds *int                      `json:"duration_seconds"`
	UpdatedAt       string                    `json:"updated_at"`
	Exercises       []ProgressExerciseRequest `json:"exercises"`
}

// ProgressExerciseRequest is one element of ProgressSaveRequest.Exercises.
type ProgressExerciseRequest struct {
	ExerciseID string               `json:"exercise_id"`
	Position   *int                 `json:"position"`
	Notes      *string              `json:"notes"`
	Sets       []ProgressSetRequest `json:"sets"`
}

// ProgressSetRequest is one element of ProgressExerciseRequest.Sets.
type ProgressSetRequest struct {
	Position        *int             `json:"position"`
	Type            string           `json:"type"`
	Reps            *int             `json:"reps"`
	Weight          *ProgressDecimal `json:"weight"`
	DurationSeconds *int             `json:"duration_seconds"`
	DistanceMeters  *ProgressDecimal `json:"distance_meters"`
	RPE             *ProgressDecimal `json:"rpe"`
	Completed       *bool            `json:"completed"`
}

// ProgressSave is a validated save request: every value parsed and in range,
// times in UTC at microsecond precision, decimals canonical. Reference checks
// that need the database (exercise and plan ids) are still to be done by the
// store.
type ProgressSave struct {
	WorkoutPlanID   *uuid.UUID
	Name            string
	Notes           *string
	StartedAt       time.Time
	EndedAt         time.Time
	DurationSeconds int
	UpdatedAt       time.Time
	Exercises       []ProgressExercise
}

// Validate checks the request against spec 03 and collects every problem, with
// the JSON path of each (exercises[2].sets[5].reps), into one *ValidationError.
// now is the server time for the rule that updated_at may not be more than
// ClockSkewTolerance ahead.
func (r *ProgressSaveRequest) Validate(now time.Time) (ProgressSave, error) {
	var (
		v   ValidationError
		out ProgressSave
	)

	if r.WorkoutPlanID != nil {
		if id, ok := progressUUID(*r.WorkoutPlanID); ok {
			out.WorkoutPlanID = &id
		} else {
			v.Add("workout_plan_id", IssueInvalidFormat)
		}
	}
	if r.Name == "" {
		v.Add("name", IssueRequired)
	} else {
		progressText(&v, "name", r.Name, ProgressNameMaxLen)
	}
	out.Name = r.Name
	if r.Notes != nil {
		progressText(&v, "notes", *r.Notes, ProgressNotesMaxLen)
	}
	out.Notes = r.Notes

	startedAt, startedOK := progressTime(&v, "started_at", r.StartedAt)
	endedAt, endedOK := progressTime(&v, "ended_at", r.EndedAt)
	if startedOK && endedOK && endedAt.Before(startedAt) {
		v.Add("ended_at", IssueOutOfRange)
	}
	out.StartedAt, out.EndedAt = startedAt, endedAt

	if r.DurationSeconds == nil {
		v.Add("duration_seconds", IssueRequired)
	} else {
		if *r.DurationSeconds < ProgressDurationMinSeconds || *r.DurationSeconds > ProgressDurationMaxSeconds {
			v.Add("duration_seconds", IssueOutOfRange)
		}
		out.DurationSeconds = *r.DurationSeconds
	}

	updatedAt, updatedOK := progressTime(&v, "updated_at", r.UpdatedAt)
	if updatedOK {
		var future *ValidationError
		if errors.As(CheckNotFuture(updatedAt, now), &future) {
			v.Issues = append(v.Issues, future.Issues...)
		}
	}
	out.UpdatedAt = updatedAt

	switch {
	case r.Exercises == nil:
		v.Add("exercises", IssueRequired)
	case len(r.Exercises) > MaxExercisesPerProgress:
		v.Add("exercises", IssueTooMany)
	}
	out.Exercises = make([]ProgressExercise, len(r.Exercises))
	positions := make(map[int]struct{}, len(r.Exercises))
	for i := range r.Exercises {
		out.Exercises[i] = r.Exercises[i].validate(&v, FieldIndex("exercises", i), positions)
	}

	if err := v.Err(); err != nil {
		return ProgressSave{}, err
	}
	return out, nil
}

// validate checks one exercise. path is its JSON path; positions holds the
// positions already used by the session's earlier exercises.
func (e *ProgressExerciseRequest) validate(v *ValidationError, path string, positions map[int]struct{}) ProgressExercise {
	var out ProgressExercise

	if e.ExerciseID == "" {
		v.Add(path+".exercise_id", IssueRequired)
	} else if id, ok := progressUUID(e.ExerciseID); ok {
		out.ExerciseID = id
	} else {
		v.Add(path+".exercise_id", IssueInvalidFormat)
	}
	out.Position = progressPosition(v, path+".position", e.Position, positions)
	if e.Notes != nil {
		progressText(v, path+".notes", *e.Notes, ProgressExerciseNotesMaxLen)
	}
	out.Notes = e.Notes

	switch {
	case e.Sets == nil:
		v.Add(path+".sets", IssueRequired)
	case len(e.Sets) > MaxSetsPerExercise:
		v.Add(path+".sets", IssueTooMany)
	}
	out.Sets = make([]ProgressSet, len(e.Sets))
	setPositions := make(map[int]struct{}, len(e.Sets))
	for i := range e.Sets {
		out.Sets[i] = e.Sets[i].validate(v, path+"."+FieldIndex("sets", i), setPositions)
	}
	return out
}

// validate checks one set. path is its JSON path; positions holds the
// positions already used by the exercise's earlier sets.
func (s *ProgressSetRequest) validate(v *ValidationError, path string, positions map[int]struct{}) ProgressSet {
	var out ProgressSet

	out.Position = progressPosition(v, path+".position", s.Position, positions)
	switch t := SetType(s.Type); {
	case s.Type == "":
		v.Add(path+".type", IssueRequired)
	case !t.IsValid():
		v.Add(path+".type", IssueInvalidValue)
	default:
		out.Type = t
	}
	out.Reps = progressCount(v, path+".reps", s.Reps)
	out.DurationSeconds = progressCount(v, path+".duration_seconds", s.DurationSeconds)
	out.Weight = progressDecimal(v, path+".weight", s.Weight, func(d ProgressDecimal) (ProgressDecimal, string) {
		return progressMeasure(d, progressMaxWeightInt)
	})
	out.DistanceMeters = progressDecimal(v, path+".distance_meters", s.DistanceMeters, func(d ProgressDecimal) (ProgressDecimal, string) {
		return progressMeasure(d, progressMaxDistanceInt)
	})
	out.RPE = progressDecimal(v, path+".rpe", s.RPE, progressRPE)
	if s.Completed == nil {
		v.Add(path+".completed", IssueRequired)
	} else {
		out.Completed = *s.Completed
	}
	return out
}

// progressUUID parses a uuid in its canonical hyphenated form (any case).
func progressUUID(s string) (uuid.UUID, bool) {
	if len(s) == 36 {
		if id, err := uuid.Parse(s); err == nil {
			return id, true
		}
	}
	return uuid.Nil, false
}

// progressText checks a text field's length in characters. NUL is rejected
// because PostgreSQL text cannot store it.
func progressText(v *ValidationError, field, s string, max int) {
	switch {
	case strings.ContainsRune(s, 0):
		v.Add(field, IssueInvalidChars)
	case utf8.RuneCountInString(s) > max:
		v.Add(field, IssueTooLong)
	}
}

// progressTime parses a required RFC 3339 timestamp into UTC at microsecond
// precision (what PostgreSQL stores, and what the conflict rule compares).
func progressTime(v *ValidationError, field, s string) (time.Time, bool) {
	if s == "" {
		v.Add(field, IssueRequired)
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		v.Add(field, IssueInvalidFormat)
		return time.Time{}, false
	}
	return t.UTC().Truncate(time.Microsecond), true
}

// progressPosition checks a required position: 0 or more, within the column,
// and not used before in the same parent (positions is updated).
func progressPosition(v *ValidationError, field string, p *int, positions map[int]struct{}) int {
	switch {
	case p == nil:
		v.Add(field, IssueRequired)
		return 0
	case *p < 0 || *p > MaxIntColumn:
		v.Add(field, IssueOutOfRange)
		return 0
	}
	if _, dup := positions[*p]; dup {
		v.Add(field, IssueDuplicate)
	}
	positions[*p] = struct{}{}
	return *p
}

// progressCount checks an optional count (reps, a duration): 0 or more and
// within the integer column.
func progressCount(v *ValidationError, field string, p *int) *int {
	if p != nil && (*p < 0 || *p > MaxIntColumn) {
		v.Add(field, IssueOutOfRange)
		return nil
	}
	return p
}

// progressDecimal runs check on an optional decimal and returns its canonical
// form, or nil when absent or invalid.
func progressDecimal(v *ValidationError, field string, d *ProgressDecimal, check func(ProgressDecimal) (ProgressDecimal, string)) *ProgressDecimal {
	if d == nil {
		return nil
	}
	canon, issue := check(*d)
	if issue != "" {
		v.Add(field, issue)
		return nil
	}
	return &canon
}
