package service_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

// planSvcEnv is the plans service on a migrated database, with a fake clock.
type planSvcEnv struct {
	svc       *service.Plans
	clock     *clock.Fake
	user      uuid.UUID
	other     uuid.UUID
	exercises []uuid.UUID
}

// planSvcNow is the fake server time.
var planSvcNow = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)

func newPlanSvcEnv(t *testing.T) *planSvcEnv {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(planSvcNow)
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)
	other, _ := testutil.SeedUser(t, db, domain.RoleUser)
	e := &planSvcEnv{svc: service.NewPlans(service.Deps{DB: db, Clock: clk}), clock: clk, user: user, other: other}
	for range 2 {
		e.exercises = append(e.exercises, testutil.SeedExercise(t, db, user))
	}
	return e
}

func planSvcPtr[T any](v T) *T { return &v }

// input is a valid request at the given client time.
func (e *planSvcEnv) input(name, updatedAt string) domain.PlanInput {
	id := e.exercises[0].String()
	return domain.PlanInput{
		Name:      &name,
		UpdatedAt: &updatedAt,
		Exercises: []domain.PlanExerciseInput{{ExerciseID: &id, Position: planSvcPtr(0), TargetSets: planSvcPtr(3)}},
	}
}

func planSvcIssues(t *testing.T, err error) []domain.FieldIssue {
	t.Helper()
	var v *domain.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v (%T), want *domain.ValidationError", err, err)
	}
	return v.Issues
}

func TestPlansServiceSaveAndGet(t *testing.T) {
	e := newPlanSvcEnv(t)
	id := uuid.New()

	plan, created, err := e.svc.Save(t.Context(), e.user, id, e.input("Push", "2026-09-19T08:00:00Z"))
	if err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}
	if plan.ID != id || plan.Name != "Push" || len(plan.Exercises) != 1 {
		t.Errorf("plan = %+v", plan)
	}

	// The same request again is a no-op, not a creation.
	again, created, err := e.svc.Save(t.Context(), e.user, id, e.input("Push", "2026-09-19T08:00:00Z"))
	if err != nil || created {
		t.Fatalf("second Save: created=%v err=%v", created, err)
	}
	if !again.ServerUpdatedAt.Equal(plan.ServerUpdatedAt) {
		t.Errorf("a no-op moved server_updated_at from %v to %v", plan.ServerUpdatedAt, again.ServerUpdatedAt)
	}

	got, err := e.svc.Get(t.Context(), e.user, id)
	if err != nil || got.Name != "Push" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if _, err := e.svc.Get(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get by another user: %v, want NotFound", err)
	}
}

func TestPlansServiceValidatesBeforeStoring(t *testing.T) {
	e := newPlanSvcEnv(t)
	id := uuid.New()
	in := e.input("", "2026-09-19T08:00:00Z")
	in.Exercises[0].TargetSets = planSvcPtr(0)

	_, _, err := e.svc.Save(t.Context(), e.user, id, in)
	want := []domain.FieldIssue{
		{Field: "name", Issue: domain.IssueTooShort},
		{Field: "exercises[0].target_sets", Issue: domain.IssueOutOfRange},
	}
	if got := planSvcIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
	if _, err := e.svc.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an invalid request created the plan: %v", err)
	}

	// Validation does not depend on who owns the id, so it cannot be used to
	// probe for another user's plan.
	other := uuid.New()
	if _, _, err := e.svc.Save(t.Context(), e.other, other, e.input("Theirs", "2026-09-19T08:00:00Z")); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.Save(t.Context(), e.user, other, in)
	if got := planSvcIssues(t, err); !slices.Equal(got, want) {
		t.Errorf("invalid request on another user's id: %v, want the same validation issues", got)
	}
}

// TestPlansServiceClockSkew: the 5 minute future rule uses the service clock.
func TestPlansServiceClockSkew(t *testing.T) {
	e := newPlanSvcEnv(t)
	limit := planSvcNow.Add(domain.ClockSkewTolerance)

	if _, _, err := e.svc.Save(t.Context(), e.user, uuid.New(), e.input("Edge", limit.Format(time.RFC3339Nano))); err != nil {
		t.Errorf("exactly 5 minutes ahead: %v, want accepted", err)
	}
	over := limit.Add(time.Microsecond).Format(time.RFC3339Nano)
	_, _, err := e.svc.Save(t.Context(), e.user, uuid.New(), e.input("Over", over))
	if got := planSvcIssues(t, err); !slices.Equal(got, []domain.FieldIssue{{Field: "updated_at", Issue: domain.IssueTooFarInFuture}}) {
		t.Errorf("5 minutes and 1 µs ahead: %v", got)
	}

	// The same request is fine once the server's clock has caught up.
	e.clock.Advance(time.Second)
	if _, _, err := e.svc.Save(t.Context(), e.user, uuid.New(), e.input("Over", over)); err != nil {
		t.Errorf("after the clock advanced: %v", err)
	}
}

// TestPlansServiceRuleOrder pins which error wins when a request breaks
// several rules at once, from the store's transaction: ownership, then the
// conflict rule, then the plan cap, then the exercise references.
func TestPlansServiceRuleOrder(t *testing.T) {
	e := newPlanSvcEnv(t)
	id := uuid.New()
	if _, _, err := e.svc.Save(t.Context(), e.user, id, e.input("Stored", "2026-09-19T08:00:00Z")); err != nil {
		t.Fatal(err)
	}
	ghost := uuid.NewString()
	withGhost := func(in domain.PlanInput) domain.PlanInput {
		in.Exercises[0].ExerciseID = &ghost
		return in
	}

	t.Run("another user's plan is NotFound before anything else", func(t *testing.T) {
		_, _, err := e.svc.Save(t.Context(), e.other, id, withGhost(e.input("x", "2026-09-19T07:00:00Z")))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("error = %v, want NotFound", err)
		}
	})
	t.Run("a stale write is a conflict before the unknown exercise", func(t *testing.T) {
		_, _, err := e.svc.Save(t.Context(), e.user, id, withGhost(e.input("x", "2026-09-19T07:00:00Z")))
		var c *domain.ConflictError
		if !errors.As(err, &c) || c.Issue != domain.IssueStale {
			t.Errorf("error = %v, want a stale conflict", err)
		}
	})
	t.Run("a newer write with an unknown exercise is a validation error", func(t *testing.T) {
		_, _, err := e.svc.Save(t.Context(), e.user, id, withGhost(e.input("x", "2026-09-19T08:30:00Z")))
		want := []domain.FieldIssue{{Field: "exercises[0].exercise_id", Issue: domain.IssueUnknownReference}}
		if got := planSvcIssues(t, err); !slices.Equal(got, want) {
			t.Errorf("issues = %v, want %v", got, want)
		}
	})
}

func TestPlansServiceList(t *testing.T) {
	e := newPlanSvcEnv(t)
	for i, name := range []string{"Pull", "push", "Legs"} {
		in := e.input(name, fmt.Sprintf("2026-09-19T08:0%d:00Z", i))
		if _, _, err := e.svc.Save(t.Context(), e.user, uuid.New(), in); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.svc.Save(t.Context(), e.other, uuid.New(), e.input("Not mine", "2026-09-19T08:00:00Z")); err != nil {
		t.Fatal(err)
	}

	summaries, err := e.svc.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range summaries.Items {
		names = append(names, it.Name)
		if it.ExerciseCount != 1 {
			t.Errorf("%s: exercise_count = %d, want 1", it.Name, it.ExerciseCount)
		}
	}
	if !slices.Equal(names, []string{"Legs", "Pull"}) || summaries.NextCursor == nil {
		t.Errorf("first page = %v, next %v; want Legs, Pull and a cursor", names, summaries.NextCursor)
	}

	expanded, err := e.svc.ListExpanded(t.Context(), e.user, domain.PlanListParams{Limit: 50, Cursor: *summaries.NextCursor})
	if err != nil || len(expanded.Items) != 1 || expanded.Items[0].Name != "push" || len(expanded.Items[0].Exercises) != 1 || expanded.NextCursor != nil {
		t.Errorf("second page = %+v, %v", expanded, err)
	}

	for _, limit := range []int{0, domain.MaxPageLimit + 1} {
		_, err := e.svc.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: limit})
		if got := planSvcIssues(t, err); !slices.Equal(got, []domain.FieldIssue{{Field: "limit", Issue: domain.IssueOutOfRange}}) {
			t.Errorf("limit %d: %v", limit, got)
		}
	}
	_, err = e.svc.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: 5, Cursor: "garbage"})
	var bad *domain.BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("bad cursor: %v, want a BadRequestError", err)
	}
}

func TestPlansServiceDelete(t *testing.T) {
	e := newPlanSvcEnv(t)
	id := uuid.New()
	if _, _, err := e.svc.Save(t.Context(), e.user, id, e.input("Doomed", "2026-09-19T08:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(t.Context(), e.other, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete by another user: %v, want NotFound", err)
	}
	for range 2 { // idempotent
		if err := e.svc.Delete(t.Context(), e.user, id); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	if _, err := e.svc.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
	_, _, err := e.svc.Save(t.Context(), e.user, id, e.input("Undo", "2026-09-19T08:59:00Z"))
	var c *domain.ConflictError
	if !errors.As(err, &c) || c.Issue != domain.IssueDeleted {
		t.Errorf("Save after Delete: %v, want a deleted conflict", err)
	}
	if err := e.svc.Delete(t.Context(), e.user, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete of an unknown id: %v, want NotFound", err)
	}
}

func TestPlansServiceDecimalsStayExact(t *testing.T) {
	e := newPlanSvcEnv(t)
	in := e.input("Decimals", "2026-09-19T08:00:00Z")
	// Decoded the way the HTTP layer does it: from JSON text.
	var body domain.PlanExerciseInput
	const raw = `{"exercise_id":"%s","position":0,"target_sets":1,"target_weight":0.1,"target_distance_meters":1e2}`
	if err := json.Unmarshal([]byte(fmt.Sprintf(raw, e.exercises[0])), &body); err != nil {
		t.Fatal(err)
	}
	in.Exercises = []domain.PlanExerciseInput{body}

	plan, _, err := e.svc.Save(t.Context(), e.user, uuid.New(), in)
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Exercises[0]
	if got.TargetWeight.String() != "0.10" || got.TargetDistanceMeters.String() != "100.00" {
		t.Errorf("stored %v / %v, want 0.10 / 100.00", got.TargetWeight, got.TargetDistanceMeters)
	}
}
