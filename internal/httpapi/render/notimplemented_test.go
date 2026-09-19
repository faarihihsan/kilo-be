package render_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/render"
)

func TestNotImplemented(t *testing.T) {
	rec := httptest.NewRecorder()
	render.NotImplemented(rec, httptest.NewRequest(http.MethodGet, "/v1/anything", nil))
	body := apitest.RequireError(t, rec, http.StatusNotImplemented, render.CodeNotImplemented)
	if body.Error.Message != render.MsgNotImplemented || len(body.Error.Details) != 0 {
		t.Errorf("body = %+v", body)
	}
}
