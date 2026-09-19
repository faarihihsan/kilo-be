package service

import (
	"context"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
)

// Plans implements the workout plan operations: list, get, save (idempotent
// upsert with the conflict rule) and delete (endpoints 6, 7, 10 and 17). Every
// method takes the caller's user id; a plan of another user is NotFound.
type Plans struct {
	plans *store.Plans
	clock clock.Clock
}

// NewPlans builds the workout plans service.
func NewPlans(d Deps) *Plans {
	return &Plans{plans: store.NewPlans(d.DB), clock: d.Clock}
}

// Save validates the request (422 with every problem, before any database
// work) and upserts the plan under the conflict rule of spec 10. It returns the
// stored plan and whether it was created (201, else 200). Errors: validation
// (422), NotFound (id belongs to another user), Conflict (stale or deleted).
func (s *Plans) Save(ctx context.Context, userID, id uuid.UUID, in domain.PlanInput) (domain.Plan, bool, error) {
	spec, err := in.Validate(s.clock.Now())
	if err != nil {
		return domain.Plan{}, false, err
	}
	return s.plans.Save(ctx, userID, id, spec)
}

// Get returns one plan with its exercises; deleted, missing and foreign plans
// are NotFound.
func (s *Plans) Get(ctx context.Context, userID, id uuid.UUID) (domain.Plan, error) {
	return s.plans.Get(ctx, userID, id)
}

// ListSummaries returns a page of plan summaries (spec 06 without expand).
func (s *Plans) ListSummaries(ctx context.Context, userID uuid.UUID, p domain.PlanListParams) (domain.PlanPage[domain.PlanSummary], error) {
	return s.plans.ListSummaries(ctx, userID, p)
}

// ListExpanded returns a page of full plans with exercises (spec 06 with
// expand=exercises).
func (s *Plans) ListExpanded(ctx context.Context, userID uuid.UUID, p domain.PlanListParams) (domain.PlanPage[domain.Plan], error) {
	return s.plans.ListExpanded(ctx, userID, p)
}

// Delete soft-deletes a plan. Deleting a deleted plan succeeds.
func (s *Plans) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.plans.SoftDelete(ctx, userID, id)
}
