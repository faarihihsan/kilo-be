package main

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/testutil"
)

// This file is the T7.1 full-stack role matrix: anonymous / user / admin
// against every route in httpapi.Routes(), driven by the real router, the real
// authenticator and real opaque tokens stored in Postgres. Unlike
// TestRoleMatrixAcrossAllRoutes in wire_test.go, which substitutes fakeAuth for
// the authenticator, these tests exercise the whole token path.

// rolesE2EIdentity is one caller of the matrix: anonymous (empty role, no
// token) or a role carrying a real, seeded bearer token.
type rolesE2EIdentity struct {
	name  string
	role  domain.Role
	token string
}

// rolesE2EProtected returns the routes that go through the authenticator and
// the role check (everything but /healthz and login).
func rolesE2EProtected() []httpapi.Route {
	var out []httpapi.Route
	for _, r := range httpapi.Routes() {
		if !r.Anonymous {
			out = append(out, r)
		}
	}
	return out
}

// TestE2ERolesMatrix runs anonymous, user and admin callers against every
// route. It asserts the access matrix from docs/api/conventions.md: anonymous
// callers are challenged with 401 on protected routes, the wrong role gets 403,
// and the right role reaches the handler.
func TestE2ERolesMatrix(t *testing.T) {
	a, db := newTestApp(t)
	a.router.RateLimit = nil // the matrix fires many requests from one address
	h := a.Handler()

	userID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin)

	routes := httpapi.Routes()
	if len(routes) == 0 {
		t.Fatal("httpapi.Routes() is empty")
	}

	for _, r := range routes {
		t.Run(r.Name, func(t *testing.T) {
			// Fresh tokens per route: Logout revokes the caller's token, so a
			// token shared across routes would poison the requests after it.
			userToken, _ := testutil.SeedToken(t, db, userID)
			adminToken, _ := testutil.SeedToken(t, db, adminID)
			identities := []rolesE2EIdentity{
				{name: "anonymous"},
				{name: "user", role: domain.RoleUser, token: userToken},
				{name: "admin", role: domain.RoleAdmin, token: adminToken},
			}

			for _, id := range identities {
				rec := authWiringDo(h, r.Method, pathFor(r.Path), id.token)
				switch {
				case r.Name == "Healthz":
					if rec.Code != http.StatusOK {
						t.Errorf("%s: status = %d, want 200; body: %s", id.name, rec.Code, rec.Body)
					}
				case r.Anonymous: // login: no token required, empty body is invalid
					requireReachedHandler(t, id.name, rec.Code)
					if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
						t.Errorf("%s: %s status = %d, want 400 or 422 (empty login body)", id.name, r.Name, rec.Code)
					}
					if got := rec.Header().Get("WWW-Authenticate"); got != "" {
						t.Errorf("%s: anonymous route sent WWW-Authenticate: %q", id.name, got)
					}
				case id.role == "":
					apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
					if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
						t.Errorf("%s: WWW-Authenticate = %q, want Bearer", id.name, got)
					}
				case slices.Contains(r.Roles, id.role):
					requireReachedHandler(t, id.name, rec.Code)
				default:
					apitest.RequireError(t, rec, http.StatusForbidden, "forbidden")
				}
			}
		})
	}
}

// TestE2ERolesExpiredAndRevokedTokens asserts that expired and revoked tokens
// are rejected with 401 and the bearer challenge on every protected route. The
// tokens are seeded once; the matrix only reads them.
func TestE2ERolesExpiredAndRevokedTokens(t *testing.T) {
	a, db := newTestApp(t)
	a.router.RateLimit = nil
	h := a.Handler()

	userID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin)
	expiredUser, _ := testutil.SeedToken(t, db, userID, testutil.WithTokenExpired())
	revokedUser, _ := testutil.SeedToken(t, db, userID, testutil.WithTokenRevoked(time.Now().Add(-time.Hour)))
	expiredAdmin, _ := testutil.SeedToken(t, db, adminID, testutil.WithTokenExpired())
	revokedAdmin, _ := testutil.SeedToken(t, db, adminID, testutil.WithTokenRevoked(time.Now().Add(-time.Hour)))

	bad := []rolesE2EIdentity{
		{name: "expired-user", role: domain.RoleUser, token: expiredUser},
		{name: "revoked-user", role: domain.RoleUser, token: revokedUser},
		{name: "expired-admin", role: domain.RoleAdmin, token: expiredAdmin},
		{name: "revoked-admin", role: domain.RoleAdmin, token: revokedAdmin},
	}

	protected := rolesE2EProtected()
	if len(protected) == 0 {
		t.Fatal("no protected routes to check")
	}
	for _, r := range protected {
		t.Run(r.Name, func(t *testing.T) {
			for _, id := range bad {
				rec := authWiringDo(h, r.Method, pathFor(r.Path), id.token)
				apitest.RequireError(t, rec, http.StatusUnauthorized, "unauthorized")
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("%s: WWW-Authenticate = %q, want Bearer", id.name, got)
				}
			}
		})
	}
}
