package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
)

// Admin implements the admin-only operations: register a user, revoke a
// user's logins, change a user's password (endpoints 1, 13, 14), plus the two
// operations of the admin CLI (create-user, reset-password). The router
// enforces the admin role before the HTTP methods run.
type Admin struct {
	users  *store.Users
	tokens *store.AuthTokens
	db     *store.DB
	clock  clock.Clock
	hasher *auth.Hasher
}

// NewAdmin builds the admin service.
func NewAdmin(d Deps) *Admin {
	return &Admin{
		users:  store.NewUsers(d.DB),
		tokens: store.NewAuthTokens(d.DB),
		db:     d.DB,
		clock:  d.Clock,
		hasher: d.Hasher,
	}
}

// RegisterRequest is the body of POST /v1/auth/register (spec 01).
type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateUserRequest is the input of the admin CLI create-user command (spec 01
// "Admin bootstrap", implementation plan section 7). It may set any role and
// bypasses the reserved-name list.
type CreateUserRequest struct {
	Username string
	Password string
	Role     domain.Role
}

// UserResponse is the 201 body of POST /v1/auth/register (spec 01).
type UserResponse struct {
	ID        uuid.UUID   `json:"id"`
	Username  string      `json:"username"`
	Role      domain.Role `json:"role"`
	CreatedAt time.Time   `json:"created_at"`
}

// ChangePasswordRequest is the body of PUT /v1/admin/users/{username}/password
// (spec 14).
type ChangePasswordRequest struct {
	Password string `json:"password"`
}

// Register creates a new user with role user (spec 01). The username is
// normalized and validated, the reserved list applies, and a taken username is
// the store's 409 conflict.
//
// Errors: Validation (422), Conflict (409 already_taken), or the hasher's own
// 429/context error.
func (s *Admin) Register(ctx context.Context, req RegisterRequest) (UserResponse, error) {
	username := domain.NormalizeUsername(req.Username)
	if err := validateCredentials(username, req.Password, true); err != nil {
		return UserResponse{}, err
	}
	return s.createUser(ctx, username, req.Password, domain.RoleUser)
}

// CreateUser creates a user for the admin CLI: any valid role, reserved names
// allowed (they are not checked). The username still has to match the format.
//
// Errors: Validation (422), Conflict (409 already_taken), or the hasher's own
// 429/context error.
func (s *Admin) CreateUser(ctx context.Context, req CreateUserRequest) (UserResponse, error) {
	username := domain.NormalizeUsername(req.Username)
	if !req.Role.IsValid() {
		return UserResponse{}, domain.NewValidation("role", domain.IssueInvalidValue)
	}
	if err := validateCredentials(username, req.Password, false); err != nil {
		return UserResponse{}, err
	}
	return s.createUser(ctx, username, req.Password, req.Role)
}

// createUser hashes the password and inserts the user, returning the response.
func (s *Admin) createUser(ctx context.Context, username, password string, role domain.Role) (UserResponse, error) {
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return UserResponse{}, err
	}
	user, err := s.users.Create(ctx, store.UserCreate{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		CreatedAt:    s.clock.Now(),
	})
	if err != nil {
		return UserResponse{}, err
	}
	return UserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Role:      user.Role,
		CreatedAt: user.CreatedAt,
	}, nil
}

// RevokeLogin force-logs-out a user by revoking every active token (spec 13).
// The target is addressed by username; an unknown one is NotFound. Revoking
// the caller's own username is allowed.
func (s *Admin) RevokeLogin(ctx context.Context, username string) (RevokeResponse, error) {
	user, err := s.users.GetByUsername(ctx, domain.NormalizeUsername(username))
	if err != nil {
		return RevokeResponse{}, err
	}
	n, err := s.tokens.RevokeAllForUser(ctx, user.ID, s.clock.Now())
	if err != nil {
		return RevokeResponse{}, err
	}
	return RevokeResponse{RevokedCount: n}, nil
}

// ChangePassword sets a new password for the target user and revokes their
// tokens in one transaction (spec 14). When the admin changes their own
// password, the caller's token is kept and only the other tokens are revoked.
//
// Errors: Validation (422), NotFound (404 unknown username).
func (s *Admin) ChangePassword(ctx context.Context, callerID, callerTokenID uuid.UUID, username, password string) error {
	if err := domain.ValidatePassword(password); err != nil {
		return err
	}
	user, err := s.users.GetByUsername(ctx, domain.NormalizeUsername(username))
	if err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	return s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.users.Using(tx).UpdatePassword(ctx, user.ID, hash, now); err != nil {
			return err
		}
		_, err := revokeAfterPasswordChange(ctx, s.tokens.Using(tx), user.ID, callerID, callerTokenID, now)
		return err
	})
}

// ResetPassword is the admin CLI emergency path: it sets a new password and
// revokes every token of the user in one transaction (implementation plan
// section 7).
//
// Errors: Validation (422), NotFound (404 unknown username).
func (s *Admin) ResetPassword(ctx context.Context, username, password string) error {
	if err := domain.ValidatePassword(password); err != nil {
		return err
	}
	user, err := s.users.GetByUsername(ctx, domain.NormalizeUsername(username))
	if err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	return s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.users.Using(tx).UpdatePassword(ctx, user.ID, hash, now); err != nil {
			return err
		}
		_, err := s.tokens.Using(tx).RevokeAllForUser(ctx, user.ID, now)
		return err
	})
}

// revokeAfterPasswordChange applies the spec-14 exception: changing one's own
// password keeps the current token and revokes the others; otherwise every
// active token of the target is revoked.
func revokeAfterPasswordChange(ctx context.Context, tokens *store.AuthTokens, targetID, callerID, callerTokenID uuid.UUID, now time.Time) (int64, error) {
	if targetID == callerID {
		return tokens.RevokeOthers(ctx, targetID, callerTokenID, now)
	}
	return tokens.RevokeAllForUser(ctx, targetID, now)
}

// validateCredentials validates a username and password together, reporting
// every issue at once. Reserved names are checked only when checkReserved is
// set (the HTTP register endpoint; the CLI bypasses the list).
func validateCredentials(username, password string, checkReserved bool) error {
	var v domain.ValidationError
	if err := domain.ValidateUsername(username); err != nil {
		appendValidation(&v, err)
	} else if checkReserved && domain.IsReservedUsername(username) {
		v.Add("username", domain.IssueReserved)
	}
	if err := domain.ValidatePassword(password); err != nil {
		appendValidation(&v, err)
	}
	return v.Err()
}

// appendValidation moves the issues of a *domain.ValidationError into v. Any
// other error is impossible from the credential validators.
func appendValidation(v *domain.ValidationError, err error) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		v.Issues = append(v.Issues, ve.Issues...)
	}
}
