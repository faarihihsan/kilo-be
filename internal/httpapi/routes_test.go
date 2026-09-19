package httpapi

import (
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
)

func TestRoutesTableIsComplete(t *testing.T) {
	routes := Routes()
	if len(routes) != 22 {
		t.Fatalf("got %d routes, want 21 endpoints + healthz", len(routes))
	}

	numbers := map[int]bool{}
	patterns := map[string]bool{}
	names := map[string]bool{}
	for _, r := range routes {
		if numbers[r.Number] {
			t.Errorf("number %d listed twice", r.Number)
		}
		numbers[r.Number] = true
		if patterns[r.Pattern()] {
			t.Errorf("pattern %q listed twice", r.Pattern())
		}
		patterns[r.Pattern()] = true
		names[r.Name] = true
	}
	for n := 0; n <= 21; n++ {
		if !numbers[n] {
			t.Errorf("endpoint %d is missing", n)
		}
	}

	// Every Handlers field is served by exactly one route, under its own name.
	typ := reflect.TypeOf(Handlers{})
	if typ.NumField() != len(routes) {
		t.Errorf("Handlers has %d fields for %d routes", typ.NumField(), len(routes))
	}
	for i := range typ.NumField() {
		if !names[typ.Field(i).Name] {
			t.Errorf("Handlers.%s has no route", typ.Field(i).Name)
		}
	}
	for _, rt := range table {
		var h Handlers
		*rt.field(&h) = func(http.ResponseWriter, *http.Request) {}
		v := reflect.ValueOf(h)
		var set []string
		for i := range v.NumField() {
			if !v.Field(i).IsNil() {
				set = append(set, typ.Field(i).Name)
			}
		}
		if len(set) != 1 || set[0] != rt.Name {
			t.Errorf("route %s selects Handlers fields %v", rt.Name, set)
		}
	}
}

func TestRoutesFlagsAndRoles(t *testing.T) {
	rateLimited := []string{"Register", "Login", "Logout", "RevokeTokens", "ListTokens", "AdminRevokeLogin", "AdminChangePassword"}
	rolesByName := map[string][]domain.Role{
		"Register":            {domain.RoleAdmin},
		"Login":               nil,
		"Logout":              {domain.RoleUser, domain.RoleAdmin},
		"RevokeTokens":        {domain.RoleUser},
		"AdminRevokeLogin":    {domain.RoleAdmin},
		"AdminChangePassword": {domain.RoleAdmin},
		"ListTokens":          {domain.RoleUser},
		"Healthz":             nil,
	}
	for _, r := range Routes() {
		wantRoles, special := rolesByName[r.Name]
		if !special {
			wantRoles = []domain.Role{domain.RoleUser} // endpoints 3-10 and 16-21
		}
		if !slices.Equal(r.Roles, wantRoles) {
			t.Errorf("%s: roles = %v, want %v", r.Name, r.Roles, wantRoles)
		}
		wantAnon := r.Name == "Login" || r.Name == "Healthz"
		if r.Anonymous != wantAnon {
			t.Errorf("%s: Anonymous = %v", r.Name, r.Anonymous)
		}
		if !r.Anonymous && len(r.Roles) == 0 {
			t.Errorf("%s: authenticated route without roles", r.Name)
		}
		if r.Anonymous && len(r.Roles) != 0 {
			t.Errorf("%s: anonymous route with roles", r.Name)
		}
		if got, want := r.AuthRateLimited, slices.Contains(rateLimited, r.Name); got != want {
			t.Errorf("%s: AuthRateLimited = %v, want %v", r.Name, got, want)
		}
		wantLimit := int64(domain.MaxBodyBytes)
		if r.Name == "SetExerciseImage" {
			wantLimit = domain.MaxImageBytes
		}
		if r.MaxBodyBytes != wantLimit {
			t.Errorf("%s: MaxBodyBytes = %d, want %d", r.Name, r.MaxBodyBytes, wantLimit)
		}
	}
}

func TestRoutesReturnsACopy(t *testing.T) {
	a := Routes()
	a[0].Path = "/mutated"
	a[0].Roles[0] = "mutated"
	b := Routes()
	if b[0].Path == "/mutated" || b[0].Roles[0] == "mutated" {
		t.Error("Routes() must not expose the internal table")
	}
}

// TestRoutesMatchReadme keeps the table in step with the endpoint table in
// docs/README.md: same method, path and auth column for every endpoint.
func TestRoutesMatchReadme(t *testing.T) {
	raw, err := os.ReadFile("../../docs/README.md")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| (\\d+) \\| [^|]+\\| `([A-Z]+) (/[^`]+)` \\| (\\w+) \\|")
	byNumber := map[int]Route{}
	for _, r := range Routes() {
		byNumber[r.Number] = r
	}

	found := 0
	for _, line := range strings.Split(string(raw), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		found++
		n, _ := strconv.Atoi(m[1])
		r, ok := byNumber[n]
		if !ok {
			t.Errorf("README lists endpoint %d, table has none", n)
			continue
		}
		if r.Method != m[2] || r.Path != m[3] {
			t.Errorf("endpoint %d: table has %s %s, README has %s %s", n, r.Method, r.Path, m[2], m[3])
		}
		var want []domain.Role
		switch m[4] {
		case "admin":
			want = []domain.Role{domain.RoleAdmin}
		case "user":
			want = []domain.Role{domain.RoleUser}
		case "any":
			want = []domain.Role{domain.RoleUser, domain.RoleAdmin}
		case "none":
			want = nil
		default:
			t.Errorf("endpoint %d: unknown auth %q in README", n, m[4])
		}
		if !slices.Equal(r.Roles, want) || r.Anonymous != (m[4] == "none") {
			t.Errorf("endpoint %d: table roles %v (anonymous %v), README auth %q", n, r.Roles, r.Anonymous, m[4])
		}
	}
	if found != 21 {
		t.Errorf("parsed %d endpoint rows from README, want 21", found)
	}
}
