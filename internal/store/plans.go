package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
)

// Plans is the data access for workout_plans and workout_plan_exercises
// (endpoints 6, 7, 10 and 17). Every method takes the caller's user id: a plan
// of another user is reported as not found, never as forbidden.
type Plans struct{ db *DB }

// NewPlans returns the Plans store on db.
func NewPlans(db *DB) *Plans { return &Plans{db: db} }

// planColumns are the columns of a plan row, in planScan order. The API name
// updated_at is the client_updated_at column.
const planColumns = `p.id, p.name, p.description, p.client_updated_at, p.created_at,
	p.server_updated_at, p.deleted_at`

// planScanner is what pgx.Row and pgx.Rows share.
type planScanner interface{ Scan(dest ...any) error }

// planScan reads the columns of planColumns, followed by dest.
func planScan(row planScanner, p *domain.Plan, dest ...any) error {
	err := row.Scan(append([]any{
		&p.ID, &p.Name, &p.Description, &p.UpdatedAt, &p.CreatedAt,
		&p.ServerUpdatedAt, &p.DeletedAt,
	}, dest...)...)
	if err != nil {
		return err
	}
	// pgx returns timestamptz in the local zone.
	p.UpdatedAt, p.CreatedAt, p.ServerUpdatedAt = p.UpdatedAt.UTC(), p.CreatedAt.UTC(), p.ServerUpdatedAt.UTC()
	if p.DeletedAt != nil {
		t := p.DeletedAt.UTC()
		p.DeletedAt = &t
	}
	p.Exercises = []domain.PlanExercise{}
	return nil
}

// ---------------------------------------------------------------------------
// Save

// Save is the idempotent upsert of endpoint 10 (docs/implementation-plan.md
// section 5), in one transaction:
//
//  1. lock the caller's user row, so this user's writes are serialised: the
//     active-plan cap is exact and server_updated_at grows in commit order for
//     the sync feed,
//  2. lock the plan row by id (FOR UPDATE); a row of another user is NotFound,
//  3. apply the sync conflict rule (domain.DecideSync): a deleted row or a
//     stale write is a 409 (stale carries the stored plan), the same updated_at
//     changes nothing and returns the stored plan,
//  4. on an insert, refuse the 101st active plan (422 plan_limit),
//  5. check that every exercise id exists (422; a soft-deleted exercise is
//     accepted),
//  6. insert or update the row, replace its exercises and set
//     server_updated_at.
//
// It returns the stored plan and whether it was created (201, else 200). When
// two requests insert the same new id at once, the loser fails on the primary
// key and runs once more, now finding the winner's row.
func (s *Plans) Save(ctx context.Context, userID, id uuid.UUID, in domain.PlanSpec) (domain.Plan, bool, error) {
	var (
		out     domain.Plan
		created bool
	)
	attempt := func() error {
		return s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, created, err = planSave(ctx, tx, userID, id, in)
			return err
		})
	}
	err := attempt()
	if c, ok := IsUniqueViolation(err); ok && c == "workout_plans_pkey" {
		err = attempt()
	}
	if err != nil {
		return domain.Plan{}, false, err
	}
	return out, created, nil
}

func planSave(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, in domain.PlanSpec) (domain.Plan, bool, error) {
	// The user lock serialises this user's writes. Without it two concurrent
	// inserts could both pass the cap, and two server_updated_at values could
	// commit out of order, which would make a sync feed miss a row.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		return domain.Plan{}, false, fmt.Errorf("store: lock user: %w", err)
	}

	var (
		owner     uuid.UUID
		state     domain.SyncState
		deletedAt *time.Time
	)
	err := tx.QueryRow(ctx,
		`SELECT user_id, client_updated_at, deleted_at FROM workout_plans WHERE id = $1 FOR UPDATE`, id,
	).Scan(&owner, &state.ClientUpdatedAt, &deletedAt)

	var existing *domain.SyncState
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return domain.Plan{}, false, fmt.Errorf("store: lock plan: %w", err)
	case owner != userID:
		return domain.Plan{}, false, domain.NewNotFound()
	default:
		state.Deleted = deletedAt != nil
		existing = &state
	}

	action := domain.DecideSync(existing, in.UpdatedAt)
	switch action {
	case domain.SyncDeleted:
		return domain.Plan{}, false, action.Err(nil)
	case domain.SyncStale:
		cur, err := planLoad(ctx, tx, "p.id = $1", id)
		if err != nil {
			return domain.Plan{}, false, err
		}
		return domain.Plan{}, false, action.Err(cur)
	case domain.SyncNoop:
		// A retry is answered with the stored copy; its exercises are not
		// looked at, so a lost response can be retried even if the master
		// changed in between.
		cur, err := planLoad(ctx, tx, "p.id = $1", id)
		if err != nil {
			return domain.Plan{}, false, err
		}
		return cur, false, nil
	}

	if action == domain.SyncInsert {
		if err := planCheckLimit(ctx, tx, userID); err != nil {
			return domain.Plan{}, false, err
		}
	}
	if err := planCheckExercises(ctx, tx, in.Exercises); err != nil {
		return domain.Plan{}, false, err
	}

	if action == domain.SyncInsert {
		_, err = tx.Exec(ctx, `
			INSERT INTO workout_plans (id, user_id, name, description, client_updated_at, created_at, server_updated_at)
			SELECT $1, $2, $3, $4, $5, t.ts, t.ts
			FROM (SELECT greatest(clock_timestamp(),
				(SELECT max(server_updated_at) FROM workout_plans WHERE user_id = $2) + interval '1 microsecond') AS ts) t`,
			id, userID, in.Name, in.Description, in.UpdatedAt)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE workout_plans p SET name = $2, description = $3, client_updated_at = $4,
				server_updated_at = t.ts
			FROM (SELECT greatest(clock_timestamp(),
				(SELECT max(server_updated_at) FROM workout_plans WHERE user_id = $5) + interval '1 microsecond') AS ts) t
			WHERE p.id = $1`,
			id, in.Name, in.Description, in.UpdatedAt, userID)
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM workout_plan_exercises WHERE workout_plan_id = $1`, id)
		}
	}
	if err != nil {
		return domain.Plan{}, false, fmt.Errorf("store: write plan: %w", err)
	}
	if err := planInsertExercises(ctx, tx, id, in.Exercises); err != nil {
		return domain.Plan{}, false, err
	}

	cur, err := planLoad(ctx, tx, "p.id = $1", id)
	if err != nil {
		return domain.Plan{}, false, err
	}
	return cur, action == domain.SyncInsert, nil
}

// planCheckLimit refuses a new plan once the user already has
// domain.MaxActivePlansPerUser active (not soft-deleted) ones. It runs inside
// the user lock, so the count cannot race with another insert.
func planCheckLimit(ctx context.Context, q Querier, userID uuid.UUID) error {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM workout_plans WHERE user_id = $1 AND deleted_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return fmt.Errorf("store: count plans: %w", err)
	}
	if n >= domain.MaxActivePlansPerUser {
		return domain.NewValidation("", domain.IssuePlanLimit)
	}
	return nil
}

// planCheckExercises is the plain-SQL validation of spec 10: every exercise_id
// must exist (a soft-deleted exercise is fine). All problems are reported
// together as a 422, with the JSON path of each.
func planCheckExercises(ctx context.Context, q Querier, exercises []domain.PlanExercise) error {
	if len(exercises) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(exercises))
	for i, e := range exercises {
		ids[i] = e.ExerciseID
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
	var v domain.ValidationError
	for i, e := range exercises {
		if _, ok := exists[e.ExerciseID]; !ok {
			v.Add(domain.FieldIndex("exercises", i)+".exercise_id", domain.IssueUnknownReference)
		}
	}
	return v.Err()
}

// planInsertExerciseSQL inserts one child row. One statement per row, sent in
// one batch by planInsertExercises. Decimals travel as text and are cast in
// SQL, so they are never floats.
const planInsertExerciseSQL = `
	INSERT INTO workout_plan_exercises (id, workout_plan_id, exercise_id, position, target_sets,
		target_reps, target_reps_max, target_weight, target_duration_seconds,
		target_distance_meters, rest_seconds, notes)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10::numeric, $11, $12)`

// planInsertExercises inserts the exercises of a plan with server generated
// ids, one queued statement per row, in a single round trip.
func planInsertExercises(ctx context.Context, q Querier, planID uuid.UUID, exercises []domain.PlanExercise) error {
	if len(exercises) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, e := range exercises {
		rowID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("store: new id: %w", err)
		}
		batch.Queue(planInsertExerciseSQL, rowID, planID, e.ExerciseID, e.Position, e.TargetSets,
			e.TargetReps, e.TargetRepsMax, planText(e.TargetWeight), e.TargetDurationSeconds,
			planText(e.TargetDistanceMeters), e.RestSeconds, e.Notes)
	}
	br := q.SendBatch(ctx, batch)
	defer br.Close()
	for range exercises {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: insert plan exercises: %w", err)
		}
	}
	return nil
}

// planText is the canonical decimal text of an optional target, or nil.
func planText(d *domain.PlanDecimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// ---------------------------------------------------------------------------
// Get

// Get returns one plan with its exercises in position order. A plan that does
// not exist, is soft-deleted or belongs to another user is NotFound.
func (s *Plans) Get(ctx context.Context, userID, id uuid.UUID) (domain.Plan, error) {
	var out domain.Plan
	err := s.db.WithTxOptions(ctx, planReadOnly, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = planLoad(ctx, tx, "p.id = $1 AND p.user_id = $2 AND p.deleted_at IS NULL", id, userID)
		return err
	})
	if err != nil {
		return domain.Plan{}, err
	}
	return out, nil
}

// planReadOnly gives a read of a parent row and its children one snapshot, so
// a concurrent save is seen entirely or not at all.
var planReadOnly = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// planLoad reads the one plan matching cond (a WHERE condition on p with its
// args) and its exercises, or returns NotFound.
func planLoad(ctx context.Context, q Querier, cond string, args ...any) (domain.Plan, error) {
	plans := make([]domain.Plan, 1)
	err := planScan(q.QueryRow(ctx, `SELECT `+planColumns+` FROM workout_plans p WHERE `+cond, args...), &plans[0])
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Plan{}, domain.NewNotFound()
	}
	if err != nil {
		return domain.Plan{}, fmt.Errorf("store: read plan: %w", err)
	}
	if err := planAttachChildren(ctx, q, plans); err != nil {
		return domain.Plan{}, err
	}
	return plans[0], nil
}

// planAttachChildren fills Exercises of every plan with one query, however
// many plans there are, ordered by position.
func planAttachChildren(ctx context.Context, q Querier, plans []domain.Plan) error {
	if len(plans) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(plans))
	byID := make(map[uuid.UUID]*domain.Plan, len(plans))
	for i := range plans {
		ids[i] = plans[i].ID
		byID[plans[i].ID] = &plans[i]
	}

	rows, err := q.Query(ctx, `
		SELECT workout_plan_id, exercise_id, position, target_sets, target_reps, target_reps_max,
			target_weight::text, target_duration_seconds, target_distance_meters::text,
			rest_seconds, notes
		FROM workout_plan_exercises WHERE workout_plan_id = ANY($1)
		ORDER BY workout_plan_id, position`, ids)
	if err != nil {
		return fmt.Errorf("store: read plan exercises: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			planID           uuid.UUID
			e                domain.PlanExercise
			weight, distance *string
		)
		if err := rows.Scan(&planID, &e.ExerciseID, &e.Position, &e.TargetSets, &e.TargetReps,
			&e.TargetRepsMax, &weight, &e.TargetDurationSeconds, &distance, &e.RestSeconds, &e.Notes); err != nil {
			return fmt.Errorf("store: read plan exercises: %w", err)
		}
		if e.TargetWeight, err = planDecimal(weight); err != nil {
			return err
		}
		if e.TargetDistanceMeters, err = planDecimal(distance); err != nil {
			return err
		}
		if p := byID[planID]; p != nil {
			p.Exercises = append(p.Exercises, e)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read plan exercises: %w", err)
	}
	return nil
}

// planDecimal parses the numeric text of a target back into a PlanDecimal.
func planDecimal(text *string) (*domain.PlanDecimal, error) {
	if text == nil {
		return nil, nil
	}
	d, err := domain.ParsePlanDecimal(*text)
	if err != nil {
		return nil, fmt.Errorf("store: parse plan decimal: %w", err)
	}
	return &d, nil
}

// ---------------------------------------------------------------------------
// List

// ListSummaries is endpoint 6 without expand: one page of plan summaries and
// the cursor of the next page (nil on the last page). p.UpdatedSince switches
// to sync mode (order (server_updated_at, id), deleted plans included); the
// default order is (lower(name), id) and hides deleted plans unless
// p.IncludeDeleted.
func (s *Plans) ListSummaries(ctx context.Context, userID uuid.UUID, p domain.PlanListParams) (domain.PlanPage[domain.PlanSummary], error) {
	limit, nameAfter, syncAfter, err := planPaging(p)
	if err != nil {
		return domain.PlanPage[domain.PlanSummary]{}, err
	}
	rows, next, err := planPage(ctx, s.db, userID, p, nameAfter, syncAfter, limit, true)
	if err != nil {
		return domain.PlanPage[domain.PlanSummary]{}, err
	}
	items := make([]domain.PlanSummary, len(rows))
	for i, r := range rows {
		items[i] = domain.PlanSummary{
			ID: r.plan.ID, Name: r.plan.Name, Description: r.plan.Description,
			ExerciseCount: r.exerciseCount, CreatedAt: r.plan.CreatedAt,
			UpdatedAt: r.plan.UpdatedAt, ServerUpdatedAt: r.plan.ServerUpdatedAt,
			DeletedAt: r.plan.DeletedAt,
		}
	}
	return domain.PlanPage[domain.PlanSummary]{Items: items, NextCursor: next}, nil
}

// ListExpanded is endpoint 6 with expand=exercises: the same page as
// ListSummaries, but full plans with exercises. Children are read in one extra
// query for the whole page, in one snapshot with the page.
func (s *Plans) ListExpanded(ctx context.Context, userID uuid.UUID, p domain.PlanListParams) (domain.PlanPage[domain.Plan], error) {
	limit, nameAfter, syncAfter, err := planPaging(p)
	if err != nil {
		return domain.PlanPage[domain.Plan]{}, err
	}
	var (
		out  []domain.Plan
		next *string
	)
	err = s.db.WithTxOptions(ctx, planReadOnly, func(ctx context.Context, tx pgx.Tx) error {
		rows, n, err := planPage(ctx, tx, userID, p, nameAfter, syncAfter, limit, false)
		if err != nil {
			return err
		}
		out, next = make([]domain.Plan, len(rows)), n
		for i, r := range rows {
			out[i] = r.plan
		}
		return planAttachChildren(ctx, tx, out)
	})
	if err != nil {
		return domain.PlanPage[domain.Plan]{}, err
	}
	return domain.PlanPage[domain.Plan]{Items: out, NextCursor: next}, nil
}

// planListRow is a plan row with the sort key of the default order and, for a
// summary page, its exercise count.
type planListRow struct {
	plan          domain.Plan
	lowerName     string
	exerciseCount int
}

// planNameAfter is the decoded default-order cursor.
type planNameAfter struct {
	lowerName string
	id        uuid.UUID
}

// planPaging resolves the page size (anything outside the allowed range is a
// 422) and decodes the cursor (a bad one, or one issued for the other sort
// order, is a 400).
func planPaging(p domain.PlanListParams) (limit int, nameAfter *planNameAfter, syncAfter *domain.Cursor, err error) {
	limit = p.Limit
	if limit < domain.MinPageLimit || limit > domain.MaxPageLimit {
		return 0, nil, nil, domain.NewValidation("limit", domain.IssueOutOfRange)
	}
	if p.Cursor == "" {
		return limit, nil, nil, nil
	}
	if p.UpdatedSince != nil {
		c, err := domain.DecodeCursor(p.Cursor)
		if err != nil {
			return 0, nil, nil, err
		}
		return limit, nil, &c, nil
	}
	name, id, err := domain.DecodePlanNameCursor(p.Cursor)
	if err != nil {
		return 0, nil, nil, err
	}
	return limit, &planNameAfter{lowerName: name, id: id}, nil, nil
}

// planPage runs the list query (limit+1 rows to learn whether there is a next
// page) and returns the rows of this page with its next cursor.
func planPage(ctx context.Context, q Querier, userID uuid.UUID, p domain.PlanListParams, nameAfter *planNameAfter, syncAfter *domain.Cursor, limit int, counts bool) ([]planListRow, *string, error) {
	sql, args := planListSQL(userID, p, nameAfter, syncAfter, limit+1, counts)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list plans: %w", err)
	}
	defer rows.Close()

	out := []planListRow{}
	for rows.Next() {
		var r planListRow
		dest := []any{&r.lowerName}
		if counts {
			dest = append(dest, &r.exerciseCount)
		}
		if err := planScan(rows, &r.plan, dest...); err != nil {
			return nil, nil, fmt.Errorf("store: list plans: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: list plans: %w", err)
	}

	var next *string
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		if p.UpdatedSince != nil {
			next = planNext(domain.EncodeCursor(domain.Cursor{At: last.plan.ServerUpdatedAt, ID: last.plan.ID}))
		} else {
			next = planNext(domain.EncodePlanNameCursor(last.lowerName, last.plan.ID))
		}
	}
	return out, next, nil
}

func planNext(s string) *string { return &s }

// planListSQL builds the list query. Values are always arguments; only fixed
// fragments are concatenated.
func planListSQL(userID uuid.UUID, p domain.PlanListParams, nameAfter *planNameAfter, syncAfter *domain.Cursor, limit int, counts bool) (string, []any) {
	args := []any{userID}
	where := []string{"p.user_id = $1"}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	syncMode := p.UpdatedSince != nil
	if syncMode {
		// PostgreSQL stores microseconds; floor the bound so a bound just
		// below a stored value still includes it, and one just above still
		// excludes it.
		where = append(where, "p.server_updated_at > "+arg(p.UpdatedSince.Truncate(time.Microsecond)))
	} else if !p.IncludeDeleted {
		where = append(where, "p.deleted_at IS NULL")
	}
	if syncMode && syncAfter != nil {
		where = append(where, fmt.Sprintf("(p.server_updated_at, p.id) > (%s::timestamptz, %s::uuid)",
			arg(syncAfter.At), arg(syncAfter.ID)))
	}
	if !syncMode && nameAfter != nil {
		where = append(where, fmt.Sprintf("(lower(p.name), p.id) > (%s, %s::uuid)",
			arg(nameAfter.lowerName), arg(nameAfter.id)))
	}

	order := "lower(p.name), p.id"
	if syncMode {
		order = "p.server_updated_at, p.id"
	}

	// lower(name) follows the plan columns, where planScan expects dest.
	cols := planColumns + ", lower(p.name)"
	if counts {
		cols += `, (SELECT count(*) FROM workout_plan_exercises e WHERE e.workout_plan_id = p.id)::int`
	}
	return fmt.Sprintf("SELECT %s FROM workout_plans p WHERE %s ORDER BY %s LIMIT %s",
		cols, strings.Join(where, " AND "), order, arg(limit)), args
}

// ---------------------------------------------------------------------------
// Delete

// SoftDelete is endpoint 17: it sets deleted_at and server_updated_at to the
// same instant and keeps the children. Deleting an already deleted plan
// succeeds without changing it (idempotent); a plan that does not exist or
// belongs to another user is NotFound.
func (s *Plans) SoftDelete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE workout_plans p SET deleted_at = t.ts, server_updated_at = t.ts
		FROM (SELECT greatest(clock_timestamp(),
			(SELECT max(server_updated_at) FROM workout_plans WHERE user_id = $2) + interval '1 microsecond') AS ts) t
		WHERE p.id = $1 AND p.user_id = $2 AND p.deleted_at IS NULL`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete plan: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var owned bool
	err = s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workout_plans WHERE id = $1 AND user_id = $2)`, id, userID).Scan(&owned)
	if err != nil {
		return fmt.Errorf("store: delete plan: %w", err)
	}
	if !owned {
		return domain.NewNotFound()
	}
	return nil
}
