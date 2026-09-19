package store

// Exercises is the data access for the exercises table.
//
// Stub from T0.9: T2 adds the methods and rewrites this
// file. Keep the type name and the constructor signature.
type Exercises struct{ db *DB }

// NewExercises returns the Exercises store on db.
func NewExercises(db *DB) *Exercises { return &Exercises{db: db} }
