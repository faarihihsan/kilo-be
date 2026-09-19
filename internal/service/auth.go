package service

import "workout-tracker-be/internal/store"

// Auth implements login, logout, token listing and revocation (endpoints 2,
// 11, 12, 15).
//
// Stub from T0.9: T5 fills in the methods and rewrites this file. Keep the
// type name and the NewAuth signature.
type Auth struct {
	users  *store.Users
	tokens *store.AuthTokens
}

// NewAuth builds the auth service.
func NewAuth(d Deps) *Auth {
	return &Auth{users: store.NewUsers(d.DB), tokens: store.NewAuthTokens(d.DB)}
}
