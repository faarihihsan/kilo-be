package middleware

import (
	"context"
	"sync/atomic"

	"workout-tracker-be/internal/domain"
)

type principalKey struct{}

// WithPrincipal returns a context carrying the authenticated caller. The auth
// middleware calls it after a token is accepted; handler tests call it to act
// as a user or admin without any token.
func WithPrincipal(ctx context.Context, p domain.Principal) context.Context {
	// The access log sits outside the authenticator, so it cannot see a
	// context derived here. It leaves a holder in the context; fill it in.
	if h, ok := ctx.Value(callerKey{}).(*callerHolder); ok {
		h.p.Store(&p)
	}
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated caller, or false when the request
// is anonymous.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(domain.Principal)
	return p, ok
}

// MustPrincipal returns the authenticated caller and panics when there is
// none. Handlers of authenticated routes may use it: the router guarantees a
// principal there, so a panic means the route was wired without auth and the
// Recover middleware turns it into a 500.
func MustPrincipal(ctx context.Context) domain.Principal {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		panic("middleware: no principal in request context")
	}
	return p
}

type callerKey struct{}

// callerHolder lets the AccessLog middleware, which runs before
// authentication, learn who the caller turned out to be.
type callerHolder struct {
	p atomic.Pointer[domain.Principal]
}
