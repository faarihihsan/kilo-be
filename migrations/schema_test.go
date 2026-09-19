package migrations_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

// This file checks the shape of the migrated schema against docs/data-model.md:
// columns and types, the constraint and index names store code matches on, and
// that every enum CHECK lists exactly the values of internal/domain. The
// behaviour of the constraints is in constraints_test.go.

const tz = "timestamp with time zone"

type column struct {
	table, name, typ string
	notNull          bool
	def              string // pg_get_expr of the default, "" for none
}

func col(table, name, typ string, notNull bool, def ...string) column {
	c := column{table: table, name: name, typ: typ, notNull: notNull}
	if len(def) > 0 {
		c.def = def[0]
	}
	return c
}

// wantColumns is data-model.md, column by column, in table order.
var wantColumns = []column{
	col("users", "id", "uuid", true),
	col("users", "username", "text", true),
	col("users", "password_hash", "text", true),
	col("users", "role", "text", true, "'user'::text"),
	col("users", "created_at", tz, true, "now()"),
	col("users", "updated_at", tz, true, "now()"),

	col("auth_tokens", "id", "uuid", true),
	col("auth_tokens", "user_id", "uuid", true),
	col("auth_tokens", "token_hash", "bytea", true),
	col("auth_tokens", "device_name", "text", false),
	col("auth_tokens", "created_at", tz, true, "now()"),
	col("auth_tokens", "expires_at", tz, true),
	col("auth_tokens", "last_used_at", tz, false),
	col("auth_tokens", "revoked_at", tz, false),

	col("exercises", "id", "uuid", true),
	col("exercises", "name", "text", true),
	col("exercises", "category", "text", true),
	col("exercises", "primary_muscle_group", "text", true),
	col("exercises", "secondary_muscle_groups", "text[]", true, "'{}'::text[]"),
	col("exercises", "equipment", "text", true),
	col("exercises", "measurement_type", "text", true),
	col("exercises", "instructions", "text", false),
	col("exercises", "image_hash", "text", false),
	col("exercises", "image_ext", "text", false),
	col("exercises", "image_size_bytes", "integer", false),
	col("exercises", "created_by", "uuid", true),
	col("exercises", "created_at", tz, true, "now()"),
	col("exercises", "updated_at", tz, true, "now()"),
	col("exercises", "deleted_at", tz, false),

	col("workout_plans", "id", "uuid", true),
	col("workout_plans", "user_id", "uuid", true),
	col("workout_plans", "name", "text", true),
	col("workout_plans", "description", "text", false),
	col("workout_plans", "client_updated_at", tz, true),
	col("workout_plans", "created_at", tz, true, "now()"),
	col("workout_plans", "server_updated_at", tz, true),
	col("workout_plans", "deleted_at", tz, false),

	col("workout_plan_exercises", "id", "uuid", true),
	col("workout_plan_exercises", "workout_plan_id", "uuid", true),
	col("workout_plan_exercises", "exercise_id", "uuid", true),
	col("workout_plan_exercises", "position", "integer", true),
	col("workout_plan_exercises", "target_sets", "integer", true),
	col("workout_plan_exercises", "target_reps", "integer", false),
	col("workout_plan_exercises", "target_reps_max", "integer", false),
	col("workout_plan_exercises", "target_weight", "numeric(7,2)", false),
	col("workout_plan_exercises", "target_duration_seconds", "integer", false),
	col("workout_plan_exercises", "target_distance_meters", "numeric(9,2)", false),
	col("workout_plan_exercises", "rest_seconds", "integer", false),
	col("workout_plan_exercises", "notes", "text", false),

	col("progress", "id", "uuid", true),
	col("progress", "user_id", "uuid", true),
	col("progress", "workout_plan_id", "uuid", false),
	col("progress", "name", "text", true),
	col("progress", "notes", "text", false),
	col("progress", "started_at", tz, true),
	col("progress", "ended_at", tz, true),
	col("progress", "duration_seconds", "integer", true),
	col("progress", "client_updated_at", tz, true),
	col("progress", "created_at", tz, true, "now()"),
	col("progress", "server_updated_at", tz, true),
	col("progress", "deleted_at", tz, false),

	col("progress_exercises", "id", "uuid", true),
	col("progress_exercises", "progress_id", "uuid", true),
	col("progress_exercises", "exercise_id", "uuid", true),
	col("progress_exercises", "position", "integer", true),
	col("progress_exercises", "notes", "text", false),

	col("progress_sets", "id", "uuid", true),
	col("progress_sets", "progress_exercise_id", "uuid", true),
	col("progress_sets", "position", "integer", true),
	col("progress_sets", "type", "text", true),
	col("progress_sets", "reps", "integer", false),
	col("progress_sets", "weight", "numeric(7,2)", false),
	col("progress_sets", "duration_seconds", "integer", false),
	col("progress_sets", "distance_meters", "numeric(9,2)", false),
	col("progress_sets", "rpe", "numeric(3,1)", false),
	col("progress_sets", "completed", "boolean", true),
}

// wantConstraints are the named constraints per kind (p primary key, u unique,
// f foreign key, c check). Store code matches on these names, so a change here
// is an API change.
var wantConstraints = map[byte][]string{
	'p': {
		"users_pkey", "auth_tokens_pkey", "exercises_pkey", "workout_plans_pkey",
		"workout_plan_exercises_pkey", "progress_pkey", "progress_exercises_pkey", "progress_sets_pkey",
	},
	'u': {
		"users_username_uniq",
		"auth_tokens_token_hash_uniq",
		"workout_plan_exercises_workout_plan_id_position_uniq",
		"progress_exercises_progress_id_position_uniq",
		"progress_sets_progress_exercise_id_position_uniq",
	},
	'f': {
		"auth_tokens_user_id_fkey",
		"exercises_created_by_fkey",
		"workout_plans_user_id_fkey",
		"workout_plan_exercises_workout_plan_id_fkey",
		"workout_plan_exercises_exercise_id_fkey",
		"progress_user_id_fkey",
		"progress_workout_plan_id_fkey",
		"progress_exercises_progress_id_fkey",
		"progress_exercises_exercise_id_fkey",
		"progress_sets_progress_exercise_id_fkey",
	},
	'c': {
		"users_username_format_chk", "users_role_chk",
		"auth_tokens_device_name_len_chk",
		"exercises_name_len_chk", "exercises_instructions_len_chk", "exercises_category_chk",
		"exercises_primary_muscle_group_chk", "exercises_secondary_muscle_groups_chk",
		"exercises_equipment_chk", "exercises_measurement_type_chk",
		"exercises_image_all_or_none_chk", "exercises_image_hash_format_chk", "exercises_image_ext_chk",
		"workout_plans_name_len_chk", "workout_plans_description_len_chk",
		"workout_plan_exercises_position_chk", "workout_plan_exercises_target_sets_chk",
		"workout_plan_exercises_target_reps_chk",
		"workout_plan_exercises_target_reps_max_requires_reps_chk",
		"workout_plan_exercises_target_reps_max_ge_reps_chk",
		"workout_plan_exercises_target_weight_chk", "workout_plan_exercises_target_duration_seconds_chk",
		"workout_plan_exercises_target_distance_meters_chk", "workout_plan_exercises_rest_seconds_chk",
		"workout_plan_exercises_notes_len_chk",
		"progress_name_len_chk", "progress_notes_len_chk", "progress_ended_at_chk", "progress_duration_seconds_chk",
		"progress_exercises_position_chk", "progress_exercises_notes_len_chk",
		"progress_sets_position_chk", "progress_sets_type_chk", "progress_sets_reps_chk",
		"progress_sets_weight_chk", "progress_sets_duration_seconds_chk", "progress_sets_distance_meters_chk",
		"progress_sets_rpe_range_chk", "progress_sets_rpe_step_chk",
	},
}

// cascadeFKs are the foreign keys with ON DELETE CASCADE. Every other foreign
// key has no action: rows are only ever soft-deleted.
var cascadeFKs = []string{
	"workout_plan_exercises_workout_plan_id_fkey",
	"progress_exercises_progress_id_fkey",
	"progress_sets_progress_exercise_id_fkey",
}

// wantIndexes are the indexes that do not back a constraint, with a fragment
// of their definition (pg_indexes.indexdef).
var wantIndexes = map[string]string{
	"exercises_name_lower_uniq":                      "CREATE UNIQUE INDEX exercises_name_lower_uniq ON public.exercises USING btree (lower(name)) WHERE (deleted_at IS NULL)",
	"exercises_updated_at_id_idx":                    "ON public.exercises USING btree (updated_at, id)",
	"exercises_primary_muscle_group_idx":             "ON public.exercises USING btree (primary_muscle_group)",
	"exercises_name_lower_trgm_idx":                  "ON public.exercises USING gin (lower(name) gin_trgm_ops)",
	"auth_tokens_user_id_idx":                        "ON public.auth_tokens USING btree (user_id)",
	"workout_plans_user_id_server_updated_at_id_idx": "ON public.workout_plans USING btree (user_id, server_updated_at, id)",
	"workout_plans_user_id_lower_name_id_idx":        "ON public.workout_plans USING btree (user_id, lower(name), id)",
	"workout_plan_exercises_exercise_id_idx":         "ON public.workout_plan_exercises USING btree (exercise_id)",
	"progress_user_id_started_at_id_idx":             "ON public.progress USING btree (user_id, started_at DESC, id)",
	"progress_user_id_server_updated_at_id_idx":      "ON public.progress USING btree (user_id, server_updated_at, id)",
	"progress_workout_plan_id_idx":                   "ON public.progress USING btree (workout_plan_id)",
	"progress_exercises_exercise_id_idx":             "ON public.progress_exercises USING btree (exercise_id)",
}

func TestColumnsMatchDataModel(t *testing.T) {
	db := testutil.NewDB(t)

	rows, err := db.Query(t.Context(), `
		SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		       coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
		  AND c.relname <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (column, error) {
		var c column
		err := r.Scan(&c.table, &c.name, &c.typ, &c.notNull, &c.def)
		return c, err
	})
	if err != nil {
		t.Fatal(err)
	}

	gotByKey := map[string]column{}
	for _, c := range got {
		gotByKey[c.table+"."+c.name] = c
	}
	wantByKey := map[string]column{}
	for _, w := range wantColumns {
		key := w.table + "." + w.name
		wantByKey[key] = w
		g, ok := gotByKey[key]
		if !ok {
			t.Errorf("column %s is missing", key)
			continue
		}
		if g != w {
			t.Errorf("column %s = {type %q, not null %v, default %q}, want {type %q, not null %v, default %q}",
				key, g.typ, g.notNull, g.def, w.typ, w.notNull, w.def)
		}
	}
	for key := range gotByKey {
		if _, ok := wantByKey[key]; !ok {
			t.Errorf("unexpected column %s (add it to wantColumns and docs/data-model.md)", key)
		}
	}
}

func TestConstraintAndIndexNames(t *testing.T) {
	db := testutil.NewDB(t)

	// Constraints. NOT NULL constraints (contype n, PostgreSQL 18+) are not
	// named API and are covered by the column test.
	rows, err := db.Query(t.Context(), `
		SELECT con.contype::text, con.conname, con.confdeltype::text
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND con.contype IN ('p', 'u', 'f', 'c')
		  AND c.relname <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	type conRow struct{ kind, name, delType string }
	cons, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (conRow, error) {
		var c conRow
		return c, r.Scan(&c.kind, &c.name, &c.delType)
	})
	if err != nil {
		t.Fatal(err)
	}

	gotKinds := map[byte][]string{}
	for _, c := range cons {
		gotKinds[c.kind[0]] = append(gotKinds[c.kind[0]], c.name)
		if c.kind == "f" {
			want := "a" // no action
			if slices.Contains(cascadeFKs, c.name) {
				want = "c" // cascade
			}
			if c.delType != want {
				t.Errorf("foreign key %s: ON DELETE code %q, want %q", c.name, c.delType, want)
			}
		}
	}
	for kind, want := range wantConstraints {
		got := gotKinds[kind]
		slices.Sort(got)
		want = slices.Sorted(slices.Values(want))
		if !slices.Equal(got, want) {
			t.Errorf("constraints of kind %c differ\n missing: %v\n unexpected: %v",
				kind, diff(want, got), diff(got, want))
		}
	}

	// Indexes: constraint-backed ones are named after their constraint.
	rows, err = db.Query(t.Context(), `
		SELECT indexname, indexdef FROM pg_indexes
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	type idxRow struct{ name, def string }
	idxs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (idxRow, error) {
		var i idxRow
		return i, r.Scan(&i.name, &i.def)
	})
	if err != nil {
		t.Fatal(err)
	}
	gotIdx := map[string]string{}
	for _, i := range idxs {
		gotIdx[i.name] = i.def
	}
	wantNames := slices.Concat(wantConstraints['p'], wantConstraints['u'])
	for name := range wantIndexes {
		wantNames = append(wantNames, name)
	}
	var gotNames []string
	for name := range gotIdx {
		gotNames = append(gotNames, name)
	}
	slices.Sort(gotNames)
	slices.Sort(wantNames)
	if !slices.Equal(gotNames, wantNames) {
		t.Errorf("indexes differ\n missing: %v\n unexpected: %v", diff(wantNames, gotNames), diff(gotNames, wantNames))
	}
	for name, fragment := range wantIndexes {
		if def, ok := gotIdx[name]; ok && !strings.Contains(def, fragment) {
			t.Errorf("index %s = %q, want it to contain %q", name, def, fragment)
		}
	}

	// PostgreSQL silently truncates identifiers at 63 bytes, which would
	// change a name. The lists above prove none was cut, this is the guard
	// for names added later.
	for _, name := range gotNames {
		if len(name) >= 63 {
			t.Errorf("identifier %q is %d bytes, PostgreSQL truncates at 63", name, len(name))
		}
	}
}

// diff returns the elements of a that are not in b.
func diff(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func names[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// enumChecks maps each enum CHECK constraint to the domain values it must list.
var enumChecks = map[string][]string{
	"users_role_chk":                        names(domain.Roles),
	"exercises_category_chk":                names(domain.Categories),
	"exercises_primary_muscle_group_chk":    names(domain.MuscleGroups),
	"exercises_secondary_muscle_groups_chk": names(domain.MuscleGroups),
	"exercises_equipment_chk":               names(domain.EquipmentList),
	"exercises_measurement_type_chk":        names(domain.MeasurementTypes),
	"exercises_image_ext_chk":               names(domain.ImageExts),
	"progress_sets_type_chk":                names(domain.SetTypes),
}

// patternChecks maps each regex CHECK constraint to the pattern it must use.
var patternChecks = map[string]string{
	"users_username_format_chk":       domain.UsernamePattern,
	"exercises_image_hash_format_chk": fmt.Sprintf("^[0-9a-f]{%d}$", domain.ImageHashLen),
}

var quoted = regexp.MustCompile(`'((?:[^']|'')*)'`)

// TestEnumChecksMatchDomain parses every CHECK definition out of the live
// database and fails when an enum list drifts from internal/domain, in either
// direction: a value missing from the CHECK, or a value the CHECK allows that
// domain does not know. Add a new enum value to domain and to a new migration
// together.
func TestEnumChecksMatchDomain(t *testing.T) {
	db := testutil.NewDB(t)

	rows, err := db.Query(t.Context(), `
		SELECT con.conname, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND con.contype = 'c'`)
	if err != nil {
		t.Fatal(err)
	}
	type defRow struct{ name, def string }
	defs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (defRow, error) {
		var d defRow
		return d, r.Scan(&d.name, &d.def)
	})
	if err != nil {
		t.Fatal(err)
	}
	defByName := map[string]string{}
	for _, d := range defs {
		defByName[d.name] = d.def
	}

	literals := func(def string) []string {
		var out []string
		for _, m := range quoted.FindAllStringSubmatch(def, -1) {
			out = append(out, strings.ReplaceAll(m[1], "''", "'"))
		}
		return out
	}

	for name, want := range enumChecks {
		def, ok := defByName[name]
		if !ok {
			t.Errorf("enum constraint %s does not exist", name)
			continue
		}
		got := literals(def)
		slices.Sort(got)
		want = slices.Sorted(slices.Values(want))
		if !slices.Equal(got, want) {
			t.Errorf("%s lists %v\n missing from the CHECK: %v\n not in domain: %v\n definition: %s",
				name, got, diff(want, got), diff(got, want), def)
		}
	}

	for name, want := range patternChecks {
		def, ok := defByName[name]
		if !ok {
			t.Errorf("pattern constraint %s does not exist", name)
			continue
		}
		if got := literals(def); len(got) != 1 || got[0] != want {
			t.Errorf("%s uses pattern %v, want [%s]\n definition: %s", name, got, want, def)
		}
	}

	// A CHECK with string literals that no list above covers is an enum or
	// pattern added without a parity check.
	for name, def := range defByName {
		if len(literals(def)) == 0 {
			continue
		}
		_, isEnum := enumChecks[name]
		_, isPattern := patternChecks[name]
		if !isEnum && !isPattern {
			t.Errorf("constraint %s has string literals but no parity check: %s", name, def)
		}
	}
}

// TestEnumChecksAcceptEveryDomainValue is the same guarantee from the other
// side: each domain value is really accepted, and a value outside the list is
// really rejected, exercised through inserts (the CHECK text could parse right
// and still not behave).
func TestEnumChecksAcceptEveryDomainValue(t *testing.T) {
	e := newEnv(t)
	base := func() map[string]any { return e.exerciseRow(nil) }

	enumColumns := []struct {
		column string
		values []string
		bad    string
		check  string
	}{
		{"category", names(domain.Categories), "yoga", "exercises_category_chk"},
		{"primary_muscle_group", names(domain.MuscleGroups), "legs", "exercises_primary_muscle_group_chk"},
		{"equipment", names(domain.EquipmentList), "sword", "exercises_equipment_chk"},
		{"measurement_type", names(domain.MeasurementTypes), "time", "exercises_measurement_type_chk"},
	}
	for _, ec := range enumColumns {
		for _, v := range ec.values {
			row := base()
			row[ec.column] = v
			e.want(t, "", e.insert("exercises", row), "%s=%s", ec.column, v)
		}
		row := base()
		row[ec.column] = ec.bad
		e.want(t, "check:"+ec.check, e.insert("exercises", row), "%s=%s", ec.column, ec.bad)
	}

	// Secondary muscle groups: every value, all together, and one bad element.
	row := base()
	row["secondary_muscle_groups"] = names(domain.MuscleGroups)
	e.want(t, "", e.insert("exercises", row), "all muscle groups as secondary")
	row = base()
	row["secondary_muscle_groups"] = []string{"chest", "legs"}
	e.want(t, "check:exercises_secondary_muscle_groups_chk", e.insert("exercises", row), "bad secondary element")

	// Image extensions come with a hash and size (all-or-none).
	for _, ext := range names(domain.ImageExts) {
		row := base()
		row["image_hash"], row["image_ext"], row["image_size_bytes"] = "0123456789abcdef", ext, 100
		e.want(t, "", e.insert("exercises", row), "image_ext=%s", ext)
	}
	row = base()
	row["image_hash"], row["image_ext"], row["image_size_bytes"] = "0123456789abcdef", "gif", 100
	e.want(t, "check:exercises_image_ext_chk", e.insert("exercises", row), "image_ext=gif")

	// Roles and set types.
	for _, r := range names(domain.Roles) {
		e.want(t, "", e.insert("users", e.userRow(map[string]any{"role": r})), "role=%s", r)
	}
	e.want(t, "check:users_role_chk", e.insert("users", e.userRow(map[string]any{"role": "root"})), "role=root")

	pe := testutil.SeedProgress(t, e.db, e.user, testutil.WithProgressExercise(e.exercise))
	var progressExerciseID string
	if err := e.db.QueryRow(t.Context(), `SELECT id::text FROM progress_exercises WHERE progress_id = $1`, pe).Scan(&progressExerciseID); err != nil {
		t.Fatal(err)
	}
	for i, st := range names(domain.SetTypes) {
		e.want(t, "", e.insert("progress_sets", e.setRow(progressExerciseID, map[string]any{"position": i, "type": st})), "type=%s", st)
	}
	e.want(t, "check:progress_sets_type_chk",
		e.insert("progress_sets", e.setRow(progressExerciseID, map[string]any{"position": 99, "type": "superset"})), "type=superset")
}
