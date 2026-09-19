package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

// planResult is the outcome of one concurrent Save.
type planResult struct {
	plan    domain.Plan
	created bool
	err     error
}

// planRace runs fn(0..n-1) at the same moment (all goroutines are released by
// one channel close) and returns the results in index order.
func planRace(n int, fn func(i int) planResult) []planResult {
	results := make([]planResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = fn(i)
		}()
	}
	close(start)
	wg.Wait()
	return results
}

// planVariant is the i-th writer's version of a plan: its name, one exercise
// row with target_sets i+1 and a note, all saying "v<i>", so a stored plan
// that mixes two writers' data is recognisable.
func (e *planEnv) variant(i int, at time.Time) domain.PlanSpec {
	row := e.ex(i%len(e.exercises), 0)
	row.TargetSets = i%20 + 1
	row.Notes = planPtr(fmt.Sprintf("v%d", i))
	return planSpec(fmt.Sprintf("v%d", i), at, row)
}

func planRequireConsistent(t *testing.T, plan domain.Plan) {
	t.Helper()
	if len(plan.Exercises) != 1 || plan.Exercises[0].Notes == nil || *plan.Exercises[0].Notes != plan.Name {
		t.Errorf("plan %q does not match its exercises: %s", plan.Name, planJSON(t, plan))
	}
}

// TestPlansConcurrentSavesOfOneID runs many writers with different updated_at
// against one id: no error other than a stale conflict, exactly one creation,
// and the plan of the greatest updated_at wins, whole (never a mix).
func TestPlansConcurrentSavesOfOneID(t *testing.T) {
	const writers = 20
	e := newPlanEnv(t)

	for round := range 3 {
		id := planNewID(t)
		results := planRace(writers, func(i int) planResult {
			// A shuffled order of timestamps: the goroutine index is not the
			// time order.
			at := planAt(round*1000 + (i*7)%writers)
			plan, created, err := e.plans.Save(t.Context(), e.user, id, e.variant(i, at))
			return planResult{plan, created, err}
		})

		created := 0
		for i, r := range results {
			switch {
			case r.err == nil:
				planRequireConsistent(t, r.plan)
				if r.created {
					created++
				}
			default:
				var c *domain.ConflictError
				if !errors.As(r.err, &c) || c.Issue != domain.IssueStale {
					t.Errorf("writer %d: %v, want success or a stale conflict", i, r.err)
					continue
				}
				current, ok := c.Current.(domain.Plan)
				if !ok {
					t.Errorf("writer %d: stale conflict without the current plan", i)
					continue
				}
				planRequireConsistent(t, current)
			}
		}
		if created != 1 {
			t.Errorf("round %d: %d writers created the plan, want exactly 1", round, created)
		}

		// The winner is the writer with the greatest timestamp: (i*7)%20 is 19
		// for i = 17.
		final := e.get(t, id)
		if want := planAt(round*1000 + writers - 1); !final.UpdatedAt.Equal(want) {
			t.Errorf("round %d: final updated_at = %v, want the maximum %v", round, final.UpdatedAt, want)
		}
		planRequireConsistent(t, final)
		if final.Name != "v17" {
			t.Errorf("round %d: final plan is %q, want the writer with the greatest updated_at (v17)", round, final.Name)
		}
		if n := e.count(t, `SELECT count(*) FROM workout_plans WHERE id = $1`, id); n != 1 {
			t.Errorf("%d rows for the id", n)
		}
		if n := e.count(t, `SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, id); n != 1 {
			t.Errorf("%d exercise rows, want 1 (children replaced, never doubled)", n)
		}
	}
	planNoLeakedConns(t, e.db)
}

// TestPlansConcurrentSavesWithEqualUpdatedAt: same updated_at, different
// content. The first accepted write wins; every other one is a no-op that
// returns that stored copy, so all writers see one plan.
func TestPlansConcurrentSavesWithEqualUpdatedAt(t *testing.T) {
	const writers = 16
	e := newPlanEnv(t)
	id := planNewID(t)
	results := planRace(writers, func(i int) planResult {
		plan, created, err := e.plans.Save(t.Context(), e.user, id, e.variant(i, planAt(5)))
		return planResult{plan, created, err}
	})

	created := 0
	winner := ""
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("writer %d: %v", i, r.err)
		}
		if r.created {
			created++
		}
		planRequireConsistent(t, r.plan)
		if winner == "" {
			winner = r.plan.Name
		}
		if r.plan.Name != winner {
			t.Errorf("writer %d saw %q, another saw %q: a no-op must return the stored copy", i, r.plan.Name, winner)
		}
	}
	if created != 1 {
		t.Errorf("%d writers created the plan, want 1", created)
	}
	if final := e.get(t, id); final.Name != winner {
		t.Errorf("stored plan is %q, the writers saw %q", final.Name, winner)
	}
}

// TestPlansConcurrentFirstInsertsByTwoUsers is the one place a primary key
// collision can happen (each user's writes are serialised): two users create
// the same id at once. One creates it, the other gets NotFound after the
// internal retry; nobody sees a unique-violation error.
func TestPlansConcurrentFirstInsertsByTwoUsers(t *testing.T) {
	e := newPlanEnv(t)
	for round := range 25 {
		id := planNewID(t)
		results := planRace(2, func(i int) planResult {
			userID := e.user
			if i == 1 {
				userID = e.other
			}
			plan, created, err := e.plans.Save(t.Context(), userID, id, e.variant(i, planAt(round)))
			return planResult{plan, created, err}
		})
		wins, notFound := 0, 0
		for i, r := range results {
			switch {
			case r.err == nil && r.created:
				wins++
			case errors.Is(r.err, domain.ErrNotFound):
				notFound++
			default:
				t.Fatalf("round %d writer %d: created=%v err=%v; want a creation or NotFound", round, i, r.created, r.err)
			}
		}
		if wins != 1 || notFound != 1 {
			t.Fatalf("round %d: %d creations and %d NotFound, want 1 and 1", round, wins, notFound)
		}
	}
}

// TestPlansConcurrentInsertsRespectTheCap: 30 different plans race for the
// last 10 slots. Exactly 10 fit; the others get plan_limit.
func TestPlansConcurrentInsertsRespectTheCap(t *testing.T) {
	e := newPlanEnv(t)
	for range domain.MaxActivePlansPerUser - 10 {
		testutil.SeedPlan(t, e.db, e.user)
	}
	results := planRace(30, func(i int) planResult {
		plan, created, err := e.plans.Save(t.Context(), e.user, planNewID(t), e.variant(i, planAt(i)))
		return planResult{plan, created, err}
	})
	ok, limited := 0, 0
	for i, r := range results {
		switch {
		case r.err == nil && r.created:
			ok++
		default:
			var v *domain.ValidationError
			if !errors.As(r.err, &v) || len(v.Issues) != 1 || v.Issues[0].Issue != domain.IssuePlanLimit {
				t.Errorf("writer %d: %v, want a creation or plan_limit", i, r.err)
				continue
			}
			limited++
		}
	}
	if ok != 10 || limited != 20 {
		t.Errorf("%d created, %d refused; want 10 and 20", ok, limited)
	}
	if n := e.count(t, `SELECT count(*) FROM workout_plans WHERE user_id = $1 AND deleted_at IS NULL`, e.user); n != domain.MaxActivePlansPerUser {
		t.Errorf("%d active plans, want exactly %d", n, domain.MaxActivePlansPerUser)
	}
}

// TestPlansConcurrentSaveAndDelete: a delete and a newer save race. Either
// order ends with the plan deleted; the save either applied first or was
// answered with a deleted conflict.
func TestPlansConcurrentSaveAndDelete(t *testing.T) {
	e := newPlanEnv(t)
	for round := range 20 {
		id := planNewID(t)
		e.save(t, id, e.variant(0, planAt(0)))
		var saveErr, deleteErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _, saveErr = e.plans.Save(t.Context(), e.user, id, e.variant(1, planAt(10)))
		}()
		go func() {
			defer wg.Done()
			<-start
			deleteErr = e.plans.SoftDelete(t.Context(), e.user, id)
		}()
		close(start)
		wg.Wait()

		if deleteErr != nil {
			t.Fatalf("round %d: delete: %v", round, deleteErr)
		}
		if saveErr != nil {
			var c *domain.ConflictError
			if !errors.As(saveErr, &c) || c.Issue != domain.IssueDeleted {
				t.Fatalf("round %d: save: %v, want success or a deleted conflict", round, saveErr)
			}
		}
		if _, err := e.plans.Get(t.Context(), e.user, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("round %d: the plan is still visible after the delete: %v", round, err)
		}
		var deleted bool
		if err := e.db.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM workout_plans WHERE id = $1`, id).Scan(&deleted); err != nil || !deleted {
			t.Fatalf("round %d: deleted = %v, %v", round, deleted, err)
		}
	}
}

// TestPlansConcurrentWritesKeepTheSyncFeedOrdered: while several writers save
// different plans of one user, a reader that follows the feed by
// server_updated_at never misses a plan. Writers of one user are serialised, so
// server_updated_at grows in commit order.
func TestPlansConcurrentWritesKeepTheSyncFeedOrdered(t *testing.T) {
	const writers, perWriter = 8, 6
	e := newPlanEnv(t)

	var wg sync.WaitGroup
	done := make(chan struct{})
	seen := map[uuid.UUID]bool{}
	var readerErr error
	wg.Add(1)
	go func() { // the "phone": pulls repeatedly with the largest value it has seen
		defer wg.Done()
		since := time.Unix(0, 0).UTC()
		for finished := false; !finished; {
			select {
			case <-done:
				finished = true // one last pull after the writers ended
			default:
			}
			page, err := e.plans.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: domain.MaxPageLimit, UpdatedSince: &since})
			if err != nil {
				readerErr = err
				return
			}
			for _, it := range page.Items {
				seen[it.ID] = true
				if it.ServerUpdatedAt.After(since) {
					since = it.ServerUpdatedAt
				}
			}
		}
	}()

	var writersWG sync.WaitGroup
	var mu sync.Mutex
	var all []uuid.UUID
	for w := range writers {
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			for k := range perWriter {
				id := planNewID(t)
				if _, _, err := e.plans.Save(t.Context(), e.user, id, e.variant(w*perWriter+k, planAt(1))); err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
				mu.Lock()
				all = append(all, id)
				mu.Unlock()
			}
		}()
	}
	writersWG.Wait()
	close(done)
	wg.Wait()

	if readerErr != nil {
		t.Fatalf("reader: %v", readerErr)
	}
	for _, id := range all {
		if !seen[id] {
			t.Errorf("plan %v was never delivered by the sync feed", id)
		}
	}
	if len(all) != writers*perWriter {
		t.Errorf("%d plans saved, want %d", len(all), writers*perWriter)
	}
}
