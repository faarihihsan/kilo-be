package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// wantMigrations mirrors the files in /migrations. Adding a migration means
// adding a row here, which keeps numbering gaps and renames deliberate.
var wantMigrations = []struct {
	version int64
	name    string
}{
	{1, "0001_users.sql"},
	{2, "0002_auth_tokens.sql"},
	{3, "0003_exercises.sql"},
	{4, "0004_workout_plans.sql"},
	{5, "0005_progress.sql"},
}

const latestVersion = 5

func openPlain(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	return conn
}

// appTables lists the tables in the public schema except goose's bookkeeping.
func appTables(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(t.Context(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func hasTrgm(t *testing.T, conn *pgx.Conn) bool {
	t.Helper()
	var ok bool
	if err := conn.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')`).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func mustStatus(t *testing.T, url string) store.Status {
	t.Helper()
	st, err := store.MigrateStatus(t.Context(), url)
	if err != nil {
		t.Fatalf("MigrateStatus: %v", err)
	}
	return st
}

func TestMigrateStatusOnEmptyDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)

	st := mustStatus(t, url)
	if st.Current != 0 || st.Latest != latestVersion {
		t.Errorf("Current, Latest = %d, %d, want 0, %d", st.Current, st.Latest, latestVersion)
	}
	if len(st.Migrations) != len(wantMigrations) {
		t.Fatalf("%d embedded migrations, want %d", len(st.Migrations), len(wantMigrations))
	}
	for i, m := range st.Migrations {
		if m.Version != wantMigrations[i].version || m.Name != wantMigrations[i].name {
			t.Errorf("migration %d = %d %s, want %d %s", i, m.Version, m.Name, wantMigrations[i].version, wantMigrations[i].name)
		}
		if m.Applied || !m.AppliedAt.IsZero() {
			t.Errorf("%s reported applied on an empty database", m.Name)
		}
	}
	if !st.Behind() || len(st.Pending()) != len(wantMigrations) {
		t.Errorf("Behind = %v, pending = %d, want behind with %d pending", st.Behind(), len(st.Pending()), len(wantMigrations))
	}

	behind, err := store.SchemaBehind(t.Context(), url)
	if err != nil || !behind {
		t.Errorf("SchemaBehind = %v, %v, want true, nil", behind, err)
	}
	// Reading the status must not migrate anything.
	if tables := appTables(t, openPlain(t, url)); len(tables) != 0 {
		t.Errorf("status created tables: %v", tables)
	}
}

func TestMigrateUpAppliesEverything(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)

	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	st := mustStatus(t, url)
	if st.Behind() || st.Current != latestVersion || st.Latest != latestVersion {
		t.Errorf("after up: Behind = %v, Current = %d, Latest = %d", st.Behind(), st.Current, st.Latest)
	}
	for _, m := range st.Migrations {
		if !m.Applied || m.AppliedAt.IsZero() {
			t.Errorf("%s not reported applied", m.Name)
		}
	}
	behind, err := store.SchemaBehind(t.Context(), url)
	if err != nil || behind {
		t.Errorf("SchemaBehind = %v, %v, want false, nil", behind, err)
	}

	conn := openPlain(t, url)
	got := appTables(t, conn)
	want := []string{"auth_tokens", "exercises", "progress", "progress_exercises", "progress_sets",
		"users", "workout_plan_exercises", "workout_plans"}
	if len(got) != len(want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tables = %v, want %v", got, want)
			break
		}
	}
	if !hasTrgm(t, conn) {
		t.Error("pg_trgm extension not installed")
	}
}

func TestMigrateUpIsIdempotent(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)

	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("first MigrateUp: %v", err)
	}
	first := mustStatus(t, url)

	// A second and third run change nothing, in particular no applied_at.
	for range 2 {
		if err := store.MigrateUp(t.Context(), url); err != nil {
			t.Fatalf("repeat MigrateUp: %v", err)
		}
	}
	again := mustStatus(t, url)
	for i := range first.Migrations {
		if !first.Migrations[i].AppliedAt.Equal(again.Migrations[i].AppliedAt) {
			t.Errorf("%s was re-applied", first.Migrations[i].Name)
		}
	}
	var rows int
	conn := openPlain(t, url)
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM goose_db_version WHERE version_id > 0`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != latestVersion {
		t.Errorf("goose_db_version has %d applied rows, want %d", rows, latestVersion)
	}
}

func TestMigrateUpRunsOnTheTemplateDatabase(t *testing.T) {
	// NewDB clones the migrated template: it must already be current, and a
	// further MigrateUp must be a no-op.
	db := testutil.NewDB(t)
	url := db.Pool().Config().ConnString()

	if behind, err := store.SchemaBehind(t.Context(), url); err != nil || behind {
		t.Fatalf("SchemaBehind = %v, %v, want false, nil", behind, err)
	}
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("MigrateUp on a migrated database: %v", err)
	}
}

func TestMigrateDownAndUpAgain(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	conn := openPlain(t, url)

	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	// One step at a time.
	if err := store.MigrateDown(t.Context(), url); err != nil {
		t.Fatalf("MigrateDown: %v", err)
	}
	st := mustStatus(t, url)
	if st.Current != latestVersion-1 || !st.Behind() {
		t.Errorf("after one down: Current = %d, Behind = %v", st.Current, st.Behind())
	}
	if pending := st.Pending(); len(pending) != 1 || pending[0].Name != "0005_progress.sql" {
		t.Errorf("pending = %+v, want only 0005", pending)
	}
	for _, gone := range []string{"progress", "progress_exercises", "progress_sets"} {
		for _, table := range appTables(t, conn) {
			if table == gone {
				t.Errorf("table %s survived down of 0005", gone)
			}
		}
	}

	// Everything.
	if err := store.MigrateDownTo(t.Context(), url, 0); err != nil {
		t.Fatalf("MigrateDownTo(0): %v", err)
	}
	if tables := appTables(t, conn); len(tables) != 0 {
		t.Errorf("tables left after full down: %v", tables)
	}
	if hasTrgm(t, conn) {
		t.Error("pg_trgm left after full down")
	}
	if st := mustStatus(t, url); st.Current != 0 || len(st.Pending()) != len(wantMigrations) {
		t.Errorf("after full down: Current = %d, pending = %d", st.Current, len(st.Pending()))
	}

	// And back up: the schema is fully recreated.
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("MigrateUp after down: %v", err)
	}
	if st := mustStatus(t, url); st.Behind() || st.Current != latestVersion {
		t.Errorf("after re-up: Current = %d, Behind = %v", st.Current, st.Behind())
	}
	if tables := appTables(t, conn); len(tables) != 8 {
		t.Errorf("tables after re-up: %v", tables)
	}
	if !hasTrgm(t, conn) {
		t.Error("pg_trgm missing after re-up")
	}
}

func TestMigrateDownToPartialThenUp(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateDownTo(t.Context(), url, 3); err != nil {
		t.Fatalf("MigrateDownTo(3): %v", err)
	}
	st := mustStatus(t, url)
	if st.Current != 3 || len(st.Pending()) != 2 {
		t.Errorf("Current = %d, pending = %d, want 3 and 2", st.Current, len(st.Pending()))
	}
	if behind, _ := store.SchemaBehind(t.Context(), url); !behind {
		t.Error("SchemaBehind = false with two migrations rolled back")
	}
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if behind, _ := store.SchemaBehind(t.Context(), url); behind {
		t.Error("SchemaBehind = true after MigrateUp")
	}
}

func TestMigrateUpConcurrently(t *testing.T) {
	// Several deploys or test binaries may migrate one fresh database at once.
	// The advisory lock must serialise them and every call must succeed.
	url := testutil.NewEmptyDatabase(t)

	const workers = 5
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			errs <- store.MigrateUp(ctx, url)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent MigrateUp: %v", err)
		}
	}
	if st := mustStatus(t, url); st.Behind() || st.Current != latestVersion {
		t.Errorf("after concurrent up: Current = %d, Behind = %v", st.Current, st.Behind())
	}
}

func TestMigrateErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	const unreachable = "postgres://user:pw@127.0.0.1:1/db?sslmode=disable"
	if err := store.MigrateUp(ctx, unreachable); err == nil {
		t.Error("MigrateUp on an unreachable server returned nil")
	}
	if _, err := store.MigrateStatus(ctx, unreachable); err == nil {
		t.Error("MigrateStatus on an unreachable server returned nil")
	}
	if behind, err := store.SchemaBehind(ctx, unreachable); err == nil || behind {
		t.Errorf("SchemaBehind = %v, %v, want an error (serve must not start on an unknown schema)", behind, err)
	}
	if err := store.MigrateUp(ctx, "not a url \x00"); err == nil {
		t.Error("MigrateUp with a garbage url returned nil")
	}
}
