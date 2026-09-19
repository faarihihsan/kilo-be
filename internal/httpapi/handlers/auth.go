package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Auth serves login, logout and token management.
//
// Stub from T0.9: T5 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewAuth signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type Auth struct {
	svc  *service.Auth
	deps Deps
}

// NewAuth builds the Auth handlers.
func NewAuth(svc *service.Auth, d Deps) *Auth { return &Auth{svc: svc, deps: d} }

// Login serves POST /v1/auth/login (endpoint 2).
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Logout serves POST /v1/auth/logout (endpoint 11).
func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// RevokeTokens serves POST /v1/auth/revoke (endpoint 12).
func (h *Auth) RevokeTokens(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// ListTokens serves GET /v1/auth/tokens (endpoint 15).
func (h *Auth) ListTokens(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
