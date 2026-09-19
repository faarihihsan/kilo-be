package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

// These tests run the router behind a real net/http server, to cover what
// httptest.ResponseRecorder cannot: chunked bodies, connection reuse after an
// early rejection and aborted responses.

func newTestServer(t *testing.T, h Handlers) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewRouter(RouterConfig{Handlers: h, Authenticator: fakeAuth(nil), Logger: discardLogger()}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServerBodyLimits(t *testing.T) {
	var saw int64
	var h Handlers
	h.CreateExercise = func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := render.DecodeJSON(w, r, &v); err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.NoContent(w)
	}
	h.SetExerciseImage = func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			render.WriteError(w, r, err)
			return
		}
		saw = n
		render.NoContent(w)
	}
	srv := newTestServer(t, h)

	post := func(path, method string, body io.Reader, contentLength int64) (int, render.ErrorBody) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = contentLength // -1: chunked
		req.Header.Set(testRoleHeader, "user")
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var eb render.ErrorBody
		b, _ := io.ReadAll(resp.Body)
		if len(b) > 0 {
			if err := json.Unmarshal(b, &eb); err != nil {
				t.Fatalf("response is not JSON: %q", b)
			}
		}
		return resp.StatusCode, eb
	}

	json1MiB := func(extra int) []byte {
		pad := domain.MaxBodyBytes + extra - len(`{"a":""}`)
		return []byte(`{"a":"` + strings.Repeat("x", pad) + `"}`)
	}

	// exactly at the limit, sent with Content-Length and chunked
	for _, cl := range []int64{int64(len(json1MiB(0))), -1} {
		if status, _ := post("/v1/exercises", "POST", bytes.NewReader(json1MiB(0)), cl); status != 204 {
			t.Errorf("1 MiB with content length %d: status %d, want 204", cl, status)
		}
	}
	// one byte over, both ways
	for _, cl := range []int64{int64(len(json1MiB(1))), -1} {
		status, eb := post("/v1/exercises", "POST", bytes.NewReader(json1MiB(1)), cl)
		if status != 413 || eb.Error.Code != "payload_too_large" {
			t.Errorf("1 MiB + 1 with content length %d: %d %s, want 413 payload_too_large", cl, status, eb.Error.Code)
		}
	}
	// the image route takes 2 MiB
	img := make([]byte, domain.MaxImageBytes)
	path := "/v1/exercises/0195f3a2-bbbb-7000-8000-000000000010/image"
	if status, _ := post(path, "PUT", bytes.NewReader(img), int64(len(img))); status != 204 || saw != domain.MaxImageBytes {
		t.Errorf("2 MiB image: status %d, read %d", status, saw)
	}
	if status, _ := post(path, "PUT", bytes.NewReader(append(img, 0)), -1); status != 413 {
		t.Errorf("2 MiB + 1 image chunked: status %d, want 413", status)
	}
}

func TestServerPanicAfterResponseStartedAbortsTheConnection(t *testing.T) {
	var h Handlers
	h.ListProgress = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[`))
		w.(http.Flusher).Flush()
		panic("late failure")
	}
	srv := newTestServer(t, h)

	req, _ := http.NewRequest("GET", srv.URL+"/v1/progress", nil)
	req.Header.Set(testRoleHeader, "user")
	resp, err := srv.Client().Do(req)
	if err != nil {
		return // aborted before the headers arrived: fine
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("a truncated response must fail on the client, not end as if complete")
	}
}

func TestServerPanicBeforeResponseIsJSON500(t *testing.T) {
	var h Handlers
	h.ListProgress = func(http.ResponseWriter, *http.Request) { panic("boom") }
	srv := newTestServer(t, h)

	req, _ := http.NewRequest("GET", srv.URL+"/v1/progress", nil)
	req.Header.Set(testRoleHeader, "user")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || !strings.Contains(string(b), `"code":"internal"`) || resp.Header.Get("X-Request-Id") == "" {
		t.Errorf("status %d, request id %q, body %s", resp.StatusCode, resp.Header.Get("X-Request-Id"), b)
	}
}
