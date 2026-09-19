package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workout-tracker-be/internal/httpapi/apitest"
)

func TestRecoverTurnsPanicInto500(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := RequestID(logger)(Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail: db password is hunter2")
	})))
	// RequestID outside here only to check the id reaches the log; the router
	// puts Recover outside RequestID (covered by the router tests).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/x?token=abc", nil))

	body := apitest.RequireError(t, rec, 500, "internal")
	if body.Error.Message != "Internal server error." {
		t.Errorf("message = %q", body.Error.Message)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("response leaks the panic value: %s", rec.Body.String())
	}

	e := logs.Find(t, "panic recovered")
	if e["level"] != "ERROR" || e["method"] != "POST" || e["path"] != "/v1/x" {
		t.Errorf("log entry = %v", e)
	}
	if !strings.Contains(e["panic"].(string), "hunter2") {
		t.Errorf("panic value not logged: %v", e["panic"])
	}
	if !strings.Contains(e["stack"].(string), "recover_test.go") {
		t.Errorf("stack does not point at the panic site: %v", e["stack"])
	}
	if strings.Contains(logs.String(), "token=abc") {
		t.Error("query string must not be logged")
	}
}

func TestRecoverLogsRequestIDWhenInsideRequestID(t *testing.T) {
	logger, logs := apitest.NewLogs()
	// Router order: Recover outside RequestID.
	h := Recover(logger)(RequestID(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, "rid-42")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	apitest.RequireError(t, rec, 500, "internal")
	if got := rec.Header().Get(RequestIDHeader); got != "rid-42" {
		t.Errorf("response X-Request-Id = %q", got)
	}
	if e := logs.Find(t, "panic recovered"); e["request_id"] != "rid-42" {
		t.Errorf("request_id = %v", e["request_id"])
	}
}

func TestRecoverPassesThroughNormalResponses(t *testing.T) {
	h := Recover(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestRecoverKeepsGoingAfterAPanic(t *testing.T) {
	logger, _ := apitest.NewLogs()
	first := true
	h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			panic("once")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rec1, rec2 := httptest.NewRecorder(), httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest("GET", "/x", nil))
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/x", nil))
	if rec1.Code != 500 || rec2.Code != 204 {
		t.Errorf("statuses = %d, %d; want 500, 204", rec1.Code, rec2.Code)
	}
}

func TestRecoverAbortsWhenResponseAlreadyStarted(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late failure")
	}))

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
		if !strings.Contains(logs.String(), "late failure") {
			t.Error("the panic must still be logged")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	t.Error("ServeHTTP returned normally")
}

func TestRecoverRepanicsErrAbortHandlerWithoutLogging(t *testing.T) {
	logger, logs := apitest.NewLogs()
	h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
		if logs.String() != "" {
			t.Errorf("ErrAbortHandler is not a bug, must not be logged: %s", logs.String())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
}
