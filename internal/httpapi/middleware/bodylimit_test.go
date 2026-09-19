package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/render"
)

// readAll answers 200 with the number of bytes read, or the mapped error.
func readAll(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if called != nil {
			*called = true
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.WriteJSON(w, 200, map[string]int{"read": len(b)})
	})
}

// stream hides the length of s, like a chunked request body.
func stream(s string) io.Reader { return struct{ io.Reader }{strings.NewReader(s)} }

func TestBodyLimit(t *testing.T) {
	tests := []struct {
		name       string
		limit      int64
		body       io.Reader
		wantStatus int
		wantCalled bool
	}{
		{"default limit accepts exactly 1 MiB", 0, strings.NewReader(strings.Repeat("a", domain.MaxBodyBytes)), 200, true},
		{"default limit rejects 1 MiB + 1 by Content-Length before the handler", 0, strings.NewReader(strings.Repeat("a", domain.MaxBodyBytes+1)), 413, false},
		{"unknown length within limit", 10, stream("0123456789"), 200, true},
		{"unknown length over limit fails while reading", 10, stream("0123456789a"), 413, true},
		{"override accepts 2 MiB", domain.MaxImageBytes, strings.NewReader(strings.Repeat("a", domain.MaxImageBytes)), 200, true},
		{"override rejects 2 MiB + 1", domain.MaxImageBytes, strings.NewReader(strings.Repeat("a", domain.MaxImageBytes+1)), 413, false},
		{"negative limit means default", -5, strings.NewReader(strings.Repeat("a", domain.MaxBodyBytes+1)), 413, false},
		{"no body", 10, nil, 200, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := BodyLimit(tc.limit)(readAll(&called))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("PUT", "/x", tc.body))

			if called != tc.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tc.wantCalled)
			}
			if tc.wantStatus == 200 {
				if rec.Code != 200 {
					t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
				}
				return
			}
			apitest.RequireError(t, rec, 413, "payload_too_large")
		})
	}
}

func TestBodyLimitLeavesAbsentBodyAlone(t *testing.T) {
	var body io.ReadCloser
	h := BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body = r.Body }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if body != http.NoBody {
		t.Error("an absent body must stay http.NoBody so DecodeJSON can report it as empty")
	}
}

func TestBodyLimitWithDecodeJSON(t *testing.T) {
	// The typical handler path: the limit set here surfaces as 413 via DecodeJSON.
	h := BodyLimit(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if err := render.DecodeJSON(w, r, &v); err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.NoContent(w)
	}))
	req := httptest.NewRequest("POST", "/x", stream(`{"a":"0123456789abcdef"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	apitest.RequireError(t, rec, 413, "payload_too_large")
}

func TestBodyLimitMessageNamesTheLimit(t *testing.T) {
	h := BodyLimit(5)(readAll(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/x", strings.NewReader("123456")))
	body := apitest.RequireError(t, rec, 413, "payload_too_large")
	if !strings.Contains(body.Error.Message, "5 bytes") {
		t.Errorf("message = %q", body.Error.Message)
	}
}
