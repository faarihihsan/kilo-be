package store

// Progress is the data access for progress, progress_exercises and progress_sets.
//
// Stub from T0.9: T4 adds the methods and rewrites this
// file. Keep the type name and the constructor signature.
type Progress struct{ db *DB }

// NewProgress returns the Progress store on db.
func NewProgress(db *DB) *Progress { return &Progress{db: db} }
