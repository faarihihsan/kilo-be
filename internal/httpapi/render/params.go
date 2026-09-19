package render

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

// Page is the parsed pagination query of a list endpoint. Cursor is opaque
// here: decoding it (and answering 400 for a bad one) is up to the caller.
type Page struct {
	Limit  int
	Cursor string
}

// ParsePage reads `limit` and `cursor` from the query string. limit defaults
// to domain.DefaultPageLimit; a value that is not an integer, or outside
// [domain.MinPageLimit, domain.MaxPageLimit], is a 422 validation_failed on
// field `limit` (it is rejected, not clamped).
func ParsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()
	p := Page{Limit: domain.DefaultPageLimit, Cursor: q.Get("cursor")}
	if !q.Has("limit") {
		return p, nil
	}
	n, err := strconv.Atoi(q.Get("limit"))
	if err != nil {
		return Page{}, domain.NewValidation("limit", domain.IssueInvalidFormat)
	}
	if n < domain.MinPageLimit || n > domain.MaxPageLimit {
		return Page{}, domain.NewValidation("limit", domain.IssueOutOfRange)
	}
	p.Limit = n
	return p, nil
}

// ParseBool reads an optional boolean query parameter (`include_deleted`). An
// absent parameter gives def; otherwise the value must be true or false
// (strconv.ParseBool spellings are not accepted), else 422 validation_failed
// with issue invalid_format on that field.
func ParseBool(r *http.Request, name string, def bool) (bool, error) {
	q := r.URL.Query()
	if !q.Has(name) {
		return def, nil
	}
	switch q.Get(name) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, domain.NewValidation(name, domain.IssueInvalidFormat)
}

// ParseTimeParam reads an optional RFC 3339 timestamp query parameter
// (`updated_since`, `from`, `to`) and returns it in UTC, or nil when the
// parameter is absent. A value that is not RFC 3339 is a 400 bad_request, as
// the list specs (05, 06, 08) say for "bad timestamp".
//
// Note for clients: a "+" in a UTC offset must be URL-encoded (%2B), or the
// query string decodes it to a space.
func ParseTimeParam(r *http.Request, name string) (*time.Time, error) {
	q := r.URL.Query()
	if !q.Has(name) {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, q.Get(name))
	if err != nil {
		return nil, domain.NewBadRequest(fmt.Sprintf("Query parameter %q must be an RFC 3339 timestamp.", name))
	}
	t = t.UTC()
	return &t, nil
}

// PathUUID reads a UUID path value (`{id}`). Only the canonical hyphenated
// form is accepted (upper case is folded); anything else is a 400
// bad_request, as the specs say for a bad path id.
func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	s := r.PathValue(name)
	// uuid.Parse also takes urn:uuid:, braces and bare hex; the API does not.
	if len(s) == 36 {
		if id, err := uuid.Parse(s); err == nil {
			return id, nil
		}
	}
	return uuid.Nil, domain.NewBadRequest(fmt.Sprintf("Path parameter %q must be a UUID.", name))
}
