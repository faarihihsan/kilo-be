package testutil_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// seededUsers is how many rows migrations/0001_users.sql inserts (the bootstrap
// admin). Every fresh test database starts with them.
const seededUsers = 1

func count(t *testing.T, q store.Querier, table string) int {
	t.Helper()
	var n int
	if err := q.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func currentDatabase(t *testing.T, db *store.DB) string {
	t.Helper()
	var name string
	if err := db.QueryRow(t.Context(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	return name
}

func TestNewDBIsolation(t *testing.T) {
	a := testutil.NewDB(t)
	b := testutil.NewDB(t)

	if currentDatabase(t, a) == currentDatabase(t, b) {
		t.Fatal("two NewDB calls share a database")
	}

	testutil.SeedUser(t, a, domain.RoleUser)
	testutil.SeedUser(t, a, domain.RoleAdmin)
	if got := count(t, a, "users"); got != seededUsers+2 {
		t.Errorf("db a users = %d, want %d", got, seededUsers+2)
	}
	if got := count(t, b, "users"); got != seededUsers {
		t.Errorf("db b sees %d users, want only its %d migrated", got, seededUsers)
	}
}

func TestNewDBIsMigrated(t *testing.T) {
	db := testutil.NewDB(t)
	for _, table := range []string{"users", "auth_tokens", "exercises", "workout_plans",
		"workout_plan_exercises", "progress", "progress_exercises", "progress_sets"} {
		want := 0
		if table == "users" {
			want = seededUsers // the bootstrap admin from migrations/0001
		}
		if got := count(t, db, table); got != want {
			t.Errorf("%s has %d rows in a fresh database, want %d", table, got, want)
		}
	}
	behind, err := store.SchemaBehind(t.Context(), db.Pool().Config().ConnString())
	if err != nil {
		t.Fatalf("SchemaBehind: %v", err)
	}
	if behind {
		t.Error("template database is behind the embedded migrations")
	}
}

func TestNewDBParallel(t *testing.T) {
	const n = 12
	var mu sync.Mutex
	seen := map[string]bool{}

	for i := range n {
		t.Run(fmt.Sprintf("sub%d", i), func(t *testing.T) {
			t.Parallel()
			db := testutil.NewDB(t)
			name := currentDatabase(t, db)

			mu.Lock()
			dup := seen[name]
			seen[name] = true
			mu.Unlock()
			if dup {
				t.Errorf("database %s handed out twice", name)
			}

			// Each subtest writes its own rows and must see only those
			// (plus the bootstrap admin from the migrations).
			uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
			testutil.SeedToken(t, db, uid)
			if got := count(t, db, "users"); got != seededUsers+1 {
				t.Errorf("users = %d, want %d", got, seededUsers+1)
			}
			if got := count(t, db, "auth_tokens"); got != 1 {
				t.Errorf("auth_tokens = %d, want 1", got)
			}
		})
	}
}

func TestNewDBDropsDatabaseOnCleanup(t *testing.T) {
	var name string
	t.Run("inner", func(t *testing.T) {
		db := testutil.NewDB(t)
		name = currentDatabase(t, db)
	})

	// The inner subtest has finished, so its cleanup has run.
	admin := testutil.NewDB(t)
	var exists bool
	err := admin.QueryRow(t.Context(),
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("query pg_database: %v", err)
	}
	if exists {
		t.Errorf("database %s still exists after the test ended", name)
	}
}

func TestNewEmptyDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	db, err := store.Open(t.Context(), url, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(db.Close)

	var tables int
	if err := db.QueryRow(t.Context(),
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("empty database has %d tables", tables)
	}
}

func TestSeedUser(t *testing.T) {
	db := testutil.NewDB(t)
	usernameRE := regexp.MustCompile(domain.UsernamePattern)

	idU, nameU := testutil.SeedUser(t, db, domain.RoleUser)
	idA, nameA := testutil.SeedUser(t, db, domain.RoleAdmin)
	if idU == idA || nameU == nameA {
		t.Fatal("seeded users are not unique")
	}
	for _, name := range []string{nameU, nameA} {
		if !usernameRE.MatchString(name) {
			t.Errorf("username %q does not match %s", name, domain.UsernamePattern)
		}
	}
	for _, id := range []uuid.UUID{idU, idA} {
		if id.Version() != 7 {
			t.Errorf("id %s is UUID v%d, want v7", id, id.Version())
		}
	}

	var role, hash string
	if err := db.QueryRow(t.Context(), "SELECT role, password_hash FROM users WHERE id = $1", idA).Scan(&role, &hash); err != nil {
		t.Fatal(err)
	}
	if role != string(domain.RoleAdmin) {
		t.Errorf("role = %q, want admin", role)
	}
	if hash != testutil.DummyPasswordHash || !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("password_hash = %q, want the dummy argon2id hash", hash)
	}

	id, name := testutil.SeedUser(t, db, domain.RoleUser,
		testutil.WithUsername("custom_name"), testutil.WithPasswordHash("h"))
	if name != "custom_name" {
		t.Errorf("WithUsername ignored: %q", name)
	}
	if err := db.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE id = $1", id).Scan(&hash); err != nil || hash != "h" {
		t.Errorf("WithPasswordHash: hash = %q, err = %v", hash, err)
	}
}

func TestSeedToken(t *testing.T) {
	db := testutil.NewDB(t)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)

	raw, id := testutil.SeedToken(t, db, uid)
	if !strings.HasPrefix(raw, domain.TokenPrefix) || len(raw) != len(domain.TokenPrefix)+43 {
		t.Errorf("raw token %q has the wrong shape (want wt_ + 43 base64url chars)", raw)
	}
	raw2, _ := testutil.SeedToken(t, db, uid)
	if raw == raw2 {
		t.Error("two tokens are identical")
	}

	var (
		hash      []byte
		device    *string
		expiresAt time.Time
		createdAt time.Time
		revokedAt *time.Time
	)
	err := db.QueryRow(t.Context(),
		"SELECT token_hash, device_name, created_at, expires_at, revoked_at FROM auth_tokens WHERE id = $1", id).
		Scan(&hash, &device, &createdAt, &expiresAt, &revokedAt)
	if err != nil {
		t.Fatal(err)
	}
	if string(hash) != string(testutil.HashToken(raw)) || len(hash) != 32 {
		t.Error("stored hash is not the SHA-256 of the raw token")
	}
	if strings.Contains(string(hash), raw) {
		t.Error("raw token stored")
	}
	if device != nil || revokedAt != nil {
		t.Errorf("defaults: device = %v, revoked = %v, want both NULL", device, revokedAt)
	}
	if ttl := expiresAt.Sub(createdAt); ttl < 364*24*time.Hour || ttl > 366*24*time.Hour {
		t.Errorf("default TTL = %v, want about 365 days", ttl)
	}

	revoked := time.Now().UTC().Add(-time.Minute)
	_, id = testutil.SeedToken(t, db, uid,
		testutil.WithTokenDevice("iPhone"), testutil.WithTokenExpired(), testutil.WithTokenRevoked(revoked))
	err = db.QueryRow(t.Context(),
		"SELECT device_name, expires_at, revoked_at FROM auth_tokens WHERE id = $1", id).Scan(&device, &expiresAt, &revokedAt)
	if err != nil {
		t.Fatal(err)
	}
	if device == nil || *device != "iPhone" {
		t.Errorf("device = %v, want iPhone", device)
	}
	if !expiresAt.Before(time.Now()) {
		t.Errorf("WithTokenExpired: expires_at = %v is in the future", expiresAt)
	}
	if revokedAt == nil {
		t.Error("WithTokenRevoked: revoked_at is NULL")
	}
}

func TestSeedExercise(t *testing.T) {
	db := testutil.NewDB(t)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)

	a := testutil.SeedExercise(t, db, uid)
	b := testutil.SeedExercise(t, db, uid)
	if a == b {
		t.Fatal("seeded exercises share an id")
	}

	var (
		category, primary, equipment, measurement string
		secondary                                 []string
		deletedAt                                 *time.Time
	)
	err := db.QueryRow(t.Context(),
		`SELECT category, primary_muscle_group, secondary_muscle_groups, equipment, measurement_type, deleted_at
		 FROM exercises WHERE id = $1`, a).Scan(&category, &primary, &secondary, &equipment, &measurement, &deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if category != "strength" || primary != "chest" || equipment != "barbell" || measurement != "reps_weight" {
		t.Errorf("unexpected defaults: %s %s %s %s", category, primary, equipment, measurement)
	}
	if len(secondary) != 0 || deletedAt != nil {
		t.Errorf("secondary = %v, deleted_at = %v, want empty and NULL", secondary, deletedAt)
	}

	// Every option, and a soft-deleted exercise does not block reuse of a name.
	custom := uuid.Must(uuid.NewV7())
	got := testutil.SeedExercise(t, db, uid,
		testutil.WithExerciseID(custom),
		testutil.WithExerciseName("Deadlift"),
		testutil.WithExerciseCategory(domain.CategoryCardio),
		testutil.WithPrimaryMuscleGroup(domain.MuscleGroupLowerBack),
		testutil.WithSecondaryMuscleGroups(domain.MuscleGroupGlutes, domain.MuscleGroupHamstrings),
		testutil.WithEquipment(domain.EquipmentKettlebell),
		testutil.WithMeasurementType(domain.MeasurementTypeDistanceDuration),
		testutil.WithInstructions("Hinge."),
		testutil.WithExerciseImage("0123456789abcdef", domain.ImageExtWebP, 1234),
		testutil.WithExerciseDeletedAt(time.Now()),
	)
	if got != custom {
		t.Errorf("WithExerciseID ignored")
	}
	testutil.SeedExercise(t, db, uid, testutil.WithExerciseName("Deadlift")) // live twin of the deleted one

	var hash, ext string
	var size int
	err = db.QueryRow(t.Context(),
		"SELECT image_hash, image_ext, image_size_bytes, secondary_muscle_groups FROM exercises WHERE id = $1", custom).
		Scan(&hash, &ext, &size, &secondary)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "0123456789abcdef" || ext != "webp" || size != 1234 || len(secondary) != 2 {
		t.Errorf("options not applied: %s %s %d %v", hash, ext, size, secondary)
	}
}

func TestSeedPlanAndProgress(t *testing.T) {
	db := testutil.NewDB(t)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	ex1 := testutil.SeedExercise(t, db, uid)
	ex2 := testutil.SeedExercise(t, db, uid, testutil.WithExerciseDeletedAt(time.Now()))

	plan := testutil.SeedPlan(t, db, uid,
		testutil.WithPlanName("Push"),
		testutil.WithPlanDescription("chest day"),
		testutil.WithPlanExercise(ex1, testutil.WithTargetSets(4), testutil.WithTargetRepsMax(12), testutil.WithTargetWeight(80.5), testutil.WithRestSeconds(120)),
		testutil.WithPlanExercise(ex2), // soft-deleted exercises are valid references
	)
	empty := testutil.SeedPlan(t, db, uid)
	if plan == empty {
		t.Fatal("plans share an id")
	}
	if got := count(t, db, "workout_plan_exercises"); got != 2 {
		t.Errorf("plan exercises = %d, want 2", got)
	}
	var positions []int
	rows, err := db.Query(t.Context(), "SELECT position FROM workout_plan_exercises WHERE workout_plan_id = $1 ORDER BY position", plan)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		positions = append(positions, p)
	}
	if len(positions) != 2 || positions[0] != 0 || positions[1] != 1 {
		t.Errorf("positions = %v, want [0 1]", positions)
	}

	prog := testutil.SeedProgress(t, db, uid,
		testutil.WithProgressPlan(plan),
		testutil.WithProgressExercise(ex1, testutil.WithSets(3)),
		testutil.WithProgressExercise(ex2, testutil.WithSet(testutil.SetSpec{
			Type: domain.SetTypeWarmup, Reps: testutil.Ptr(12), RPE: testutil.Ptr(8.5), NotCompleted: true,
		})),
	)
	testutil.SeedProgress(t, db, uid, testutil.WithProgressDeletedAt(time.Now()))
	if got := count(t, db, "progress"); got != 2 {
		t.Errorf("progress = %d, want 2", got)
	}
	if got := count(t, db, "progress_exercises"); got != 2 {
		t.Errorf("progress_exercises = %d, want 2", got)
	}
	if got := count(t, db, "progress_sets"); got != 4 {
		t.Errorf("progress_sets = %d, want 4", got)
	}
	var endedAfterStart bool
	if err := db.QueryRow(t.Context(), "SELECT ended_at - started_at = interval '3600 seconds' FROM progress WHERE id = $1", prog).Scan(&endedAfterStart); err != nil || !endedAfterStart {
		t.Errorf("ended_at is not started_at + duration (err = %v)", err)
	}
}

func TestSeedsWorkInsideATransaction(t *testing.T) {
	db := testutil.NewDB(t)
	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		uid, _ := testutil.SeedUser(t, tx, domain.RoleUser)
		testutil.SeedExercise(t, tx, uid)
		return context.Canceled // roll back
	})
	if err == nil {
		t.Fatal("WithTx returned nil")
	}
	if got := count(t, db, "users"); got != seededUsers {
		t.Errorf("rolled-back seeds left %d users, want only the %d migrated", got, seededUsers)
	}
}

func TestTruncate(t *testing.T) {
	db := testutil.NewDB(t)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	ex := testutil.SeedExercise(t, db, uid)
	testutil.SeedPlan(t, db, uid, testutil.WithPlanExercise(ex))

	// A named table takes its referencing tables with it.
	testutil.Truncate(t, db, "workout_plans")
	if got := count(t, db, "workout_plans") + count(t, db, "workout_plan_exercises"); got != 0 {
		t.Errorf("plans not truncated, %d rows left", got)
	}
	if got := count(t, db, "exercises"); got != 1 {
		t.Errorf("exercises = %d, want 1 (untouched)", got)
	}

	// No names: everything, but the migration bookkeeping stays.
	testutil.Truncate(t, db)
	if got := count(t, db, "users") + count(t, db, "exercises"); got != 0 {
		t.Errorf("Truncate() left %d rows", got)
	}
	if got := count(t, db, "goose_db_version"); got == 0 {
		t.Error("Truncate() emptied goose_db_version")
	}
}

// fakeTB records how NewDB ends a test, so the unreachable-server behaviour
// can be tested without failing this test. Fatalf and Skipf unwind with a
// panic, as the real ones stop the goroutine.
type fakeTB struct {
	testing.TB
	fatal, skip string
}

type stopTest struct{}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
	panic(stopTest{})
}

func (f *fakeTB) Skipf(format string, args ...any) {
	f.skip = fmt.Sprintf(format, args...)
	panic(stopTest{})
}

// runUntilStopped runs fn and swallows the stopTest panic.
func runUntilStopped(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopTest); !ok {
				panic(r)
			}
		}
	}()
	fn()
}

const unreachableURL = "postgres://postgres:s3cr3t-pw@127.0.0.1:1/postgres?sslmode=disable"

func TestUnreachableServerFailsWithInstructions(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", unreachableURL)
	t.Setenv("TEST_SKIP_DB", "")

	for name, call := range map[string]func(testing.TB){
		"NewDB":            func(tb testing.TB) { testutil.NewDB(tb) },
		"NewEmptyDatabase": func(tb testing.TB) { testutil.NewEmptyDatabase(tb) },
	} {
		fake := &fakeTB{TB: t}
		runUntilStopped(func() { call(fake) })

		if fake.skip != "" {
			t.Errorf("%s skipped without TEST_SKIP_DB: %s", name, fake.skip)
		}
		for _, want := range []string{"make db-up", "TEST_DATABASE_URL", "TEST_SKIP_DB=1", "127.0.0.1:1"} {
			if !strings.Contains(fake.fatal, want) {
				t.Errorf("%s: failure message lacks %q:\n%s", name, want, fake.fatal)
			}
		}
		if strings.Contains(fake.fatal, "s3cr3t-pw") {
			t.Errorf("%s: failure message leaks the password:\n%s", name, fake.fatal)
		}
	}
}

func TestUnreachableServerSkipsWhenAsked(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", unreachableURL)
	t.Setenv("TEST_SKIP_DB", "1")

	fake := &fakeTB{TB: t}
	runUntilStopped(func() { testutil.NewDB(fake) })
	if fake.fatal != "" || fake.skip == "" {
		t.Errorf("fatal = %q, skip = %q, want a skip only", fake.fatal, fake.skip)
	}
}

func TestSkipFlagDoesNotSkipWhenServerIsUp(t *testing.T) {
	// TEST_SKIP_DB only covers an unreachable server: with one available the
	// tests run, so a forgotten variable cannot hide them.
	t.Setenv("TEST_SKIP_DB", "1")
	fake := &fakeTB{TB: t}
	runUntilStopped(func() { testutil.NewDB(fake) })
	if fake.fatal != "" || fake.skip != "" {
		t.Errorf("fatal = %q, skip = %q, want NewDB to succeed", fake.fatal, fake.skip)
	}
}

func TestAdminURL(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", "")
	if got := testutil.AdminURL(); got != testutil.DefaultDatabaseURL {
		t.Errorf("AdminURL() = %q, want the default %q", got, testutil.DefaultDatabaseURL)
	}
	t.Setenv("TEST_DATABASE_URL", "postgres://x@example:1/y")
	if got := testutil.AdminURL(); got != "postgres://x@example:1/y" {
		t.Errorf("AdminURL() = %q, want the override", got)
	}
}
