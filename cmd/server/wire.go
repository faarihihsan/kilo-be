package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/handlers"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/store"
)

// This file is the composition root: the one place that builds the services
// and handlers and hands them to the router. Later tasks never change the
// constructor signatures used here (service.NewX(Deps), handlers.NewX(svc,
// handlers.Deps), see the seams_test.go files); they fill in the bodies. The
// only planned edit was T1's: the Hasher and Limiter in newServiceDeps and the
// Authenticator and RateLimit slots in newApp.

// app is the assembled application.
type app struct {
	// deps are the shared service dependencies; background jobs use them too.
	deps service.Deps
	// router is everything httpapi.NewRouter needs. Tests replace its
	// Authenticator before calling Handler.
	router httpapi.RouterConfig
}

// Handler returns the API handler (all routes plus /healthz).
func (a *app) Handler() http.Handler { return httpapi.NewRouter(a.router) }

// newServiceDeps builds the dependencies shared by all services: the real
// clock, the exercise image store at cfg.MediaDir, the argon2id hasher (its
// dummy hash costs one hash at startup) and the login limiter.
func newServiceDeps(cfg *config.Config, logger *slog.Logger, db *store.DB) (service.Deps, error) {
	// In production the media directory is created by the operator and, under
	// systemd, must exist before the unit starts (deploy/workout-tracker.service).
	// Creating it here would hide a missing mount and silently fill the root
	// disk, so only development creates it (media.NewStore does).
	if cfg.Env.IsProduction() {
		fi, err := os.Stat(cfg.MediaDir)
		if err != nil {
			return service.Deps{}, fmt.Errorf("MEDIA_DIR: %w (create it and make it writable by the service user)", err)
		}
		if !fi.IsDir() {
			return service.Deps{}, fmt.Errorf("MEDIA_DIR %q is not a directory", cfg.MediaDir)
		}
	}
	mediaStore, err := media.NewStore(cfg.MediaDir)
	if err != nil {
		return service.Deps{}, fmt.Errorf("MEDIA_DIR: %w", err)
	}
	hasher, err := auth.NewHasher(auth.HasherConfig{
		MemoryKiB:     cfg.Argon2MemoryKiB,
		Time:          cfg.Argon2Time,
		Parallelism:   cfg.Argon2Parallelism,
		MaxConcurrent: cfg.Argon2MaxConcurrent,
	})
	if err != nil {
		return service.Deps{}, fmt.Errorf("ARGON2_*: %w", err)
	}
	clk := clock.Real{}
	return service.Deps{
		DB:     db,
		Clock:  clk,
		Config: cfg,
		Logger: logger,
		Media:  mediaStore,
		Hasher: hasher,
		Limiter: auth.NewLoginLimiter(clk, auth.LoginLimiterConfig{
			MaxFailsUser: cfg.LoginMaxFailsUser,
			MaxFailsIP:   cfg.LoginMaxFailsIP,
			Lock:         cfg.LoginLock,
		}),
	}, nil
}

// newApp wires the whole application on top of an open, migrated database:
// six services, six handler types, the httpapi.Handlers table and the router
// configuration.
func newApp(cfg *config.Config, logger *slog.Logger, db *store.DB) (*app, error) {
	sd, err := newServiceDeps(cfg, logger, db)
	if err != nil {
		return nil, err
	}
	hd := handlers.Deps{Config: cfg, Logger: logger, Clock: sd.Clock}

	auth := handlers.NewAuth(service.NewAuth(sd), hd)
	admin := handlers.NewAdmin(service.NewAdmin(sd), hd)
	exercises := handlers.NewExercises(service.NewExercises(sd), hd)
	images := handlers.NewExerciseImages(service.NewExerciseImages(sd), hd)
	plans := handlers.NewPlans(service.NewPlans(sd), hd)
	progress := handlers.NewProgress(service.NewProgress(sd), hd)

	// One line per route, in endpoint order (docs/README.md). The router fills
	// a nil field with a 501, so a forgotten line would go unnoticed at
	// runtime: TestAppWiresEveryHandler checks each field.
	h := httpapi.Handlers{
		Register:            admin.Register,            // 1
		Login:               auth.Login,                // 2
		SaveProgress:        progress.Save,             // 3
		GetProgress:         progress.Get,              // 4
		ListProgress:        progress.List,             // 5
		ListWorkoutPlans:    plans.List,                // 6
		GetWorkoutPlan:      plans.Get,                 // 7
		ListExercises:       exercises.List,            // 8
		CreateExercise:      exercises.Create,          // 9
		SaveWorkoutPlan:     plans.Save,                // 10
		Logout:              auth.Logout,               // 11
		RevokeTokens:        auth.RevokeTokens,         // 12
		AdminRevokeLogin:    admin.RevokeLogin,         // 13
		AdminChangePassword: admin.ChangePassword,      // 14
		ListTokens:          auth.ListTokens,           // 15
		DeleteProgress:      progress.Delete,           // 16
		DeleteWorkoutPlan:   plans.Delete,              // 17
		UpdateExercise:      exercises.Update,          // 18
		DeleteExercise:      exercises.Delete,          // 19
		SetExerciseImage:    images.Set,                // 20
		DeleteExerciseImage: images.Delete,             // 21
		Healthz:             httpapi.HealthHandler(db), // liveness with a DB ping
	}

	return &app{
		deps: sd,
		router: httpapi.RouterConfig{
			Handlers:      h,
			Logger:        logger,
			Authenticator: middleware.NewAuthenticator(store.NewAuthTokens(db), sd.Clock),
			// Per-IP limit for the routes flagged AuthRateLimited; it also puts
			// the resolved client IP in the context for the login handler.
			// Tests that fire many requests from one address at those routes
			// replace it with nil.
			RateLimit: middleware.NewRateLimit(middleware.RateLimitConfig{
				Clock:          sd.Clock,
				TrustedProxies: cfg.TrustedProxyCIDRs,
			}),
		},
	}, nil
}
