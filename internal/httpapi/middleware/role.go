package middleware

import (
	"net/http"
	"slices"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

// RequireRoles lets a request through only when the authenticated caller has
// one of the given roles. No principal in the context is a 401, a principal
// with another role a 403. With no roles given nobody passes (fail closed).
//
// It must run after the authenticator, which is what puts the principal in the
// context.
func RequireRoles(roles ...domain.Role) func(http.Handler) http.Handler {
	allowed := slices.Clone(roles)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				render.WriteError(w, r, domain.NewUnauthorized())
				return
			}
			if !slices.Contains(allowed, p.Role) {
				render.WriteError(w, r, domain.NewForbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
