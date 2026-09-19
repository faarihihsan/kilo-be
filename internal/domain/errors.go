package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Typed errors returned by services and stores. One place in httpapi maps them
// to the JSON error format and status codes in docs/api/conventions.md; any
// other error is a 500 `internal` and is never shown to the client.
//
// All are pointer types. Detect them with errors.As (to read fields) or with
// errors.Is against the Err* sentinel of the same kind, and wrap them freely
// with fmt.Errorf("...: %w", err).
//
// Message, when set, is text safe to show to the client. Error() is for logs
// and may combine more; the HTTP layer should not send it.

// Sentinels for errors.Is; each typed error below matches its own sentinel.
var (
	ErrNotFound             = errors.New("not found")
	ErrConflict             = errors.New("conflict")
	ErrValidation           = errors.New("validation failed")
	ErrForbidden            = errors.New("forbidden")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrRateLimited          = errors.New("rate limited")
	ErrBadRequest           = errors.New("bad request")
	ErrPayloadTooLarge      = errors.New("payload too large")
	ErrUnsupportedMediaType = errors.New("unsupported media type")
)

func orDefault(msg, def string) string {
	if msg != "" {
		return msg
	}
	return def
}

// NotFoundError is a missing resource, or one owned by another user (404).
type NotFoundError struct{ Message string }

func NewNotFound() *NotFoundError { return &NotFoundError{} }

func (e *NotFoundError) Error() string        { return orDefault(e.Message, "not found") }
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ForbiddenError is an authenticated caller with the wrong role (403).
type ForbiddenError struct{ Message string }

func NewForbidden() *ForbiddenError { return &ForbiddenError{} }

func (e *ForbiddenError) Error() string        { return orDefault(e.Message, "forbidden") }
func (e *ForbiddenError) Is(target error) bool { return target == ErrForbidden }

// UnauthorizedError is a missing, invalid, expired or revoked token, or bad
// credentials (401). Login must give the same message for an unknown user and
// a wrong password.
type UnauthorizedError struct{ Message string }

func NewUnauthorized() *UnauthorizedError { return &UnauthorizedError{} }

func (e *UnauthorizedError) Error() string        { return orDefault(e.Message, "unauthorized") }
func (e *UnauthorizedError) Is(target error) bool { return target == ErrUnauthorized }

// BadRequestError is a malformed request: bad JSON, unknown field, bad cursor,
// bad timestamp, bad path id (400). Message says what is wrong.
type BadRequestError struct{ Message string }

func NewBadRequest(message string) *BadRequestError { return &BadRequestError{Message: message} }

func (e *BadRequestError) Error() string        { return orDefault(e.Message, "bad request") }
func (e *BadRequestError) Is(target error) bool { return target == ErrBadRequest }

// PayloadTooLargeError is a body over the limit (413).
type PayloadTooLargeError struct{ Message string }

func NewPayloadTooLarge() *PayloadTooLargeError { return &PayloadTooLargeError{} }

func (e *PayloadTooLargeError) Error() string        { return orDefault(e.Message, "payload too large") }
func (e *PayloadTooLargeError) Is(target error) bool { return target == ErrPayloadTooLarge }

// UnsupportedMediaTypeError is a wrong Content-Type or file type (415).
type UnsupportedMediaTypeError struct{ Message string }

func NewUnsupportedMediaType() *UnsupportedMediaTypeError { return &UnsupportedMediaTypeError{} }

func (e *UnsupportedMediaTypeError) Error() string {
	return orDefault(e.Message, "unsupported media type")
}
func (e *UnsupportedMediaTypeError) Is(target error) bool { return target == ErrUnsupportedMediaType }

// RateLimitedError is a locked-out or throttled caller (429). The HTTP layer
// sends RetryAfterSeconds() as the Retry-After header.
type RateLimitedError struct {
	Message    string
	RetryAfter time.Duration
}

func NewRateLimited(retryAfter time.Duration) *RateLimitedError {
	return &RateLimitedError{RetryAfter: retryAfter}
}

// RetryAfterSeconds is RetryAfter rounded up to whole seconds, never negative.
func (e *RateLimitedError) RetryAfterSeconds() int {
	if e.RetryAfter <= 0 {
		return 0
	}
	return int((e.RetryAfter + time.Second - 1) / time.Second)
}

func (e *RateLimitedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "rate limited, retry after " + e.RetryAfter.String()
}
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// FieldIssue is one entry of `error.details`. Field is a JSON field path and
// may be empty when the issue is about the request as a whole.
type FieldIssue struct {
	Field string `json:"field,omitempty"`
	Issue string `json:"issue"`
}

// FieldIndex returns "field[i]", for building nested paths such as
// FieldIndex("exercises", 2) + "." + FieldIndex("sets", 0) + ".rpe".
func FieldIndex(field string, i int) string { return fmt.Sprintf("%s[%d]", field, i) }

// ConflictError is a duplicate, a stale write or a write to a deleted row
// (409). Issue is one of the conflict Issue* constants.
type ConflictError struct {
	Message string
	Issue   string
	// Field names the conflicting request field, for example "username".
	Field string
	// ExistingID is the id of the row that caused the conflict (exercise name
	// taken). uuid.Nil means not set.
	ExistingID uuid.UUID
	// Current is the server's copy of the row for a stale write, in its
	// response shape. nil means not set.
	Current any
}

// ConflictOption sets an optional part of a ConflictError.
type ConflictOption func(*ConflictError)

// OnField sets ConflictError.Field.
func OnField(field string) ConflictOption { return func(e *ConflictError) { e.Field = field } }

// WithExistingID sets ConflictError.ExistingID.
func WithExistingID(id uuid.UUID) ConflictOption {
	return func(e *ConflictError) { e.ExistingID = id }
}

// WithCurrent sets ConflictError.Current.
func WithCurrent(current any) ConflictOption { return func(e *ConflictError) { e.Current = current } }

func NewConflict(issue string, opts ...ConflictOption) *ConflictError {
	e := &ConflictError{Issue: issue}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Details is the single {field, issue} entry describing the conflict, in the
// same shape as validation details.
func (e *ConflictError) Details() []FieldIssue {
	return []FieldIssue{{Field: e.Field, Issue: e.Issue}}
}

func (e *ConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Field != "" {
		return "conflict: " + e.Field + ": " + e.Issue
	}
	return "conflict: " + e.Issue
}
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// ValidationError is well-formed input with invalid values (422). It carries
// one entry per problem so the client can show them all at once.
type ValidationError struct {
	Message string
	Issues  []FieldIssue
}

// NewValidation returns an error with a single issue.
func NewValidation(field, issue string) *ValidationError {
	return &ValidationError{Issues: []FieldIssue{{Field: field, Issue: issue}}}
}

// NewValidationIssues returns an error with all the given issues.
func NewValidationIssues(issues ...FieldIssue) *ValidationError {
	return &ValidationError{Issues: issues}
}

// Add appends an issue. The zero ValidationError is ready to use as a
// collector: call Add for each problem, then return Err().
func (e *ValidationError) Add(field, issue string) {
	e.Issues = append(e.Issues, FieldIssue{Field: field, Issue: issue})
}

// Empty reports whether no issue was added.
func (e *ValidationError) Empty() bool { return e == nil || len(e.Issues) == 0 }

// Err returns nil when no issue was added, otherwise e. It returns a plain
// nil interface, so `return v.Err()` is safe.
func (e *ValidationError) Err() error {
	if e.Empty() {
		return nil
	}
	return e
}

func (e *ValidationError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if len(e.Issues) == 0 {
		return "validation failed"
	}
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		if is.Field != "" {
			parts[i] = is.Field + ": " + is.Issue
		} else {
			parts[i] = is.Issue
		}
	}
	return "validation failed: " + strings.Join(parts, "; ")
}
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }
