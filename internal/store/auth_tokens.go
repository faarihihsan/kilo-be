package store

// AuthTokens is the data access for the auth_tokens table.
//
// Stub from T0.9: T1 adds the methods and rewrites this
// file. Keep the type name and the constructor signature.
type AuthTokens struct{ db *DB }

// NewAuthTokens returns the AuthTokens store on db.
func NewAuthTokens(db *DB) *AuthTokens { return &AuthTokens{db: db} }
