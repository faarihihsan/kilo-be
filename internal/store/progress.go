package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
)

// Progress is the data access for progress, progress_exercises and progress_sets
// (endpoints 3, 4, 5 and 16). Every method takes the caller's user id: a
// session of another user is reported as not found, never as forbidden.
type Progress struct{ db *DB }

// NewProgress returns the Progress store on db.
func NewProgress(db *DB) *Progress { return &Progress{db: db} }

// progressColumns are the columns of a session row, in progressScan order.
const progressColumns = `p.id, p.workout_plan_id, p.name, p.notes, p.started_at, p.ended_at,
	p.duration_seconds, p.client_updated_at, p.created_at, p.server_updated_at, p.deleted_at`

// progressScanner is what pgx.Row and pgx.Rows share.
type progressScanner interface{ Scan(dest ...any) error }

// progressScan reads the columns of progressColumns, followed by dest.
func progressScan(row progressScanner, p *domain.Progress, dest ...any) error {
	err := row.Scan(append([]any{
		&p.ID, &p.WorkoutPlanID, &p.Name, &p.Notes, &p.StartedAt, &p.EndedAt,
		&p.DurationSeconds, &p.UpdatedAt, &p.CreatedAt, &p.ServerUpdatedAt, &p.DeletedAt,
	}, dest...)...)
	if err != nil {
		return err
	}
	// pgx returns timestamptz in the local zone.
	p.StartedAt, p.EndedAt = p.StartedAt.UTC(), p.EndedAt.UTC()
	p.UpdatedAt, p.CreatedAt, p.ServerUpdatedAt = p.UpdatedAt.UTC(), p.CreatedAt.UTC(), p.ServerUpdatedAt.UTC()
	if p.DeletedAt != nil {
		t := p.DeletedAt.UTC()
		p.DeletedAt = &t
	}
	p.Exercises = []domain.ProgressExercise{}
	return nil
}

// ---------------------------------------------------------------------------
// Save

// Save is the idempotent upsert of endpoint 3 (docs/implementation-plan.md
// section 5), in one transaction:
//
//  1. lock the row by id (FOR UPDATE); a row of another user is NotFound,
//  2. apply the sync conflict rule (domain.DecideSync): a deleted row or a
//     stale write is a 409 (stale carries the stored session), the same
//     updated_at changes nothing and returns the stored session,
//  3. otherwise check that the exercise and plan ids exist (422),
//  4. insert or update the row, replace its exercises and sets, and set
//     server_updated_at.
//
// It returns the stored session and whether it was created (201, else 200).
// When two requests insert the same new id at once, the loser fails on the
// primary key and runs once more, now finding the winner's row.
func (s *Progress) Save(ctx context.Context, userID, id uuid.UUID, in domain.ProgressSave) (domain.Progress, bool, error) {
	return saveWithRetry(ctx, s.db, "progress_pkey", func(ctx context.Context, tx pgx.Tx) (domain.Progress, bool, error) {
		return progressSave(ctx, tx, userID, id, in)
	})
}

func progressSave(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, in domain.ProgressSave) (domain.Progress, bool, error) {
	action, err := lockForSync(ctx, tx, "progress", "progress", userID, id, in.UpdatedAt)
	if err != nil {
		return domain.Progress{}, false, err
	}
	if cur, done, err := syncPreflight(action, func() (domain.Progress, error) {
		return progressLoad(ctx, tx, "p.id = $1", id)
	}); done {
		return cur, false, err
	}

	if err := progressCheckRefs(ctx, tx, userID, in); err != nil {
		return domain.Progress{}, false, err
	}

	// One instant for created_at (on insert), server_updated_at and the
	// children, taken after the lock so it is later than any earlier write to
	// this row: a client that pulled after that write cannot miss this one.
	if action == domain.SyncInsert {
		_, err = tx.Exec(ctx, `
			INSERT INTO progress (id, user_id, workout_plan_id, name, notes, started_at, ended_at,
				duration_seconds, client_updated_at, created_at, server_updated_at)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, t.ts, t.ts
			FROM (SELECT clock_timestamp() AS ts) t`,
			id, userID, in.WorkoutPlanID, in.Name, in.Notes, in.StartedAt, in.EndedAt,
			in.DurationSeconds, in.UpdatedAt)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE progress p SET workout_plan_id = $2, name = $3, notes = $4, started_at = $5,
				ended_at = $6, duration_seconds = $7, client_updated_at = $8, server_updated_at = t.ts
			FROM (SELECT clock_timestamp() AS ts) t
			WHERE p.id = $1`,
			id, in.WorkoutPlanID, in.Name, in.Notes, in.StartedAt, in.EndedAt,
			in.DurationSeconds, in.UpdatedAt)
		if err == nil {
			// The sets go with their exercises (ON DELETE CASCADE).
			_, err = tx.Exec(ctx, `DELETE FROM progress_exercises WHERE progress_id = $1`, id)
		}
	}
	if err != nil {
		return domain.Progress{}, false, fmt.Errorf("store: write progress: %w", err)
	}
	if err := progressInsertChildren(ctx, tx, id, in.Exercises); err != nil {
		return domain.Progress{}, false, err
	}

	cur, err := progressLoad(ctx, tx, "p.id = $1", id)
	if err != nil {
		return domain.Progress{}, false, err
	}
	return cur, action == domain.SyncInsert, nil
}

// progressCheckRefs is the plain-SQL validation of spec 03: every exercise_id
// exists (a soft-deleted exercise is fine) and workout_plan_id, when given, is
// a plan of the caller (a soft-deleted plan is fine). All problems are
// reported together as a 422.
func progressCheckRefs(ctx context.Context, q Querier, userID uuid.UUID, in domain.ProgressSave) error {
	var v domain.ValidationError

	if in.WorkoutPlanID != nil {
		var ok bool
		err := q.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM workout_plans WHERE id = $1 AND user_id = $2)`,
			*in.WorkoutPlanID, userID).Scan(&ok)
		if err != nil {
			return fmt.Errorf("store: check plan: %w", err)
		}
		if !ok {
			v.Add("workout_plan_id", domain.IssueUnknownReference)
		}
	}

	ids := make([]uuid.UUID, 0, len(in.Exercises))
	for _, e := range in.Exercises {
		ids = append(ids, e.ExerciseID)
	}
	if err := addExerciseRefIssues(ctx, q, ids, &v); err != nil {
		return err
	}
	return v.Err()
}

// progressInsertChildren inserts the exercises and all their sets with server
// generated ids: two statements however many rows, each fed by parallel
// arrays through unnest. Decimals travel as text and are cast in SQL, so they
// are never floats.
func progressInsertChildren(ctx context.Context, q Querier, progressID uuid.UUID, exercises []domain.ProgressExercise) error {
	if len(exercises) == 0 {
		return nil
	}
	var (
		exID, exExerciseID []uuid.UUID
		exPosition         []int32
		exNotes            []*string

		setID, setExerciseRow          []uuid.UUID
		setPosition                    []int32
		setType                        []string
		setReps, setDuration           []*int32
		setWeight, setDistance, setRPE []*string
		setCompleted                   []bool
	)
	for _, e := range exercises {
		rowID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("store: new id: %w", err)
		}
		exID = append(exID, rowID)
		exExerciseID = append(exExerciseID, e.ExerciseID)
		exPosition = append(exPosition, int32(e.Position))
		exNotes = append(exNotes, e.Notes)

		for _, st := range e.Sets {
			id, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("store: new id: %w", err)
			}
			setID = append(setID, id)
			setExerciseRow = append(setExerciseRow, rowID)
			setPosition = append(setPosition, int32(st.Position))
			setType = append(setType, string(st.Type))
			setReps = append(setReps, progressInt32(st.Reps))
			setDuration = append(setDuration, progressInt32(st.DurationSeconds))
			setWeight = append(setWeight, progressText(st.Weight))
			setDistance = append(setDistance, progressText(st.DistanceMeters))
			setRPE = append(setRPE, progressText(st.RPE))
			setCompleted = append(setCompleted, st.Completed)
		}
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO progress_exercises (id, progress_id, exercise_id, position, notes)
		SELECT id, $1, exercise_id, position, notes
		FROM unnest($2::uuid[], $3::uuid[], $4::int4[], $5::text[]) AS t(id, exercise_id, position, notes)`,
		progressID, exID, exExerciseID, exPosition, exNotes); err != nil {
		return fmt.Errorf("store: insert progress exercises: %w", err)
	}
	if len(setID) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO progress_sets (id, progress_exercise_id, position, type, reps, weight,
			duration_seconds, distance_meters, rpe, completed)
		SELECT id, progress_exercise_id, position, type, reps, weight::numeric,
			duration_seconds, distance_meters::numeric, rpe::numeric, completed
		FROM unnest($1::uuid[], $2::uuid[], $3::int4[], $4::text[], $5::int4[], $6::text[],
			$7::int4[], $8::text[], $9::text[], $10::bool[])
			AS t(id, progress_exercise_id, position, type, reps, weight,
				duration_seconds, distance_meters, rpe, completed)`,
		setID, setExerciseRow, setPosition, setType, setReps, setWeight,
		setDuration, setDistance, setRPE, setCompleted); err != nil {
		return fmt.Errorf("store: insert progress sets: %w", err)
	}
	return nil
}

func progressInt32(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func progressText(d *domain.ProgressDecimal) *string {
	if d == nil {
		return nil
	}
	s := string(*d)
	return &s
}

// ---------------------------------------------------------------------------
// Get

// Get returns one session with its exercises and sets. A session that does not
// exist, is soft-deleted or belongs to another user is NotFound.
func (s *Progress) Get(ctx context.Context, userID, id uuid.UUID) (domain.Progress, error) {
	var out domain.Progress
	err := s.db.WithTxOptions(ctx, readOnlyTx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = progressLoad(ctx, tx, "p.id = $1 AND p.user_id = $2 AND p.deleted_at IS NULL", id, userID)
		return err
	})
	if err != nil {
		return domain.Progress{}, err
	}
	return out, nil
}

// progressLoad reads the one session matching cond (a WHERE condition on p
// with its args) and its children, or returns NotFound.
func progressLoad(ctx context.Context, q Querier, cond string, args ...any) (domain.Progress, error) {
	sessions := make([]domain.Progress, 1)
	err := progressScan(q.QueryRow(ctx, `SELECT `+progressColumns+` FROM progress p WHERE `+cond, args...), &sessions[0])
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Progress{}, domain.NewNotFound()
	}
	if err != nil {
		return domain.Progress{}, fmt.Errorf("store: read progress: %w", err)
	}
	if err := progressAttachChildren(ctx, q, sessions); err != nil {
		return domain.Progress{}, err
	}
	return sessions[0], nil
}

// progressAttachChildren fills Exercises (and their Sets) of every session with
// two queries, however many sessions there are.
func progressAttachChildren(ctx context.Context, q Querier, sessions []domain.Progress) error {
	if len(sessions) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(sessions))
	byID := make(map[uuid.UUID]*domain.Progress, len(sessions))
	for i := range sessions {
		ids[i] = sessions[i].ID
		byID[sessions[i].ID] = &sessions[i]
	}

	rows, err := q.Query(ctx, `
		SELECT id, progress_id, exercise_id, position, notes
		FROM progress_exercises WHERE progress_id = ANY($1)
		ORDER BY progress_id, position`, ids)
	if err != nil {
		return fmt.Errorf("store: read progress exercises: %w", err)
	}
	// Position of each exercise row inside its session, to attach its sets.
	type slot struct {
		session *domain.Progress
		index   int
	}
	slots := make(map[uuid.UUID]slot)
	var (
		rowID, progressID uuid.UUID
		e                 domain.ProgressExercise
	)
	_, err = pgx.ForEachRow(rows, []any{&rowID, &progressID, &e.ExerciseID, &e.Position, &e.Notes}, func() error {
		s := byID[progressID]
		slots[rowID] = slot{s, len(s.Exercises)}
		e.Sets = []domain.ProgressSet{}
		s.Exercises = append(s.Exercises, e)
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: read progress exercises: %w", err)
	}

	rows, err = q.Query(ctx, `
		SELECT s.progress_exercise_id, s.position, s.type, s.reps, s.weight::text,
			s.duration_seconds, s.distance_meters::text, s.rpe::text, s.completed
		FROM progress_sets s
		JOIN progress_exercises e ON e.id = s.progress_exercise_id
		WHERE e.progress_id = ANY($1)
		ORDER BY s.progress_exercise_id, s.position`, ids)
	if err != nil {
		return fmt.Errorf("store: read progress sets: %w", err)
	}
	var st domain.ProgressSet
	_, err = pgx.ForEachRow(rows, []any{&rowID, &st.Position, &st.Type, &st.Reps, &st.Weight,
		&st.DurationSeconds, &st.DistanceMeters, &st.RPE, &st.Completed}, func() error {
		sl := slots[rowID]
		ex := &sl.session.Exercises[sl.index]
		ex.Sets = append(ex.Sets, st)
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: read progress sets: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// List

// ListSummaries is endpoint 5 without expand: one page of session summaries and
// the cursor of the next page (nil on the last page). after is the decoded
// cursor of the previous page, nil for the first. limit is the page size.
//
// Default order is started_at DESC, id, with the cursor (started_at, id). With
// f.UpdatedSince the order is (server_updated_at, id) ascending, deleted
// sessions included, with the cursor (server_updated_at, id).
func (s *Progress) ListSummaries(ctx context.Context, userID uuid.UUID, f domain.ProgressListFilter, after *domain.Cursor, limit int) ([]domain.ProgressSummary, *domain.Cursor, error) {
	rows, next, err := progressPage(ctx, s.db, userID, f, after, limit, true)
	if err != nil {
		return nil, nil, err
	}
	out := make([]domain.ProgressSummary, len(rows))
	for i, r := range rows {
		out[i] = domain.ProgressSummary{
			ID: r.ID, WorkoutPlanID: r.WorkoutPlanID, Name: r.Name,
			StartedAt: r.StartedAt, EndedAt: r.EndedAt, DurationSeconds: r.DurationSeconds,
			ExerciseCount: r.exerciseCount, SetCount: r.setCount,
			UpdatedAt: r.UpdatedAt, ServerUpdatedAt: r.ServerUpdatedAt, DeletedAt: r.DeletedAt,
		}
	}
	return out, next, nil
}

// ListExpanded is endpoint 5 with expand=exercises: the same page as
// ListSummaries, but full sessions with exercises and sets. Children are read
// in two extra queries for the whole page, in one snapshot with the page.
func (s *Progress) ListExpanded(ctx context.Context, userID uuid.UUID, f domain.ProgressListFilter, after *domain.Cursor, limit int) ([]domain.Progress, *domain.Cursor, error) {
	var (
		out  []domain.Progress
		next *domain.Cursor
	)
	err := s.db.WithTxOptions(ctx, readOnlyTx, func(ctx context.Context, tx pgx.Tx) error {
		rows, n, err := progressPage(ctx, tx, userID, f, after, limit, false)
		if err != nil {
			return err
		}
		out, next = make([]domain.Progress, len(rows)), n
		for i, r := range rows {
			out[i] = r.Progress
		}
		return progressAttachChildren(ctx, tx, out)
	})
	if err != nil {
		return nil, nil, err
	}
	return out, next, nil
}

// progressListRow is a session row with the counts of the summary.
type progressListRow struct {
	domain.Progress
	exerciseCount, setCount int
}

// progressPage runs the list query: limit+1 rows to learn whether there is a
// next page, and returns the rows of this page with the next cursor.
func progressPage(ctx context.Context, q Querier, userID uuid.UUID, f domain.ProgressListFilter, after *domain.Cursor, limit int, counts bool) ([]progressListRow, *domain.Cursor, error) {
	sql, args := progressListSQL(userID, f, after, limit+1, counts)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("store: list progress: %w", err)
	}
	defer rows.Close()

	out := []progressListRow{}
	for rows.Next() {
		var r progressListRow
		dest := []any{}
		if counts {
			dest = append(dest, &r.exerciseCount, &r.setCount)
		}
		if err := progressScan(rows, &r.Progress, dest...); err != nil {
			return nil, nil, fmt.Errorf("store: list progress: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: list progress: %w", err)
	}

	var next *domain.Cursor
	if page, ok := pageOf(out, limit); ok {
		out = page
		last := out[limit-1]
		next = &domain.Cursor{At: last.StartedAt, ID: last.ID}
		if f.UpdatedSince != nil {
			next.At = last.ServerUpdatedAt
		}
	}
	return out, next, nil
}

// progressListSQL builds the list query. Values are always arguments; only
// fixed fragments are concatenated.
func progressListSQL(userID uuid.UUID, f domain.ProgressListFilter, after *domain.Cursor, limit int, counts bool) (string, []any) {
	args := []any{userID}
	where := []string{"p.user_id = $1"}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	syncMode := f.UpdatedSince != nil
	if syncMode {
		where = append(where, "p.server_updated_at > "+arg(*f.UpdatedSince))
	} else if !f.IncludeDeleted {
		where = append(where, "p.deleted_at IS NULL")
	}
	if f.WorkoutPlanID != nil {
		where = append(where, "p.workout_plan_id = "+arg(*f.WorkoutPlanID))
	}
	if f.From != nil {
		where = append(where, "p.started_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "p.started_at <= "+arg(*f.To))
	}

	order := "p.started_at DESC, p.id"
	if syncMode {
		order = "p.server_updated_at, p.id"
	}
	if after != nil {
		at, id := arg(after.At), arg(after.ID)
		if syncMode {
			where = append(where, fmt.Sprintf("(p.server_updated_at, p.id) > (%s::timestamptz, %s::uuid)", at, id))
		} else {
			// started_at descends and id ascends, so no single row comparison.
			where = append(where, fmt.Sprintf("(p.started_at < %[1]s::timestamptz OR (p.started_at = %[1]s::timestamptz AND p.id > %[2]s::uuid))", at, id))
		}
	}

	// The counts follow the session columns, where progressScan expects dest.
	cols := progressColumns
	if counts {
		cols += `,
			(SELECT count(*) FROM progress_exercises e WHERE e.progress_id = p.id)::int,
			(SELECT count(*) FROM progress_sets s JOIN progress_exercises e ON e.id = s.progress_exercise_id
				WHERE e.progress_id = p.id)::int`
	}
	return fmt.Sprintf("SELECT %s FROM progress p WHERE %s ORDER BY %s LIMIT %s",
		cols, strings.Join(where, " AND "), order, arg(limit)), args
}

// ---------------------------------------------------------------------------
// Delete

// SoftDelete is endpoint 16: it sets deleted_at and server_updated_at to the
// same instant and keeps the children. Deleting an already deleted session
// succeeds without changing it (idempotent); a session that does not exist or
// belongs to another user is NotFound.
func (s *Progress) SoftDelete(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE progress p SET deleted_at = t.ts, server_updated_at = t.ts
		FROM (SELECT clock_timestamp() AS ts) t
		WHERE p.id = $1 AND p.user_id = $2 AND p.deleted_at IS NULL`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete progress: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return softDeleteAnswer(ctx, s.db, "progress", "progress", id, userID)
}
