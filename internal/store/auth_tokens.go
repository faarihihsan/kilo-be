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

// TokenRecord is what the auth middleware needs about a token: the token row
// joined with its user's role. LookupByHash returns it whatever the token's
// state; the caller decides from ExpiresAt and RevokedAt.
type TokenRecord struct {
	TokenID    uuid.UUID
	UserID     uuid.UUID
	Role       domain.Role
	DeviceName *string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// TokenInfo is one token as shown to its owner (spec 15, without `current`,
// which the service derives from the caller's TokenID) and as returned by
// Create.
type TokenInfo struct {
	ID         uuid.UUID
	DeviceName *string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  time.Time
}

// TokenCreate is the input of AuthTokens.Create.
type TokenCreate struct {
	UserID uuid.UUID
	// Hash is auth.HashToken of the raw token (32 bytes); the raw token is
	// never stored.
	Hash []byte
	// DeviceName is the optional label of the device; at most
	// domain.DeviceNameMaxLen characters (the table's CHECK enforces it).
	DeviceName *string
	CreatedAt  time.Time
	// ExpiresAt is CreatedAt plus the token TTL.
	ExpiresAt time.Time
}

// AuthTokens is the data access for the auth_tokens table.
//
// Times passed in come from the service's clock (never the database's), so
// expiry and revocation are deterministic under a fake clock; they are
// truncated to microseconds and returned in UTC. "Active" always means not
// revoked and not expired at the given time.
type AuthTokens struct{ q Querier }

// NewAuthTokens returns the AuthTokens store on db.
func NewAuthTokens(db *DB) *AuthTokens { return &AuthTokens{q: db} }

// Using returns an AuthTokens that runs its queries on q instead of the pool,
// typically the pgx.Tx handed to DB.WithTx. See Users.Using for an example.
func (t *AuthTokens) Using(q Querier) *AuthTokens { return &AuthTokens{q: q} }

// tokenMicro is the timestamp precision stored by PostgreSQL.
func tokenMicro(t time.Time) time.Time { return t.Truncate(time.Microsecond).UTC() }

// Create inserts a token with a new UUID v7 id and returns it as stored. The
// user must exist (otherwise the error satisfies IsForeignKeyViolation). A
// duplicate hash (impossible for random tokens) satisfies IsUniqueViolation.
func (t *AuthTokens) Create(ctx context.Context, in TokenCreate) (TokenInfo, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return TokenInfo{}, fmt.Errorf("store: create token: new id: %w", err)
	}
	info := TokenInfo{
		ID:         id,
		DeviceName: in.DeviceName,
		CreatedAt:  tokenMicro(in.CreatedAt),
		ExpiresAt:  tokenMicro(in.ExpiresAt),
	}
	_, err = t.q.Exec(ctx, `
		INSERT INTO auth_tokens (id, user_id, token_hash, device_name, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, in.UserID, in.Hash, in.DeviceName, info.CreatedAt, info.ExpiresAt)
	if err != nil {
		return TokenInfo{}, fmt.Errorf("store: create token: %w", err)
	}
	return info, nil
}

// LookupByHash finds a token by the SHA-256 of the raw token (auth.HashToken)
// and returns it with its user's role, or domain.NewNotFound() (which the
// authenticator answers with 401). It returns revoked and expired tokens too.
func (t *AuthTokens) LookupByHash(ctx context.Context, hash []byte) (TokenRecord, error) {
	var (
		rec  TokenRecord
		role string
	)
	err := t.q.QueryRow(ctx, `
		SELECT t.id, t.user_id, u.role, t.device_name, t.created_at, t.expires_at, t.last_used_at, t.revoked_at
		FROM auth_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1`, hash).
		Scan(&rec.TokenID, &rec.UserID, &role, &rec.DeviceName, &rec.CreatedAt, &rec.ExpiresAt, &rec.LastUsedAt, &rec.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenRecord{}, domain.NewNotFound()
	}
	if err != nil {
		return TokenRecord{}, fmt.Errorf("store: lookup token: %w", err)
	}
	rec.Role = domain.Role(role)
	rec.CreatedAt = rec.CreatedAt.UTC()
	rec.ExpiresAt = rec.ExpiresAt.UTC()
	rec.LastUsedAt = tokenUTC(rec.LastUsedAt)
	rec.RevokedAt = tokenUTC(rec.RevokedAt)
	return rec, nil
}

// TouchLastUsed sets last_used_at to now unless it was already set within
// domain.TokenLastUsedInterval before now, so a busy token causes at most one
// write per hour. It reports whether the row changed.
func (t *AuthTokens) TouchLastUsed(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	now = tokenMicro(now)
	tag, err := t.q.Exec(ctx, `
		UPDATE auth_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $3)`,
		id, now, now.Add(-domain.TokenLastUsedInterval))
	if err != nil {
		return false, fmt.Errorf("store: touch token: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListByUser returns the user's active tokens at now, newest first (spec 15).
func (t *AuthTokens) ListByUser(ctx context.Context, userID uuid.UUID, now time.Time) ([]TokenInfo, error) {
	rows, err := t.q.Query(ctx, `
		SELECT id, device_name, created_at, last_used_at, expires_at
		FROM auth_tokens
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC`,
		userID, tokenMicro(now))
	if err != nil {
		return nil, fmt.Errorf("store: list tokens: %w", err)
	}
	defer rows.Close()

	items := []TokenInfo{}
	for rows.Next() {
		var it TokenInfo
		if err := rows.Scan(&it.ID, &it.DeviceName, &it.CreatedAt, &it.LastUsedAt, &it.ExpiresAt); err != nil {
			return nil, fmt.Errorf("store: list tokens: %w", err)
		}
		it.CreatedAt = it.CreatedAt.UTC()
		it.ExpiresAt = it.ExpiresAt.UTC()
		it.LastUsedAt = tokenUTC(it.LastUsedAt)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tokens: %w", err)
	}
	return items, nil
}

// RevokeByID revokes one active token of the user and returns how many
// changed: 1, or 0 when it was already revoked or expired (spec 12: idempotent,
// revoked_count 0). A token that does not exist or belongs to another user is
// domain.NewNotFound(), so ownership never leaks. Logout is RevokeByID with
// the caller's own token id.
func (t *AuthTokens) RevokeByID(ctx context.Context, userID, tokenID uuid.UUID, now time.Time) (int64, error) {
	var owned, revoked int64
	err := t.q.QueryRow(ctx, `
		WITH owned AS (
			SELECT id FROM auth_tokens WHERE id = $1 AND user_id = $2
		), revoked AS (
			UPDATE auth_tokens SET revoked_at = $3
			WHERE id IN (SELECT id FROM owned) AND revoked_at IS NULL AND expires_at > $3
			RETURNING id
		)
		SELECT (SELECT count(*) FROM owned), (SELECT count(*) FROM revoked)`,
		tokenID, userID, tokenMicro(now)).Scan(&owned, &revoked)
	if err != nil {
		return 0, fmt.Errorf("store: revoke token: %w", err)
	}
	if owned == 0 {
		return 0, domain.NewNotFound()
	}
	return revoked, nil
}

// RevokeOthers revokes every active token of the user except exceptTokenID
// (spec 12 scope "others"; also the own-password case of spec 14) and returns
// how many were revoked.
func (t *AuthTokens) RevokeOthers(ctx context.Context, userID, exceptTokenID uuid.UUID, now time.Time) (int64, error) {
	tag, err := t.q.Exec(ctx, `
		UPDATE auth_tokens SET revoked_at = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL AND expires_at > $3`,
		userID, exceptTokenID, tokenMicro(now))
	if err != nil {
		return 0, fmt.Errorf("store: revoke other tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RevokeAllForUser revokes every active token of the user (spec 12 scope
// "all", specs 13 and 14, the admin CLI) and returns how many were revoked.
// It works on a transaction via Using.
func (t *AuthTokens) RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) (int64, error) {
	tag, err := t.q.Exec(ctx, `
		UPDATE auth_tokens SET revoked_at = $2
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2`,
		userID, tokenMicro(now))
	if err != nil {
		return 0, fmt.Errorf("store: revoke all tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeExpired deletes the tokens that expired or were revoked before cutoff
// and returns how many. The daily job passes
// now.Add(-domain.TokenPurgeAfter) (30 days).
func (t *AuthTokens) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := t.q.Exec(ctx,
		`DELETE FROM auth_tokens WHERE expires_at < $1 OR revoked_at < $1`,
		tokenMicro(cutoff))
	if err != nil {
		return 0, fmt.Errorf("store: purge tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// tokenUTC converts a nullable timestamp scanned from the database to UTC.
func tokenUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
