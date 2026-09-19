package service

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
)

// Auth implements login, logout, token listing and revocation (endpoints 2,
// 11, 12, 15). Login decides the role from the user row; role enforcement
// itself happens in the router before these methods run.
type Auth struct {
	users   *store.Users
	tokens  *store.AuthTokens
	clock   clock.Clock
	hasher  *auth.Hasher
	limiter *auth.LoginLimiter
}

// NewAuth builds the auth service.
func NewAuth(d Deps) *Auth {
	return &Auth{
		users:   store.NewUsers(d.DB),
		tokens:  store.NewAuthTokens(d.DB),
		clock:   d.Clock,
		hasher:  d.Hasher,
		limiter: d.Limiter,
	}
}

// LoginRequest is the body of POST /v1/auth/login (spec 02). DeviceName is
// optional; the JSON null and an absent field are both nil.
type LoginRequest struct {
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	DeviceName *string `json:"device_name"`
}

// UserRef is the user summary embedded in the login response.
type UserRef struct {
	ID       uuid.UUID   `json:"id"`
	Username string      `json:"username"`
	Role     domain.Role `json:"role"`
}

// LoginResponse is the 200 body of POST /v1/auth/login (spec 02).
type LoginResponse struct {
	TokenID     uuid.UUID `json:"token_id"`
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        UserRef   `json:"user"`
}

// TokenItem is one entry of the list-tokens response (spec 15).
type TokenItem struct {
	ID         uuid.UUID  `json:"id"`
	DeviceName *string    `json:"device_name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Current    bool       `json:"current"`
}

// TokenListResponse is the 200 body of GET /v1/auth/tokens (spec 15).
type TokenListResponse struct {
	Items []TokenItem `json:"items"`
}

// RevokeRequest is the body of POST /v1/auth/revoke (spec 12). Exactly one of
// TokenID and Scope must be set.
type RevokeRequest struct {
	TokenID *string `json:"token_id"`
	Scope   *string `json:"scope"`
}

// RevokeResponse is the 200 body of the revoke endpoints (specs 12, 13).
type RevokeResponse struct {
	RevokedCount int64 `json:"revoked_count"`
}

// Scope values of RevokeRequest (spec 12).
const (
	ScopeOthers = "others"
	ScopeAll    = "all"
)

// validate checks the presence rules of a login request: a non-empty username
// (after normalization) and password, and a device_name within the limit.
func (r LoginRequest) validate() error {
	var v domain.ValidationError
	if domain.NormalizeUsername(r.Username) == "" {
		v.Add("username", domain.IssueRequired)
	}
	if r.Password == "" {
		v.Add("password", domain.IssueRequired)
	}
	if r.DeviceName != nil && utf8.RuneCountInString(*r.DeviceName) > domain.DeviceNameMaxLen {
		v.Add("device_name", domain.IssueTooLong)
	}
	return v.Err()
}

// Login verifies credentials and issues a new opaque token (spec 02).
//
// The brute-force limiter is consulted before any verification: while the
// username or the client IP is locked, the answer is 429 even for a correct
// password, and no failure is recorded. An unknown username is verified
// against the hasher's dummy hash so it costs the same as a wrong password,
// and is counted as a failure like one. On success the username's failure
// counter is cleared.
//
// Errors: Validation (422), RateLimited (429), Unauthorized (401), or the
// hasher's own 429/context error.
func (s *Auth) Login(ctx context.Context, ip string, req LoginRequest) (LoginResponse, error) {
	if err := req.validate(); err != nil {
		return LoginResponse{}, err
	}
	username := domain.NormalizeUsername(req.Username)

	if retryAfter, blocked := s.limiter.Check(username, ip); blocked {
		return LoginResponse{}, domain.NewRateLimited(retryAfter)
	}

	user, err := s.users.GetByUsername(ctx, username)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// Same work as a wrong password, so timing does not reveal whether
		// the username exists (spec 02, timing-safe verification).
		if dummyErr := s.hasher.VerifyDummy(ctx, req.Password); dummyErr != nil {
			return LoginResponse{}, dummyErr
		}
		s.limiter.RecordFailure(username, ip)
		return LoginResponse{}, domain.NewUnauthorized()
	case err != nil:
		return LoginResponse{}, err
	}

	ok, err := s.hasher.Verify(ctx, req.Password, user.PasswordHash)
	if err != nil {
		return LoginResponse{}, err
	}
	if !ok {
		s.limiter.RecordFailure(username, ip)
		return LoginResponse{}, domain.NewUnauthorized()
	}
	s.limiter.RecordSuccess(username)

	return s.issueToken(ctx, user, req.DeviceName)
}

// issueToken creates and stores a new token for user and builds the response.
func (s *Auth) issueToken(ctx context.Context, user store.User, deviceName *string) (LoginResponse, error) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		return LoginResponse{}, err
	}
	now := s.clock.Now()
	info, err := s.tokens.Create(ctx, store.TokenCreate{
		UserID:     user.ID,
		Hash:       hash,
		DeviceName: deviceName,
		CreatedAt:  now,
		ExpiresAt:  now.Add(domain.DefaultTokenTTL),
	})
	if err != nil {
		return LoginResponse{}, err
	}
	return LoginResponse{
		TokenID:     info.ID,
		AccessToken: raw,
		TokenType:   "Bearer",
		ExpiresAt:   info.ExpiresAt,
		User:        UserRef{ID: user.ID, Username: user.Username, Role: user.Role},
	}, nil
}

// Logout revokes the token that authenticated the request (spec 11). A token
// that is unknown or owned by another user is NotFound (the authenticator
// normally rejects a revoked token first).
func (s *Auth) Logout(ctx context.Context, userID, tokenID uuid.UUID) error {
	_, err := s.tokens.RevokeByID(ctx, userID, tokenID, s.clock.Now())
	return err
}

// Revoke revokes one token by id or every other / all active tokens (spec 12).
// It requires exactly one of TokenID and Scope; the current token may be named
// by id and is the one excluded by scope "others".
//
// Errors: Validation (422 exactly-one/bad scope/bad id), NotFound (unknown or
// foreign token_id).
func (s *Auth) Revoke(ctx context.Context, userID, currentTokenID uuid.UUID, req RevokeRequest) (RevokeResponse, error) {
	hasID, hasScope := req.TokenID != nil, req.Scope != nil
	if hasID == hasScope {
		return RevokeResponse{}, revokeExactlyOneError(hasID)
	}

	now := s.clock.Now()
	if hasScope {
		switch *req.Scope {
		case ScopeOthers:
			n, err := s.tokens.RevokeOthers(ctx, userID, currentTokenID, now)
			if err != nil {
				return RevokeResponse{}, err
			}
			return RevokeResponse{RevokedCount: n}, nil
		case ScopeAll:
			n, err := s.tokens.RevokeAllForUser(ctx, userID, now)
			if err != nil {
				return RevokeResponse{}, err
			}
			return RevokeResponse{RevokedCount: n}, nil
		default:
			return RevokeResponse{}, domain.NewValidation("scope", domain.IssueInvalidValue)
		}
	}

	id, err := uuid.Parse(*req.TokenID)
	if err != nil {
		return RevokeResponse{}, domain.NewValidation("token_id", domain.IssueInvalidFormat)
	}
	n, err := s.tokens.RevokeByID(ctx, userID, id, now)
	if err != nil {
		return RevokeResponse{}, err
	}
	return RevokeResponse{RevokedCount: n}, nil
}

// revokeExactlyOneError reports the both / neither case of spec 12: exactly one
// of token_id and scope is required.
func revokeExactlyOneError(both bool) error {
	ve := &domain.ValidationError{Message: "Provide exactly one of token_id or scope."}
	if both {
		ve.Add("token_id", domain.IssueInvalidValue)
		ve.Add("scope", domain.IssueInvalidValue)
	} else {
		ve.Add("token_id", domain.IssueRequired)
		ve.Add("scope", domain.IssueRequired)
	}
	return ve
}

// ListTokens returns the caller's active tokens, newest first, marking the one
// that authenticated the request (spec 15). Secrets are never included.
func (s *Auth) ListTokens(ctx context.Context, userID, currentTokenID uuid.UUID) (TokenListResponse, error) {
	infos, err := s.tokens.ListByUser(ctx, userID, s.clock.Now())
	if err != nil {
		return TokenListResponse{}, err
	}
	items := make([]TokenItem, len(infos))
	for i, info := range infos {
		items[i] = TokenItem{
			ID:         info.ID,
			DeviceName: info.DeviceName,
			CreatedAt:  info.CreatedAt,
			LastUsedAt: info.LastUsedAt,
			ExpiresAt:  info.ExpiresAt,
			Current:    info.ID == currentTokenID,
		}
	}
	return TokenListResponse{Items: items}, nil
}
