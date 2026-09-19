// Package domain holds the types every layer shares: enums, limits, the
// authenticated Principal and the typed errors that the HTTP layer translates
// to the JSON error format in docs/api/conventions.md.
//
// It imports nothing from this module. Resource-specific rules (exercise,
// plan, progress) are added by their own tasks in domain/<resource>.go.
package domain

import "github.com/google/uuid"

// Principal is the authenticated caller, attached to the request context by
// the auth middleware.
type Principal struct {
	UserID uuid.UUID
	Role   Role
	// TokenID is the id of the auth token that authenticated this request.
	// Logout, list-tokens (`current`) and revoke (`scope: others`) need it.
	// It is uuid.Nil where no token is involved (tests, CLI).
	TokenID uuid.UUID
}
