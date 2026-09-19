package handlers_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// planVariantBody is the i-th writer's version of a plan. The name, the notes of
// its one exercise and target_sets all say which writer it is, so a stored plan
// that mixes two writers is recognisable.
func planVariantBody(a *planAPI, i int, updatedAt string) string {
	return planBody(fmt.Sprintf("v%d", i), updatedAt,
		fmt.Sprintf(`{"exercise_id":%q,"position":0,"target_sets":%d,"notes":"v%d"}`, a.exercises[i%len(a.exercises)], i%20+1, i))
}

// TestPlansHTTPConcurrentPutsOfOneID: many clients PUT the same id at the same
// moment, with different and equal updated_at values. Under -race this must end
// in one consistent plan, the one with the greatest updated_at, and no request
// may be answered with a 5xx.
func TestPlansHTTPConcurrentPutsOfOneID(t *testing.T) {
	const writers = 24
	a := newPlanAPI(t)

	type result struct {
		status int
		body   string
	}
	race := func(id uuid.UUID, bodyFor func(i int) string) []result {
		results := make([]result, writers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec := a.put(a.user, id, bodyFor(i))
				results[i] = result{rec.Code, rec.Body.String()}
			}()
		}
		close(start)
		wg.Wait()
		return results
	}
	statuses := func(results []result) map[int]int {
		m := map[int]int{}
		for _, r := range results {
			m[r.status]++
		}
		return m
	}

	t.Run("different updated_at: the maximum wins", func(t *testing.T) {
		for round := range 3 {
			id := uuid.New()
			// updated_at = base + (i*5 mod 24) seconds is a permutation of 0..23 (gcd(5,24)=1),
			// so the goroutine index is not the time order. The maximum, 23, is i = 23*5^-1 = 23*5 mod 24 = 19.
			results := race(id, func(i int) string {
				return planVariantBody(a, i, planTime(time.Duration(-3*time.Hour+time.Duration((i*5)%writers)*time.Second)))
			})
			got := statuses(results)
			for status := range got {
				if status != http.StatusCreated && status != http.StatusOK && status != http.StatusConflict {
					t.Errorf("round %d: unexpected status %d in %v", round, status, got)
				}
			}
			if got[http.StatusCreated] != 1 {
				t.Errorf("round %d: %d creations, want exactly 1 (statuses %v)", round, got[http.StatusCreated], got)
			}
			final := planRequireOK(t, a.get(a.user, id), http.StatusOK)
			if final["name"] != "v19" {
				t.Errorf("round %d: final plan is %v, want v19 (greatest updated_at)", round, final["name"])
			}
			if want := planHTTPNow.Add(-3*time.Hour + 23*time.Second).Format(time.RFC3339); final["updated_at"] != want {
				t.Errorf("round %d: updated_at = %v, want %v", round, final["updated_at"], want)
			}
			exs := final["exercises"].([]any)
			if len(exs) != 1 || exs[0].(map[string]any)["notes"] != "v19" || fmt.Sprint(exs[0].(map[string]any)["target_sets"]) != "20" {
				t.Errorf("round %d: exercises = %v, want the ones of v19", round, exs)
			}
			// Every 409 carries a consistent copy of some writer's plan.
			for _, r := range results {
				if r.status != http.StatusConflict {
					continue
				}
				if !strings.Contains(r.body, `"issue":"stale"`) || !strings.Contains(r.body, `"current":{"id":"`+id.String()+`"`) {
					t.Errorf("round %d: a 409 without the current plan: %s", round, r.body)
				}
			}
		}
	})

	t.Run("equal updated_at: the first accepted wins, the rest are no-ops", func(t *testing.T) {
		id := uuid.New()
		results := race(id, func(i int) string { return planVariantBody(a, i, planTime(-time.Hour)) })
		got := statuses(results)
		if got[http.StatusCreated] != 1 || got[http.StatusOK] != writers-1 || len(got) != 2 {
			t.Fatalf("statuses = %v, want one 201 and %d 200", got, writers-1)
		}
		final := planRequireOK(t, a.get(a.user, id), http.StatusOK)
		// Every response, 201 or 200 no-op, is the plan of the winner.
		for i, r := range results {
			if !strings.Contains(r.body, fmt.Sprintf(`"name":%q`, final["name"])) {
				t.Errorf("writer %d saw a plan other than the stored one: %s", i, r.body)
			}
		}
	})

	t.Run("ids race independently and all succeed", func(t *testing.T) {
		ids := make([]uuid.UUID, writers)
		for i := range ids {
			ids[i] = uuid.New()
		}
		var wg sync.WaitGroup
		codes := make([]int, writers)
		start := make(chan struct{})
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = a.put(a.user, ids[i], planVariantBody(a, i, planTime(-time.Hour))).Code
			}()
		}
		close(start)
		wg.Wait()
		for i, c := range codes {
			if c != http.StatusCreated {
				t.Errorf("writer %d: status %d, want 201", i, c)
			}
		}
		if got := len(planItems(t, a.list(a.user, "limit=200"))); got < writers {
			t.Errorf("list shows %d plans, want at least %d", got, writers)
		}
	})

	a.planRequireNoServerErrors()
}

// TestPlansHTTPConcurrentReadsDuringWrites: readers never see half a save. A
// plan is rewritten in a loop while others GET it and list it expanded; every
// copy they see is one writer's plan, whole.
func TestPlansHTTPConcurrentReadsDuringWrites(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	planRequireOK(t, a.put(a.user, id, planVariantBody(a, 0, planTime(-2*time.Hour))), http.StatusCreated)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	var mu sync.Mutex
	var problems []string
	note := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	consistent := func(plan map[string]any) bool {
		exs, _ := plan["exercises"].([]any)
		if len(exs) != 1 {
			return false
		}
		return exs[0].(map[string]any)["notes"] == plan["name"]
	}
	for range 3 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := a.get(a.user, id)
				if rec.Code != http.StatusOK {
					note("GET status %d", rec.Code)
					return
				}
				if plan := planJSONOf(t, rec); !consistent(plan) {
					note("GET saw a mixed plan: %s", rec.Body.String())
				}
				for _, it := range planItems(t, a.list(a.user, "expand=exercises")) {
					if !consistent(it) {
						note("list saw a mixed plan: %v", it)
					}
				}
			}
		}()
	}
	for i := 1; i <= 25; i++ {
		rec := a.put(a.user, id, planVariantBody(a, i, planTime(time.Duration(-2*time.Hour+time.Duration(i)*time.Second))))
		if rec.Code != http.StatusOK {
			t.Fatalf("write %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	close(stop)
	readers.Wait()
	if len(problems) > 0 {
		slices.Sort(problems)
		t.Errorf("%d problem(s), first: %s", len(problems), problems[0])
	}
	a.planRequireNoServerErrors()
}
