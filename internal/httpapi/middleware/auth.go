package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/store"
)

// AuthTokenStore is what the authenticator needs from the token table.
// *store.AuthTokens satisfies it; tests may substitute their own. (The
// interface names store's record type, so this file is the one place the
// middleware package touches store, for a value type only.)
type AuthTokenStore interface {
	// LookupByHash returns the token with this SHA-256 hash, revoked or
	// expired ones included, or an error matching domain.ErrNotFound.
	LookupByHash(ctx context.Context, hash []byte) (store.TokenRecord, error)
	// TouchLastUsed sets last_used_at unless it is younger than an hour.
	TouchLastUsed(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
}

var _ AuthTokenStore = (*store.AuthTokens)(nil)

// NewAuthenticator returns the middleware for the RouterConfig.Authenticator
// slot. For every request it:
//
//  1. reads Authorization: Bearer <token> (auth.ParseBearer); anything that is
//     not a well-formed token is a 401 without touching the database;
//  2. looks the token up by the SHA-256 of the whole string;
//  3. answers 401 when the token is unknown, revoked or expired (a 401 comes
//     with `WWW-Authenticate: Bearer` from the error mapper, and never says
//     which of the three it was);
//  4. puts domain.Principal{UserID, Role, TokenID} in the request context
//     (WithPrincipal), so MustPrincipal works in handlers and the access log
//     learns the user;
//  5. if last_used_at is empty or at least an hour old, records the use. That
//     write is best effort: a failure is logged and the request carries on.
//
// A database error during the lookup is an internal error (500), not a 401:
// an outage must not look like every client being logged out.
//
// Neither the token nor the Authorization header is ever logged or echoed.
// clk is the source of "now" for expiry (clock.Real{} in production). Warnings
// go to the request-scoped logger (render.LoggerFrom), which carries the
// request id.
func NewAuthenticator(tokens AuthTokenStore, clk clock.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			raw, ok := auth.ParseBearer(r.Header.Get("Authorization"))
			if !ok {
				render.WriteError(w, r, domain.NewUnauthorized())
				return
			}
			rec, err := tokens.LookupByHash(ctx, auth.HashToken(raw))
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					render.WriteError(w, r, domain.NewUnauthorized())
					return
				}
				render.WriteError(w, r, err)
				return
			}

			now := clk.Now()
			if rec.RevokedAt != nil || !rec.ExpiresAt.After(now) {
				render.WriteError(w, r, domain.NewUnauthorized())
				return
			}

			if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) >= domain.TokenLastUsedInterval {
				if _, err := tokens.TouchLastUsed(ctx, rec.TokenID, now); err != nil {
					render.LoggerFrom(ctx).WarnContext(ctx, "auth: could not record token use",
						slog.String("token_id", rec.TokenID.String()),
						slog.Any("error", err))
				}
			}

			p := domain.Principal{UserID: rec.UserID, Role: rec.Role, TokenID: rec.TokenID}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
		})
	}
}
