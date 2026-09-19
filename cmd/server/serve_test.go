package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

const testTimeout = 30 * time.Second

func TestOpenDBRefusesAnUnmigratedDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	cfg := testConfig(t, devEnv(t, url))

	db, err := openDB(t.Context(), cfg)
	if err == nil {
		db.Close()
		t.Fatal("openDB accepted a database without migrations")
	}
	// The operator must be told what to do, not just that something is wrong.
	contains(t, "error", err.Error(), "schema is behind", "server migrate up",
		fmt.Sprintf("%d migration(s) pending", migrationCount(t)))
}

func TestOpenDBRefusesAPartlyMigratedDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateDown(t.Context(), url); err != nil {
		t.Fatal(err)
	}
	_, err := openDB(t.Context(), testConfig(t, devEnv(t, url)))
	if err == nil {
		t.Fatal("openDB accepted a database that lacks the latest migration")
	}
	contains(t, "error", err.Error(), "server migrate up", "1 migration(s) pending")
}

func TestOpenDBAcceptsAMigratedDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	setEnv(t, devEnv(t, url))
	keepSlogDefault(t)

	// The same path as the deploy: `server migrate up`, then serve's check.
	var stdout, stderr strings.Builder
	if code := run(t.Context(), []string{"migrate", "up"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("migrate up exit code = %d, stderr: %s", code, stderr.String())
	}
	db, err := openDB(t.Context(), testConfig(t, devEnv(t, url)))
	if err != nil {
		t.Fatalf("openDB after migrate up: %v", err)
	}
	defer db.Close()
	if err := db.Ping(t.Context()); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestOpenDBReportsAnUnreachableDatabase(t *testing.T) {
	// A port nobody listens on: bind and release it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := testConfig(t, devEnv(t, "postgres://postgres:secret-pw@"+addr+"/x?sslmode=disable&connect_timeout=2"))
	_, err = openDB(t.Context(), cfg)
	if err == nil {
		t.Fatal("openDB succeeded against a closed port")
	}
	if strings.Contains(err.Error(), "secret-pw") {
		t.Errorf("error leaks the database password: %v", err)
	}
}

// startServe runs serve on an ephemeral loopback port against a migrated
// database and returns its base URL, the captured logs and a stop function
// that cancels the context and returns serve's result.
func startServe(t *testing.T, env map[string]string) (baseURL string, logs *apitest.Logs, stop func() error) {
	t.Helper()
	cfg := testConfig(t, env)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := apitest.NewLogs()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logger, ln) }()

	stop = func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(testTimeout):
			t.Fatal("serve did not return after its context was cancelled")
			return nil
		}
	}
	t.Cleanup(func() { cancel() })
	return "http://" + ln.Addr().String(), logs, stop
}

func TestServeStartsAndShutsDownGracefully(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	if err := store.MigrateUp(t.Context(), url); err != nil {
		t.Fatal(err)
	}
	baseURL, logs, stop := startServe(t, devEnv(t, url))

	// Wait until the server answers.
	deadline := time.Now().Add(testTimeout)
	var resp *http.Response
	for {
		var err error
		resp, err = http.Get(baseURL + "/healthz")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("GET /healthz = %d %s, want 200 ok", resp.StatusCode, body)
	}

	resp, err := http.Get(baseURL + "/v1/exercises")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/exercises = %d, want 401 (no authenticator wired yet)", resp.StatusCode)
	}

	if err := stop(); err != nil {
		t.Fatalf("serve returned %v after a graceful stop, want nil", err)
	}

	// The configuration is logged, and only in redacted form.
	starting := logs.Find(t, "starting")
	if _, ok := starting["config"].(map[string]any); !ok {
		t.Errorf("starting log has no config group: %v", starting)
	}
	if strings.Contains(logs.String(), ":postgres@") {
		t.Errorf("logs contain the database password:\n%s", logs.String())
	}
	contains(t, "logs", logs.String(), "REDACTED")
	logs.Find(t, "listening")
	logs.Find(t, "shutting down")
	logs.Find(t, "stopped")
}

func TestServeRefusesAnUnmigratedDatabase(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	cfg := testConfig(t, devEnv(t, url))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := apitest.NewLogs()

	err = serve(t.Context(), cfg, logger, ln)
	if err == nil {
		t.Fatal("serve started on an unmigrated database")
	}
	contains(t, "error", err.Error(), "server migrate up")
	if _, err := ln.Accept(); err == nil {
		t.Error("the listener was left open after a failed start")
	}
}

func TestServeHTTPDrainsInFlightRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := apitest.NewLogs()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- serveHTTP(ctx, srv, ln, testTimeout, logger) }()

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{body: string(b), err: err}
	}()

	<-entered
	cancel() // SIGTERM arrives while the request is running
	select {
	case err := <-served:
		t.Fatalf("serveHTTP returned (%v) before the in-flight request finished", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)

	if r := <-got; r.err != nil || r.body != "done" {
		t.Errorf("in-flight request = %q, %v; want it to complete", r.body, r.err)
	}
	if err := <-served; err != nil {
		t.Errorf("serveHTTP = %v, want nil", err)
	}
	// And the listener is closed: no new connections.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("server still accepts connections after shutdown")
	}
}

func TestServeHTTPCutsOffAfterTheGracePeriod(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := apitest.NewLogs()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- serveHTTP(ctx, srv, ln, 100*time.Millisecond, logger) }()
	go func() { _, _ = http.Get("http://" + ln.Addr().String()) }()

	<-entered
	cancel()
	select {
	case err := <-served:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("serveHTTP = %v, want a graceful shutdown deadline error", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("serveHTTP hung on a request that never finishes")
	}
}
