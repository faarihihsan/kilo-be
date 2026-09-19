package handlers

import (
	"net/http"
	"net/url"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Exercises serves the exercise catalogue: endpoints 8, 9, 18 and 19. The
// router only lets users (not admins) reach these handlers.
type Exercises struct {
	svc  *service.Exercises
	deps Deps
}

// NewExercises builds the Exercises handlers.
func NewExercises(svc *service.Exercises, d Deps) *Exercises { return &Exercises{svc: svc, deps: d} }

// List serves GET /v1/exercises (endpoint 8, httpapi.Handlers.ListExercises).
//
// A bad query parameter is reported as the first problem found, in this order:
// updated_since (400), limit and include_deleted (422), then what the service
// checks: q and the enum filters (422) and the cursor (400). A filter that is
// present but empty counts as a value, and is invalid for the enums.
func (h *Exercises) List(w http.ResponseWriter, r *http.Request) {
	since, err := render.ParseTimeParam(r, "updated_since")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	page, err := render.ParsePage(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	includeDeleted, err := render.ParseBool(r, "include_deleted", false)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}

	q := r.URL.Query()
	result, err := h.svc.List(r.Context(), service.ExerciseListParams{
		Query:              q.Get("q"),
		Category:           exerciseQueryValue(q, "category"),
		PrimaryMuscleGroup: exerciseQueryValue(q, "primary_muscle_group"),
		Equipment:          exerciseQueryValue(q, "equipment"),
		Limit:              page.Limit,
		Cursor:             page.Cursor,
		UpdatedSince:       since,
		IncludeDeleted:     includeDeleted,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, result)
}

// exerciseQueryValue returns the value of a query parameter, or nil when it was
// not sent.
func exerciseQueryValue(q url.Values, name string) *string {
	if !q.Has(name) {
		return nil
	}
	v := q.Get(name)
	return &v
}

// Create serves POST /v1/exercises (endpoint 9, httpapi.Handlers.CreateExercise):
// 201 with the new exercise, or 200 with the existing one when the same id
// was already created with identical content.
func (h *Exercises) Create(w http.ResponseWriter, r *http.Request) {
	caller := middleware.MustPrincipal(r.Context())
	var in domain.ExerciseCreateInput
	if err := render.DecodeJSON(w, r, &in); err != nil {
		render.WriteError(w, r, err)
		return
	}
	e, created, err := h.svc.Create(r.Context(), caller.UserID, in)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	render.WriteJSON(w, status, e)
}

// Update serves PUT /v1/exercises/{id} (endpoint 18, httpapi.Handlers.UpdateExercise).
func (h *Exercises) Update(w http.ResponseWriter, r *http.Request) {
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var in domain.ExerciseInput
	if err := render.DecodeJSON(w, r, &in); err != nil {
		render.WriteError(w, r, err)
		return
	}
	e, err := h.svc.Update(r.Context(), id, in)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, e)
}

// Delete serves DELETE /v1/exercises/{id} (endpoint 19, httpapi.Handlers.DeleteExercise).
func (h *Exercises) Delete(w http.ResponseWriter, r *http.Request) {
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
