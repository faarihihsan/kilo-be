package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/render"
)

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHealthHandlerOK(t *testing.T) {
	var sawDeadline bool
	h := HealthHandler(pingerFunc(func(ctx context.Context) error {
		d, ok := ctx.Deadline()
		sawDeadline = ok && time.Until(d) <= healthTimeout
		return nil
	}))
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != render.ContentTypeJSON {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if !sawDeadline {
		t.Error("Ping must get a context with a deadline of at most 2s")
	}
}

func TestHealthHandlerUnavailable(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := HealthHandler(pingerFunc(func(context.Context) error {
		return errors.New(`dial tcp 10.0.0.5:5432: connection refused`)
	}))
	req := httptest.NewRequest("GET", "/healthz", nil)
	req = req.WithContext(render.WithLogger(req.Context(), logger))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"status":"unavailable"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Error("the ping error must not reach the client")
	}
	if e := logs.Find(t, "healthz: ping failed"); !strings.Contains(e["error"].(string), "connection refused") {
		t.Errorf("ping error not logged: %v", e)
	}
}

func TestHealthHandlerTimesOut(t *testing.T) {
	h := healthHandler(pingerFunc(func(ctx context.Context) error {
		<-ctx.Done() // a hung database
		return ctx.Err()
	}), 30*time.Millisecond)

	logger, _ := apitest.NewLogs()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req = req.WithContext(render.WithLogger(req.Context(), logger))
	start := time.Now()
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 503 {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestHealthHandlerNilPinger(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler(nil)(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHealthTimeoutIsTwoSeconds(t *testing.T) {
	if healthTimeout != 2*time.Second {
		t.Errorf("healthTimeout = %v", healthTimeout)
	}
}
