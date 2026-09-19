package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// ExerciseImages serves exercise images.
//
// Stub from T0.9: T6 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewExerciseImages signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type ExerciseImages struct {
	svc  *service.ExerciseImages
	deps Deps
}

// NewExerciseImages builds the ExerciseImages handlers.
func NewExerciseImages(svc *service.ExerciseImages, d Deps) *ExerciseImages {
	return &ExerciseImages{svc: svc, deps: d}
}

// Set serves PUT /v1/exercises/{id}/image (endpoint 20, httpapi.Handlers.SetExerciseImage).
func (h *ExerciseImages) Set(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Delete serves DELETE /v1/exercises/{id}/image (endpoint 21, httpapi.Handlers.DeleteExerciseImage).
func (h *ExerciseImages) Delete(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
