package handlers

import (
	"net/http"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/service"
)

// Progress serves workout progress.
//
// Stub from T0.9: T4 replaces the method bodies (decode, call the service,
// encode) and rewrites this file. Keep the type name, the NewProgress signature and
// the method names; cmd/server wires them to the httpapi.Handlers fields.
type Progress struct {
	svc  *service.Progress
	deps Deps
}

// NewProgress builds the Progress handlers.
func NewProgress(svc *service.Progress, d Deps) *Progress { return &Progress{svc: svc, deps: d} }

// List serves GET /v1/progress (endpoint 5, httpapi.Handlers.ListProgress).
func (h *Progress) List(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Get serves GET /v1/progress/{id} (endpoint 4, httpapi.Handlers.GetProgress).
func (h *Progress) Get(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Save serves PUT /v1/progress/{id} (endpoint 3, httpapi.Handlers.SaveProgress).
func (h *Progress) Save(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }

// Delete serves DELETE /v1/progress/{id} (endpoint 16, httpapi.Handlers.DeleteProgress).
func (h *Progress) Delete(w http.ResponseWriter, r *http.Request) { render.NotImplemented(w, r) }
