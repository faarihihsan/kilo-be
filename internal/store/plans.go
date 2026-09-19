package store

// Plans is the data access for workout_plans and workout_plan_exercises.
//
// Stub from T0.9: T3 adds the methods and rewrites this
// file. Keep the type name and the constructor signature.
type Plans struct{ db *DB }

// NewPlans returns the Plans store on db.
func NewPlans(db *DB) *Plans { return &Plans{db: db} }
