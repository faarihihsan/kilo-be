// Package testutil is the shared test harness: a throwaway migrated PostgreSQL
// database per test, raw-SQL seed helpers and a truncate helper. Tests use a
// real PostgreSQL (docs/implementation-plan.md section 9), started with
// `make db-up` (docker-compose.test.yml, localhost:55432).
//
// Configuration, all optional:
//
//	TEST_DATABASE_URL  admin connection, a postgres:// URL of a superuser on a
//	                   server where creating databases is allowed. Default:
//	                   postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable
//	TEST_SKIP_DB=1     skip (instead of fail) database tests when the server is
//	                   unreachable, for machines without Docker.
//
// How it stays fast and safe with many test binaries and agents on one server:
// the migrated schema is built once into a template database whose name embeds
// a hash of the embedded migration files (built under a PostgreSQL advisory
// lock, so concurrent processes do not race), and every NewDB clones it with
// CREATE DATABASE ... TEMPLATE under a random name. Nothing is shared between
// tests.
//
// This package imports internal/store, so a test in package store must be an
// external test (package store_test) to use it.
package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"workout-tracker-be/internal/store"
	"workout-tracker-be/migrations"
)

const (
	// DefaultDatabaseURL is the admin URL of the docker-compose.test.yml server.
	DefaultDatabaseURL = "postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable"

	envDatabaseURL = "TEST_DATABASE_URL"
	envSkipDB      = "TEST_SKIP_DB"

	// testMaxConns bounds each test database's pool. Pools open connections
	// lazily, so a test that runs one query at a time uses one.
	testMaxConns = 8

	// harnessVersion is part of the template name. Bump it when the way the
	// template is built changes, so old templates are not reused.
	harnessVersion = "1"

	// templateLockKey is the advisory lock that serialises template builders.
	// goose uses a different key for its own migration lock.
	templateLockKey int64 = 0x7774_5f74_706c_0001

	// staleAfter is how old a leaked test database must be before a later run
	// removes it. It is far above the longest test run.
	staleAfter = 2 * time.Hour

	sqlstateObjectInUse = "55006"
)

// AdminURL returns the connection URL of the server that hosts the throwaway
// databases: TEST_DATABASE_URL, or DefaultDatabaseURL when unset.
func AdminURL() string {
	if u := os.Getenv(envDatabaseURL); u != "" {
		return u
	}
	return DefaultDatabaseURL
}

// NewDB creates a throwaway database from the migrated template and returns a
// store.DB connected to it. The database is dropped, and the pool closed, when
// the test ends. Safe to call from parallel tests and parallel test binaries.
//
// When the server is unreachable the test fails with instructions, or is
// skipped if TEST_SKIP_DB=1.
func NewDB(t testing.TB) *store.DB {
	t.Helper()
	dbURL := createDatabase(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dbURL, testMaxConns)
	if err != nil {
		t.Fatalf("testutil: open test database: %v", err)
	}
	t.Cleanup(db.Close) // runs before the drop registered by createDatabase
	return db
}

// NewEmptyDatabase creates a throwaway database with NO migrations applied and
// returns its URL, for tests of the migration runner itself. It is dropped when
// the test ends.
func NewEmptyDatabase(t testing.TB) string {
	t.Helper()
	return createDatabase(t, true)
}

// createDatabase creates a uniquely named database, cloned from the migrated
// template (or empty), registers its removal and returns its URL.
func createDatabase(t testing.TB, empty bool) string {
	t.Helper()
	admin := AdminURL()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	source := "template0"
	if !empty {
		tpl, err := templateFor(admin)
		if err != nil {
			failOrSkip(t, admin, err)
		}
		source = tpl
	}

	name := fmt.Sprintf("wt_test_%d_%s", time.Now().Unix(), randHex(4))
	conn, err := connectAdmin(ctx, admin)
	if err != nil {
		failOrSkip(t, admin, err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	stmt := "CREATE DATABASE " + quoteIdent(name) + " TEMPLATE " + quoteIdent(source)
	// A concurrent clone or the tail of a template build can briefly hold the
	// source ("being accessed by other users"). Retry.
	for attempt := 1; ; attempt++ {
		_, err = conn.Exec(ctx, stmt)
		if err == nil || !hasSQLState(err, sqlstateObjectInUse) || attempt >= 40 {
			break
		}
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("testutil: create database from %s: %v", source, err)
	}

	t.Cleanup(func() { dropDatabase(t, admin, name) })
	return urlForDatabase(t, admin, name)
}

// dropDatabase removes a test database, best effort.
func dropDatabase(t testing.TB, admin, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := connectAdmin(ctx, admin)
	if err != nil {
		t.Logf("testutil: cannot drop database %s: %v", name, err)
		return
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)"); err != nil {
		t.Logf("testutil: cannot drop database %s: %v", name, err)
	}
}

// --- template ---------------------------------------------------------------

type templateEntry struct {
	once sync.Once
	name string
	err  error
}

var (
	templatesMu sync.Mutex
	templates   = map[string]*templateEntry{} // by admin URL
)

// templateFor returns the name of the migrated template database on the server
// at admin, building it once per process (and once per server across processes).
func templateFor(admin string) (string, error) {
	templatesMu.Lock()
	e := templates[admin]
	if e == nil {
		e = &templateEntry{}
		templates[admin] = e
	}
	templatesMu.Unlock()

	e.once.Do(func() {
		// Not the calling test's context: the result is shared by every
		// test in the process, and must not die with the first one.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		e.name, e.err = ensureTemplate(ctx, admin)
		if e.err == nil {
			sweepStale(ctx, admin)
		}
	})
	return e.name, e.err
}

// ensureTemplate returns the template database for the current migrations,
// creating it if this is the first process to need it.
func ensureTemplate(ctx context.Context, admin string) (string, error) {
	hash, err := migrationsHash()
	if err != nil {
		return "", err
	}
	name := "wt_tpl_" + hash[:16]

	conn, err := connectAdmin(ctx, admin)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.WithoutCancel(ctx))

	// One builder at a time. Everyone else waits here, then finds the finished
	// template. Advisory locks are per database, and every process connects to
	// the same admin database. The lock dies with the connection.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		return "", fmt.Errorf("testutil: lock template build: %w", err)
	}

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("testutil: look up template: %w", err)
	}
	if exists {
		return name, nil
	}

	// Under the lock, any half-built template is left over from a crashed run.
	dropMatching(ctx, conn, `wt\_tpl\_%\_build\_%`)

	// Build under a temporary name and rename when complete, so nobody can
	// ever clone a half-migrated template.
	building := name + "_build_" + randHex(4)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoteIdent(building)+" TEMPLATE template0"); err != nil {
		return "", fmt.Errorf("testutil: create template database: %w", err)
	}
	buildURL, err := databaseURL(admin, building)
	if err != nil {
		return "", err
	}
	if err := store.MigrateUp(ctx, buildURL); err != nil {
		return "", fmt.Errorf("testutil: migrate template database: %w", err)
	}
	// The renamed database must have no sessions. The migration connections
	// are closed, but the server may still be tearing the backends down.
	rename := "ALTER DATABASE " + quoteIdent(building) + " RENAME TO " + quoteIdent(name)
	for attempt := 1; ; attempt++ {
		_, err = conn.Exec(ctx, rename)
		if err == nil || !hasSQLState(err, sqlstateObjectInUse) || attempt >= 40 {
			break
		}
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	if err != nil {
		return "", fmt.Errorf("testutil: publish template database: %w", err)
	}
	return name, nil
}

// migrationsHash fingerprints the embedded migration files (names and
// contents) plus the harness version.
func migrationsHash() (string, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, harnessVersion+"\x00")
	err := fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, path+"\x00")
		_, _ = h.Write(b)
		_, _ = io.WriteString(h, "\x00")
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("testutil: hash migrations: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sweepStale drops test databases that an earlier run leaked (a crashed or
// killed test binary never reaches its cleanup). Best effort.
func sweepStale(ctx context.Context, admin string) {
	conn, err := connectAdmin(ctx, admin)
	if err != nil {
		return
	}
	defer conn.Close(context.WithoutCancel(ctx))

	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE 'wt\_test\_%'`)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleAfter).Unix()
	for _, name := range names {
		// wt_test_<unix seconds>_<hex>
		parts := strings.Split(name, "_")
		if len(parts) != 4 {
			continue
		}
		if created, err := strconv.ParseInt(parts[2], 10, 64); err == nil && created < cutoff {
			_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
		}
	}
}

// dropMatching drops every database whose name matches the LIKE pattern.
func dropMatching(ctx context.Context, conn *pgx.Conn, pattern string) {
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE datname LIKE $1", pattern)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, name := range names {
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
	}
}

// --- connections and errors -------------------------------------------------

// unreachableError means the admin connection could not be established.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string { return e.err.Error() }
func (e *unreachableError) Unwrap() error { return e.err }

// connectAdmin opens one connection to the admin database.
func connectAdmin(ctx context.Context, admin string) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return nil, &unreachableError{err}
	}
	return conn, nil
}

// failOrSkip ends the test for an unusable server: a skip when the server is
// unreachable and TEST_SKIP_DB=1, a failure with instructions otherwise.
func failOrSkip(t testing.TB, admin string, err error) {
	t.Helper()
	var unreachable *unreachableError
	if errors.As(err, &unreachable) {
		if os.Getenv(envSkipDB) == "1" {
			t.Skipf("testutil: test database unreachable, skipping because %s=1", envSkipDB)
		}
		t.Fatalf("testutil: cannot connect to the test database at %s: %v\n"+
			"Start it with `make db-up` (docker compose -f docker-compose.test.yml up -d --wait),\n"+
			"or set %s to another PostgreSQL server, or set %s=1 to skip database tests.",
			redact(admin), unreachable.err, envDatabaseURL, envSkipDB)
	}
	t.Fatalf("testutil: prepare test database: %v", err)
}

// redact hides the password of a connection URL for messages.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparsable url>"
	}
	return u.Redacted()
}

// databaseURL returns admin with its database name replaced.
func databaseURL(admin, dbname string) (string, error) {
	u, err := url.Parse(admin)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("testutil: %s must be a postgres:// URL", envDatabaseURL)
	}
	u.Path = "/" + dbname
	return u.String(), nil
}

func urlForDatabase(t testing.TB, admin, dbname string) string {
	t.Helper()
	u, err := databaseURL(admin, dbname)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func hasSQLState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}
