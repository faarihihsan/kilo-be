package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/service"
)

// HTTP server timeouts. ReadTimeout covers the whole request including the body
// (a 2 MiB image over a slow mobile link); WriteTimeout covers the handler and
// the response. MaxHeaderBytes keeps the net/http default (1 MiB).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second

	// shutdownGrace is how long in-flight requests get to finish after SIGINT
	// or SIGTERM. It is below TimeoutStopSec=30 of the systemd unit, so the
	// process exits by itself before systemd sends SIGKILL.
	shutdownGrace = 15 * time.Second
)

// runServe implements `server serve`: load the configuration, log to stdout
// as JSON and run until ctx is cancelled (SIGINT/SIGTERM, see main).
func runServe(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return usagef("serve takes no arguments, got %q", args)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	// Bind first: a busy port fails at once, before the database is touched.
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on HTTP_ADDR: %w", err)
	}
	return serve(ctx, cfg, logger, ln)
}

// serve opens the database (refusing a schema that is behind), builds the
// application and serves HTTP on ln until ctx is cancelled, then shuts down
// gracefully. It owns ln and closes it.
//
// Shutdown order: stop accepting and drain in-flight requests, stop the
// background jobs, close the database. Every error before the server is
// running is returned, and main turns it into a non-zero exit code.
func serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, ln net.Listener) error {
	defer ln.Close()

	// The configuration is logged through its LogValue, which redacts the
	// DATABASE_URL password. Never log cfg fields or the URL directly.
	logger.InfoContext(ctx, "starting",
		slog.String("version", version),
		slog.Any("config", cfg),
	)

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close() // runs last, after the background jobs stopped

	a, err := newApp(cfg, logger, db)
	if err != nil {
		return err
	}

	backgroundCtx, stopBackground := context.WithCancel(ctx)
	waitBackground := startBackground(backgroundCtx, a.deps)
	defer func() {
		stopBackground()
		waitBackground()
	}()

	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		// net/http's own messages (bad requests, accept errors) go through
		// slog like everything else.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	logger.InfoContext(ctx, "listening", slog.String("addr", ln.Addr().String()))
	if err := serveHTTP(ctx, srv, ln, shutdownGrace, logger); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}

// serveHTTP runs srv on ln until it fails or ctx is cancelled. After
// cancellation it stops accepting connections and waits up to grace for
// in-flight requests; requests still running then are cut off and an error is
// returned.
func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration, logger *slog.Logger) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		// Serve only returns on its own when the listener fails.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.String("grace", grace.String()))
	// The shutdown deadline must not inherit the cancellation that started it.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close() // cut off what is left
		<-served
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// startBackground starts the periodic jobs that run inside `serve` and returns
// a function that blocks until they have all stopped. Every job must return
// when ctx is cancelled; serve waits for them before it closes the database.
//
// The only job is the daily token purge (revoked or expired for more than 30
// days). It is the only place to start background work.
func startBackground(ctx context.Context, deps service.Deps) (wait func()) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runTokenPurge(ctx, deps, tokenPurgeInterval)
	}()
	return wg.Wait
}
