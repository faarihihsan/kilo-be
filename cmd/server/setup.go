package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/store"
)

// startupTimeout bounds connecting to the database and checking its schema,
// so a wrong host fails within seconds instead of hanging the deploy.
const startupTimeout = 30 * time.Second

// newLogger returns the JSON logger of the process (docs/implementation-plan.md
// section 1). serve logs to stdout, journald and Docker collect it; subcommands
// log to stderr and keep stdout for their output.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// openDB opens the connection pool for cfg and checks that the schema is
// current. It refuses a database that lacks migrations embedded in this
// binary: every query assumes the tables exist, and a half-migrated schema
// must not serve traffic. The operator fixes it with `server migrate up`.
//
// It is the database step of `serve`; subcommands that work on data (admin,
// media gc) call it too, after config.Load and newLogger. The caller closes
// the returned DB.
func openDB(ctx context.Context, cfg *config.Config) (*store.DB, error) {
	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	db, err := store.Open(ctx, cfg.DatabaseURL, int32(cfg.DBMaxConns))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	st, err := store.MigrateStatus(ctx, cfg.DatabaseURL)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("check database schema: %w", err)
	}
	if st.Behind() {
		db.Close()
		return nil, fmt.Errorf("database schema is behind this binary (at version %d, needs %d, %d migration(s) pending): run `server migrate up` first",
			st.Current, st.Latest, len(st.Pending()))
	}
	return db, nil
}
