package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Auth serves login, logout and token management: endpoints 2, 11, 12 and 15.
// The router sends login through the per-IP rate limiter, so the login handler
// can read the resolved client IP from the context.
type Auth struct {
	svc  *service.Auth
	deps Deps
}

// NewAuth builds the Auth handlers.
func NewAuth(svc *service.Auth, d Deps) *Auth { return &Auth{svc: svc, deps: d} }

// Login serves POST /v1/auth/login (endpoint 2). The client IP set by the rate
// limiter middleware keys the brute-force limiter.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req service.LoginRequest
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Login(r.Context(), middleware.ClientIPFrom(r.Context()), req)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, res)
}

// Logout serves POST /v1/auth/logout (endpoint 11): revoke the current token.
func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	caller := middleware.MustPrincipal(r.Context())
	if err := h.svc.Logout(r.Context(), caller.UserID, caller.TokenID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.NoContent(w)
}

// RevokeTokens serves POST /v1/auth/revoke (endpoint 12).
func (h *Auth) RevokeTokens(w http.ResponseWriter, r *http.Request) {
	caller := middleware.MustPrincipal(r.Context())
	var req service.RevokeRequest
	if err := render.DecodeJSON(w, r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Revoke(r.Context(), caller.UserID, caller.TokenID, req)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, res)
}

// ListTokens serves GET /v1/auth/tokens (endpoint 15).
func (h *Auth) ListTokens(w http.ResponseWriter, r *http.Request) {
	caller := middleware.MustPrincipal(r.Context())
	res, err := h.svc.ListTokens(r.Context(), caller.UserID, caller.TokenID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, res)
}
