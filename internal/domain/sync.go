package domain

import (
	"strconv"
	"time"
)

// The sync conflict rule (docs/api/endpoints/03-save-progress.md#conflict-rule,
// shared by plans in spec 10) as pure functions. The store runs them inside its
// upsert transaction after locking the row (SELECT ... FOR UPDATE):
//
//	action := domain.DecideSync(existing, req.UpdatedAt)
//	if err := action.Err(current); err != nil { return err } // 409 stale / deleted
//	// SyncInsert -> 201, SyncUpdate -> replace children, SyncNoop -> return stored copy
//
// and call CheckNotFuture on request validation, before any database work.
//
// Precision: PostgreSQL timestamptz stores microseconds, and the stored
// client_updated_at was read back from it, while the request value may carry
// nanoseconds (RFC 3339 allows them). Every comparison here is made on values
// truncated (floored) to the microsecond, so a retry that differs from the
// stored value only below 1 µs is a no-op rather than a stale write. No floats
// are involved anywhere.

// SyncAction is the outcome of the conflict rule for one incoming save.
type SyncAction int

const (
	// SyncInsert: no row with this id exists -> create, 201.
	SyncInsert SyncAction = iota + 1
	// SyncUpdate: the request is newer than the stored copy -> replace, 200.
	SyncUpdate
	// SyncNoop: same updated_at as stored -> change nothing, return the stored
	// copy, 200. Makes a retry after a lost response safe.
	SyncNoop
	// SyncStale: the request is older than the stored copy -> 409 with
	// issue "stale" and the server's copy as Current.
	SyncStale
	// SyncDeleted: the row is soft-deleted -> 409 with issue "deleted". Deleted
	// wins, even against a newer updated_at.
	SyncDeleted
)

func (a SyncAction) String() string {
	switch a {
	case SyncInsert:
		return "insert"
	case SyncUpdate:
		return "update"
	case SyncNoop:
		return "noop"
	case SyncStale:
		return "stale"
	case SyncDeleted:
		return "deleted"
	}
	return "SyncAction(" + strconv.Itoa(int(a)) + ")"
}

// Err returns the 409 ConflictError for SyncStale (carrying current, the
// server's copy in its response shape) and SyncDeleted (current is not
// included, per spec 03), and nil for every other action.
func (a SyncAction) Err(current any) error {
	switch a {
	case SyncStale:
		return NewConflict(IssueStale, WithCurrent(current))
	case SyncDeleted:
		return NewConflict(IssueDeleted)
	}
	return nil
}

// SyncState is what the conflict rule needs to know about an existing row.
type SyncState struct {
	// ClientUpdatedAt is the client-supplied updated_at of the last accepted
	// save (the client_updated_at column).
	ClientUpdatedAt time.Time
	// Deleted is true when deleted_at is set.
	Deleted bool
}

// DecideSync applies the conflict rule. existing is nil when no row has the
// id. The row's owner is checked by the caller before this (another user's row
// is a 404, never a conflict).
//
//	existing == nil            -> SyncInsert
//	existing.Deleted           -> SyncDeleted (regardless of incoming)
//	incoming <  stored         -> SyncStale
//	incoming == stored         -> SyncNoop
//	incoming >  stored         -> SyncUpdate
//
// The comparison is at microsecond precision (see the package comment above).
func DecideSync(existing *SyncState, incoming time.Time) SyncAction {
	if existing == nil {
		return SyncInsert
	}
	if existing.Deleted {
		return SyncDeleted
	}
	in, stored := truncMicro(incoming), truncMicro(existing.ClientUpdatedAt)
	switch {
	case in.Before(stored):
		return SyncStale
	case in.After(stored):
		return SyncUpdate
	default:
		return SyncNoop
	}
}

// CheckNotFuture rejects an updated_at more than ClockSkewTolerance ahead of
// now with a 422 validation error on field "updated_at", issue
// IssueTooFarInFuture. Exactly ClockSkewTolerance ahead is allowed. Both times
// are compared at microsecond precision, like the rest of the sync rule.
// Timestamps in the past, however old, are always accepted here.
func CheckNotFuture(incoming, now time.Time) error {
	if truncMicro(incoming).After(truncMicro(now).Add(ClockSkewTolerance)) {
		return NewValidation("updated_at", IssueTooFarInFuture)
	}
	return nil
}

// truncMicro floors t to the microsecond and drops any monotonic clock reading.
// internal/clock has the same helper; domain imports nothing from this module.
func truncMicro(t time.Time) time.Time { return t.Truncate(time.Microsecond) }
