package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
)

// The sync upsert skeleton shared by plans and progress (endpoints 10 and 3,
// docs/implementation-plan.md section 5): retry the transaction once when two
// clients insert the same new id at once, lock the row, apply the conflict
// rule and answer the deleted/stale/no-op cases. The per-resource differences
// (the user lock and active-plan cap of plans, the reference checks, the write
// statements and the monotonic server_updated_at) stay with each store.

// saveWithRetry runs one upsert attempt in its own READ COMMITTED transaction
// and returns the stored object and whether it was created. When the attempt
// fails on a unique violation of pkey (the race of two concurrent inserts of
// the same new id), it runs exactly once more, now finding the winner's row.
func saveWithRetry[T any](ctx context.Context, db *DB, pkey string, save func(context.Context, pgx.Tx) (T, bool, error)) (T, bool, error) {
	var (
		out     T
		created bool
	)
	attempt := func() error {
		return db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, created, err = save(ctx, tx)
			return err
		})
	}
	err := attempt()
	if c, ok := IsUniqueViolation(err); ok && c == pkey {
		err = attempt()
	}
	if err != nil {
		var zero T
		return zero, false, err
	}
	return out, created, nil
}

// lockForSync locks the row with id in table (FOR UPDATE) and returns the
// conflict-rule decision for the incoming client updated_at. table and what
// are fixed identifiers from the caller, never client input; what names the
// row in the error message ("plan", "progress"). A row of another user is
// NotFound, never a conflict. The caller must run this inside a transaction.
func lockForSync(ctx context.Context, tx pgx.Tx, table, what string, userID, id uuid.UUID, incoming time.Time) (domain.SyncAction, error) {
	var (
		owner     uuid.UUID
		state     domain.SyncState
		deletedAt *time.Time
	)
	err := tx.QueryRow(ctx,
		`SELECT user_id, client_updated_at, deleted_at FROM `+table+` WHERE id = $1 FOR UPDATE`, id,
	).Scan(&owner, &state.ClientUpdatedAt, &deletedAt)

	var existing *domain.SyncState
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return 0, fmt.Errorf("store: lock %s: %w", what, err)
	case owner != userID:
		return 0, domain.NewNotFound()
	default:
		state.Deleted = deletedAt != nil
		existing = &state
	}
	return domain.DecideSync(existing, incoming), nil
}

// syncPreflight answers the conflict actions that do not write: a deleted or
// stale row is the 409 from domain.SyncAction.Err (stale carrying the current
// copy), and a no-op is the stored copy, so a retry after a lost response is
// safe. load reads the current row by id. done is true when the caller must not
// write and out/err are final; for SyncInsert and SyncUpdate done is false and
// the caller performs the write.
func syncPreflight[T any](action domain.SyncAction, load func() (T, error)) (out T, done bool, err error) {
	switch action {
	case domain.SyncDeleted:
		return out, true, action.Err(nil)
	case domain.SyncStale:
		cur, err := load()
		if err != nil {
			return out, true, err
		}
		return out, true, action.Err(cur)
	case domain.SyncNoop:
		cur, err := load()
		if err != nil {
			return out, true, err
		}
		return cur, true, nil
	}
	return out, false, nil
}

// addExerciseRefIssues checks that every id exists in the exercise master (a
// soft-deleted exercise still counts, per specs 03 and 10) and adds one
// unknown_reference issue per unknown id to v, with the JSON path of each
// (exercises[i].exercise_id). It queries only when there is at least one id.
func addExerciseRefIssues(ctx context.Context, q Querier, ids []uuid.UUID, v *domain.ValidationError) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM exercises WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("store: check exercises: %w", err)
	}
	known, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return fmt.Errorf("store: check exercises: %w", err)
	}
	exists := make(map[uuid.UUID]struct{}, len(known))
	for _, id := range known {
		exists[id] = struct{}{}
	}
	for i, id := range ids {
		if _, ok := exists[id]; !ok {
			v.Add(domain.FieldIndex("exercises", i)+".exercise_id", domain.IssueUnknownReference)
		}
	}
	return nil
}
