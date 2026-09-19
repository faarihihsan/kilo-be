// Package store is the data-access layer: hand-written pgx SQL, one file per
// table group. This file holds the shared plumbing: the connection pool, the
// Querier abstraction that lets store methods run inside or outside a
// transaction, WithTx, and helpers to recognise constraint violations.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the subset of pgx used by store methods. It is satisfied by
// *pgxpool.Pool, pgx.Tx and *DB, so one method body runs inside a transaction
// (WithTx) or straight on the pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	// SendBatch lets a method insert many child rows in one round trip.
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = pgx.Tx(nil)
	_ Querier = (*DB)(nil)
)

// DB wraps the pgx connection pool. It is safe for concurrent use.
type DB struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL at url, limits the pool to maxConns connections
// (maxConns <= 0 keeps the pgx default) and verifies the server is reachable.
// Sessions run in UTC. The url is never included in returned errors.
func Open(ctx context.Context, url string, maxConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx redacts the password in this error.
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["timezone"]; !ok {
		cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	db := &DB{pool: pool}
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// Close releases every connection. Call it once, after all queries finished.
func (db *DB) Close() { db.pool.Close() }

// Ping checks that the database answers. It backs /healthz.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Pool exposes the underlying pool for code that needs pgx directly (the
// migration runner, tests). Prefer Querier in store methods.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Exec, Query, QueryRow and SendBatch run on the pool, so a *DB is a Querier.

func (db *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return db.pool.Exec(ctx, sql, args...)
}

func (db *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return db.pool.Query(ctx, sql, args...)
}

func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return db.pool.QueryRow(ctx, sql, args...)
}

func (db *DB) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return db.pool.SendBatch(ctx, b)
}

// rollbackTimeout bounds the rollback of a transaction whose context is
// already cancelled, so cleanup cannot hang.
const rollbackTimeout = 5 * time.Second

// WithTx runs fn in a READ COMMITTED transaction. It commits when fn returns
// nil. It rolls back when fn returns an error (returned unchanged, so callers
// can still use errors.Is/As and the violation helpers), when fn panics (the
// panic keeps propagating) and when fn ends its goroutine with runtime.Goexit,
// as t.FailNow does. The rollback also happens when ctx was cancelled.
//
// fn must use tx (or a store method taking a Querier with tx) for all its
// queries. Do not call WithTx from inside fn: transactions do not nest.
func (db *DB) WithTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return db.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// WithTxOptions is WithTx with explicit options, for example a read-only
// transaction (pgx.TxOptions{AccessMode: pgx.ReadOnly}) or SERIALIZABLE.
func (db *DB) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := db.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		// Reached on error, panic and Goexit. A failed rollback (dead
		// connection) is not actionable: the server discards the transaction
		// when the session ends, and pgx drops a broken connection.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		// Keep %w: a deferred constraint can fail here and callers may want
		// to inspect it.
		return fmt.Errorf("store: commit tx: %w", err)
	}
	committed = true
	return nil
}

// SQLSTATE codes of the constraint violations the application handles.
const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
)

// IsUniqueViolation reports whether err is (or wraps) a unique violation and
// returns the name of the violated constraint or unique index, for example
// "exercises_name_lower_uniq". Match on the name to tell violations apart.
func IsUniqueViolation(err error) (constraint string, ok bool) {
	return isViolation(err, pgUniqueViolation)
}

// IsForeignKeyViolation reports whether err is (or wraps) a foreign key
// violation, either inserting an unknown reference or deleting a referenced
// row, and returns the constraint name, for example "auth_tokens_user_id_fkey".
func IsForeignKeyViolation(err error) (constraint string, ok bool) {
	return isViolation(err, pgForeignKeyViolation)
}

// IsCheckViolation reports whether err is (or wraps) a CHECK violation and
// returns the constraint name, for example "users_role_chk".
func IsCheckViolation(err error) (constraint string, ok bool) {
	return isViolation(err, pgCheckViolation)
}

func isViolation(err error, code string) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == code {
		return pgErr.ConstraintName, true
	}
	return "", false
}
