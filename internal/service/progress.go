package service

import "workout-tracker-be/internal/store"

// Progress implements the progress operations: list, get, save (idempotent
// upsert with the conflict rule) and delete (endpoints 3, 4, 5, 16).
//
// Stub from T0.9: T4 fills in the methods and rewrites this file. Keep the
// type name and the NewProgress signature.
type Progress struct {
	progress *store.Progress
}

// NewProgress builds the progress service.
func NewProgress(d Deps) *Progress {
	return &Progress{progress: store.NewProgress(d.DB)}
}
