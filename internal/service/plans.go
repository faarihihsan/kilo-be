package service

import "workout-tracker-be/internal/store"

// Plans implements the workout plan operations: list, get, save (idempotent
// upsert with the conflict rule) and delete (endpoints 6, 7, 10, 17).
//
// Stub from T0.9: T3 fills in the methods and rewrites this file. Keep the
// type name and the NewPlans signature.
type Plans struct {
	plans *store.Plans
}

// NewPlans builds the workout plans service.
func NewPlans(d Deps) *Plans {
	return &Plans{plans: store.NewPlans(d.DB)}
}
