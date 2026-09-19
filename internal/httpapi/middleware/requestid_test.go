package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/render"
)

func TestRequestID(t *testing.T) {
	tests := []struct {
		name       string
		inbound    string
		wantKept   bool
		wantFormat string
	}{
		{name: "valid id is kept", inbound: "req-123_abc.DEF", wantKept: true},
		{name: "uuid is kept", inbound: "0195f3a2-bbbb-7000-8000-000000000010", wantKept: true},
		{name: "64 chars is kept", inbound: strings.Repeat("a", 64), wantKept: true},
		{name: "65 chars is replaced", inbound: strings.Repeat("a", 65)},
		{name: "missing is generated", inbound: ""},
		{name: "space is replaced", inbound: "a b"},
		{name: "quote is replaced", inbound: `a"b`},
		{name: "control char is replaced", inbound: "a\tb"},
		{name: "non ascii is replaced", inbound: "caf\u00e9"},
		{name: "slash is replaced", inbound: "a/b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := apitest.NewLogs()
			var ctxID string
			var loggerSeen *bool
			h := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctxID = RequestIDFrom(r.Context())
				render.LoggerFrom(r.Context()).Info("inside")
				ok := true
				loggerSeen = &ok
			}))

			req := httptest.NewRequest("GET", "/x", nil)
			if tc.inbound != "" {
				req.Header.Set(RequestIDHeader, tc.inbound)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(RequestIDHeader)
			if got == "" {
				t.Fatal("no X-Request-Id on the response")
			}
			if tc.wantKept {
				if got != tc.inbound {
					t.Errorf("id = %q, want the inbound %q", got, tc.inbound)
				}
			} else {
				id, err := uuid.Parse(got)
				if err != nil || id.Version() != 7 {
					t.Errorf("generated id %q is not a UUID v7 (err %v)", got, err)
				}
			}
			if ctxID != got {
				t.Errorf("context id = %q, header id = %q", ctxID, got)
			}
			if loggerSeen == nil {
				t.Fatal("handler did not run")
			}
			if e := logs.Find(t, "inside"); e["request_id"] != got {
				t.Errorf("logger attr request_id = %v, want %q", e["request_id"], got)
			}
		})
	}
}

func TestRequestIDFromWithoutMiddleware(t *testing.T) {
	if id := RequestIDFrom(httptest.NewRequest("GET", "/", nil).Context()); id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

func TestRequestIDIsUniquePerRequest(t *testing.T) {
	h := RequestID(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	seen := map[string]bool{}
	for range 50 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		id := rec.Header().Get(RequestIDHeader)
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
