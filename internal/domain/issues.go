package domain

// Issue strings are the stable machine values of `details[].issue` in error
// responses. Field paths that go with them use JSON names, with array indexes
// for nested items, for example exercises[2].sets[0].rpe (see FieldIndex).

// Conflict issues (409 conflict). Named by the specs.
const (
	// IssueStale: the request's updated_at is older than the stored one
	// (specs 03, 10). The error carries the server's copy as Current.
	IssueStale = "stale"
	// IssueDeleted: the row is soft-deleted; deleted wins (specs 03, 09, 10, 18, 20).
	IssueDeleted = "deleted"
	// IssueAlreadyExists: an exercise name is taken (specs 09, 18); the error
	// carries the other exercise's id as ExistingID.
	IssueAlreadyExists = "already_exists"
	// IssueAlreadyTaken: the username is taken (spec 01).
	IssueAlreadyTaken = "already_taken"
	// IssueIDTaken: the exercise id exists with different content (spec 09).
	IssueIDTaken = "id_taken"
)

// Validation issues (422 validation_failed) named by the specs.
const (
	IssueRequired           = "required"             // missing, null or empty where a value is needed
	IssueTooShort           = "too_short"            // below the minimum length
	IssueTooLong            = "too_long"             // above the maximum length
	IssueInvalidChars       = "invalid_chars"        // characters outside the allowed set (username)
	IssueInvalidFormat      = "invalid_format"       // malformed value (conventions.md example)
	IssueInvalidValue       = "invalid_value"        // not one of a fixed list (enums.md)
	IssueReserved           = "reserved"             // reserved username
	IssuePlanLimit          = "plan_limit"           // 100 active plans reached (spec 10)
	IssueInvalidImage       = "invalid_image"        // corrupt or undecodable image (spec 20)
	IssueTooLargeDimensions = "too_large_dimensions" // image over 2000 px (spec 20)
)

// Validation issues the specs describe in prose without naming them. They are
// fixed here so every handler reports the same string.
const (
	// IssueOutOfRange: a number, count or timestamp outside its allowed range
	// (target_sets 1-20, rest_seconds 0-3600, rpe 1-10, ended_at before
	// started_at, target_reps_max below target_reps, over the column maximum).
	IssueOutOfRange = "out_of_range"
	// IssueTooMany: too many array items (50 exercises, 100 sets, 5 secondary
	// muscle groups).
	IssueTooMany = "too_many"
	// IssueDuplicate: a value that must be unique repeats (position within
	// its parent, secondary muscle group listed twice).
	IssueDuplicate = "duplicate"
	// IssueContainsPrimary: secondary_muscle_groups contains the primary one.
	IssueContainsPrimary = "contains_primary"
	// IssueTooManyDecimals: a weight or distance with more than DecimalPlaces
	// decimals. (An rpe that is not a multiple of RPEStep is IssueInvalidValue.)
	IssueTooManyDecimals = "too_many_decimals"
	// IssueTooFarInFuture: updated_at is more than ClockSkewTolerance ahead.
	IssueTooFarInFuture = "too_far_in_future"
	// IssueUnknownReference: exercise_id or workout_plan_id does not exist
	// (or the plan belongs to another user).
	IssueUnknownReference = "unknown_reference"
)
