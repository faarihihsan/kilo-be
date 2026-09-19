package store

// Users is the data access for the users table.
//
// Stub from T0.9: T1 adds the methods and rewrites this
// file. Keep the type name and the constructor signature.
type Users struct{ db *DB }

// NewUsers returns the Users store on db.
func NewUsers(db *DB) *Users { return &Users{db: db} }
