package store_test

import "workout-tracker-be/internal/store"

// Compile-time pin of the seam signatures fixed by T0.9. Services build their
// stores with NewX(db); a task that changes one of them breaks this file
// instead of the merge.
var (
	_ func(*store.DB) *store.Users      = store.NewUsers
	_ func(*store.DB) *store.AuthTokens = store.NewAuthTokens
	_ func(*store.DB) *store.Exercises  = store.NewExercises
	_ func(*store.DB) *store.Plans      = store.NewPlans
	_ func(*store.DB) *store.Progress   = store.NewProgress
)
