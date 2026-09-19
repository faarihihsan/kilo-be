package service

// Compile-time pin of the seam signatures fixed by T0.9. cmd/server builds
// every service with NewX(Deps); a task that changes one of them breaks this
// file instead of the merge. Deps itself is only extended by T1 (Hasher,
// Limiter), never changed.
var (
	_ func(Deps) *Auth           = NewAuth
	_ func(Deps) *Admin          = NewAdmin
	_ func(Deps) *Exercises      = NewExercises
	_ func(Deps) *ExerciseImages = NewExerciseImages
	_ func(Deps) *Plans          = NewPlans
	_ func(Deps) *Progress       = NewProgress
)
