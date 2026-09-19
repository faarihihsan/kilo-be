package main

import (
	"context"
	"testing"
	"time"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

var purgeTestNow = time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)

func TestPurgeExpiredTokens(t *testing.T) {
	db := testutil.NewDB(t)
	clk := clock.NewFake(purgeTestNow)
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)

	testutil.SeedToken(t, db, user, testutil.WithTokenExpiresAt(purgeTestNow.Add(-31*24*time.Hour))) // purged
	testutil.SeedToken(t, db, user, testutil.WithTokenRevoked(purgeTestNow.Add(-31*24*time.Hour)))   // purged
	testutil.SeedToken(t, db, user, testutil.WithTokenExpiresAt(purgeTestNow.Add(-24*time.Hour)))    // kept
	testutil.SeedToken(t, db, user, testutil.WithTokenRevoked(purgeTestNow.Add(-time.Hour)))         // kept
	testutil.SeedToken(t, db, user)                                                                  // active, kept

	purgeExpiredTokens(t.Context(), service.Deps{DB: db, Clock: clk, Logger: discardLogger()})

	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM auth_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("tokens left = %d, want 3", n)
	}
}

func TestStartBackgroundStopsOnCancel(t *testing.T) {
	db := testutil.NewDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	wait := startBackground(ctx, service.Deps{DB: db, Clock: clock.NewFake(purgeTestNow), Logger: discardLogger()})
	cancel()

	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startBackground did not stop after its context was cancelled")
	}
}
