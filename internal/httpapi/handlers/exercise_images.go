package handlers

import (
	"net/http"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/service"
)

// ExerciseImages serves the image endpoints 20 and 21. The router only lets
// users (not admins) reach these handlers.
type ExerciseImages struct {
	svc  *service.ExerciseImages
	deps Deps
}

// NewExerciseImages builds the ExerciseImages handlers.
func NewExerciseImages(svc *service.ExerciseImages, d Deps) *ExerciseImages {
	return &ExerciseImages{svc: svc, deps: d}
}

// Set serves PUT /v1/exercises/{id}/image (endpoint 20,
// httpapi.Handlers.SetExerciseImage). The body is the raw image bytes; the
// route caps it at domain.MaxImageBytes, and media.ReadLimited enforces the
// same cap (413) and rejects an empty body (400). The service does the image
// pipeline.
func (h *ExerciseImages) Set(w http.ResponseWriter, r *http.Request) {
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	data, err := media.ReadLimited(r.Body, domain.MaxImageBytes)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	e, err := h.svc.Set(r.Context(), id, r.Header.Get("Content-Type"), data)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, e)
}

// Delete serves DELETE /v1/exercises/{id}/image (endpoint 21,
// httpapi.Handlers.DeleteExerciseImage). It is idempotent: 204 when there was
// no image or the exercise was already soft-deleted, 404 when it never existed.
func (h *ExerciseImages) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.NoContent(w)
}
