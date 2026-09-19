package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func databaseExists(t *testing.T, name string) bool {
	t.Helper()
	conn, err := connectAdmin(t.Context(), AdminURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(t.Context())
	var exists bool
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestSweepStaleDropsOnlyOldTestDatabases checks the cleanup of databases that a
// crashed test binary leaked: old ones go, recent ones (a parallel run) and
// unrelated names stay.
func TestSweepStaleDropsOnlyOldTestDatabases(t *testing.T) {
	admin := AdminURL()
	conn, err := connectAdmin(t.Context(), admin)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Closed by a cleanup registered before the drops below, so it runs after them.
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	suffix := randHex(3)
	old := fmt.Sprintf("wt_test_%d_%s", time.Now().Add(-3*time.Hour).Unix(), suffix)
	recent := fmt.Sprintf("wt_test_%d_%s", time.Now().Unix(), suffix)
	unrelated := "wt_other_" + suffix
	for _, name := range []string{old, recent, unrelated} {
		if _, err := conn.Exec(t.Context(), "CREATE DATABASE "+quoteIdent(name)+" TEMPLATE template0"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() {
			// t.Context() is already cancelled when cleanups run.
			_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
		})
	}

	sweepStale(t.Context(), admin)

	if databaseExists(t, old) {
		t.Error("database older than the cutoff was not dropped")
	}
	if !databaseExists(t, recent) {
		t.Error("a recent test database was dropped")
	}
	if !databaseExists(t, unrelated) {
		t.Error("a database that is not a test database was dropped")
	}
}
