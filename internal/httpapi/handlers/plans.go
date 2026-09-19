package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Plans serves workout plans.
//
// Stub from T0.9: T3 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewPlans signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type Plans struct {
	svc  *service.Plans
	deps Deps
}

// NewPlans builds the Plans handlers.
func NewPlans(svc *service.Plans, d Deps) *Plans { return &Plans{svc: svc, deps: d} }

// List serves GET /v1/workout-plans (endpoint 6, httpapi.Handlers.ListWorkoutPlans).
func (h *Plans) List(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Get serves GET /v1/workout-plans/{id} (endpoint 7, httpapi.Handlers.GetWorkoutPlan).
func (h *Plans) Get(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Save serves PUT /v1/workout-plans/{id} (endpoint 10, httpapi.Handlers.SaveWorkoutPlan).
func (h *Plans) Save(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Delete serves DELETE /v1/workout-plans/{id} (endpoint 17, httpapi.Handlers.DeleteWorkoutPlan).
func (h *Plans) Delete(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
