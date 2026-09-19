// Package service holds the business rules: validation, the sync conflict
// rule, limits, ownership and transactions. It sits between the HTTP handlers
// and the store:
//
//	httpapi/handlers -> service -> store
//
// Every service is built from one shared Deps by a constructor of the fixed
// shape NewX(d Deps) *X. A service builds the stores it needs from d.DB
// (store.NewExercises(d.DB), ...) and may use any store, but never another
// service: cross-resource rules (a plan validating its exercise ids, an image
// upload updating an exercise row) go through the stores. Deps therefore stays
// the only thing services share.
//
// Errors are the typed errors of package domain; the HTTP layer maps them.
// Services do not import net/http.
package service

import (
	"log/slog"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
)

// Deps is what every service constructor receives. cmd/server builds it once
// (wire.go); tests build it by hand, with a clock.Fake and a testutil database.
type Deps struct {
	DB     *store.DB
	Clock  clock.Clock
	Config *config.Config
	Logger *slog.Logger
	// Media is the exercise image store (MEDIA_DIR).
	Media *media.Store
	// Hasher hashes and verifies passwords (argon2id) under the configured
	// cost and concurrency cap. Used by login, register, admin change-password
	// and the admin CLI.
	Hasher *auth.Hasher
	// Limiter is the in-memory login brute-force limiter, shared by every
	// login attempt of the process.
	Limiter *auth.LoginLimiter
}
