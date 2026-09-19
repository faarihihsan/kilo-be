package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Progress serves workout progress: decode, call the service, encode. All the
// rules (validation, ownership, the conflict rule) live below.
type Progress struct {
	svc  *service.Progress
	deps Deps
}

// NewProgress builds the Progress handlers.
func NewProgress(svc *service.Progress, d Deps) *Progress { return &Progress{svc: svc, deps: d} }

// List serves GET /v1/progress (endpoint 5, httpapi.Handlers.ListProgress).
func (h *Progress) List(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	params, expand, err := progressListParams(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if expand {
		page, err := h.svc.ListExpanded(r.Context(), p.UserID, params)
		if err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.WriteJSON(w, http.StatusOK, page)
		return
	}
	page, err := h.svc.List(r.Context(), p.UserID, params)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, page)
}

// Get serves GET /v1/progress/{id} (endpoint 4, httpapi.Handlers.GetProgress).
func (h *Progress) Get(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	session, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, session)
}

// Save serves PUT /v1/progress/{id} (endpoint 3, httpapi.Handlers.SaveProgress):
// 201 when the session is new, 200 when it was updated or the save was a no-op.
func (h *Progress) Save(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req domain.ProgressSaveRequest
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	session, created, err := h.svc.Save(r.Context(), p.UserID, id, &req)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	render.WriteJSON(w, status, session)
}

// Delete serves DELETE /v1/progress/{id} (endpoint 16, httpapi.Handlers.DeleteProgress).
func (h *Progress) Delete(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), p.UserID, id); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.NoContent(w)
}

// progressListParams reads the query of endpoint 5 (spec 05): a bad cursor,
// timestamp or workout_plan_id is a 400 (the cursor is decoded by the service),
// a limit out of range, a bad include_deleted or a bad expand a 422.
func progressListParams(r *http.Request) (params domain.ProgressListParams, expand bool, err error) {
	page, err := render.ParsePage(r)
	if err != nil {
		return params, false, err
	}
	params.Limit, params.Cursor = page.Limit, page.Cursor

	if params.UpdatedSince, err = render.ParseTimeParam(r, "updated_since"); err != nil {
		return params, false, err
	}
	if params.From, err = render.ParseTimeParam(r, "from"); err != nil {
		return params, false, err
	}
	if params.To, err = render.ParseTimeParam(r, "to"); err != nil {
		return params, false, err
	}
	if params.IncludeDeleted, err = render.ParseBool(r, "include_deleted", false); err != nil {
		return params, false, err
	}

	q := r.URL.Query()
	if q.Has("workout_plan_id") {
		s := q.Get("workout_plan_id")
		id, err := uuid.Parse(s)
		if err != nil || len(s) != 36 { // uuid.Parse also takes braces, urn: and bare hex
			return params, false, domain.NewBadRequest("Query parameter \"workout_plan_id\" must be a UUID.")
		}
		params.WorkoutPlanID = &id
	}
	if q.Has("expand") {
		if q.Get("expand") != domain.ExpandExercises {
			return params, false, domain.NewValidation("expand", domain.IssueInvalidValue)
		}
		expand = true
	}
	return params, expand, nil
}
