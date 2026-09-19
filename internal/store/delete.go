package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

// softDeleteAnswer turns a soft-delete UPDATE that matched no row into the
// outcome for the caller: success when the row exists for this user but was
// already deleted (the delete is idempotent), NotFound when it does not exist
// or belongs to another user. table and what are fixed identifiers from the
// caller, never client input; what names the row in the error message.
func softDeleteAnswer(ctx context.Context, q Querier, table, what string, id, userID uuid.UUID) error {
	var owned bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND user_id = $2)`, id, userID).Scan(&owned)
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", what, err)
	}
	if !owned {
		return domain.NewNotFound()
	}
	return nil
}
