package service

import "workout-tracker-be/internal/store"

// Exercises implements list, create, update and delete of exercises
// (endpoints 8, 9, 18, 19).
//
// Stub from T0.9: T2 fills in the methods and rewrites this file. Keep the
// type name and the NewExercises signature.
type Exercises struct {
	exercises *store.Exercises
}

// NewExercises builds the exercises service.
func NewExercises(d Deps) *Exercises {
	return &Exercises{exercises: store.NewExercises(d.DB)}
}
