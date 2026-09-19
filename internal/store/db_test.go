package store_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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

// insertUser inserts a user through any Querier, so the same helper runs
// against the pool and inside a transaction.
func insertUser(ctx context.Context, q store.Querier, username string) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	_, err := q.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, role) VALUES ($1, $2, 'x', 'user')`, id, username)
	return id, err
}

func userExists(t *testing.T, q store.Querier, username string) bool {
	t.Helper()
	var exists bool
	err := q.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1)`, username).Scan(&exists)
	if err != nil {
		t.Fatalf("userExists: %v", err)
	}
	return exists
}

// noLeakedConns fails the test when a connection is still checked out, which
// is what a transaction left open would look like.
func noLeakedConns(t *testing.T, db *store.DB) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for db.Pool().Stat().AcquiredConns() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d connection(s) still acquired: a transaction was left open", db.Pool().Stat().AcquiredConns())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpen(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)

	t.Run("applies max conns and pings", func(t *testing.T) {
		db, err := store.Open(t.Context(), url, 3)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer db.Close()
		if got := db.Pool().Config().MaxConns; got != 3 {
			t.Errorf("MaxConns = %d, want 3", got)
		}
		if err := db.Ping(t.Context()); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})

	t.Run("zero max conns keeps the pgx default", func(t *testing.T) {
		db, err := store.Open(t.Context(), url, 0)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer db.Close()
		if got := db.Pool().Config().MaxConns; got < 1 {
			t.Errorf("MaxConns = %d, want the pgx default (>= 1)", got)
		}
	})

	t.Run("sessions run in UTC", func(t *testing.T) {
		db, err := store.Open(t.Context(), url, 1)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer db.Close()
		var tz string
		if err := db.QueryRow(t.Context(), "SHOW timezone").Scan(&tz); err != nil {
			t.Fatal(err)
		}
		if tz != "UTC" {
			t.Errorf("timezone = %q, want UTC", tz)
		}
	})

	t.Run("ping fails after close", func(t *testing.T) {
		db, err := store.Open(t.Context(), url, 1)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		db.Close()
		if err := db.Ping(t.Context()); err == nil {
			t.Error("Ping after Close succeeded")
		}
	})

	t.Run("unparsable url", func(t *testing.T) {
		_, err := store.Open(t.Context(), "postgres://user:hunter2@host:notaport/db", 1)
		if err == nil {
			t.Fatal("Open succeeded")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the password: %v", err)
		}
	})

	t.Run("unreachable server", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, err := store.Open(ctx, "postgres://user:hunter2@127.0.0.1:1/db?sslmode=disable", 1)
		if err == nil {
			t.Fatal("Open succeeded")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the password: %v", err)
		}
	})
}

func TestQuerierWorksOnPoolAndTx(t *testing.T) {
	db := testutil.NewDB(t)

	if _, err := insertUser(t.Context(), db, "on_pool"); err != nil {
		t.Fatalf("insert on *DB: %v", err)
	}
	if _, err := insertUser(t.Context(), db.Pool(), "on_raw_pool"); err != nil {
		t.Fatalf("insert on pool: %v", err)
	}
	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		_, err := insertUser(ctx, tx, "on_tx")
		return err
	})
	if err != nil {
		t.Fatalf("insert on tx: %v", err)
	}
	for _, name := range []string{"on_pool", "on_raw_pool", "on_tx"} {
		if !userExists(t, db, name) {
			t.Errorf("user %s missing", name)
		}
	}

	// SendBatch is part of Querier so child rows can be inserted in one round trip.
	var q store.Querier = db
	batch := &pgx.Batch{}
	batch.Queue(`SELECT 1`)
	batch.Queue(`SELECT 2`)
	br := q.SendBatch(t.Context(), batch)
	var a, b int
	if err := br.QueryRow().Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := br.QueryRow().Scan(&b); err != nil {
		t.Fatal(err)
	}
	if err := br.Close(); err != nil || a != 1 || b != 2 {
		t.Errorf("batch = %d, %d, err = %v", a, b, err)
	}
}

func TestWithTxCommits(t *testing.T) {
	db := testutil.NewDB(t)
	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := insertUser(ctx, tx, "commit_a"); err != nil {
			return err
		}
		if userExists(t, db, "commit_a") {
			t.Error("uncommitted row visible from another connection")
		}
		_, err := insertUser(ctx, tx, "commit_b")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if !userExists(t, db, "commit_a") || !userExists(t, db, "commit_b") {
		t.Error("committed rows are missing")
	}
	noLeakedConns(t, db)
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := testutil.NewDB(t)
	sentinel := errors.New("boom")

	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := insertUser(ctx, tx, "rollback_a"); err != nil {
			return err
		}
		return sentinel
	})
	if err != sentinel { //nolint:errorlint // must be returned unchanged, not merely wrapped
		t.Errorf("WithTx returned %v, want the fn error unchanged", err)
	}
	if userExists(t, db, "rollback_a") {
		t.Error("row survived the rollback")
	}
	noLeakedConns(t, db)
}

func TestWithTxIsAtomicAcrossStatements(t *testing.T) {
	db := testutil.NewDB(t)
	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := insertUser(ctx, tx, "dup_name"); err != nil {
			return err
		}
		_, err := insertUser(ctx, tx, "dup_name") // unique violation
		return err
	})
	if c, ok := store.IsUniqueViolation(err); !ok || c != "users_username_uniq" {
		t.Fatalf("WithTx error = %v, want unique violation on users_username_uniq", err)
	}
	if userExists(t, db, "dup_name") {
		t.Error("first insert survived the failed transaction")
	}
	// The connection must still be usable afterwards.
	if _, err := insertUser(t.Context(), db, "after_failure"); err != nil {
		t.Errorf("pool unusable after failed transaction: %v", err)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	db := testutil.NewDB(t)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			if _, err := insertUser(ctx, tx, "panic_a"); err != nil {
				return err
			}
			panic("kaboom")
		})
		t.Error("WithTx returned after a panic")
	}()

	if recovered != "kaboom" {
		t.Errorf("recovered %v, want the original panic value to propagate", recovered)
	}
	if userExists(t, db, "panic_a") {
		t.Error("row survived the panic")
	}
	noLeakedConns(t, db)
}

func TestWithTxRollsBackOnGoexit(t *testing.T) {
	// t.FailNow inside a transaction body ends the goroutine with Goexit.
	db := testutil.NewDB(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
			if _, err := insertUser(ctx, tx, "goexit_a"); err != nil {
				return err
			}
			runtime.Goexit()
			return nil
		})
	}()
	wg.Wait()

	if userExists(t, db, "goexit_a") {
		t.Error("row survived Goexit")
	}
	noLeakedConns(t, db)
}

func TestWithTxRollsBackWhenContextIsCancelled(t *testing.T) {
	db := testutil.NewDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := insertUser(ctx, tx, "cancel_a"); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("WithTx returned %v, want context.Canceled", err)
	}
	if userExists(t, db, "cancel_a") {
		t.Error("row survived the cancelled transaction")
	}
	noLeakedConns(t, db)

	// Cancelled before the transaction starts.
	if err := db.WithTx(ctx, func(context.Context, pgx.Tx) error {
		t.Error("fn ran with a cancelled context")
		return nil
	}); err == nil {
		t.Error("WithTx with a cancelled context returned nil")
	}
}

func TestWithTxIsolationAndOptions(t *testing.T) {
	db := testutil.NewDB(t)
	show := func(ctx context.Context, tx pgx.Tx) string {
		var level string
		if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level); err != nil {
			t.Fatal(err)
		}
		return level
	}

	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if got := show(ctx, tx); got != "read committed" {
			t.Errorf("default isolation = %q, want read committed", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithTxOptions(t.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context, tx pgx.Tx) error {
		if got := show(ctx, tx); got != "serializable" {
			t.Errorf("isolation = %q, want serializable", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithTxOptions(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := insertUser(ctx, tx, "read_only")
		return err
	})
	if err == nil {
		t.Error("write in a read-only transaction succeeded")
	}
}

func TestViolationHelpers(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := t.Context()

	uid, username := testutil.SeedUser(t, db, domain.RoleUser)
	testutil.SeedExercise(t, db, uid, testutil.WithExerciseName("Bench Press"))

	uniqueUser := func() error { _, err := insertUser(ctx, db, username); return err }
	uniqueExercise := func() error {
		_, err := db.Exec(ctx, `
			INSERT INTO exercises (id, name, category, primary_muscle_group, equipment, measurement_type, created_by)
			VALUES ($1, 'bench PRESS', 'strength', 'chest', 'barbell', 'reps_weight', $2)`, uuid.Must(uuid.NewV7()), uid)
		return err
	}
	foreignKey := func() error {
		_, err := db.Exec(ctx, `
			INSERT INTO auth_tokens (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, now())`,
			uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), testutil.HashToken("x"))
		return err
	}
	foreignKeyOnDelete := func() error {
		testutil.SeedToken(t, db, uid)
		_, err := db.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid)
		return err
	}
	check := func() error {
		_, err := db.Exec(ctx, `INSERT INTO users (id, username, password_hash, role) VALUES ($1, 'root_user', 'x', 'root')`,
			uuid.Must(uuid.NewV7()))
		return err
	}

	tests := []struct {
		name       string
		run        func() error
		unique     string
		foreignKey string
		check      string
	}{
		{"unique constraint", uniqueUser, "users_username_uniq", "", ""},
		{"unique partial index", uniqueExercise, "exercises_name_lower_uniq", "", ""},
		{"foreign key on insert", foreignKey, "", "auth_tokens_user_id_fkey", ""},
		{"foreign key on delete", foreignKeyOnDelete, "", "auth_tokens_user_id_fkey", ""},
		{"check", check, "", "", "users_role_chk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("statement succeeded, want a violation")
			}
			// Wrapping must not hide the violation.
			for label, e := range map[string]error{"bare": err, "wrapped": fmt.Errorf("store: insert: %w", err)} {
				checkDetector(t, label+" unique", store.IsUniqueViolation, e, tc.unique)
				checkDetector(t, label+" foreign key", store.IsForeignKeyViolation, e, tc.foreignKey)
				checkDetector(t, label+" check", store.IsCheckViolation, e, tc.check)
			}
		})
	}

	t.Run("other errors are not violations", func(t *testing.T) {
		for _, err := range []error{nil, errors.New("plain"), pgx.ErrNoRows, context.Canceled, fmt.Errorf("wrap: %w", pgx.ErrNoRows)} {
			if c, ok := store.IsUniqueViolation(err); ok || c != "" {
				t.Errorf("IsUniqueViolation(%v) = %q, %v", err, c, ok)
			}
			if c, ok := store.IsForeignKeyViolation(err); ok || c != "" {
				t.Errorf("IsForeignKeyViolation(%v) = %q, %v", err, c, ok)
			}
			if c, ok := store.IsCheckViolation(err); ok || c != "" {
				t.Errorf("IsCheckViolation(%v) = %q, %v", err, c, ok)
			}
		}
	})
}

// checkDetector asserts a violation helper fires exactly when want != "" and
// then names the expected constraint.
func checkDetector(t *testing.T, label string, detect func(error) (string, bool), err error, want string) {
	t.Helper()
	got, ok := detect(err)
	if want == "" {
		if ok || got != "" {
			t.Errorf("%s: detected %q, want no match", label, got)
		}
		return
	}
	if !ok || got != want {
		t.Errorf("%s: got (%q, %v), want (%q, true)", label, got, ok, want)
	}
}
