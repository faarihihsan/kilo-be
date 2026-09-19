package service

import "workout-tracker-be/internal/store"

// Admin implements the admin-only operations: register a user, revoke a
// user's logins, change a user's password (endpoints 1, 13, 14).
//
// Stub from T0.9: T5 fills in the methods and rewrites this file. Keep the
// type name and the NewAdmin signature.
type Admin struct {
	users  *store.Users
	tokens *store.AuthTokens
}

// NewAdmin builds the admin service.
func NewAdmin(d Deps) *Admin {
	return &Admin{users: store.NewUsers(d.DB), tokens: store.NewAuthTokens(d.DB)}
}
