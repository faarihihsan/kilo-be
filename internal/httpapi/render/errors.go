// Package render is the HTTP-facing toolbox shared by the router, the
// middleware and the handlers: JSON in and out, the one mapping from domain
// errors to the error format of docs/api/conventions.md, and parsing of path
// and query parameters.
//
// It imports domain only, so middleware and handlers can both import it
// without a cycle.
package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

// Stable machine values of error.code (docs/api/conventions.md).
const (
	CodeBadRequest           = "bad_request"
	CodeUnauthorized         = "unauthorized"
	CodeForbidden            = "forbidden"
	CodeNotFound             = "not_found"
	CodeConflict             = "conflict"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeValidationFailed     = "validation_failed"
	CodeRateLimited          = "rate_limited"
	CodeInternal             = "internal"

	// Not in the conventions table; used by the router for requests that
	// never reach a handler.
	CodeMethodNotAllowed = "method_not_allowed"
	CodeNotImplemented   = "not_implemented"
)

// Default messages, used when a domain error carries none.
const (
	msgBadRequest           = "The request is malformed."
	msgUnauthorized         = "Authentication is required."
	msgForbidden            = "You are not allowed to perform this action."
	msgNotFound             = "The resource was not found."
	msgConflict             = "The request conflicts with the current state of the resource."
	msgPayloadTooLarge      = "The request body is too large."
	msgUnsupportedMediaType = "The content type is not supported."
	msgValidationFailed     = "The request has invalid values."
	msgRateLimited          = "Too many requests. Try again later."

	// MsgInternal is the only text a client ever sees for an unexpected error.
	MsgInternal = "Internal server error."
)

// ErrorBody is the JSON body of every non-2xx response.
type ErrorBody struct {
	Error ErrorInfo `json:"error"`
}

// ErrorInfo is the content of ErrorBody.
type ErrorInfo struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []Detail `json:"details,omitempty"`
}

// Detail is one element of error.details. A conflict adds ExistingID
// (already_exists) or Current (stale) to its first element.
type Detail struct {
	Field      string `json:"field,omitempty"`
	Issue      string `json:"issue"`
	ExistingID string `json:"existing_id,omitempty"`
	Current    any    `json:"current,omitempty"`
}

// NewErrorBody builds a body without details.
func NewErrorBody(code, message string) ErrorBody {
	return ErrorBody{Error: ErrorInfo{Code: code, Message: message}}
}

// mapped is the full HTTP shape of an error: status, body and extra headers.
type mapped struct {
	status int
	body   ErrorBody
	header http.Header
}

// MapError is the single translation from an error to a status code and the
// JSON error body. Domain errors are found with errors.As, so they may be
// wrapped. It also understands *http.MaxBytesError (413) and the errors of
// encoding/json (400). Anything else is a 500 `internal` with a generic message:
// the original text is never part of the body.
//
// WriteError additionally sets the headers that go with some statuses
// (WWW-Authenticate on 401, Retry-After on 429); use it, not MapError, to
// answer a request.
func MapError(err error) (status int, body ErrorBody) {
	m := mapError(err)
	return m.status, m.body
}

func mapError(err error) mapped {
	var (
		notFound    *domain.NotFoundError
		conflict    *domain.ConflictError
		validation  *domain.ValidationError
		badRequest  *domain.BadRequestError
		unauth      *domain.UnauthorizedError
		forbidden   *domain.ForbiddenError
		tooLarge    *domain.PayloadTooLargeError
		unsupported *domain.UnsupportedMediaTypeError
		rateLimited *domain.RateLimitedError
		maxBytes    *http.MaxBytesError
	)

	switch {
	case err == nil:
		return internalError()

	case errors.As(err, &notFound):
		return simple(http.StatusNotFound, CodeNotFound, notFound.Message, msgNotFound)

	case errors.As(err, &conflict):
		m := simple(http.StatusConflict, CodeConflict, conflict.Message, msgConflict)
		if conflict.Issue != "" {
			d := Detail{Field: conflict.Field, Issue: conflict.Issue, Current: conflict.Current}
			if conflict.ExistingID != uuid.Nil {
				d.ExistingID = conflict.ExistingID.String()
			}
			m.body.Error.Details = []Detail{d}
		}
		return m

	case errors.As(err, &validation):
		m := simple(http.StatusUnprocessableEntity, CodeValidationFailed, validation.Message, msgValidationFailed)
		if len(validation.Issues) > 0 {
			m.body.Error.Details = make([]Detail, len(validation.Issues))
			for i, is := range validation.Issues {
				m.body.Error.Details[i] = Detail{Field: is.Field, Issue: is.Issue}
			}
		}
		return m

	case errors.As(err, &badRequest):
		return simple(http.StatusBadRequest, CodeBadRequest, badRequest.Message, msgBadRequest)

	case errors.As(err, &unauth):
		m := simple(http.StatusUnauthorized, CodeUnauthorized, unauth.Message, msgUnauthorized)
		m.header = http.Header{"Www-Authenticate": {"Bearer"}}
		return m

	case errors.As(err, &forbidden):
		return simple(http.StatusForbidden, CodeForbidden, forbidden.Message, msgForbidden)

	case errors.As(err, &tooLarge):
		return simple(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, tooLarge.Message, msgPayloadTooLarge)

	case errors.As(err, &unsupported):
		return simple(http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, unsupported.Message, msgUnsupportedMediaType)

	case errors.As(err, &rateLimited):
		m := simple(http.StatusTooManyRequests, CodeRateLimited, rateLimited.Message, msgRateLimited)
		if secs := rateLimited.RetryAfterSeconds(); secs > 0 {
			m.header = http.Header{"Retry-After": {strconv.Itoa(secs)}}
		}
		return m

	case errors.As(err, &maxBytes):
		return simple(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("The request body must not exceed %d bytes.", maxBytes.Limit), msgPayloadTooLarge)
	}

	if br := jsonBadRequest(err); br != nil {
		return simple(http.StatusBadRequest, CodeBadRequest, br.Message, msgBadRequest)
	}
	return internalError()
}

func simple(status int, code, message, fallback string) mapped {
	if message == "" {
		message = fallback
	}
	return mapped{status: status, body: NewErrorBody(code, message)}
}

func internalError() mapped {
	return simple(http.StatusInternalServerError, CodeInternal, MsgInternal, MsgInternal)
}

// WriteError answers the request with the JSON error for err (see MapError),
// plus WWW-Authenticate or Retry-After where the status calls for it. An
// unexpected error (a 500) is logged with the request-scoped logger, never
// sent to the client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	m := mapError(err)
	if m.status == http.StatusInternalServerError {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "unhandled error",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
	}
	writeMapped(w, m)
}

// WriteErrorResponse writes an error body that does not come from an error
// value (404 and 405 of the router, 501 stubs, the recovered panic).
func WriteErrorResponse(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, NewErrorBody(code, message))
}

// MsgNotImplemented is the message of the 501 stub answer.
const MsgNotImplemented = "This endpoint is not implemented yet."

// NotImplemented answers 501 with the error code `not_implemented`. It is the
// body of every stub handler that a resource task has not replaced yet, and
// the router's default for a route without a handler.
func NotImplemented(w http.ResponseWriter, _ *http.Request) {
	WriteErrorResponse(w, http.StatusNotImplemented, CodeNotImplemented, MsgNotImplemented)
}

func writeMapped(w http.ResponseWriter, m mapped) {
	for k, v := range m.header {
		w.Header()[k] = v
	}
	WriteJSON(w, m.status, m.body)
}

// jsonBadRequest turns the errors encoding/json reports for bad client input
// (syntax, wrong type, unknown field) into a 400 domain error, or returns nil
// for any other error. It deliberately does not look at io.EOF: a wrapped EOF
// from anywhere else (a lost database connection) must stay a 500. DecodeJSON
// handles the EOFs of its own reads.
func jsonBadRequest(err error) *domain.BadRequestError {
	var (
		syntax  *json.SyntaxError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &syntax):
		return domain.NewBadRequest(fmt.Sprintf("Malformed JSON at offset %d.", syntax.Offset))
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return domain.NewBadRequest(fmt.Sprintf("Field %q has the wrong JSON type.", clip(typeErr.Field)))
		}
		return domain.NewBadRequest("The JSON value has the wrong type.")
	}
	// encoding/json has no typed error for DisallowUnknownFields.
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		name := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
		return domain.NewBadRequest(fmt.Sprintf("Unknown field %q.", clip(name)))
	}
	return nil
}

// clip keeps client-supplied names short before they are echoed back.
func clip(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// loggerKey is the context key of the request-scoped logger.
type loggerKey struct{}

// WithLogger returns a context carrying the request-scoped logger (the
// RequestID middleware sets one with the request id attached).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFrom returns the request-scoped logger, or slog.Default() when none
// was set.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
