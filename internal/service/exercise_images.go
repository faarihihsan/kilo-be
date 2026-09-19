package service

import (
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
)

// ExerciseImages implements setting and removing an exercise image (endpoints
// 20, 21).
//
// Stub from T0.9: T6 fills in the methods and rewrites this file. Keep the
// type name and the NewExerciseImages signature.
type ExerciseImages struct {
	exercises *store.Exercises
	files     *media.Store
}

// NewExerciseImages builds the exercise image service. It uses the exercises
// store directly, not the Exercises service.
func NewExerciseImages(d Deps) *ExerciseImages {
	return &ExerciseImages{exercises: store.NewExercises(d.DB), files: d.Media}
}
