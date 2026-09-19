package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"workout-tracker-be/migrations"
)

// The schema is defined by the embedded goose SQL files in /migrations, applied
// in numeric order and recorded in the goose_db_version table. Production is
// forward-only: `server migrate up` (or MigrateUp) runs before `serve`, which
// refuses to start while SchemaBehind reports true. The Down sections exist for
// local development only.
//
// These functions take a database URL, not a *DB, because they run before the
// application pool exists and open their own short-lived connection.

// MigrationState is one embedded migration and whether the database has it.
type MigrationState struct {
	Version   int64
	Name      string // file name, for example 0003_exercises.sql
	Applied   bool
	AppliedAt time.Time // zero unless Applied
}

// Status compares the migrations embedded in this binary with a database.
type Status struct {
	// Current is the highest version applied in the database, 0 for none. It
	// can exceed Latest when the database was migrated by a newer binary.
	Current int64
	// Latest is the highest version embedded in this binary.
	Latest int64
	// Migrations lists every embedded migration in version order.
	Migrations []MigrationState
}

// Pending returns the embedded migrations the database does not have yet.
func (s Status) Pending() []MigrationState {
	var pending []MigrationState
	for _, m := range s.Migrations {
		if !m.Applied {
			pending = append(pending, m)
		}
	}
	return pending
}

// Behind reports whether the database lacks at least one embedded migration.
func (s Status) Behind() bool { return len(s.Pending()) > 0 }

// MigrateUp applies every pending migration and logs each one through
// slog.Default. It is safe to run concurrently from several processes (a
// PostgreSQL advisory lock serialises them) and does nothing when the schema
// is current.
func MigrateUp(ctx context.Context, url string) error {
	p, err := newProvider(url, true)
	if err != nil {
		return err
	}
	defer p.Close()

	results, err := p.Up(ctx)
	logResults(ctx, results)
	if err != nil {
		return fmt.Errorf("store: migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recently applied migration. Local
// development only: production migrations are forward-only.
func MigrateDown(ctx context.Context, url string) error {
	p, err := newProvider(url, true)
	if err != nil {
		return err
	}
	defer p.Close()

	res, err := p.Down(ctx)
	if res != nil {
		logResults(ctx, []*goose.MigrationResult{res})
	}
	if err != nil {
		return fmt.Errorf("store: migrate down: %w", err)
	}
	return nil
}

// MigrateDownTo rolls back applied migrations until version is the highest
// applied one; 0 rolls back everything. Local development only.
func MigrateDownTo(ctx context.Context, url string, version int64) error {
	p, err := newProvider(url, true)
	if err != nil {
		return err
	}
	defer p.Close()

	results, err := p.DownTo(ctx, version)
	logResults(ctx, results)
	if err != nil {
		return fmt.Errorf("store: migrate down to %d: %w", version, err)
	}
	return nil
}

// MigrateStatus reports which embedded migrations the database has. It creates
// goose's bookkeeping table if the database never saw a migration, and changes
// nothing else.
func MigrateStatus(ctx context.Context, url string) (Status, error) {
	// No advisory lock: a read must not wait for a running migration.
	p, err := newProvider(url, false)
	if err != nil {
		return Status{}, err
	}
	defer p.Close()

	rows, err := p.Status(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("store: migrate status: %w", err)
	}
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("store: migrate status: %w", err)
	}

	st := Status{Current: current, Migrations: make([]MigrationState, 0, len(rows))}
	for _, r := range rows {
		m := MigrationState{
			Version:   r.Source.Version,
			Name:      r.Source.Path,
			Applied:   r.State == goose.StateApplied,
			AppliedAt: r.AppliedAt,
		}
		st.Migrations = append(st.Migrations, m)
		st.Latest = max(st.Latest, m.Version)
	}
	return st, nil
}

// SchemaBehind reports whether the database lacks migrations embedded in this
// binary. `serve` calls it at startup and refuses to run when it is true,
// telling the operator to run `migrate up`.
func SchemaBehind(ctx context.Context, url string) (bool, error) {
	st, err := MigrateStatus(ctx, url)
	if err != nil {
		return false, err
	}
	return st.Behind(), nil
}

// newProvider builds a goose provider over the embedded migrations and a
// database/sql handle backed by pgx. Closing the provider closes the handle.
func newProvider(url string, withLock bool) (*goose.Provider, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	sqlDB := stdlib.OpenDB(*cfg)

	opts := []goose.ProviderOption{
		// goose would log through the standard logger; we log results ourselves.
		goose.WithSlog(slog.New(slog.DiscardHandler)),
		goose.WithDisableGlobalRegistry(true),
	}
	if withLock {
		// One process migrates at a time. Retry every second for two minutes.
		locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 120))
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("store: migration lock: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}

	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, opts...)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: migration provider: %w", err)
	}
	return p, nil
}

func logResults(ctx context.Context, results []*goose.MigrationResult) {
	for _, r := range results {
		if r == nil || r.Source == nil {
			continue
		}
		slog.LogAttrs(ctx, slog.LevelInfo, "migration "+r.Direction,
			slog.Int64("version", r.Source.Version),
			slog.String("file", r.Source.Path),
			slog.Duration("duration", r.Duration),
		)
	}
}
