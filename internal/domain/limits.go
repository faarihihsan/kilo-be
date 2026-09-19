package domain

import (
	"math"
	"time"
)

// Every numeric limit and pattern from the specs lives here once. Lengths are
// counted in characters (runes), not bytes, except where a name says Bytes.
// Numeric constants are untyped so they work as int, int64 or float64.
//
// Not named here because they are simply "not negative": positions, and the
// metrics reps, weight, duration_seconds, distance_meters and the plan targets.

// Request bodies (docs/api/conventions.md).
const (
	MaxBodyBytes  = 1 << 20 // 1 MiB, every route except the image upload
	MaxImageBytes = 2 << 20 // 2 MiB, the image upload body and the image itself
)

// Pagination (docs/api/conventions.md). A limit outside [MinPageLimit,
// MaxPageLimit] is a 422, not clamped.
const (
	DefaultPageLimit = 50
	MinPageLimit     = 1
	MaxPageLimit     = 200
)

// ExpandExercises is the only value of the `expand` query parameter on the
// plan and progress list endpoints (specs 05, 06).
const ExpandExercises = "exercises"

// ClockSkewTolerance is how far in the future a client-supplied updated_at may
// be before the save is rejected with a 422 (specs 03 and 10).
const ClockSkewTolerance = 5 * time.Minute

// Users and auth (specs 01, 02, data-model users and auth_tokens).
const (
	UsernameMinLen = 3
	UsernameMaxLen = 30
	// UsernamePattern also backs the CHECK constraint on users.username. It
	// applies to the trimmed, lowercased username.
	UsernamePattern = `^[a-z0-9_]{3,30}$`

	PasswordMaxBytes = 128 // DoS cap for hashing; there is no other password policy
	DeviceNameMaxLen = 100

	TokenPrefix      = "wt_"
	TokenRandomBytes = 32

	DefaultTokenTTLDays = 365
	DefaultTokenTTL     = DefaultTokenTTLDays * 24 * time.Hour

	// TokenLastUsedInterval is the most often last_used_at is written.
	TokenLastUsedInterval = time.Hour
	// TokenPurgeAfter is how long an expired or revoked token row is kept.
	TokenPurgeAfter = 30 * 24 * time.Hour
)

// ReservedUsernames cannot be registered over HTTP (spec 01); the admin CLI may
// bypass the list. Read-only: check a name with IsReservedUsername.
var ReservedUsernames = []string{
	"admin", "administrator", "root", "system", "support",
	"api", "me", "null", "undefined", "anonymous",
}

// Exercises (specs 09, 18, data-model exercises).
const (
	ExerciseNameMinLen         = 1
	ExerciseNameMaxLen         = 100
	ExerciseInstructionsMaxLen = 4000
	MaxSecondaryMuscleGroups   = 5
)

// Exercise images (spec 20). MaxImageDimension applies to each side.
const (
	MaxImageDimension = 2000
	ImageHashLen      = 16 // hex chars of the SHA-256 used in the file name
)

// Workout plans (spec 10, data-model workout_plans and workout_plan_exercises).
const (
	PlanNameMinLen          = 1
	PlanNameMaxLen          = 100
	PlanDescriptionMaxLen   = 1000
	PlanExerciseNotesMaxLen = 1000
	MaxExercisesPerPlan     = 50
	MaxActivePlansPerUser   = 100
	PlanTargetSetsMin       = 1
	PlanTargetSetsMax       = 20
	PlanTargetRepsMin       = 1
	PlanRestSecondsMin      = 0
	PlanRestSecondsMax      = 3600
)

// Progress sessions (spec 03, data-model progress, progress_exercises and
// progress_sets).
const (
	ProgressNameMinLen          = 1
	ProgressNameMaxLen          = 100
	ProgressNotesMaxLen         = 2000
	ProgressExerciseNotesMaxLen = 1000
	MaxExercisesPerProgress     = 50
	MaxSetsPerExercise          = 100
	ProgressDurationMinSeconds  = 0
	ProgressDurationMaxSeconds  = 86400
	RPEMin                      = 1
	RPEMax                      = 10
	RPEStep                     = 0.5
)

// Numeric precision. Weights are kg and distances are meters, both with at
// most DecimalPlaces decimals. The specs give no upper bound for these values,
// but the column types do: a larger value overflows the column and would
// surface as a 500, so validators must reject it with a 422 instead.
const (
	DecimalPlaces = 2

	MaxWeightKg       = 99999.99   // numeric(7,2)
	MaxDistanceMeters = 9999999.99 // numeric(9,2)
	// MaxIntColumn bounds every integer column the specs leave without an
	// upper limit (reps, durations, positions, target_reps, ...).
	MaxIntColumn = math.MaxInt32
)
