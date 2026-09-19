package service

import (
	"context"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
)

// Progress implements the progress operations: list, get, save (idempotent
// upsert with the conflict rule) and delete (endpoints 3, 4, 5, 16). Every
// method takes the caller's user id; a session of another user is NotFound.
type Progress struct {
	progress *store.Progress
	clock    clock.Clock
}

// NewProgress builds the progress service.
func NewProgress(d Deps) *Progress {
	return &Progress{progress: store.NewProgress(d.DB), clock: d.Clock}
}

// Save validates the request (422 with every problem, before any database
// work) and upserts the session under the conflict rule of spec 03. It returns
// the stored session and whether it was created (201, else 200). Errors:
// validation (422), NotFound (id belongs to another user), Conflict (stale or
// deleted).
func (s *Progress) Save(ctx context.Context, userID, id uuid.UUID, req *domain.ProgressSaveRequest) (domain.Progress, bool, error) {
	in, err := req.Validate(s.clock.Now())
	if err != nil {
		return domain.Progress{}, false, err
	}
	return s.progress.Save(ctx, userID, id, in)
}

// Get returns one session; deleted, missing and foreign sessions are NotFound.
func (s *Progress) Get(ctx context.Context, userID, id uuid.UUID) (domain.Progress, error) {
	return s.progress.Get(ctx, userID, id)
}

// List returns a page of session summaries (spec 05 without expand).
func (s *Progress) List(ctx context.Context, userID uuid.UUID, p domain.ProgressListParams) (domain.ProgressPage[domain.ProgressSummary], error) {
	limit, after, err := progressPaging(p)
	if err != nil {
		return domain.ProgressPage[domain.ProgressSummary]{}, err
	}
	items, next, err := s.progress.ListSummaries(ctx, userID, p.ProgressListFilter, after, limit)
	if err != nil {
		return domain.ProgressPage[domain.ProgressSummary]{}, err
	}
	return domain.ProgressPage[domain.ProgressSummary]{Items: items, NextCursor: progressNextCursor(next)}, nil
}

// ListExpanded returns a page of full sessions with exercises and sets
// (spec 05 with expand=exercises).
func (s *Progress) ListExpanded(ctx context.Context, userID uuid.UUID, p domain.ProgressListParams) (domain.ProgressPage[domain.Progress], error) {
	limit, after, err := progressPaging(p)
	if err != nil {
		return domain.ProgressPage[domain.Progress]{}, err
	}
	items, next, err := s.progress.ListExpanded(ctx, userID, p.ProgressListFilter, after, limit)
	if err != nil {
		return domain.ProgressPage[domain.Progress]{}, err
	}
	return domain.ProgressPage[domain.Progress]{Items: items, NextCursor: progressNextCursor(next)}, nil
}

// Delete soft-deletes a session. Deleting a deleted session succeeds.
func (s *Progress) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.progress.SoftDelete(ctx, userID, id)
}

// progressPaging resolves the page size (0 means the default, anything outside
// the allowed range is a 422) and decodes the cursor (a bad one is a 400).
func progressPaging(p domain.ProgressListParams) (limit int, after *domain.Cursor, err error) {
	limit = p.Limit
	if limit == 0 {
		limit = domain.DefaultPageLimit
	}
	if limit < domain.MinPageLimit || limit > domain.MaxPageLimit {
		return 0, nil, domain.NewValidation("limit", domain.IssueOutOfRange)
	}
	if p.Cursor != "" {
		c, err := domain.DecodeCursor(p.Cursor)
		if err != nil {
			return 0, nil, err
		}
		after = &c
	}
	return limit, after, nil
}

func progressNextCursor(c *domain.Cursor) *string {
	if c == nil {
		return nil
	}
	s := domain.EncodeCursor(*c)
	return &s
}
