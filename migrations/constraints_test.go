package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// Behavioural tests: rows go in through plain INSERTs and the database must
// accept or reject them as data-model.md says. Limits come from internal/domain
// so a change to a limit that is not mirrored in a migration fails here.
//
// Outcomes are compared as strings: "" is success, "check:<constraint>",
// "unique:<constraint>", "fk:<constraint>", "notnull:<column>" or "overflow".

var t0 = time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)

func rep(s string, n int) string { return strings.Repeat(s, n) }

// env is a migrated database with one user and one exercise.
type env struct {
	t        *testing.T
	db       *store.DB
	user     uuid.UUID
	exercise uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.NewDB(t)
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)
	return &env{t: t, db: db, user: user, exercise: testutil.SeedExercise(t, db, user)}
}

func id() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// insert runs INSERT INTO table with the given column values.
func (e *env) insert(table string, row map[string]any) error {
	cols := slices.Sorted(maps.Keys(row))
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = row[c]
	}
	_, err := e.db.Exec(e.t.Context(),
		"INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(placeholders, ", ")+")", args...)
	return err
}

// exec runs any statement and returns its error.
func (e *env) exec(sql string, args ...any) error {
	_, err := e.db.Exec(e.t.Context(), sql, args...)
	return err
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(e.t.Context(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

// classify names the outcome of a statement, see the file comment.
func classify(err error) string {
	if err == nil {
		return ""
	}
	if c, ok := store.IsCheckViolation(err); ok {
		return "check:" + c
	}
	if c, ok := store.IsUniqueViolation(err); ok {
		return "unique:" + c
	}
	if c, ok := store.IsForeignKeyViolation(err); ok {
		return "fk:" + c
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23502":
			return "notnull:" + pgErr.ColumnName
		case "22003":
			return "overflow"
		}
		return "pg:" + pgErr.Code + " " + pgErr.Message
	}
	return "error:" + err.Error()
}

func (e *env) want(t *testing.T, want string, err error, format string, args ...any) {
	t.Helper()
	if got := classify(err); got != want {
		t.Errorf("%s: outcome %q, want %q", fmt.Sprintf(format, args...), got, want)
	}
}

// Row builders: valid rows to which a test applies overrides. A nil value
// means NULL.

func with(row map[string]any, over map[string]any) map[string]any {
	maps.Copy(row, over)
	return row
}

func (e *env) userRow(over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "username": "u_" + uuid.NewString()[:8], "password_hash": "x", "role": "user",
	}, over)
}

func (e *env) tokenRow(over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "user_id": e.user, "token_hash": testutil.HashToken(uuid.NewString()),
		"expires_at": t0.Add(365 * 24 * time.Hour),
	}, over)
}

func (e *env) exerciseRow(over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "name": "Exercise " + uuid.NewString(), "category": "strength",
		"primary_muscle_group": "chest", "equipment": "barbell", "measurement_type": "reps_weight",
		"created_by": e.user,
	}, over)
}

func (e *env) planRow(over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "user_id": e.user, "name": "Plan",
		"client_updated_at": t0, "server_updated_at": t0,
	}, over)
}

func (e *env) planExRow(planID uuid.UUID, over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "workout_plan_id": planID, "exercise_id": e.exercise, "position": 0, "target_sets": 3,
	}, over)
}

func (e *env) progressRow(over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "user_id": e.user, "name": "Push day", "started_at": t0, "ended_at": t0.Add(time.Hour),
		"duration_seconds": 3600, "client_updated_at": t0, "server_updated_at": t0,
	}, over)
}

func (e *env) progressExRow(progressID any, over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "progress_id": progressID, "exercise_id": e.exercise, "position": 0,
	}, over)
}

func (e *env) setRow(progressExerciseID any, over map[string]any) map[string]any {
	return with(map[string]any{
		"id": id(), "progress_exercise_id": progressExerciseID, "position": 0, "type": "normal", "completed": true,
	}, over)
}

// --- users and auth_tokens ---------------------------------------------------

func TestUsersConstraints(t *testing.T) {
	e := newEnv(t)

	t.Run("username format", func(t *testing.T) {
		const check = "check:users_username_format_chk"
		tests := []struct{ username, want string }{
			{"abc", ""},
			{"a_1", ""},
			{"___", ""},
			{"012", ""},
			{rep("a", domain.UsernameMinLen), ""},
			{rep("a", domain.UsernameMaxLen), ""},
			{rep("a", domain.UsernameMinLen-1), check},
			{rep("a", domain.UsernameMaxLen+1), check},
			{"", check},
			{"Abc", check}, // stored lowercase
			{"ab-c", check},
			{"ab c", check},
			{"ab.c", check},
			{"abc\n", check}, // $ must not match before a trailing newline
			{"\nabc", check},
			{"abé", check},
		}
		for _, tc := range tests {
			e.want(t, tc.want, e.insert("users", e.userRow(map[string]any{"username": tc.username})), "username %q", tc.username)
		}
	})

	t.Run("role", func(t *testing.T) {
		for role, want := range map[string]string{
			"user": "", "admin": "", "root": "check:users_role_chk", "": "check:users_role_chk", "Admin": "check:users_role_chk",
		} {
			e.want(t, want, e.insert("users", e.userRow(map[string]any{"role": role})), "role %q", role)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		row := e.userRow(nil)
		delete(row, "role")
		if err := e.insert("users", row); err != nil {
			t.Fatal(err)
		}
		var role string
		var createdAt, updatedAt time.Time
		err := e.db.QueryRow(t.Context(), `SELECT role, created_at, updated_at FROM users WHERE id = $1`, row["id"]).Scan(&role, &createdAt, &updatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if role != "user" {
			t.Errorf("default role = %q, want user", role)
		}
		if time.Since(createdAt) > time.Minute || time.Since(updatedAt) > time.Minute {
			t.Errorf("created_at/updated_at defaults are not now(): %v %v", createdAt, updatedAt)
		}
	})

	t.Run("unique username", func(t *testing.T) {
		row := e.userRow(map[string]any{"username": "taken_name"})
		e.want(t, "", e.insert("users", row), "first insert")
		e.want(t, "unique:users_username_uniq", e.insert("users", e.userRow(map[string]any{"username": "taken_name"})), "duplicate")
	})

	t.Run("not null", func(t *testing.T) {
		e.want(t, "notnull:password_hash", e.insert("users", e.userRow(map[string]any{"password_hash": nil})), "password_hash")
		e.want(t, "notnull:username", e.insert("users", e.userRow(map[string]any{"username": nil})), "username")
	})
}

func TestAuthTokensConstraints(t *testing.T) {
	e := newEnv(t)

	e.want(t, "", e.insert("auth_tokens", e.tokenRow(nil)), "valid token")

	// Defaults: created_at now(), the nullable columns NULL.
	row := e.tokenRow(nil)
	if err := e.insert("auth_tokens", row); err != nil {
		t.Fatal(err)
	}
	var createdAt time.Time
	var device *string
	var lastUsed, revoked *time.Time
	if err := e.db.QueryRow(t.Context(),
		`SELECT created_at, device_name, last_used_at, revoked_at FROM auth_tokens WHERE id = $1`, row["id"]).
		Scan(&createdAt, &device, &lastUsed, &revoked); err != nil {
		t.Fatal(err)
	}
	if time.Since(createdAt) > time.Minute || device != nil || lastUsed != nil || revoked != nil {
		t.Errorf("defaults wrong: created_at=%v device=%v last_used=%v revoked=%v", createdAt, device, lastUsed, revoked)
	}

	e.want(t, "", e.insert("auth_tokens", e.tokenRow(map[string]any{"device_name": rep("d", domain.DeviceNameMaxLen)})), "device_name at max")
	e.want(t, "check:auth_tokens_device_name_len_chk",
		e.insert("auth_tokens", e.tokenRow(map[string]any{"device_name": rep("d", domain.DeviceNameMaxLen+1)})), "device_name over max")

	dup := testutil.HashToken("same-token")
	e.want(t, "", e.insert("auth_tokens", e.tokenRow(map[string]any{"token_hash": dup})), "first hash")
	e.want(t, "unique:auth_tokens_token_hash_uniq", e.insert("auth_tokens", e.tokenRow(map[string]any{"token_hash": dup})), "duplicate hash")

	e.want(t, "fk:auth_tokens_user_id_fkey", e.insert("auth_tokens", e.tokenRow(map[string]any{"user_id": id()})), "unknown user")
	e.want(t, "notnull:expires_at", e.insert("auth_tokens", e.tokenRow(map[string]any{"expires_at": nil})), "expires_at NULL")
	e.want(t, "notnull:token_hash", e.insert("auth_tokens", e.tokenRow(map[string]any{"token_hash": nil})), "token_hash NULL")
}

// --- exercises ---------------------------------------------------------------

func TestExercisesConstraints(t *testing.T) {
	e := newEnv(t)

	t.Run("secondary muscle groups default to an empty array", func(t *testing.T) {
		row := e.exerciseRow(nil)
		if err := e.insert("exercises", row); err != nil {
			t.Fatal(err)
		}
		var secondary []string
		var n int
		err := e.db.QueryRow(t.Context(),
			`SELECT secondary_muscle_groups, cardinality(secondary_muscle_groups) FROM exercises WHERE id = $1`, row["id"]).Scan(&secondary, &n)
		if err != nil {
			t.Fatal(err)
		}
		if secondary == nil || len(secondary) != 0 || n != 0 {
			t.Errorf("secondary = %#v (cardinality %d), want an empty, non-NULL array", secondary, n)
		}
		e.want(t, "notnull:secondary_muscle_groups",
			e.insert("exercises", e.exerciseRow(map[string]any{"secondary_muscle_groups": nil})), "explicit NULL")
	})

	t.Run("name length is counted in characters", func(t *testing.T) {
		const check = "check:exercises_name_len_chk"
		tests := []struct{ name, want string }{
			{"", check},
			{rep("a", domain.ExerciseNameMinLen), ""},
			{rep("a", domain.ExerciseNameMaxLen), ""},
			{rep("é", domain.ExerciseNameMaxLen), ""}, // 200 bytes, 100 characters
			{rep("a", domain.ExerciseNameMaxLen+1), check},
			{rep("é", domain.ExerciseNameMaxLen+1), check},
		}
		for _, tc := range tests {
			e.want(t, tc.want, e.insert("exercises", e.exerciseRow(map[string]any{"name": tc.name})),
				"name of %d characters", len([]rune(tc.name)))
		}
	})

	t.Run("instructions length", func(t *testing.T) {
		e.want(t, "", e.insert("exercises", e.exerciseRow(map[string]any{"instructions": rep("i", domain.ExerciseInstructionsMaxLen)})), "at max")
		e.want(t, "check:exercises_instructions_len_chk",
			e.insert("exercises", e.exerciseRow(map[string]any{"instructions": rep("i", domain.ExerciseInstructionsMaxLen+1)})), "over max")
		e.want(t, "", e.insert("exercises", e.exerciseRow(map[string]any{"instructions": nil})), "NULL")
	})

	t.Run("name is unique among live exercises only", func(t *testing.T) {
		first := e.exerciseRow(map[string]any{"name": "Bench Press"})
		e.want(t, "", e.insert("exercises", first), "first")

		const unique = "unique:exercises_name_lower_uniq"
		e.want(t, unique, e.insert("exercises", e.exerciseRow(map[string]any{"name": "Bench Press"})), "same name")
		e.want(t, unique, e.insert("exercises", e.exerciseRow(map[string]any{"name": "bench PRESS"})), "different case")
		e.want(t, "", e.insert("exercises", e.exerciseRow(map[string]any{"name": "Bench Press 2"})), "different name")

		// Soft delete frees the name.
		if err := e.exec(`UPDATE exercises SET deleted_at = now() WHERE id = $1`, first["id"]); err != nil {
			t.Fatal(err)
		}
		second := e.exerciseRow(map[string]any{"name": "Bench Press"})
		e.want(t, "", e.insert("exercises", second), "reuse after soft delete")
		// Several deleted rows may share a name.
		third := e.exerciseRow(map[string]any{"name": "BENCH press", "deleted_at": t0})
		e.want(t, "", e.insert("exercises", third), "another deleted row with the same name")
		// Un-deleting while a live twin exists is a violation.
		e.want(t, unique, e.exec(`UPDATE exercises SET deleted_at = NULL WHERE id = $1`, first["id"]), "undelete next to a live twin")
		// Renaming onto another live name is too, renaming a row to its own name is not.
		other := e.exerciseRow(map[string]any{"name": "Squat"})
		e.want(t, "", e.insert("exercises", other), "another exercise")
		e.want(t, unique, e.exec(`UPDATE exercises SET name = 'bench press' WHERE id = $1`, other["id"]), "rename onto a live name")
		e.want(t, "", e.exec(`UPDATE exercises SET name = 'BENCH PRESS' WHERE id = $1`, second["id"]), "change case of its own name")
	})

	t.Run("image columns are all NULL or all set", func(t *testing.T) {
		const check = "check:exercises_image_all_or_none_chk"
		hash, ext, size := "0123456789abcdef", "webp", 2048
		tests := []struct {
			name               string
			hash, ext, sizeVal any
			want               string
		}{
			{"none", nil, nil, nil, ""},
			{"all three", hash, ext, size, ""},
			{"hash only", hash, nil, nil, check},
			{"ext only", nil, ext, nil, check},
			{"size only", nil, nil, size, check},
			{"hash and ext", hash, ext, nil, check},
			{"hash and size", hash, nil, size, check},
			{"ext and size", nil, ext, size, check},
		}
		for _, tc := range tests {
			row := e.exerciseRow(map[string]any{"image_hash": tc.hash, "image_ext": tc.ext, "image_size_bytes": tc.sizeVal})
			e.want(t, tc.want, e.insert("exercises", row), "%s", tc.name)
		}

		// Clearing an image (spec 21) sets all three to NULL in one UPDATE.
		row := e.exerciseRow(map[string]any{"image_hash": hash, "image_ext": ext, "image_size_bytes": size})
		if err := e.insert("exercises", row); err != nil {
			t.Fatal(err)
		}
		e.want(t, "", e.exec(`UPDATE exercises SET image_hash = NULL, image_ext = NULL, image_size_bytes = NULL WHERE id = $1`, row["id"]), "clear image")
		e.want(t, check, e.exec(`UPDATE exercises SET image_hash = $2 WHERE id = $1`, row["id"], hash), "set hash alone")
	})

	t.Run("image hash format", func(t *testing.T) {
		const check = "check:exercises_image_hash_format_chk"
		for hash, want := range map[string]string{
			"0123456789abcdef":              "",
			rep("f", domain.ImageHashLen):   "",
			"0123456789ABCDEF":              check, // lowercase hex only
			rep("a", domain.ImageHashLen-1): check,
			rep("a", domain.ImageHashLen+1): check,
			"0123456789abcdeg":              check,
			"":                              check,
			"0123456789abcdef\n":            check,
		} {
			row := e.exerciseRow(map[string]any{"image_hash": hash, "image_ext": "png", "image_size_bytes": 1})
			e.want(t, want, e.insert("exercises", row), "hash %q", hash)
		}
	})

	t.Run("created_by references a user", func(t *testing.T) {
		e.want(t, "fk:exercises_created_by_fkey", e.insert("exercises", e.exerciseRow(map[string]any{"created_by": id()})), "unknown user")
		e.want(t, "notnull:created_by", e.insert("exercises", e.exerciseRow(map[string]any{"created_by": nil})), "NULL")
	})

	t.Run("defaults", func(t *testing.T) {
		row := e.exerciseRow(nil)
		if err := e.insert("exercises", row); err != nil {
			t.Fatal(err)
		}
		var createdAt, updatedAt time.Time
		var deletedAt *time.Time
		if err := e.db.QueryRow(t.Context(), `SELECT created_at, updated_at, deleted_at FROM exercises WHERE id = $1`, row["id"]).
			Scan(&createdAt, &updatedAt, &deletedAt); err != nil {
			t.Fatal(err)
		}
		if time.Since(createdAt) > time.Minute || time.Since(updatedAt) > time.Minute || deletedAt != nil {
			t.Errorf("defaults wrong: %v %v %v", createdAt, updatedAt, deletedAt)
		}
	})

	t.Run("name search uses the trigram index", func(t *testing.T) {
		for i := range 30 {
			testutil.SeedExercise(t, e.db, e.user, testutil.WithExerciseName(fmt.Sprintf("Search seed %d", i)))
		}
		plan := e.explain(`SELECT id FROM exercises WHERE lower(name) LIKE '%' || lower($1) || '%'`, "bench")
		if !strings.Contains(plan, "exercises_name_lower_trgm_idx") {
			t.Errorf("plan does not use the trigram index:\n%s", plan)
		}
		// updated_at sync feed and the muscle group filter have their indexes too.
		plan = e.explain(`SELECT id FROM exercises WHERE (updated_at, id) > ($1, $2) ORDER BY updated_at, id LIMIT 10`, t0, uuid.Nil)
		if !strings.Contains(plan, "exercises_updated_at_id_idx") {
			t.Errorf("plan does not use the sync index:\n%s", plan)
		}
		plan = e.explain(`SELECT id FROM exercises WHERE primary_muscle_group = $1`, "chest")
		if !strings.Contains(plan, "exercises_primary_muscle_group_idx") {
			t.Errorf("plan does not use the muscle group index:\n%s", plan)
		}
	})
}

// explain returns the plan of a query with sequential scans disabled, so the
// planner shows which index it would use even on a tiny table. Extra settings
// (for example "enable_sort = off") narrow the choice further.
func (e *env) explain(query string, args ...any) string {
	return e.explainWith(nil, query, args...)
}

func (e *env) explainWith(settings []string, query string, args ...any) string {
	e.t.Helper()
	var plan []string
	err := e.db.WithTx(e.t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		for _, s := range append([]string{"enable_seqscan = off"}, settings...) {
			if _, err := tx.Exec(ctx, "SET LOCAL "+s); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "EXPLAIN "+query, args...)
		if err != nil {
			return err
		}
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		e.t.Fatalf("explain: %v", err)
	}
	return strings.Join(plan, "\n")
}

// --- workout plans -----------------------------------------------------------

func TestWorkoutPlansConstraints(t *testing.T) {
	e := newEnv(t)

	t.Run("plan columns", func(t *testing.T) {
		tests := []struct {
			name string
			over map[string]any
			want string
		}{
			{"valid", nil, ""},
			{"name at max", map[string]any{"name": rep("n", domain.PlanNameMaxLen)}, ""},
			{"name over max", map[string]any{"name": rep("n", domain.PlanNameMaxLen+1)}, "check:workout_plans_name_len_chk"},
			{"empty name", map[string]any{"name": ""}, "check:workout_plans_name_len_chk"},
			{"description at max", map[string]any{"description": rep("d", domain.PlanDescriptionMaxLen)}, ""},
			{"description over max", map[string]any{"description": rep("d", domain.PlanDescriptionMaxLen+1)}, "check:workout_plans_description_len_chk"},
			{"description NULL", map[string]any{"description": nil}, ""},
			{"unknown user", map[string]any{"user_id": id()}, "fk:workout_plans_user_id_fkey"},
			{"client_updated_at NULL", map[string]any{"client_updated_at": nil}, "notnull:client_updated_at"},
			{"server_updated_at NULL", map[string]any{"server_updated_at": nil}, "notnull:server_updated_at"},
		}
		for _, tc := range tests {
			e.want(t, tc.want, e.insert("workout_plans", e.planRow(tc.over)), "%s", tc.name)
		}
		// Duplicate names are allowed.
		e.want(t, "", e.insert("workout_plans", e.planRow(map[string]any{"name": "Same"})), "first Same")
		e.want(t, "", e.insert("workout_plans", e.planRow(map[string]any{"name": "Same"})), "second Same")
	})

	t.Run("plan exercise columns", func(t *testing.T) {
		plan := testutil.SeedPlan(t, e.db, e.user)
		next := 0 // each accepted row needs its own position
		try := func(over map[string]any) error {
			next++
			return e.insert("workout_plan_exercises", e.planExRow(plan, with(map[string]any{"position": next}, over)))
		}

		tests := []struct {
			name string
			over map[string]any
			want string
		}{
			{"defaults only", nil, ""},
			{"high position", map[string]any{"position": 1000}, ""},
			{"negative position", map[string]any{"position": -1}, "check:workout_plan_exercises_position_chk"},

			{"target_sets min", map[string]any{"target_sets": domain.PlanTargetSetsMin}, ""},
			{"target_sets max", map[string]any{"target_sets": domain.PlanTargetSetsMax}, ""},
			{"target_sets below min", map[string]any{"target_sets": domain.PlanTargetSetsMin - 1}, "check:workout_plan_exercises_target_sets_chk"},
			{"target_sets above max", map[string]any{"target_sets": domain.PlanTargetSetsMax + 1}, "check:workout_plan_exercises_target_sets_chk"},
			{"target_sets NULL", map[string]any{"target_sets": nil}, "notnull:target_sets"},

			{"target_reps min", map[string]any{"target_reps": domain.PlanTargetRepsMin}, ""},
			{"target_reps zero", map[string]any{"target_reps": 0}, "check:workout_plan_exercises_target_reps_chk"},

			{"reps range", map[string]any{"target_reps": 8, "target_reps_max": 12}, ""},
			{"reps range of one", map[string]any{"target_reps": 8, "target_reps_max": 8}, ""},
			{"reps_max below reps", map[string]any{"target_reps": 8, "target_reps_max": 7}, "check:workout_plan_exercises_target_reps_max_ge_reps_chk"},
			{"reps_max without reps", map[string]any{"target_reps": nil, "target_reps_max": 10}, "check:workout_plan_exercises_target_reps_max_requires_reps_chk"},
			{"reps_max NULL with reps", map[string]any{"target_reps": 8, "target_reps_max": nil}, ""},

			{"weight zero", map[string]any{"target_weight": "0"}, ""},
			{"weight fractional", map[string]any{"target_weight": "82.5"}, ""},
			{"weight negative", map[string]any{"target_weight": "-0.01"}, "check:workout_plan_exercises_target_weight_chk"},
			{"weight max", map[string]any{"target_weight": decimal(domain.MaxWeightKg)}, ""},
			{"weight overflows numeric(7,2)", map[string]any{"target_weight": decimal(math.Floor(domain.MaxWeightKg) + 1)}, "overflow"},

			{"duration zero", map[string]any{"target_duration_seconds": 0}, ""},
			{"duration negative", map[string]any{"target_duration_seconds": -1}, "check:workout_plan_exercises_target_duration_seconds_chk"},

			{"distance zero", map[string]any{"target_distance_meters": "0"}, ""},
			{"distance negative", map[string]any{"target_distance_meters": "-1"}, "check:workout_plan_exercises_target_distance_meters_chk"},
			{"distance max", map[string]any{"target_distance_meters": decimal(domain.MaxDistanceMeters)}, ""},
			{"distance overflows numeric(9,2)", map[string]any{"target_distance_meters": decimal(math.Floor(domain.MaxDistanceMeters) + 1)}, "overflow"},

			{"rest min", map[string]any{"rest_seconds": domain.PlanRestSecondsMin}, ""},
			{"rest max", map[string]any{"rest_seconds": domain.PlanRestSecondsMax}, ""},
			{"rest below min", map[string]any{"rest_seconds": domain.PlanRestSecondsMin - 1}, "check:workout_plan_exercises_rest_seconds_chk"},
			{"rest above max", map[string]any{"rest_seconds": domain.PlanRestSecondsMax + 1}, "check:workout_plan_exercises_rest_seconds_chk"},

			{"notes at max", map[string]any{"notes": rep("n", domain.PlanExerciseNotesMaxLen)}, ""},
			{"notes over max", map[string]any{"notes": rep("n", domain.PlanExerciseNotesMaxLen+1)}, "check:workout_plan_exercises_notes_len_chk"},

			{"unknown exercise", map[string]any{"exercise_id": id()}, "fk:workout_plan_exercises_exercise_id_fkey"},
		}
		for _, tc := range tests {
			e.want(t, tc.want, try(tc.over), "%s", tc.name)
		}
	})

	t.Run("plan exercise positions are unique per plan", func(t *testing.T) {
		planA := testutil.SeedPlan(t, e.db, e.user)
		planB := testutil.SeedPlan(t, e.db, e.user)
		e.want(t, "", e.insert("workout_plan_exercises", e.planExRow(planA, map[string]any{"position": 0})), "A/0")
		e.want(t, "unique:workout_plan_exercises_workout_plan_id_position_uniq",
			e.insert("workout_plan_exercises", e.planExRow(planA, map[string]any{"position": 0})), "A/0 again")
		e.want(t, "", e.insert("workout_plan_exercises", e.planExRow(planA, map[string]any{"position": 1})), "A/1")
		e.want(t, "", e.insert("workout_plan_exercises", e.planExRow(planB, map[string]any{"position": 0})), "B/0, other plan")
		// The same exercise may repeat in a plan, at other positions (already true above).
	})

	t.Run("plan children are replaced in one transaction", func(t *testing.T) {
		// The save endpoint deletes and re-inserts every child row, reusing
		// positions. The unique constraint must not get in the way.
		plan := testutil.SeedPlan(t, e.db, e.user,
			testutil.WithPlanExercise(e.exercise), testutil.WithPlanExercise(e.exercise))
		err := e.db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM workout_plan_exercises WHERE workout_plan_id = $1`, plan); err != nil {
				return err
			}
			for pos := range 2 {
				_, err := tx.Exec(ctx, `
					INSERT INTO workout_plan_exercises (id, workout_plan_id, exercise_id, position, target_sets)
					VALUES ($1, $2, $3, $4, 3)`, id(), plan, e.exercise, pos)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("replace children: %v", err)
		}
		if n := e.count(`SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, plan); n != 2 {
			t.Errorf("children = %d, want 2", n)
		}
	})

	t.Run("deleting a plan cascades to its exercises", func(t *testing.T) {
		plan := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanExercise(e.exercise), testutil.WithPlanExercise(e.exercise))
		other := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanExercise(e.exercise))
		if n := e.count(`SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, plan); n != 2 {
			t.Fatalf("seeded children = %d", n)
		}
		if err := e.exec(`DELETE FROM workout_plans WHERE id = $1`, plan); err != nil {
			t.Fatal(err)
		}
		if n := e.count(`SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, plan); n != 0 {
			t.Errorf("children left after cascade: %d", n)
		}
		if n := e.count(`SELECT count(*) FROM workout_plan_exercises WHERE workout_plan_id = $1`, other); n != 1 {
			t.Errorf("another plan's child changed: %d", n)
		}
	})

	t.Run("exercise references", func(t *testing.T) {
		softDeleted := testutil.SeedExercise(t, e.db, e.user, testutil.WithExerciseDeletedAt(t0))
		plan := testutil.SeedPlan(t, e.db, e.user)
		e.want(t, "", e.insert("workout_plan_exercises", e.planExRow(plan, map[string]any{"exercise_id": softDeleted})), "soft-deleted exercise is still valid")

		// A referenced exercise cannot be hard-deleted: nothing is hard-deleted in v1.
		e.want(t, "fk:workout_plan_exercises_exercise_id_fkey", e.exec(`DELETE FROM exercises WHERE id = $1`, softDeleted), "delete a referenced exercise")
	})
}

// decimal formats v with the two decimals the numeric columns keep.
func decimal(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// --- progress ----------------------------------------------------------------

func TestProgressConstraints(t *testing.T) {
	e := newEnv(t)

	t.Run("progress columns", func(t *testing.T) {
		tests := []struct {
			name string
			over map[string]any
			want string
		}{
			{"valid", nil, ""},
			{"freestyle has no plan", map[string]any{"workout_plan_id": nil}, ""},
			{"unknown plan", map[string]any{"workout_plan_id": id()}, "fk:progress_workout_plan_id_fkey"},
			{"unknown user", map[string]any{"user_id": id()}, "fk:progress_user_id_fkey"},

			{"name at max", map[string]any{"name": rep("n", domain.ProgressNameMaxLen)}, ""},
			{"name over max", map[string]any{"name": rep("n", domain.ProgressNameMaxLen+1)}, "check:progress_name_len_chk"},
			{"empty name", map[string]any{"name": ""}, "check:progress_name_len_chk"},
			{"notes at max", map[string]any{"notes": rep("n", domain.ProgressNotesMaxLen)}, ""},
			{"notes over max", map[string]any{"notes": rep("n", domain.ProgressNotesMaxLen+1)}, "check:progress_notes_len_chk"},

			{"ended before started", map[string]any{"started_at": t0, "ended_at": t0.Add(-time.Second)}, "check:progress_ended_at_chk"},
			{"ended one microsecond before", map[string]any{"started_at": t0, "ended_at": t0.Add(-time.Microsecond)}, "check:progress_ended_at_chk"},
			{"ended equals started", map[string]any{"started_at": t0, "ended_at": t0}, ""},

			{"duration min", map[string]any{"duration_seconds": domain.ProgressDurationMinSeconds}, ""},
			{"duration max", map[string]any{"duration_seconds": domain.ProgressDurationMaxSeconds}, ""},
			{"duration below min", map[string]any{"duration_seconds": domain.ProgressDurationMinSeconds - 1}, "check:progress_duration_seconds_chk"},
			{"duration above max", map[string]any{"duration_seconds": domain.ProgressDurationMaxSeconds + 1}, "check:progress_duration_seconds_chk"},

			{"started_at NULL", map[string]any{"started_at": nil}, "notnull:started_at"},
			{"ended_at NULL", map[string]any{"ended_at": nil}, "notnull:ended_at"},
			{"server_updated_at NULL", map[string]any{"server_updated_at": nil}, "notnull:server_updated_at"},
		}
		for _, tc := range tests {
			e.want(t, tc.want, e.insert("progress", e.progressRow(tc.over)), "%s", tc.name)
		}
	})

	t.Run("a soft-deleted plan is still a valid reference", func(t *testing.T) {
		plan := testutil.SeedPlan(t, e.db, e.user, testutil.WithPlanDeletedAt(t0))
		e.want(t, "", e.insert("progress", e.progressRow(map[string]any{"workout_plan_id": plan})), "progress of a soft-deleted plan")
		e.want(t, "fk:progress_workout_plan_id_fkey", e.exec(`DELETE FROM workout_plans WHERE id = $1`, plan), "hard delete of a referenced plan")
	})

	t.Run("progress exercise columns", func(t *testing.T) {
		progress := testutil.SeedProgress(t, e.db, e.user)
		next := 0
		try := func(over map[string]any) error {
			next++
			return e.insert("progress_exercises", e.progressExRow(progress, with(map[string]any{"position": next}, over)))
		}
		softDeleted := testutil.SeedExercise(t, e.db, e.user, testutil.WithExerciseDeletedAt(t0))

		e.want(t, "", try(nil), "valid")
		e.want(t, "check:progress_exercises_position_chk", try(map[string]any{"position": -1}), "negative position")
		e.want(t, "", try(map[string]any{"notes": rep("n", domain.ProgressExerciseNotesMaxLen)}), "notes at max")
		e.want(t, "check:progress_exercises_notes_len_chk", try(map[string]any{"notes": rep("n", domain.ProgressExerciseNotesMaxLen+1)}), "notes over max")
		e.want(t, "fk:progress_exercises_exercise_id_fkey", try(map[string]any{"exercise_id": id()}), "unknown exercise")
		e.want(t, "", try(map[string]any{"exercise_id": softDeleted}), "soft-deleted exercise")
		e.want(t, "fk:progress_exercises_progress_id_fkey",
			e.insert("progress_exercises", e.progressExRow(id(), nil)), "unknown progress")
	})

	t.Run("progress exercise positions are unique per session", func(t *testing.T) {
		a := testutil.SeedProgress(t, e.db, e.user)
		b := testutil.SeedProgress(t, e.db, e.user)
		e.want(t, "", e.insert("progress_exercises", e.progressExRow(a, map[string]any{"position": 0})), "A/0")
		e.want(t, "unique:progress_exercises_progress_id_position_uniq",
			e.insert("progress_exercises", e.progressExRow(a, map[string]any{"position": 0})), "A/0 again")
		e.want(t, "", e.insert("progress_exercises", e.progressExRow(b, map[string]any{"position": 0})), "B/0, other session")
	})

	t.Run("progress set columns", func(t *testing.T) {
		progress := testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressExercise(e.exercise))
		var pe uuid.UUID
		if err := e.db.QueryRow(t.Context(), `SELECT id FROM progress_exercises WHERE progress_id = $1`, progress).Scan(&pe); err != nil {
			t.Fatal(err)
		}
		next := 0
		try := func(over map[string]any) error {
			next++
			return e.insert("progress_sets", e.setRow(pe, with(map[string]any{"position": next}, over)))
		}

		tests := []struct {
			name string
			over map[string]any
			want string
		}{
			{"valid", nil, ""},
			{"all metrics", map[string]any{"reps": 8, "weight": "82.5", "duration_seconds": 30, "distance_meters": "400.5", "rpe": "8.5"}, ""},
			{"all metrics NULL", map[string]any{"reps": nil, "weight": nil, "duration_seconds": nil, "distance_meters": nil, "rpe": nil}, ""},
			{"not completed", map[string]any{"completed": false}, ""},
			{"completed NULL", map[string]any{"completed": nil}, "notnull:completed"},
			{"type NULL", map[string]any{"type": nil}, "notnull:type"},

			{"negative position", map[string]any{"position": -1}, "check:progress_sets_position_chk"},
			{"reps zero", map[string]any{"reps": 0}, ""},
			{"reps negative", map[string]any{"reps": -1}, "check:progress_sets_reps_chk"},
			{"weight zero", map[string]any{"weight": "0"}, ""},
			{"weight negative", map[string]any{"weight": "-0.5"}, "check:progress_sets_weight_chk"},
			{"weight max", map[string]any{"weight": decimal(domain.MaxWeightKg)}, ""},
			{"weight overflows numeric(7,2)", map[string]any{"weight": decimal(math.Floor(domain.MaxWeightKg) + 1)}, "overflow"},
			{"duration zero", map[string]any{"duration_seconds": 0}, ""},
			{"duration negative", map[string]any{"duration_seconds": -1}, "check:progress_sets_duration_seconds_chk"},
			{"distance zero", map[string]any{"distance_meters": "0"}, ""},
			{"distance negative", map[string]any{"distance_meters": "-1"}, "check:progress_sets_distance_meters_chk"},
			{"distance max", map[string]any{"distance_meters": decimal(domain.MaxDistanceMeters)}, ""},
			{"distance overflows numeric(9,2)", map[string]any{"distance_meters": decimal(math.Floor(domain.MaxDistanceMeters) + 1)}, "overflow"},

			{"rpe step 1.3", map[string]any{"rpe": "1.3"}, "check:progress_sets_rpe_step_chk"},
			{"rpe step 7.7", map[string]any{"rpe": "7.7"}, "check:progress_sets_rpe_step_chk"},
			{"rpe step 9.9", map[string]any{"rpe": "9.9"}, "check:progress_sets_rpe_step_chk"},
			{"rpe below min", map[string]any{"rpe": "0.5"}, "check:progress_sets_rpe_range_chk"},
			{"rpe zero", map[string]any{"rpe": "0"}, "check:progress_sets_rpe_range_chk"},
			{"rpe above max", map[string]any{"rpe": "10.5"}, "check:progress_sets_rpe_range_chk"},
			{"rpe 11", map[string]any{"rpe": "11"}, "check:progress_sets_rpe_range_chk"},
			{"rpe negative", map[string]any{"rpe": "-1"}, "check:progress_sets_rpe_range_chk"},
		}
		for _, tc := range tests {
			e.want(t, tc.want, try(tc.over), "%s", tc.name)
		}

		// Every legal RPE, 1 to 10 in steps of 0.5, is accepted.
		for v := float64(domain.RPEMin); v <= domain.RPEMax; v += domain.RPEStep {
			e.want(t, "", try(map[string]any{"rpe": strconv.FormatFloat(v, 'f', 1, 64)}), "rpe %.1f", v)
		}
	})

	t.Run("progress set positions are unique per exercise", func(t *testing.T) {
		progress := testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressExercise(e.exercise), testutil.WithProgressExercise(e.exercise))
		rows, err := e.db.Query(t.Context(), `SELECT id FROM progress_exercises WHERE progress_id = $1 ORDER BY position`, progress)
		if err != nil {
			t.Fatal(err)
		}
		pes, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(pes) != 2 {
			t.Fatalf("progress exercises = %v, err = %v", pes, err)
		}
		e.want(t, "", e.insert("progress_sets", e.setRow(pes[0], map[string]any{"position": 0})), "first/0")
		e.want(t, "unique:progress_sets_progress_exercise_id_position_uniq",
			e.insert("progress_sets", e.setRow(pes[0], map[string]any{"position": 0})), "first/0 again")
		e.want(t, "", e.insert("progress_sets", e.setRow(pes[1], map[string]any{"position": 0})), "second/0, other exercise")
		e.want(t, "fk:progress_sets_progress_exercise_id_fkey", e.insert("progress_sets", e.setRow(id(), nil)), "unknown progress exercise")
	})

	t.Run("deleting a session cascades to exercises and sets", func(t *testing.T) {
		exB := testutil.SeedExercise(t, e.db, e.user)
		progress := testutil.SeedProgress(t, e.db, e.user,
			testutil.WithProgressExercise(e.exercise, testutil.WithSets(3)),
			testutil.WithProgressExercise(exB, testutil.WithSets(2)))
		other := testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressExercise(e.exercise, testutil.WithSets(1)))

		childCounts := func(p uuid.UUID) (exercises, sets int) {
			exercises = e.count(`SELECT count(*) FROM progress_exercises WHERE progress_id = $1`, p)
			sets = e.count(`SELECT count(*) FROM progress_sets s JOIN progress_exercises pe ON pe.id = s.progress_exercise_id WHERE pe.progress_id = $1`, p)
			return
		}
		if ex, sets := childCounts(progress); ex != 2 || sets != 5 {
			t.Fatalf("seeded children = %d exercises, %d sets, want 2 and 5", ex, sets)
		}

		// Removing one exercise takes only its sets.
		if err := e.exec(`DELETE FROM progress_exercises WHERE progress_id = $1 AND position = 1`, progress); err != nil {
			t.Fatal(err)
		}
		if ex, sets := childCounts(progress); ex != 1 || sets != 3 {
			t.Errorf("after deleting one exercise: %d exercises, %d sets, want 1 and 3", ex, sets)
		}

		if err := e.exec(`DELETE FROM progress WHERE id = $1`, progress); err != nil {
			t.Fatal(err)
		}
		if ex, sets := childCounts(progress); ex != 0 || sets != 0 {
			t.Errorf("children left after cascade: %d exercises, %d sets", ex, sets)
		}
		if ex, sets := childCounts(other); ex != 1 || sets != 1 {
			t.Errorf("another session's children changed: %d exercises, %d sets", ex, sets)
		}
	})

	t.Run("children are replaced in one transaction", func(t *testing.T) {
		progress := testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressExercise(e.exercise, testutil.WithSets(2)))
		err := e.db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM progress_exercises WHERE progress_id = $1`, progress); err != nil {
				return err
			}
			peID := id()
			if _, err := tx.Exec(ctx, `INSERT INTO progress_exercises (id, progress_id, exercise_id, position) VALUES ($1, $2, $3, 0)`,
				peID, progress, e.exercise); err != nil {
				return err
			}
			for pos := range 2 {
				if _, err := tx.Exec(ctx, `INSERT INTO progress_sets (id, progress_exercise_id, position, type, completed) VALUES ($1, $2, $3, 'normal', true)`,
					id(), peID, pos); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("replace children: %v", err)
		}
	})

	t.Run("indexes serve the list and sync queries", func(t *testing.T) {
		for i := range 30 {
			testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressStartedAt(t0.Add(time.Duration(i)*time.Hour)))
		}
		// An ordered index scan: no bitmap scan and no sort step.
		ordered := []string{"enable_bitmapscan = off", "enable_sort = off"}
		plan := e.explainWith(ordered, `SELECT id FROM progress WHERE user_id = $1 ORDER BY started_at DESC, id LIMIT 20`, e.user)
		if !strings.Contains(plan, "progress_user_id_started_at_id_idx") {
			t.Errorf("default listing does not use its index:\n%s", plan)
		}
		plan = e.explainWith(ordered, `SELECT id FROM progress WHERE user_id = $1 AND (server_updated_at, id) > ($2, $3) ORDER BY server_updated_at, id LIMIT 20`, e.user, t0, uuid.Nil)
		if !strings.Contains(plan, "progress_user_id_server_updated_at_id_idx") {
			t.Errorf("sync feed does not use its index:\n%s", plan)
		}
	})
}
