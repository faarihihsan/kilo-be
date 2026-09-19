// Package handlers holds the HTTP handlers, one file per resource: auth,
// admin, exercises, exercise_images, plans and progress. cmd/server builds one
// value of each type (NewX(svc, Deps)) and hands its methods to the router as
// httpapi.Handlers fields; the router itself never imports this package.
//
// Handlers contain no business logic. A handler does exactly this:
//
//  1. decode the request (render.DecodeJSON, render.ParseUUID, the query and
//     pagination parsers, middleware.MustPrincipal for the caller),
//  2. call one method of its service,
//  3. encode the result (render.WriteJSON, render.NoContent) or hand the error
//     to render.WriteError, which maps domain errors to the JSON error format.
//
// Validation, ownership, the conflict rule and every SQL statement belong to
// the service and store layers. Imports: service, domain, render and
// middleware. A handler never imports store (or pgx): if it needs data, the
// service returns it. A test in this package enforces that.
package handlers

import (
	"log/slog"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
)

// Deps is what every handler constructor receives besides its service. It only
// carries what a handler needs to build a response (for example the media base
// URL from Config), not what a service needs.
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	Clock  clock.Clock
}
