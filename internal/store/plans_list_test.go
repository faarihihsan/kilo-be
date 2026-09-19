package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// planLister runs one page of the list in one of its two shapes and reduces it
// to what the paging tests compare: the ids in order and the next cursor.
type planLister func(ctx context.Context, p *store.Plans, userID uuid.UUID, params domain.PlanListParams) ([]uuid.UUID, *string, error)

func planListSummaries(ctx context.Context, p *store.Plans, userID uuid.UUID, params domain.PlanListParams) ([]uuid.UUID, *string, error) {
	page, err := p.ListSummaries(ctx, userID, params)
	var ids []uuid.UUID
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	return ids, page.NextCursor, err
}

func planListExpanded(ctx context.Context, p *store.Plans, userID uuid.UUID, params domain.PlanListParams) ([]uuid.UUID, *string, error) {
	page, err := p.ListExpanded(ctx, userID, params)
	var ids []uuid.UUID
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	return ids, page.NextCursor, err
}

// planWalk follows next_cursor until it is nil and returns every id seen.
func planWalk(t *testing.T, list planLister, p *store.Plans, userID uuid.UUID, params domain.PlanListParams) []uuid.UUID {
	t.Helper()
	var all []uuid.UUID
	for range 1000 {
		ids, next, err := list(t.Context(), p, userID, params)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(ids) > params.Limit {
			t.Fatalf("page has %d items, limit %d", len(ids), params.Limit)
		}
		all = append(all, ids...)
		if next == nil {
			return all
		}
		if len(ids) != params.Limit {
			t.Fatalf("next_cursor set on a short page (%d of %d items)", len(ids), params.Limit)
		}
		params.Cursor = *next
	}
	t.Fatal("paging did not end")
	return nil
}

// planSeeded is a plan created for the list tests.
type planSeeded struct {
	id   uuid.UUID
	name string
}

// planSeedNames seeds one plan per name, with ids in ascending order.
func planSeedNames(t *testing.T, e *planEnv, userID uuid.UUID, names ...string) []planSeeded {
	t.Helper()
	var out []planSeeded
	for _, name := range names {
		id := testutil.SeedPlan(t, e.db, userID, testutil.WithPlanID(planNewID(t)), testutil.WithPlanName(name))
		out = append(out, planSeeded{id, name})
	}
	return out
}

// planByName is the expected default order: lower(name), then id.
func planByName(plans []planSeeded) []uuid.UUID {
	sorted := slices.Clone(plans)
	slices.SortFunc(sorted, func(a, b planSeeded) int {
		if c := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
			return c
		}
		return strings.Compare(a.id.String(), b.id.String())
	})
	ids := make([]uuid.UUID, len(sorted))
	for i, p := range sorted {
		ids[i] = p.id
	}
	return ids
}

func TestPlansListDefaultOrderAndPaging(t *testing.T) {
	e := newPlanEnv(t)
	// Mixed case, equal names in different case and exactly equal, digits.
	seeded := planSeedNames(t, e, e.user,
		"banana", "Apple", "apple", "cherry", "APPLE", "Cherryx", "apple", "10 sets", "Zebra", "banana")
	want := planByName(seeded)

	for name, list := range map[string]planLister{"summaries": planListSummaries, "expanded": planListExpanded} {
		t.Run(name, func(t *testing.T) {
			for _, limit := range []int{1, 2, 3, 4, 9, len(seeded), len(seeded) + 1, domain.MaxPageLimit} {
				got := planWalk(t, list, e.plans, e.user, domain.PlanListParams{Limit: limit})
				if !slices.Equal(got, want) {
					t.Errorf("limit %d: order\n got  %v\n want %v", limit, got, want)
				}
			}
		})
	}

	t.Run("an exact page boundary has no next cursor", func(t *testing.T) {
		_, next, err := planListSummaries(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: len(seeded)})
		if err != nil || next != nil {
			t.Errorf("limit == total: next = %v, err = %v; want nil, nil", next, err)
		}
		_, next, err = planListSummaries(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: len(seeded) - 1})
		if err != nil || next == nil {
			t.Errorf("limit == total-1: next = %v, err = %v; want a cursor", next, err)
		}
	})
}

func TestPlansListPagingIsStableUnderWrites(t *testing.T) {
	e := newPlanEnv(t)
	seeded := planSeedNames(t, e, e.user, "b", "d", "f", "h", "j", "l")
	params := domain.PlanListParams{Limit: 2}

	page1, next, err := planListSummaries(t.Context(), e.plans, e.user, params)
	if err != nil || next == nil {
		t.Fatalf("page 1: %v %v", next, err)
	}
	if want := []uuid.UUID{seeded[0].id, seeded[1].id}; !slices.Equal(page1, want) {
		t.Fatalf("page 1 = %v, want %v", page1, want)
	}

	// Between the pages: one plan sorts before the cursor, one after it, one
	// ahead of the cursor is deleted, and one behind it is renamed to sort
	// first.
	before := planSeedNames(t, e, e.user, "a")[0]
	after := planSeedNames(t, e, e.user, "g")[0]
	if err := e.plans.SoftDelete(t.Context(), e.user, seeded[3].id); err != nil { // "h"
		t.Fatal(err)
	}
	e.save(t, seeded[0].id, planSpec("zzz renamed", planFuture())) // "b", already seen

	var rest []uuid.UUID
	for cursor := *next; ; {
		params.Cursor = cursor
		ids, n, err := planListSummaries(t.Context(), e.plans, e.user, params)
		if err != nil {
			t.Fatal(err)
		}
		rest = append(rest, ids...)
		if n == nil {
			break
		}
		cursor = *n
	}
	// f, g (new), j and l; h is deleted; a (new) sorts before the cursor, so it
	// is not shown; b was seen on page 1, but its new name puts it after the
	// cursor, so it comes again, once, at the end.
	want := []uuid.UUID{seeded[2].id, after.id, seeded[4].id, seeded[5].id, seeded[0].id}
	if !slices.Equal(rest, want) {
		t.Errorf("remaining pages = %v\n want %v (a = %v)", rest, want, before.id)
	}
}

func TestPlansListFilters(t *testing.T) {
	e := newPlanEnv(t)
	live := planSeedNames(t, e, e.user, "live a", "live b")[0]
	gone := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanName("gone"), testutil.WithPlanDeletedAt(planAt(5)))
	planSeedNames(t, e, e.other, "theirs")

	t.Run("soft-deleted plans are hidden by default", func(t *testing.T) {
		ids, _, err := planListSummaries(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: 50})
		if err != nil || slices.Contains(ids, gone) || len(ids) != 2 || ids[0] != live.id {
			t.Errorf("ids = %v, err = %v", ids, err)
		}
	})
	t.Run("include_deleted shows them in name order, with deleted_at", func(t *testing.T) {
		page, err := e.plans.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: 50, IncludeDeleted: true})
		if err != nil || len(page.Items) != 3 {
			t.Fatalf("items = %v, err = %v", page.Items, err)
		}
		if page.Items[0].Name != "gone" || page.Items[0].DeletedAt == nil || !page.Items[0].DeletedAt.Equal(planAt(5)) || page.Items[1].DeletedAt != nil {
			t.Errorf("items = %+v", page.Items)
		}
	})
	t.Run("only the caller's plans", func(t *testing.T) {
		page, err := e.plans.ListSummaries(t.Context(), e.other, domain.PlanListParams{Limit: 50, IncludeDeleted: true})
		if err != nil || len(page.Items) != 1 || page.Items[0].Name != "theirs" {
			t.Errorf("items = %+v, err = %v", page.Items, err)
		}
		page, err = e.plans.ListSummaries(t.Context(), uuid.New(), domain.PlanListParams{Limit: 50})
		if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
			t.Errorf("unknown user: %+v, %v; want an empty, non-nil page", page, err)
		}
	})
	t.Run("an empty page has non-nil items in both shapes", func(t *testing.T) {
		s, err := e.plans.ListSummaries(t.Context(), uuid.New(), domain.PlanListParams{Limit: 5})
		if err != nil || s.Items == nil {
			t.Errorf("summaries: %+v, %v", s, err)
		}
		x, err := e.plans.ListExpanded(t.Context(), uuid.New(), domain.PlanListParams{Limit: 5})
		if err != nil || x.Items == nil {
			t.Errorf("expanded: %+v, %v", x, err)
		}
	})
}

func TestPlansListLimits(t *testing.T) {
	e := newPlanEnv(t)
	for _, limit := range []int{-1, 0, domain.MaxPageLimit + 1} {
		_, err := e.plans.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: limit})
		if got := planIssues(t, err); !slices.Equal(got, []domain.FieldIssue{{Field: "limit", Issue: domain.IssueOutOfRange}}) {
			t.Errorf("summaries limit %d: %v", limit, got)
		}
		_, err = e.plans.ListExpanded(t.Context(), e.user, domain.PlanListParams{Limit: limit})
		planIssues(t, err)
	}
}

func TestPlansListSummaryContent(t *testing.T) {
	e := newPlanEnv(t)
	none := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanName("a none"))
	two := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanName("b two"), testutil.WithPlanDescription("desc"),
		testutil.WithPlanClientUpdatedAt(planAt(7)), testutil.WithPlanServerUpdatedAt(planAt(8)),
		testutil.WithPlanExercise(e.exercises[0]), testutil.WithPlanExercise(e.exercises[0]))
	five := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanName("c five"),
		testutil.WithPlanExercise(e.exercises[0]), testutil.WithPlanExercise(e.exercises[1]), testutil.WithPlanExercise(e.exercises[2]),
		testutil.WithPlanExercise(e.exercises[3]), testutil.WithPlanExercise(e.exercises[0]))

	page, err := e.plans.ListSummaries(t.Context(), e.user, domain.PlanListParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]domain.PlanSummary{}
	for _, it := range page.Items {
		got[it.ID] = it
	}
	if got[none].ExerciseCount != 0 || got[two].ExerciseCount != 2 || got[five].ExerciseCount != 5 {
		t.Errorf("exercise_count = %d, %d, %d; want 0, 2, 5", got[none].ExerciseCount, got[two].ExerciseCount, got[five].ExerciseCount)
	}
	s := got[two]
	if s.Name != "b two" || s.Description == nil || *s.Description != "desc" || !s.UpdatedAt.Equal(planAt(7)) || !s.ServerUpdatedAt.Equal(planAt(8)) {
		t.Errorf("summary = %+v", s)
	}
	for _, ts := range []time.Time{s.UpdatedAt, s.CreatedAt, s.ServerUpdatedAt} {
		if ts.Location() != time.UTC {
			t.Errorf("time %v is not in UTC", ts)
		}
	}
}

// TestPlansListExpandedMatchesGet checks that every item of the expanded list
// is exactly what Get returns for that plan.
func TestPlansListExpandedMatchesGet(t *testing.T) {
	e := newPlanEnv(t)
	ids := []uuid.UUID{}
	for i, name := range []string{"one", "two", "three", "empty"} {
		var rows []domain.PlanExercise
		for pos := range i * 2 { // 0, 2, 4, 6 exercises; the last plan gets none
			row := e.ex(pos%4, 6-pos)
			row.TargetWeight = planDecimal(t, fmt.Sprintf("%d.%02d", 20+pos, pos))
			row.Notes = planPtr(fmt.Sprintf("note %d", pos))
			rows = append(rows, row)
		}
		if name == "empty" {
			rows = nil
		}
		id := planNewID(t)
		e.save(t, id, planSpec(name, planAt(i), rows...))
		ids = append(ids, id)
	}
	page, err := e.plans.ListExpanded(t.Context(), e.user, domain.PlanListParams{Limit: 10})
	if err != nil || len(page.Items) != len(ids) {
		t.Fatalf("items = %d, err = %v", len(page.Items), err)
	}
	for _, it := range page.Items {
		if it.Exercises == nil {
			t.Errorf("%s: Exercises is nil, want [] for JSON", it.Name)
		}
		if want := e.get(t, it.ID); planJSON(t, it) != planJSON(t, want) {
			t.Errorf("%s: expanded item\n %s\n differs from Get\n %s", it.Name, planJSON(t, it), planJSON(t, want))
		}
	}
}

// TestPlansListSyncMode covers updated_since: order (server_updated_at, id),
// strictly after the given time, deleted plans included, cursor paging across
// equal timestamps.
func TestPlansListSyncMode(t *testing.T) {
	e := newPlanEnv(t)
	seed := func(name string, sec int, opts ...testutil.PlanOption) uuid.UUID {
		opts = append(opts, testutil.WithPlanID(planNewID(t)), testutil.WithPlanName(name), testutil.WithPlanServerUpdatedAt(planAt(sec)))
		return testutil.SeedPlan(t, e.db, e.user, opts...)
	}
	// Names deliberately sort against the time order.
	p30 := seed("a", 30)
	p20a := seed("z", 20)
	p20b := seed("y", 20, testutil.WithPlanDeletedAt(planAt(20))) // same time, larger id, deleted
	p10 := seed("m", 10)
	p05 := seed("n", 5)
	theirs := testutil.SeedPlan(t, e.db, e.other, testutil.WithPlanServerUpdatedAt(planAt(15)))
	since := func(sec int) *time.Time { at := planAt(sec); return &at }

	tests := []struct {
		name  string
		since *time.Time
		want  []uuid.UUID
	}{
		{"from the epoch: everything, deleted included", planTime(time.Unix(0, 0).UTC()), []uuid.UUID{p05, p10, p20a, p20b, p30}},
		{"strictly after: the plan at exactly the time is not repeated", since(10), []uuid.UUID{p20a, p20b, p30}},
		{"after the two equal timestamps", since(20), []uuid.UUID{p30}},
		{"one microsecond before them", planTime(planAt(20).Add(-time.Microsecond)), []uuid.UUID{p20a, p20b, p30}},
		{"sub-microsecond above a stored value excludes it", planTime(planAt(20).Add(500 * time.Nanosecond)), []uuid.UUID{p30}},
		{"sub-microsecond below a stored value includes it", planTime(planAt(20).Add(-500 * time.Nanosecond)), []uuid.UUID{p20a, p20b, p30}},
		{"after the last", since(30), nil},
		{"a time zone other than UTC is the same instant", planTime(planAt(10).In(time.FixedZone("x", 7*3600))), []uuid.UUID{p20a, p20b, p30}},
	}
	for name, list := range map[string]planLister{"summaries": planListSummaries, "expanded": planListExpanded} {
		for _, tt := range tests {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				for _, limit := range []int{1, 2, 50} {
					got := planWalk(t, list, e.plans, e.user, domain.PlanListParams{Limit: limit, UpdatedSince: tt.since})
					if !slices.Equal(got, tt.want) {
						t.Errorf("limit %d: got %v, want %v", limit, got, tt.want)
					}
				}
			})
		}
	}
	if slices.Contains(planWalk(t, planListSummaries, e.plans, e.user, domain.PlanListParams{Limit: 50, UpdatedSince: since(0)}), theirs) {
		t.Error("another user's plan is in the feed")
	}

	t.Run("include_deleted=false does not hide deleted plans in sync mode", func(t *testing.T) {
		got := planWalk(t, planListSummaries, e.plans, e.user, domain.PlanListParams{Limit: 50, UpdatedSince: since(0), IncludeDeleted: false})
		if !slices.Contains(got, p20b) {
			t.Errorf("deleted plan missing from %v", got)
		}
	})
	t.Run("a deleted plan is expanded with its rows and deleted_at", func(t *testing.T) {
		id := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanServerUpdatedAt(planAt(40)), testutil.WithPlanDeletedAt(planAt(40)),
			testutil.WithPlanExercise(e.exercises[0]))
		page, err := e.plans.ListExpanded(t.Context(), e.user, domain.PlanListParams{Limit: 50, UpdatedSince: since(35)})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
			t.Fatalf("items = %+v, err = %v", page.Items, err)
		}
		if page.Items[0].DeletedAt == nil || len(page.Items[0].Exercises) != 1 {
			t.Errorf("item = %+v", page.Items[0])
		}
	})
	t.Run("a save appears in the feed after the previous one", func(t *testing.T) {
		first, _ := e.save(t, planNewID(t), planSpec("first", planAt(1)))
		second, _ := e.save(t, planNewID(t), planSpec("second", planAt(1)))
		got := planWalk(t, planListSummaries, e.plans, e.user, domain.PlanListParams{Limit: 50, UpdatedSince: &first.ServerUpdatedAt})
		if !slices.Equal(got, []uuid.UUID{second.ID}) {
			t.Errorf("feed after the first save = %v, want only the second (%v)", got, second.ID)
		}
		if !second.ServerUpdatedAt.After(first.ServerUpdatedAt) {
			t.Errorf("server_updated_at did not advance: %v then %v", first.ServerUpdatedAt, second.ServerUpdatedAt)
		}
	})
}

func planTime(t time.Time) *time.Time { return &t }

func TestPlansListSyncPagingIsStableUnderWrites(t *testing.T) {
	e := newPlanEnv(t)
	var ids []uuid.UUID
	for i := range 6 {
		ids = append(ids, testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanID(planNewID(t)), testutil.WithPlanServerUpdatedAt(planAt(10*(i+1)))))
	}
	epoch := time.Unix(0, 0).UTC()
	params := domain.PlanListParams{Limit: 2, UpdatedSince: &epoch}
	page1, next, err := planListSummaries(t.Context(), e.plans, e.user, params)
	if err != nil || next == nil || !slices.Equal(page1, ids[:2]) {
		t.Fatalf("page 1 = %v %v %v", page1, next, err)
	}
	// Between the pages a seen plan changes (moves to the end of the feed) and
	// an unseen one is deleted.
	e.save(t, ids[0], planSpec("touched", planFuture()))
	if err := e.plans.SoftDelete(t.Context(), e.user, ids[3]); err != nil {
		t.Fatal(err)
	}
	var rest []uuid.UUID
	for cursor := *next; ; {
		params.Cursor = cursor
		got, n, err := planListSummaries(t.Context(), e.plans, e.user, params)
		if err != nil {
			t.Fatal(err)
		}
		rest = append(rest, got...)
		if n == nil {
			break
		}
		cursor = *n
	}
	// 30, 50, 60, then the two plans that changed after page 1, in the order
	// they changed: 10 (touched), 40 (deleted). Each appears once, at its new
	// position in the feed.
	want := []uuid.UUID{ids[2], ids[4], ids[5], ids[0], ids[3]}
	if !slices.Equal(rest, want) {
		t.Errorf("rest = %v, want %v", rest, want)
	}
}

func TestPlansListCursorMisuseIsBadRequest(t *testing.T) {
	e := newPlanEnv(t)
	planSeedNames(t, e, e.user, "a", "b", "c")
	epoch := time.Unix(0, 0).UTC()

	_, nameCursor, err := planListSummaries(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: 1})
	if err != nil || nameCursor == nil {
		t.Fatalf("name cursor: %v %v", nameCursor, err)
	}
	_, syncCursor, err := planListSummaries(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: 1, UpdatedSince: &epoch})
	if err != nil || syncCursor == nil {
		t.Fatalf("sync cursor: %v %v", syncCursor, err)
	}

	tests := []struct {
		name   string
		cursor string
		since  *time.Time
	}{
		{"garbage", "not-a-cursor!", nil},
		{"garbage in sync mode", "not-a-cursor!", &epoch},
		{"valid base64, not a cursor", "AAAA", nil},
		{"a name-order cursor in sync mode", *nameCursor, &epoch},
		{"a sync cursor in name order", *syncCursor, nil},
		{"another key cursor with the wrong parts", domain.EncodeKeyCursor("name", "a"), nil},
		{"the right shape with a bad id", domain.EncodeKeyCursor("name", "a", "not-a-uuid"), nil},
		{"the right shape with another tag", domain.EncodeKeyCursor("nope", "a", uuid.NewString()), nil},
	}
	for _, tt := range tests {
		for name, list := range map[string]planLister{"summaries": planListSummaries, "expanded": planListExpanded} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				_, _, err := list(t.Context(), e.plans, e.user, domain.PlanListParams{Limit: 10, Cursor: tt.cursor, UpdatedSince: tt.since})
				var bad *domain.BadRequestError
				if !errors.As(err, &bad) {
					t.Fatalf("error = %v (%T), want *domain.BadRequestError", err, err)
				}
			})
		}
	}
	planNoLeakedConns(t, e.db)
}

// planQueryCounter counts the statements a pool sends.
type planQueryCounter struct{ n atomic.Int64 }

func (c *planQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *planQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (c *planQueryCounter) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}
func (c *planQueryCounter) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
	c.n.Add(1)
}
func (c *planQueryCounter) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// planTraced returns a plan store on its own pool over the same database, with
// a statement counter.
func planTraced(t *testing.T, e *planEnv) (*store.Plans, *planQueryCounter) {
	t.Helper()
	cfg := e.db.Pool().Config()
	counter := &planQueryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return store.NewPlans(store.PlanTestDB(pool)), counter
}

// TestPlansReadsDoNotQueryPerPlan is the no-N+1 check: the number of
// statements a read sends must not depend on how many plans or exercises it
// returns.
func TestPlansReadsDoNotQueryPerPlan(t *testing.T) {
	e := newPlanEnv(t)
	traced, counter := planTraced(t, e)

	seed := func(n, exercises int) {
		for range n {
			opts := []testutil.PlanOption{testutil.WithPlanID(planNewID(t))}
			for range exercises {
				opts = append(opts, testutil.WithPlanExercise(e.exercises[0]))
			}
			testutil.SeedPlan(t, e.db, e.user, opts...)
		}
	}
	measure := func(f func()) int64 {
		before := counter.n.Load()
		f()
		return counter.n.Load() - before
	}
	page := domain.PlanListParams{Limit: domain.MaxPageLimit}
	summaries := func(wantItems int) {
		p, err := traced.ListSummaries(t.Context(), e.user, page)
		if err != nil || len(p.Items) != wantItems {
			t.Fatalf("summaries: %d items, %v; want %d", len(p.Items), err, wantItems)
		}
	}
	expanded := func(wantItems int) {
		p, err := traced.ListExpanded(t.Context(), e.user, page)
		if err != nil || len(p.Items) != wantItems {
			t.Fatalf("expanded: %d items, %v; want %d", len(p.Items), err, wantItems)
		}
	}

	seed(2, 1)
	smallSummaries := measure(func() { summaries(2) })
	smallExpanded := measure(func() { expanded(2) })
	seed(40, 12)
	bigSummaries := measure(func() { summaries(42) })
	bigExpanded := measure(func() { expanded(42) })

	if smallSummaries != bigSummaries || smallExpanded != bigExpanded {
		t.Errorf("statements grew with the data: summaries %d -> %d, expanded %d -> %d",
			smallSummaries, bigSummaries, smallExpanded, bigExpanded)
	}
	// list + one children query + the snapshot transaction's begin and commit.
	if bigExpanded > 5 || bigSummaries > 4 {
		t.Errorf("statements: summaries %d, expanded %d; want at most 4 and 5", bigSummaries, bigExpanded)
	}
	t.Logf("statements per call: summaries %d, expanded %d", bigSummaries, bigExpanded)

	t.Run("get", func(t *testing.T) {
		few, many := planNewID(t), planNewID(t)
		e.save(t, few, planSpec("few", planAt(0), e.ex(0, 0)))
		var rows []domain.PlanExercise
		for pos := range 40 {
			rows = append(rows, e.ex(pos%4, pos))
		}
		e.save(t, many, planSpec("many", planAt(0), rows...))
		nFew := measure(func() { _, _ = traced.Get(t.Context(), e.user, few) })
		nMany := measure(func() { _, _ = traced.Get(t.Context(), e.user, many) })
		if nFew != nMany || nMany > 4 {
			t.Errorf("Get sent %d statements for 1 exercise and %d for 40; want equal and at most 4", nFew, nMany)
		}
	})
	t.Run("save", func(t *testing.T) {
		few, many := planNewID(t), planNewID(t)
		var rows []domain.PlanExercise
		for pos := range 50 {
			rows = append(rows, e.ex(pos%4, pos))
		}
		save := func(id uuid.UUID, spec domain.PlanSpec) {
			if _, _, err := traced.Save(t.Context(), e.user, id, spec); err != nil {
				t.Fatalf("Save: %v", err)
			}
		}
		nFew := measure(func() { save(few, planSpec("few", planAt(0), e.ex(0, 0))) })
		nMany := measure(func() { save(many, planSpec("many", planAt(0), rows...)) })
		// The 49 extra inserts travel in one batch, and are counted one by one
		// by the tracer; what must not grow is the number of round trips of
		// the other statements: lock, select, count, check, write, delete, load.
		if nMany-nFew != 49 {
			t.Errorf("Save sent %d statements for 1 exercise and %d for 50; want exactly 49 more (one insert per row)", nFew, nMany)
		}
	})
}

// TestPlansListCursorCarriesLongMultibyteNames: the name cursor of the longest
// allowed names (100 four-byte characters) still round-trips.
func TestPlansListCursorCarriesLongMultibyteNames(t *testing.T) {
	e := newPlanEnv(t)
	base := strings.Repeat("🏋", domain.PlanNameMaxLen-1)
	seeded := planSeedNames(t, e, e.user, base+"c", base+"a", base+"b")
	want := planByName(seeded)
	if got := planWalk(t, planListSummaries, e.plans, e.user, domain.PlanListParams{Limit: 1}); !slices.Equal(got, want) {
		t.Errorf("walk = %v, want %v", got, want)
	}
}
