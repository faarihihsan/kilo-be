package render_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/render"
)

func mustJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMapError(t *testing.T) {
	existing := uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010")
	serverCopy := map[string]any{"id": "x", "name": "Bench", "updated_at": "2026-09-19T10:00:00Z"}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string // exact JSON
	}{
		{
			name:       "not found default message",
			err:        domain.NewNotFound(),
			wantStatus: 404,
			wantBody:   `{"error":{"code":"not_found","message":"The resource was not found."}}`,
		},
		{
			name:       "not found custom message",
			err:        &domain.NotFoundError{Message: "No such user."},
			wantStatus: 404,
			wantBody:   `{"error":{"code":"not_found","message":"No such user."}}`,
		},
		{
			name:       "conflict with field",
			err:        domain.NewConflict(domain.IssueAlreadyTaken, domain.OnField("username")),
			wantStatus: 409,
			wantBody: `{"error":{"code":"conflict","message":"The request conflicts with the current state of the resource.",
				"details":[{"field":"username","issue":"already_taken"}]}}`,
		},
		{
			name:       "conflict already_exists carries existing_id",
			err:        domain.NewConflict(domain.IssueAlreadyExists, domain.OnField("name"), domain.WithExistingID(existing)),
			wantStatus: 409,
			wantBody: `{"error":{"code":"conflict","message":"The request conflicts with the current state of the resource.",
				"details":[{"field":"name","issue":"already_exists","existing_id":"0195f3a2-bbbb-7000-8000-000000000010"}]}}`,
		},
		{
			name:       "conflict stale carries current",
			err:        domain.NewConflict(domain.IssueStale, domain.WithCurrent(serverCopy)),
			wantStatus: 409,
			wantBody: `{"error":{"code":"conflict","message":"The request conflicts with the current state of the resource.",
				"details":[{"issue":"stale","current":{"id":"x","name":"Bench","updated_at":"2026-09-19T10:00:00Z"}}]}}`,
		},
		{
			name:       "conflict deleted with message",
			err:        &domain.ConflictError{Message: "The session was deleted.", Issue: domain.IssueDeleted},
			wantStatus: 409,
			wantBody:   `{"error":{"code":"conflict","message":"The session was deleted.","details":[{"issue":"deleted"}]}}`,
		},
		{
			name: "validation with all issues",
			err: domain.NewValidationIssues(
				domain.FieldIssue{Field: "username", Issue: domain.IssueInvalidFormat},
				domain.FieldIssue{Field: domain.FieldIndex("exercises", 2) + ".sets[0].rpe", Issue: domain.IssueOutOfRange},
				domain.FieldIssue{Issue: domain.IssueRequired},
			),
			wantStatus: 422,
			wantBody: `{"error":{"code":"validation_failed","message":"The request has invalid values.","details":[
				{"field":"username","issue":"invalid_format"},
				{"field":"exercises[2].sets[0].rpe","issue":"out_of_range"},
				{"issue":"required"}]}}`,
		},
		{
			name:       "validation without issues has no details",
			err:        &domain.ValidationError{},
			wantStatus: 422,
			wantBody:   `{"error":{"code":"validation_failed","message":"The request has invalid values."}}`,
		},
		{
			name:       "bad request",
			err:        domain.NewBadRequest("Bad cursor."),
			wantStatus: 400,
			wantBody:   `{"error":{"code":"bad_request","message":"Bad cursor."}}`,
		},
		{
			name:       "unauthorized",
			err:        domain.NewUnauthorized(),
			wantStatus: 401,
			wantBody:   `{"error":{"code":"unauthorized","message":"Authentication is required."}}`,
		},
		{
			name:       "unauthorized custom message",
			err:        &domain.UnauthorizedError{Message: "Invalid username or password."},
			wantStatus: 401,
			wantBody:   `{"error":{"code":"unauthorized","message":"Invalid username or password."}}`,
		},
		{
			name:       "forbidden",
			err:        domain.NewForbidden(),
			wantStatus: 403,
			wantBody:   `{"error":{"code":"forbidden","message":"You are not allowed to perform this action."}}`,
		},
		{
			name:       "payload too large",
			err:        domain.NewPayloadTooLarge(),
			wantStatus: 413,
			wantBody:   `{"error":{"code":"payload_too_large","message":"The request body is too large."}}`,
		},
		{
			name:       "unsupported media type",
			err:        domain.NewUnsupportedMediaType(),
			wantStatus: 415,
			wantBody:   `{"error":{"code":"unsupported_media_type","message":"The content type is not supported."}}`,
		},
		{
			name:       "rate limited",
			err:        domain.NewRateLimited(90 * time.Second),
			wantStatus: 429,
			wantBody:   `{"error":{"code":"rate_limited","message":"Too many requests. Try again later."}}`,
		},
		{
			name:       "max bytes error",
			err:        &http.MaxBytesError{Limit: 1048576},
			wantStatus: 413,
			wantBody:   `{"error":{"code":"payload_too_large","message":"The request body must not exceed 1048576 bytes."}}`,
		},
		{
			name:       "json syntax error",
			err:        &json.SyntaxError{Offset: 7},
			wantStatus: 400,
			wantBody:   `{"error":{"code":"bad_request","message":"Malformed JSON at offset 7."}}`,
		},
		{
			name:       "json type error",
			err:        &json.UnmarshalTypeError{Field: "exercises.sets.reps", Value: "string"},
			wantStatus: 400,
			wantBody:   `{"error":{"code":"bad_request","message":"Field \"exercises.sets.reps\" has the wrong JSON type."}}`,
		},
		{
			name:       "json unknown field",
			err:        errors.New(`json: unknown field "colour"`),
			wantStatus: 400,
			wantBody:   `{"error":{"code":"bad_request","message":"Unknown field \"colour\"."}}`,
		},
		{
			name:       "wrapped not found",
			err:        fmt.Errorf("get plan: %w", fmt.Errorf("store: %w", domain.NewNotFound())),
			wantStatus: 404,
			wantBody:   `{"error":{"code":"not_found","message":"The resource was not found."}}`,
		},
		{
			name:       "wrapped validation",
			err:        fmt.Errorf("save: %w", domain.NewValidation("name", domain.IssueTooLong)),
			wantStatus: 422,
			wantBody: `{"error":{"code":"validation_failed","message":"The request has invalid values.",
				"details":[{"field":"name","issue":"too_long"}]}}`,
		},
		{
			name:       "wrapped max bytes error",
			err:        fmt.Errorf("read body: %w", &http.MaxBytesError{Limit: 5}),
			wantStatus: 413,
			wantBody:   `{"error":{"code":"payload_too_large","message":"The request body must not exceed 5 bytes."}}`,
		},
		{
			name:       "joined errors still find a domain error",
			err:        errors.Join(errors.New("cleanup failed"), domain.NewForbidden()),
			wantStatus: 403,
			wantBody:   `{"error":{"code":"forbidden","message":"You are not allowed to perform this action."}}`,
		},
		{
			name:       "unknown error",
			err:        errors.New(`pq: password authentication failed for user "workout"`),
			wantStatus: 500,
			wantBody:   `{"error":{"code":"internal","message":"Internal server error."}}`,
		},
		{
			name:       "wrapped unknown error",
			err:        fmt.Errorf("insert token: %w", errors.New("dial tcp 10.0.0.5:5432: connection refused")),
			wantStatus: 500,
			wantBody:   `{"error":{"code":"internal","message":"Internal server error."}}`,
		},
		{
			name:       "stray EOF from elsewhere stays a 500",
			err:        fmt.Errorf("query: %w", io.ErrUnexpectedEOF),
			wantStatus: 500,
			wantBody:   `{"error":{"code":"internal","message":"Internal server error."}}`,
		},
		{
			name:       "nil error",
			err:        nil,
			wantStatus: 500,
			wantBody:   `{"error":{"code":"internal","message":"Internal server error."}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := render.MapError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			var want any
			if err := json.Unmarshal([]byte(tc.wantBody), &want); err != nil {
				t.Fatalf("bad test JSON: %v", err)
			}
			if got := mustJSON(t, body); !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v\nwant   %v", got, want)
			}
		})
	}
}

func TestWriteErrorHeadersAndFormat(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantHeader map[string]string
		absent     []string
	}{
		{"unauthorized sets WWW-Authenticate", domain.NewUnauthorized(), 401, map[string]string{"WWW-Authenticate": "Bearer"}, []string{"Retry-After"}},
		{"wrapped unauthorized too", fmt.Errorf("auth: %w", domain.NewUnauthorized()), 401, map[string]string{"WWW-Authenticate": "Bearer"}, nil},
		{"rate limited sets Retry-After rounded up", domain.NewRateLimited(1500 * time.Millisecond), 429, map[string]string{"Retry-After": "2"}, []string{"WWW-Authenticate"}},
		{"rate limited without duration has no Retry-After", domain.NewRateLimited(0), 429, nil, []string{"Retry-After"}},
		{"not found has neither", domain.NewNotFound(), 404, nil, []string{"WWW-Authenticate", "Retry-After"}},
		{"forbidden has no WWW-Authenticate", domain.NewForbidden(), 403, nil, []string{"WWW-Authenticate"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			render.WriteError(rec, httptest.NewRequest("GET", "/x", nil), tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			for k, v := range tc.wantHeader {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			for _, k := range tc.absent {
				if got := rec.Header().Get(k); got != "" {
					t.Errorf("header %s = %q, want none", k, got)
				}
			}
			apitest.DecodeError(t, rec) // strict shape
		})
	}
}

func TestWriteErrorNeverLeaksInternalErrors(t *testing.T) {
	secret := `pq: password authentication failed for user "workout" at 10.0.0.5:5432`
	logger, logs := apitest.NewLogs()
	req := httptest.NewRequest("POST", "/v1/auth/login?x=1", nil)
	req = req.WithContext(render.WithLogger(req.Context(), logger.With("request_id", "rid-1")))

	rec := httptest.NewRecorder()
	render.WriteError(rec, req, fmt.Errorf("insert: %w", errors.New(secret)))

	apitest.RequireError(t, rec, 500, "internal")
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("response leaks internal error: %s", rec.Body.String())
	}

	e := logs.Find(t, "unhandled error")
	if e["level"] != "ERROR" || e["request_id"] != "rid-1" || e["path"] != "/v1/auth/login" || e["method"] != "POST" {
		t.Errorf("log entry = %v", e)
	}
	if !strings.Contains(fmt.Sprint(e["error"]), secret) {
		t.Errorf("original error is not logged: %v", e)
	}
}

func TestWriteErrorDoesNotLogClientErrors(t *testing.T) {
	logger, logs := apitest.NewLogs()
	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(render.WithLogger(req.Context(), logger))
	render.WriteError(httptest.NewRecorder(), req, domain.NewNotFound())
	if s := logs.String(); s != "" {
		t.Errorf("unexpected log output: %s", s)
	}
}

func TestWriteErrorResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	render.WriteErrorResponse(rec, 501, render.CodeNotImplemented, "nope")
	body := apitest.RequireError(t, rec, 501, "not_implemented")
	if body.Error.Message != "nope" || len(body.Error.Details) != 0 {
		t.Errorf("body = %+v", body)
	}
}
