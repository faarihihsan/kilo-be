package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Admin serves the admin-only endpoints: register, admin-revoke-login and
// admin-change-password (1, 13, 14). The router enforces the admin role.
type Admin struct {
	svc  *service.Admin
	deps Deps
}

// NewAdmin builds the Admin handlers.
func NewAdmin(svc *service.Admin, d Deps) *Admin { return &Admin{svc: svc, deps: d} }

// Register serves POST /v1/auth/register (endpoint 1) and answers 201 with the
// created user.
func (h *Admin) Register(w http.ResponseWriter, r *http.Request) {
	var req service.RegisterRequest
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Register(r.Context(), req)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, res)
}

// RevokeLogin serves POST /v1/admin/users/{username}/revoke-login (endpoint 13,
// httpapi.Handlers.AdminRevokeLogin).
func (h *Admin) RevokeLogin(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.RevokeLogin(r.Context(), r.PathValue("username"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, res)
}

// ChangePassword serves PUT /v1/admin/users/{username}/password (endpoint 14,
// httpapi.Handlers.AdminChangePassword) and answers 204.
func (h *Admin) ChangePassword(w http.ResponseWriter, r *http.Request) {
	caller := middleware.MustPrincipal(r.Context())
	var req service.ChangePasswordRequest
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.ChangePassword(r.Context(), caller.UserID, caller.TokenID, r.PathValue("username"), req.Password); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.NoContent(w)
}
