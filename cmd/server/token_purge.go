package main

import (
	"context"
	"log/slog"
	"time"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/store"
)

// tokenPurgeInterval is how often the purge job runs (docs/api/endpoints/02-
// login.md: "a daily in-process job deletes token rows expired or revoked more
// than 30 days ago").
const tokenPurgeInterval = 24 * time.Hour

// runTokenPurge deletes expired or revoked token rows once at start and then
// every interval, until ctx is cancelled. It is started by startBackground and
// returns promptly when ctx ends.
func runTokenPurge(ctx context.Context, deps service.Deps, interval time.Duration) {
	purgeExpiredTokens(ctx, deps)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			purgeExpiredTokens(ctx, deps)
		}
	}
}

// purgeExpiredTokens deletes the tokens expired or revoked more than
// domain.TokenPurgeAfter before the clock's now.
func purgeExpiredTokens(ctx context.Context, deps service.Deps) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cutoff := deps.Clock.Now().Add(-domain.TokenPurgeAfter)
	n, err := store.NewAuthTokens(deps.DB).PurgeExpired(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			logger.ErrorContext(ctx, "token purge failed", slog.Any("error", err))
		}
		return
	}
	if n > 0 {
		logger.InfoContext(ctx, "purged expired tokens", slog.Int64("count", n))
	}
}
