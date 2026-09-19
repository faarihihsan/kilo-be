// Package apitest has small helpers for tests of the HTTP layer: capturing
// slog output and asserting the JSON error format. Handler tests of the
// resource tasks can use it too.
package apitest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workout-tracker-be/internal/httpapi/render"
)

// Logs is a goroutine-safe buffer of JSON log lines.
type Logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// NewLogs returns a logger (JSON, all levels) that writes into the returned
// Logs.
func NewLogs() (*slog.Logger, *Logs) {
	l := &Logs{}
	return slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})), l
}

func (l *Logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// String is everything logged so far, as raw JSON lines.
func (l *Logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Entries parses every logged line.
func (l *Logs) Entries(t testing.TB) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// Find returns the first entry whose msg is msg, and fails the test if there
// is none.
func (l *Logs) Find(t testing.TB, msg string) map[string]any {
	t.Helper()
	for _, e := range l.Entries(t) {
		if e["msg"] == msg {
			return e
		}
	}
	t.Fatalf("no log entry with msg %q in:\n%s", msg, l.String())
	return nil
}

// DecodeError decodes the recorded response as the JSON error format, strictly.
func DecodeError(t testing.TB, rec *httptest.ResponseRecorder) render.ErrorBody {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	var body render.ErrorBody
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("body is not the JSON error format: %v\nbody: %s", err, rec.Body.String())
	}
	return body
}

// RequireError asserts status, JSON content type and error.code of the
// recorded response and returns the decoded body.
func RequireError(t testing.TB, rec *httptest.ResponseRecorder, status int, code string) render.ErrorBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != render.ContentTypeJSON {
		t.Fatalf("Content-Type = %q, want %q", ct, render.ContentTypeJSON)
	}
	body := DecodeError(t, rec)
	if body.Error.Code != code {
		t.Fatalf("error.code = %q, want %q; body: %s", body.Error.Code, code, rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Fatalf("error.message is empty; body: %s", rec.Body.String())
	}
	return body
}
