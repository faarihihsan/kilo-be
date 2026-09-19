package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Exercises serves the exercise catalogue.
//
// Stub from T0.9: T2 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewExercises signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type Exercises struct {
	svc  *service.Exercises
	deps Deps
}

// NewExercises builds the Exercises handlers.
func NewExercises(svc *service.Exercises, d Deps) *Exercises { return &Exercises{svc: svc, deps: d} }

// List serves GET /v1/exercises (endpoint 8, httpapi.Handlers.ListExercises).
func (h *Exercises) List(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Create serves POST /v1/exercises (endpoint 9, httpapi.Handlers.CreateExercise).
func (h *Exercises) Create(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Update serves PUT /v1/exercises/{id} (endpoint 18, httpapi.Handlers.UpdateExercise).
func (h *Exercises) Update(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Delete serves DELETE /v1/exercises/{id} (endpoint 19, httpapi.Handlers.DeleteExercise).
func (h *Exercises) Delete(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
