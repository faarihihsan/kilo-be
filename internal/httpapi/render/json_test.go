package render_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

type payload struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// streamOf hides the length of s, like a chunked request body.
func streamOf(s string) io.Reader { return struct{ io.Reader }{strings.NewReader(s)} }

func TestDecodeJSON(t *testing.T) {
	const ct = "application/json"
	tests := []struct {
		name        string
		body        io.Reader // nil = no body
		contentType string
		wantStatus  int // 0 = success
		wantCode    string
		want        payload
	}{
		{name: "valid", body: strings.NewReader(`{"name":"a","n":3}`), contentType: ct, want: payload{"a", 3}},
		{name: "charset utf-8", body: strings.NewReader(`{"name":"a"}`), contentType: "application/json; charset=utf-8", want: payload{Name: "a"}},
		{name: "charset upper case and media type case", body: strings.NewReader(`{"name":"a"}`), contentType: "Application/JSON; charset=UTF-8", want: payload{Name: "a"}},
		{name: "trailing whitespace is fine", body: strings.NewReader("{\"name\":\"a\"}\n \t\n"), contentType: ct, want: payload{Name: "a"}},
		{name: "unknown length body", body: streamOf(`{"name":"a"}`), contentType: ct, want: payload{Name: "a"}},

		{name: "unknown field", body: strings.NewReader(`{"name":"a","colour":"red"}`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "malformed", body: strings.NewReader(`{"name":`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "syntax error", body: strings.NewReader(`{name: 1}`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "wrong type", body: strings.NewReader(`{"name":"a","n":"three"}`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "not an object", body: strings.NewReader(`[1,2]`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "two values", body: strings.NewReader(`{"name":"a"}{"name":"b"}`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "value then garbage", body: strings.NewReader(`{"name":"a"} xyz`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "value then second value with unknown field", body: strings.NewReader(`{"name":"a"} {"zzz":1}`), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "no body", body: nil, contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "no body and no content type", body: nil, contentType: "", wantStatus: 400, wantCode: "bad_request"},
		{name: "empty stream", body: streamOf(``), contentType: ct, wantStatus: 400, wantCode: "bad_request"},
		{name: "whitespace only", body: strings.NewReader("  \n"), contentType: ct, wantStatus: 400, wantCode: "bad_request"},

		{name: "missing content type", body: strings.NewReader(`{"name":"a"}`), contentType: "", wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "text/plain", body: strings.NewReader(`{"name":"a"}`), contentType: "text/plain", wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "json suffix type", body: strings.NewReader(`{"name":"a"}`), contentType: "application/vnd.api+json", wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "other charset", body: strings.NewReader(`{"name":"a"}`), contentType: "application/json; charset=iso-8859-1", wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "garbage content type", body: strings.NewReader(`{"name":"a"}`), contentType: ";;;", wantStatus: 415, wantCode: "unsupported_media_type"},

		{name: "over the default limit", body: streamOf(`{"name":"` + strings.Repeat("a", domain.MaxBodyBytes) + `"}`), contentType: ct, wantStatus: 413, wantCode: "payload_too_large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", tc.body)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			var got payload
			err := render.DecodeJSON(httptest.NewRecorder(), req, &got)
			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("DecodeJSON: %v", err)
				}
				if got != tc.want {
					t.Errorf("got %+v, want %+v", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("DecodeJSON succeeded with %+v, want status %d", got, tc.wantStatus)
			}
			status, body := render.MapError(err)
			if status != tc.wantStatus || body.Error.Code != tc.wantCode {
				t.Errorf("mapped to %d %s, want %d %s (err: %v)", status, body.Error.Code, tc.wantStatus, tc.wantCode, err)
			}
		})
	}
}

func TestDecodeJSONBodyLimitFromMiddlewareStyleReader(t *testing.T) {
	// A limit smaller than the default, as BodyLimit would set it, wins.
	req := httptest.NewRequest("POST", "/x", streamOf(`{"name":"0123456789"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(rec, req.Body, 8)
	var p payload
	err := render.DecodeJSON(rec, req, &p)
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) {
		t.Fatalf("err = %v, want *http.MaxBytesError", err)
	}
	if status, _ := render.MapError(err); status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d", status)
	}
}

func TestDecodeJSONBadDestinationIsInternal(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	err := render.DecodeJSON(httptest.NewRecorder(), req, payload{}) // not a pointer
	if err == nil {
		t.Fatal("want an error")
	}
	if status, _ := render.MapError(err); status != 500 {
		t.Errorf("status = %d, want 500 (a caller bug, not a client error)", status)
	}
}

func TestDecodeJSONReadFailureIsBadRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/x", struct{ io.Reader }{failingReader{}})
	req.Header.Set("Content-Type", "application/json")
	err := render.DecodeJSON(httptest.NewRecorder(), req, &payload{})
	if status, _ := render.MapError(err); status != 400 {
		t.Errorf("status = %d, want 400 (err: %v)", status, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	render.WriteJSON(rec, http.StatusCreated, map[string]any{"id": "a", "n": 1})
	if rec.Code != 201 {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff")
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"a","n":1}` {
		t.Errorf("body = %s", got)
	}
}

func TestWriteJSONEncodeFailureIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	render.WriteJSON(rec, 200, map[string]any{"bad": make(chan int)})
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestNoContent(t *testing.T) {
	rec := httptest.NewRecorder()
	render.NoContent(rec)
	if rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("got %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}
