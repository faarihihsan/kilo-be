package render_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

func withQuery(q string) *http.Request { return httptest.NewRequest("GET", "/x?"+q, nil) }

func asValidation(t *testing.T, err error) *domain.ValidationError {
	t.Helper()
	status, _ := render.MapError(err)
	if status != 422 {
		t.Fatalf("err %v maps to %d, want 422", err, status)
	}
	var v *domain.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("err %v is not a *domain.ValidationError", err)
	}
	return v
}

func TestParsePage(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		want      render.Page
		wantIssue string // "" = success
	}{
		{"defaults", "", render.Page{Limit: 50}, ""},
		{"limit only", "limit=10", render.Page{Limit: 10}, ""},
		{"min", "limit=1", render.Page{Limit: 1}, ""},
		{"max", "limit=200", render.Page{Limit: 200}, ""},
		{"cursor passes through untouched", "cursor=AbC_-9%3D%3D&limit=5", render.Page{Limit: 5, Cursor: "AbC_-9=="}, ""},
		{"cursor without limit", "cursor=xyz", render.Page{Limit: 50, Cursor: "xyz"}, ""},
		{"zero", "limit=0", render.Page{}, domain.IssueOutOfRange},
		{"above max", "limit=201", render.Page{}, domain.IssueOutOfRange},
		{"negative", "limit=-1", render.Page{}, domain.IssueOutOfRange},
		{"huge", "limit=99999999999999999999", render.Page{}, domain.IssueInvalidFormat},
		{"not a number", "limit=abc", render.Page{}, domain.IssueInvalidFormat},
		{"float", "limit=1.5", render.Page{}, domain.IssueInvalidFormat},
		{"empty value", "limit=", render.Page{}, domain.IssueInvalidFormat},
		{"spaces", "limit=%2010", render.Page{}, domain.IssueInvalidFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := render.ParsePage(withQuery(tc.query))
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("ParsePage: %v", err)
				}
				if got != tc.want {
					t.Errorf("got %+v, want %+v", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParsePage(%q) = %+v, want error", tc.query, got)
			}
			v := asValidation(t, err)
			want := []domain.FieldIssue{{Field: "limit", Issue: tc.wantIssue}}
			if !reflect.DeepEqual(v.Issues, want) {
				t.Errorf("issues = %+v, want %+v", v.Issues, want)
			}
		})
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		query   string
		def     bool
		want    bool
		wantErr bool
	}{
		{"", false, false, false},
		{"", true, true, false},
		{"include_deleted=true", false, true, false},
		{"include_deleted=false", true, false, false},
		{"include_deleted=TRUE", false, false, true},
		{"include_deleted=1", false, false, true},
		{"include_deleted=yes", true, false, true},
		{"include_deleted=", true, false, true},
	}
	for _, tc := range tests {
		got, err := render.ParseBool(withQuery(tc.query), "include_deleted", tc.def)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error", tc.query)
				continue
			}
			v := asValidation(t, err)
			if want := []domain.FieldIssue{{Field: "include_deleted", Issue: domain.IssueInvalidFormat}}; !reflect.DeepEqual(v.Issues, want) {
				t.Errorf("%q: issues = %+v", tc.query, v.Issues)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q (def %v) = %v, %v; want %v", tc.query, tc.def, got, err, tc.want)
		}
	}
}

func TestParseTimeParam(t *testing.T) {
	utc := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		v = v.UTC()
		return &v
	}
	tests := []struct {
		name    string
		query   string
		want    *time.Time
		wantErr bool
	}{
		{"absent", "", nil, false},
		{"utc", "updated_since=2026-09-19T08:30:00Z", utc("2026-09-19T08:30:00Z"), false},
		{"epoch", "updated_since=1970-01-01T00:00:00Z", utc("1970-01-01T00:00:00Z"), false},
		{"fraction", "updated_since=2026-09-19T08:30:00.123456Z", utc("2026-09-19T08:30:00.123456Z"), false},
		{"offset is converted to UTC", "updated_since=2026-09-19T10:30:00%2B02:00", utc("2026-09-19T08:30:00Z"), false},
		{"negative offset", "updated_since=2026-09-19T03:30:00-05:00", utc("2026-09-19T08:30:00Z"), false},
		{"plus decoded to space", "updated_since=2026-09-19T10:30:00+02:00", nil, true},
		{"date only", "updated_since=2026-09-19", nil, true},
		{"no zone", "updated_since=2026-09-19T08:30:00", nil, true},
		{"unix seconds", "updated_since=1789806600", nil, true},
		{"garbage", "updated_since=yesterday", nil, true},
		{"empty", "updated_since=", nil, true},
		{"month 13", "updated_since=2026-13-01T00:00:00Z", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := render.ParseTimeParam(withQuery(tc.query), "updated_since")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want error", got)
				}
				status, body := render.MapError(err)
				if status != 400 || body.Error.Code != "bad_request" {
					t.Errorf("mapped to %d %s, want 400 bad_request", status, body.Error.Code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && (!got.Equal(*tc.want) || got.Location() != time.UTC)) {
				t.Errorf("got %v, want %v (UTC)", got, tc.want)
			}
		})
	}
}

func TestPathUUID(t *testing.T) {
	const canon = "0195f3a2-bbbb-7000-8000-000000000010"
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"canonical", canon, canon, false},
		{"upper case is folded", "0195F3A2-BBBB-7000-8000-000000000010", canon, false},
		{"empty", "", "", true},
		{"not a uuid", "abc", "", true},
		{"bare hex", "0195f3a2bbbb70008000000000000010", "", true},
		{"urn form", "urn:uuid:" + canon, "", true},
		{"braces", "{" + canon + "}", "", true},
		{"bad hex digit", "0195f3a2-bbbb-7000-8000-00000000001z", "", true},
		{"trailing space", canon + " ", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			req.SetPathValue("id", tc.value)
			got, err := render.PathUUID(req, "id")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want error", got)
				}
				status, body := render.MapError(err)
				if status != 400 || body.Error.Code != "bad_request" {
					t.Errorf("mapped to %d %s, want 400 bad_request", status, body.Error.Code)
				}
				return
			}
			if err != nil || got.String() != tc.want {
				t.Errorf("got %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestPathUUIDMissingWildcard(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	if _, err := render.PathUUID(req, "id"); err == nil {
		t.Fatal("want error for an unset path value")
	}
}
