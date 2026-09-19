package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Admin serves the admin-only endpoints.
//
// Stub from T0.9: T5 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewAdmin signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type Admin struct {
	svc  *service.Admin
	deps Deps
}

// NewAdmin builds the Admin handlers.
func NewAdmin(svc *service.Admin, d Deps) *Admin { return &Admin{svc: svc, deps: d} }

// Register serves POST /v1/auth/register (endpoint 1).
func (h *Admin) Register(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// RevokeLogin serves POST /v1/admin/users/{username}/revoke-login (endpoint 13, httpapi.Handlers.AdminRevokeLogin).
func (h *Admin) RevokeLogin(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// ChangePassword serves PUT /v1/admin/users/{username}/password (endpoint 14, httpapi.Handlers.AdminChangePassword).
func (h *Admin) ChangePassword(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
