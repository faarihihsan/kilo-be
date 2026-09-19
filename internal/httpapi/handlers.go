package httpapi

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
)

// Handlers holds one handler per route, named after the operation. The
// handler task (T0.9 and the resource tasks) builds the real ones and passes
// them to NewRouter; a nil field is served by NotImplementedHandlers. The
// router never imports the handler package, so there is no import cycle.
type Handlers struct {
	// 1-2: auth
	Register http.HandlerFunc // POST /v1/auth/register
	Login    http.HandlerFunc // POST /v1/auth/login

	// 3-5: progress
	SaveProgress http.HandlerFunc // PUT /v1/progress/{id}
	GetProgress  http.HandlerFunc // GET /v1/progress/{id}
	ListProgress http.HandlerFunc // GET /v1/progress

	// 6-7: workout plans
	ListWorkoutPlans http.HandlerFunc // GET /v1/workout-plans
	GetWorkoutPlan   http.HandlerFunc // GET /v1/workout-plans/{id}

	// 8-9: exercises
	ListExercises  http.HandlerFunc // GET /v1/exercises
	CreateExercise http.HandlerFunc // POST /v1/exercises

	// 10: workout plans
	SaveWorkoutPlan http.HandlerFunc // PUT /v1/workout-plans/{id}

	// 11-15: session and admin
	Logout              http.HandlerFunc // POST /v1/auth/logout
	RevokeTokens        http.HandlerFunc // POST /v1/auth/revoke
	AdminRevokeLogin    http.HandlerFunc // POST /v1/admin/users/{username}/revoke-login
	AdminChangePassword http.HandlerFunc // PUT /v1/admin/users/{username}/password
	ListTokens          http.HandlerFunc // GET /v1/auth/tokens

	// 16-17: deletes
	DeleteProgress    http.HandlerFunc // DELETE /v1/progress/{id}
	DeleteWorkoutPlan http.HandlerFunc // DELETE /v1/workout-plans/{id}

	// 18-21: exercises
	UpdateExercise      http.HandlerFunc // PUT /v1/exercises/{id}
	DeleteExercise      http.HandlerFunc // DELETE /v1/exercises/{id}
	SetExerciseImage    http.HandlerFunc // PUT /v1/exercises/{id}/image
	DeleteExerciseImage http.HandlerFunc // DELETE /v1/exercises/{id}/image

	// Not one of the 21 endpoints: liveness, see HealthHandler.
	Healthz http.HandlerFunc // GET /healthz
}

// NotImplementedHandlers returns Handlers where every route answers 501 with
// the JSON error code `not_implemented`.
func NotImplementedHandlers() Handlers {
	var h Handlers
	h.fillDefaults()
	return h
}

// fillDefaults replaces every nil handler with the 501 stub. The route table
// lists each field once, so no field can be forgotten.
func (h *Handlers) fillDefaults() {
	for _, rt := range table {
		if f := rt.field(h); *f == nil {
			*f = render.NotImplemented
		}
	}
}
