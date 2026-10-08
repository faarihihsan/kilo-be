package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
)

// logEntry returns the first entry whose message starts with prefix.
func logEntry(t *testing.T, logs *apitest.Logs, prefix string) map[string]any {
	t.Helper()
	for _, e := range logs.Entries(t) {
		if msg, _ := e["msg"].(string); strings.HasPrefix(msg, prefix) {
			return e
		}
	}
	t.Fatalf("no log entry with message starting %q in:\n%s", prefix, logs.String())
	return nil
}

func TestAccessLogFields(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := RequestID(nil)(AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})))

	req := httptest.NewRequest("PUT", "/v1/progress/abc?x=1", nil)
	req.Header.Set(RequestIDHeader, "rid-7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.Entries(t)
	if len(entries) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(entries), logs.String())
	}
	if got := entries[0]["msg"]; got != "Request = method:PUT, uri:/v1/progress/abc?x=1" {
		t.Errorf("request line = %v", got)
	}
	if entries[0]["level"] != "INFO" || entries[0]["request_id"] != "rid-7" {
		t.Errorf("request entry = %v", entries[0])
	}
	if _, ok := entries[0]["user_id"]; ok {
		t.Errorf("anonymous request must not have user_id: %v", entries[0]["user_id"])
	}
	if got, _ := entries[1]["msg"].(string); !strings.HasPrefix(got, "Response = time:") || !strings.Contains(got, `Payload:{"ok":true}`) {
		t.Errorf("response line = %v", entries[1]["msg"])
	}
	if entries[1]["status"] != float64(201) || entries[1]["level"] != "INFO" || entries[1]["request_id"] != "rid-7" {
		t.Errorf("response entry = %v", entries[1])
	}
}

func TestAccessLogUserIDFromInnerAuthentication(t *testing.T) {
	logger, logs := apitest.NewLogs()
	uid := uuid.New()
	// Like the real pipeline: AccessLog outside, the authenticator inside.
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithPrincipal(r.Context(), domain.Principal{UserID: uid, Role: domain.RoleUser})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	h := AccessLog(logger)(authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	e := logEntry(t, logs, "Request = method:GET, uri:/x")
	if e["user_id"] != uid.String() {
		t.Errorf("user_id = %v, want %s", e["user_id"], uid)
	}
	resp := logEntry(t, logs, "Response = time:")
	if resp["status"] != float64(200) {
		t.Errorf("status = %v, want 200 for a handler that wrote nothing", resp["status"])
	}
}

func TestAccessLogRedactsCredentials(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // handlers read the body; that is what is captured
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"response-secret-body"}`))
	}))
	req := httptest.NewRequest("POST", "/v1/auth/login?password=q-secret",
		strings.NewReader(`{"username":"alice","password":"hunter2-body-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wt_header-secret-token")
	req.Header.Set("Cookie", "session=cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logs.String()
	for _, secret := range []string{"hunter2", "body-secret", "wt_header", "header-secret", "Bearer", "q-secret", "cookie-secret", "response-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, `\"password\":\"***\"`) || !strings.Contains(out, `\"token\":\"***\"`) {
		t.Errorf("secrets were not redacted:\n%s", out)
	}
}

func TestAccessLogLevelsAndPanic(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus float64
		wantLevel  string
	}{
		{"4xx is info", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, 404, "INFO"},
		{"5xx is error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, 503, "ERROR"},
		{"panic before any output is logged as 500", func(http.ResponseWriter, *http.Request) { panic("x") }, 500, "ERROR"},
		{"panic after the status keeps the status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); panic("x") }, 202, "INFO"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := apitest.NewLogs()
			h := AccessLog(logger)(tc.handler)
			func() {
				defer func() { _ = recover() }() // Recover is not part of this test
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
			}()
			e := logEntry(t, logs, "Response = time:")
			if e["status"] != tc.wantStatus || e["level"] != tc.wantLevel {
				t.Errorf("status = %v, level = %v; want %v, %s", e["status"], e["level"], tc.wantStatus, tc.wantLevel)
			}
		})
	}
}

func TestAccessLogRequestURI(t *testing.T) {
	logger, logs := apitest.NewLogs()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/progress/{id}", func(w http.ResponseWriter, r *http.Request) {})
	AccessLog(logger)(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/progress/123?plan=abc", nil))

	e := logEntry(t, logs, "Request = method:GET")
	if e["msg"] != "Request = method:GET, uri:/v1/progress/123?plan=abc" {
		t.Errorf("request line = %v", e["msg"])
	}
}

func TestRecorderKeepsFlushAndUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := recordResponse(inner)
	if recordResponse(rec) != rec {
		t.Error("recordResponse must reuse an existing recorder")
	}
	if rec.Unwrap() != http.ResponseWriter(inner) {
		t.Error("Unwrap must return the wrapped writer")
	}
	if err := http.NewResponseController(rec).Flush(); err != nil {
		t.Errorf("Flush through the recorder: %v", err)
	}
	if !inner.Flushed || rec.status != 200 {
		t.Errorf("flushed = %v, status = %d", inner.Flushed, rec.status)
	}
}

func TestRecorderIgnoresInterimStatus(t *testing.T) {
	rec := recordResponse(httptest.NewRecorder())
	rec.WriteHeader(http.StatusEarlyHints)
	if rec.wroteHeader {
		t.Fatal("1xx must not count as the final status")
	}
	rec.WriteHeader(http.StatusOK)
	rec.WriteHeader(http.StatusInternalServerError) // ignored: first final status wins
	if rec.status != 200 {
		t.Errorf("status = %d, want 200", rec.status)
	}
}
