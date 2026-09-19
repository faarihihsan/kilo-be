package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
)

// User is a row of the users table.
type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Role         domain.Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UserCreate is the input of Users.Create.
type UserCreate struct {
	// Username must already be normalized (domain.NormalizeUsername) and
	// valid; the table's CHECK constraint rejects anything else.
	Username string
	// PasswordHash is the argon2id encoded string (auth.Hasher.Hash).
	PasswordHash string
	Role         domain.Role
	// CreatedAt sets created_at and updated_at (the service passes clock.Now()).
	CreatedAt time.Time
}

// Users is the data access for the users table.
//
// Lookups that find nothing return domain.NewNotFound(). Timestamps are
// returned in UTC. Times written are truncated to microseconds, PostgreSQL's
// precision.
type Users struct{ q Querier }

// NewUsers returns the Users store on db.
func NewUsers(db *DB) *Users { return &Users{q: db} }

// Using returns a Users that runs its queries on q instead of the pool,
// typically the pgx.Tx handed to DB.WithTx, so that a user change and a token
// revocation commit together:
//
//	err := db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
//		if err := users.Using(tx).UpdatePassword(ctx, id, hash, now); err != nil {
//			return err
//		}
//		_, err := tokens.Using(tx).RevokeAllForUser(ctx, id, now)
//		return err
//	})
func (u *Users) Using(q Querier) *Users { return &Users{q: q} }

// Create inserts a user with a new UUID v7 id and returns it. A taken username
// is a *domain.ConflictError with issue already_taken on field username
// (spec 01, 409).
func (u *Users) Create(ctx context.Context, in UserCreate) (User, error) {
	if !in.Role.IsValid() {
		return User{}, fmt.Errorf("store: create user: invalid role %q", in.Role)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return User{}, fmt.Errorf("store: create user: new id: %w", err)
	}
	created := in.CreatedAt.Truncate(time.Microsecond).UTC()

	_, err = u.q.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		id, in.Username, in.PasswordHash, string(in.Role), created)
	if err != nil {
		if name, ok := IsUniqueViolation(err); ok && name == "users_username_uniq" {
			conflict := domain.NewConflict(domain.IssueAlreadyTaken, domain.OnField("username"))
			conflict.Message = "Username already taken"
			return User{}, conflict
		}
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	return User{
		ID:           id,
		Username:     in.Username,
		PasswordHash: in.PasswordHash,
		Role:         in.Role,
		CreatedAt:    created,
		UpdatedAt:    created,
	}, nil
}

const userColumns = `id, username, password_hash, role, created_at, updated_at`

// GetByUsername returns the user with this exact (normalized) username, or
// domain.NewNotFound(). Login must treat not found like a wrong password.
func (u *Users) GetByUsername(ctx context.Context, username string) (User, error) {
	user, err := userScan(u.q.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1`, username))
	if err != nil {
		return User{}, userErr("get user by username", err)
	}
	return user, nil
}

// GetByID returns the user with this id, or domain.NewNotFound().
func (u *Users) GetByID(ctx context.Context, id uuid.UUID) (User, error) {
	user, err := userScan(u.q.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err != nil {
		return User{}, userErr("get user by id", err)
	}
	return user, nil
}

// UpdatePassword replaces the password hash and sets updated_at to now. It
// returns domain.NewNotFound() if the user does not exist. It does not touch
// tokens: revoke them in the same transaction (see Using).
func (u *Users) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, now time.Time) error {
	tag, err := u.q.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1`,
		id, passwordHash, now.Truncate(time.Microsecond).UTC())
	if err != nil {
		return fmt.Errorf("store: update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound()
	}
	return nil
}

func userScan(row pgx.Row) (User, error) {
	var (
		user User
		role string
	)
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &role, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return User{}, err
	}
	user.Role = domain.Role(role)
	user.CreatedAt = user.CreatedAt.UTC()
	user.UpdatedAt = user.UpdatedAt.UTC()
	return user, nil
}

// userErr maps "no rows" to domain.NewNotFound() and wraps anything else.
func userErr(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NewNotFound()
	}
	return fmt.Errorf("store: %s: %w", op, err)
}
