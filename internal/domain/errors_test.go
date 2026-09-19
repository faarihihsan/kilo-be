package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// kindCase pairs an error with its sentinel so the kinds can be checked
// against each other.
type kindCase struct {
	name     string
	err      error
	sentinel error
}

func kindCases() []kindCase {
	return []kindCase{
		{"NotFound", NewNotFound(), ErrNotFound},
		{"Conflict", NewConflict(IssueStale), ErrConflict},
		{"Validation", NewValidation("name", IssueRequired), ErrValidation},
		{"Forbidden", NewForbidden(), ErrForbidden},
		{"Unauthorized", NewUnauthorized(), ErrUnauthorized},
		{"RateLimited", NewRateLimited(time.Minute), ErrRateLimited},
		{"BadRequest", NewBadRequest("malformed JSON"), ErrBadRequest},
		{"PayloadTooLarge", NewPayloadTooLarge(), ErrPayloadTooLarge},
		{"UnsupportedMediaType", NewUnsupportedMediaType(), ErrUnsupportedMediaType},
	}
}

func TestErrorsMatchOnlyTheirOwnSentinel(t *testing.T) {
	cases := kindCases()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Error() == "" {
				t.Error("Error() is empty")
			}
			for _, other := range cases {
				got := errors.Is(tc.err, other.sentinel)
				want := other.name == tc.name
				if got != want {
					t.Errorf("errors.Is(%s, Err%s) = %v, want %v", tc.name, other.name, got, want)
				}
			}
		})
	}
}

func TestErrorsSurviveWrapping(t *testing.T) {
	for _, tc := range kindCases() {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("layer two: %w", fmt.Errorf("layer one: %w", tc.err))
			if !errors.Is(wrapped, tc.sentinel) {
				t.Errorf("errors.Is lost the sentinel through wrapping")
			}
			joined := errors.Join(errors.New("unrelated"), wrapped)
			if !errors.Is(joined, tc.sentinel) {
				t.Errorf("errors.Is lost the sentinel through errors.Join")
			}
		})
	}
}

func TestErrorsAsReturnsTypedValue(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		var target *NotFoundError
		if !errors.As(fmt.Errorf("get: %w", NewNotFound()), &target) {
			t.Fatal("errors.As failed")
		}
	})
	t.Run("Forbidden", func(t *testing.T) {
		var target *ForbiddenError
		if !errors.As(fmt.Errorf("x: %w", NewForbidden()), &target) {
			t.Fatal("errors.As failed")
		}
	})
	t.Run("Unauthorized", func(t *testing.T) {
		var target *UnauthorizedError
		if !errors.As(fmt.Errorf("x: %w", NewUnauthorized()), &target) {
			t.Fatal("errors.As failed")
		}
	})
	t.Run("BadRequest", func(t *testing.T) {
		var target *BadRequestError
		if !errors.As(fmt.Errorf("x: %w", NewBadRequest("bad cursor")), &target) {
			t.Fatal("errors.As failed")
		}
		if target.Message != "bad cursor" {
			t.Errorf("Message = %q, want %q", target.Message, "bad cursor")
		}
	})
	t.Run("PayloadTooLarge", func(t *testing.T) {
		var target *PayloadTooLargeError
		if !errors.As(fmt.Errorf("x: %w", NewPayloadTooLarge()), &target) {
			t.Fatal("errors.As failed")
		}
	})
	t.Run("UnsupportedMediaType", func(t *testing.T) {
		var target *UnsupportedMediaTypeError
		if !errors.As(fmt.Errorf("x: %w", NewUnsupportedMediaType()), &target) {
			t.Fatal("errors.As failed")
		}
	})
	t.Run("does not match another type", func(t *testing.T) {
		var target *ConflictError
		if errors.As(fmt.Errorf("x: %w", NewNotFound()), &target) {
			t.Fatal("errors.As matched the wrong type")
		}
	})
}

func TestMessageOverridesDefault(t *testing.T) {
	e := &NotFoundError{Message: "exercise not found"}
	if got := e.Error(); got != "exercise not found" {
		t.Errorf("Error() = %q", got)
	}
	if got := NewNotFound().Error(); got != "not found" {
		t.Errorf("default Error() = %q", got)
	}
}

func TestConflict(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		e := NewConflict(IssueDeleted)
		if e.Issue != IssueDeleted {
			t.Errorf("Issue = %q", e.Issue)
		}
		if e.Field != "" || e.ExistingID != uuid.Nil || e.Current != nil {
			t.Errorf("optional fields set: %+v", e)
		}
		if got := e.Details(); !slices.Equal(got, []FieldIssue{{Issue: "deleted"}}) {
			t.Errorf("Details() = %v", got)
		}
		if got := e.Error(); got != "conflict: deleted" {
			t.Errorf("Error() = %q", got)
		}
	})

	t.Run("with field", func(t *testing.T) {
		e := NewConflict(IssueAlreadyTaken, OnField("username"))
		if got := e.Details(); !slices.Equal(got, []FieldIssue{{Field: "username", Issue: "already_taken"}}) {
			t.Errorf("Details() = %v", got)
		}
		if got := e.Error(); got != "conflict: username: already_taken" {
			t.Errorf("Error() = %q", got)
		}
	})

	t.Run("with existing id", func(t *testing.T) {
		id := uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010")
		e := NewConflict(IssueAlreadyExists, OnField("name"), WithExistingID(id))
		var target *ConflictError
		if !errors.As(fmt.Errorf("create: %w", e), &target) {
			t.Fatal("errors.As failed")
		}
		if target.ExistingID != id || target.Field != "name" || target.Issue != IssueAlreadyExists {
			t.Errorf("got %+v", target)
		}
	})

	t.Run("with current", func(t *testing.T) {
		type snapshot struct{ Name string }
		cur := snapshot{Name: "Push"}
		e := NewConflict(IssueStale, WithCurrent(cur))
		got, ok := e.Current.(snapshot)
		if !ok || got != cur {
			t.Errorf("Current = %#v, want %#v", e.Current, cur)
		}
	})

	t.Run("message wins in Error", func(t *testing.T) {
		e := NewConflict(IssueStale)
		e.Message = "stale write"
		if e.Error() != "stale write" {
			t.Errorf("Error() = %q", e.Error())
		}
	})
}

func TestValidation(t *testing.T) {
	t.Run("single issue", func(t *testing.T) {
		e := NewValidation("username", IssueReserved)
		want := []FieldIssue{{Field: "username", Issue: "reserved"}}
		if !slices.Equal(e.Issues, want) {
			t.Errorf("Issues = %v, want %v", e.Issues, want)
		}
		if got := e.Error(); got != "validation failed: username: reserved" {
			t.Errorf("Error() = %q", got)
		}
	})

	t.Run("many issues", func(t *testing.T) {
		e := NewValidationIssues(
			FieldIssue{Field: "name", Issue: IssueRequired},
			FieldIssue{Field: "category", Issue: IssueInvalidValue},
			FieldIssue{Issue: IssueTooManyDecimals},
		)
		if len(e.Issues) != 3 {
			t.Fatalf("len(Issues) = %d, want 3", len(e.Issues))
		}
		if got := e.Error(); got != "validation failed: name: required; category: invalid_value; too_many_decimals" {
			t.Errorf("Error() = %q", got)
		}
	})

	t.Run("collector", func(t *testing.T) {
		var v ValidationError
		if !v.Empty() || v.Err() != nil {
			t.Fatal("zero value should be empty and Err() nil")
		}
		v.Add("name", IssueRequired)
		v.Add(FieldIndex("exercises", 1)+".position", IssueDuplicate)
		if v.Empty() {
			t.Fatal("Empty() = true after Add")
		}
		err := v.Err()
		var target *ValidationError
		if !errors.As(err, &target) || !errors.Is(err, ErrValidation) {
			t.Fatalf("Err() = %v, want a *ValidationError", err)
		}
		want := []FieldIssue{
			{Field: "name", Issue: "required"},
			{Field: "exercises[1].position", Issue: "duplicate"},
		}
		if !slices.Equal(target.Issues, want) {
			t.Errorf("Issues = %v, want %v", target.Issues, want)
		}
	})

	t.Run("Err on nil receiver returns a nil interface", func(t *testing.T) {
		var v *ValidationError
		if err := v.Err(); err != nil {
			t.Errorf("Err() = %v, want nil", err)
		}
	})

	t.Run("no issues still has a message", func(t *testing.T) {
		if got := (&ValidationError{}).Error(); got != "validation failed" {
			t.Errorf("Error() = %q", got)
		}
	})
}

func TestFieldIndex(t *testing.T) {
	got := FieldIndex("exercises", 2) + "." + FieldIndex("sets", 0) + ".rpe"
	if want := "exercises[2].sets[0].rpe"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestFieldIssueJSONShape(t *testing.T) {
	// FieldIssue is marshalled as-is by the error mapper: field is omitted
	// when empty, issue is always present.
	b, err := json.Marshal(FieldIssue{Issue: IssueStale})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"issue":"stale"}` {
		t.Errorf("json = %s", b)
	}
	b, err = json.Marshal(FieldIssue{Field: "username", Issue: IssueReserved})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"field":"username","issue":"reserved"}` {
		t.Errorf("json = %s", b)
	}
}

func TestRateLimitedRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int
	}{
		{-time.Second, 0},
		{0, 0},
		{time.Nanosecond, 1},
		{500 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{15 * time.Minute, 900},
		{15*time.Minute + time.Millisecond, 901},
	}
	for _, tc := range tests {
		if got := NewRateLimited(tc.in).RetryAfterSeconds(); got != tc.want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}

	e := NewRateLimited(15 * time.Minute)
	if e.RetryAfter != 15*time.Minute {
		t.Errorf("RetryAfter = %v", e.RetryAfter)
	}
	if !strings.Contains(e.Error(), "15m0s") {
		t.Errorf("Error() = %q, want it to mention the wait", e.Error())
	}
}

func TestIssueConstantsAreDistinctSnakeCase(t *testing.T) {
	issues := []string{
		IssueStale, IssueDeleted, IssueAlreadyExists, IssueAlreadyTaken, IssueIDTaken,
		IssueRequired, IssueTooShort, IssueTooLong, IssueInvalidChars, IssueInvalidFormat,
		IssueInvalidValue, IssueReserved, IssuePlanLimit, IssueInvalidImage,
		IssueTooLargeDimensions, IssueOutOfRange, IssueTooMany, IssueDuplicate,
		IssueContainsPrimary, IssueTooManyDecimals, IssueTooFarInFuture, IssueUnknownReference,
	}
	seen := map[string]bool{}
	for _, s := range issues {
		if s == "" || s != strings.ToLower(s) || strings.ContainsAny(s, " -") {
			t.Errorf("issue %q is not lowercase snake_case", s)
		}
		if seen[s] {
			t.Errorf("issue %q is defined twice", s)
		}
		seen[s] = true
	}
	// The spec-named strings, spelled out so a typo in a constant is caught.
	for _, s := range []string{
		"stale", "deleted", "already_exists", "already_taken", "id_taken",
		"required", "too_short", "too_long", "invalid_chars", "invalid_format",
		"invalid_value", "reserved", "plan_limit", "invalid_image", "too_large_dimensions",
	} {
		if !seen[s] {
			t.Errorf("spec issue %q has no constant", s)
		}
	}
}
