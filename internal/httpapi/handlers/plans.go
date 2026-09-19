package handlers

import (
	"net/http"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Plans serves workout plans: decode, call the service, encode. All the rules
// (validation, ownership, the conflict rule) live below.
type Plans struct {
	svc  *service.Plans
	deps Deps
}

// NewPlans builds the Plans handlers.
func NewPlans(svc *service.Plans, d Deps) *Plans { return &Plans{svc: svc, deps: d} }

// List serves GET /v1/workout-plans (endpoint 6, httpapi.Handlers.ListWorkoutPlans).
func (h *Plans) List(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	params, expand, err := planListParams(r)
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
	page, err := h.svc.ListSummaries(r.Context(), p.UserID, params)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, page)
}

// Get serves GET /v1/workout-plans/{id} (endpoint 7, httpapi.Handlers.GetWorkoutPlan).
func (h *Plans) Get(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	plan, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, plan)
}

// Save serves PUT /v1/workout-plans/{id} (endpoint 10, httpapi.Handlers.SaveWorkoutPlan):
// 201 when the plan is new, 200 when it was updated or the save was a no-op.
func (h *Plans) Save(w http.ResponseWriter, r *http.Request) {
	p := middleware.MustPrincipal(r.Context())
	id, err := render.PathUUID(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req domain.PlanInput
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	plan, created, err := h.svc.Save(r.Context(), p.UserID, id, req)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	render.WriteJSON(w, status, plan)
}

// Delete serves DELETE /v1/workout-plans/{id} (endpoint 17, httpapi.Handlers.DeleteWorkoutPlan).
func (h *Plans) Delete(w http.ResponseWriter, r *http.Request) {
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

// planListParams reads the query of endpoint 6 (spec 06): a bad timestamp is a
// 400 (the cursor is decoded by the service), a limit out of range, a bad
// include_deleted or a bad expand a 422.
func planListParams(r *http.Request) (params domain.PlanListParams, expand bool, err error) {
	page, err := render.ParsePage(r)
	if err != nil {
		return params, false, err
	}
	params.Limit, params.Cursor = page.Limit, page.Cursor

	if params.UpdatedSince, err = render.ParseTimeParam(r, "updated_since"); err != nil {
		return params, false, err
	}
	if params.IncludeDeleted, err = render.ParseBool(r, "include_deleted", false); err != nil {
		return params, false, err
	}

	q := r.URL.Query()
	if q.Has("expand") {
		if q.Get("expand") != domain.ExpandExercises {
			return params, false, domain.NewValidation("expand", domain.IssueInvalidValue)
		}
		expand = true
	}
	return params, expand, nil
}
