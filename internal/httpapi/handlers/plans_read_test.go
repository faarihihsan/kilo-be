package handlers_test

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/testutil"
)

// TestPlansHTTPAccessMatrix: the role matrix of the four plan routes. Users
// pass, admins are 403 and anonymous callers 401, before the handler looks at
// the request.
func TestPlansHTTPAccessMatrix(t *testing.T) {
	a := newPlanAPI(t)
	id := a.seedPlan(a.user.id)
	routes := []struct {
		name   string
		method string
		path   string
		body   string
		okCode int
	}{
		{"list", http.MethodGet, "/v1/workout-plans", "", http.StatusOK},
		{"get", http.MethodGet, "/v1/workout-plans/" + id.String(), "", http.StatusOK},
		{"save", http.MethodPut, "/v1/workout-plans/" + uuid.NewString(), planBody("x", planTime(0)), http.StatusCreated},
		{"delete", http.MethodDelete, "/v1/workout-plans/" + id.String(), "", http.StatusNoContent},
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			apitest.RequireError(t, a.do(r.method, r.path, planAnon, r.body), http.StatusUnauthorized, "unauthorized")
			apitest.RequireError(t, a.do(r.method, r.path, a.admin, r.body), http.StatusForbidden, "forbidden")
			// Even a request that would be a 400 or 404 is a 403 for an admin.
			apitest.RequireError(t, a.do(r.method, "/v1/workout-plans/not-a-uuid?limit=0", a.admin, "{"), http.StatusForbidden, "forbidden")
			if rec := a.do(r.method, r.path, a.user, r.body); rec.Code != r.okCode {
				t.Errorf("user: status = %d, want %d; %s", rec.Code, r.okCode, rec.Body.String())
			}
		})
	}
}

func TestPlansHTTPGet(t *testing.T) {
	a := newPlanAPI(t)
	id := a.seedPlan(a.user.id, testutil.WithPlanName("Seeded"),
		testutil.WithPlanExercise(a.exercises[0], testutil.WithTargetWeight(62.5)))
	gone := a.seedPlan(a.user.id, testutil.WithPlanDeletedAt(planHTTPNow))

	got := planRequireOK(t, a.get(a.user, id), http.StatusOK)
	if got["id"] != id.String() || got["name"] != "Seeded" {
		t.Errorf("plan = %v", got)
	}
	tests := []struct {
		name string
		who  planCaller
		path string
		want int
		code string
	}{
		{"missing", a.user, "/v1/workout-plans/" + uuid.NewString(), http.StatusNotFound, "not_found"},
		{"another user's", a.other, "/v1/workout-plans/" + id.String(), http.StatusNotFound, "not_found"},
		{"soft-deleted", a.user, "/v1/workout-plans/" + gone.String(), http.StatusNotFound, "not_found"},
		{"bad id", a.user, "/v1/workout-plans/1234", http.StatusBadRequest, "bad_request"},
		{"upper case id is the same id", a.user, "/v1/workout-plans/" + strings.ToUpper(id.String()), http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := a.do(http.MethodGet, tt.path, tt.who, "")
			if tt.code == "" {
				planRequireOK(t, rec, tt.want)
				return
			}
			apitest.RequireError(t, rec, tt.want, tt.code)
		})
	}
}

func TestPlansHTTPDelete(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	planRequireOK(t, a.put(a.user, id, planBody("Doomed", planTime(-time.Hour), planExercise(a.exercises[0], 0, ""))), http.StatusCreated)
	before := planRequireOK(t, a.get(a.user, id), http.StatusOK)

	rec := a.del(a.user, id)
	planRequireStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("204 with a body: %q", rec.Body.String())
	}
	apitest.RequireError(t, a.get(a.user, id), http.StatusNotFound, "not_found")

	// Idempotent: deleting again is 204.
	planRequireStatus(t, a.del(a.user, id), http.StatusNoContent)

	// Hidden from the list unless asked, present in the sync feed with deleted_at.
	if items := planItems(t, a.list(a.user, "")); len(items) != 0 {
		t.Errorf("the default list still shows the deleted plan: %v", items)
	}
	items := planItems(t, a.list(a.user, "include_deleted=true"))
	if len(items) != 1 || items[0]["deleted_at"] == nil {
		t.Errorf("include_deleted=true: %v", items)
	}
	feed := planItems(t, a.list(a.user, "updated_since=1970-01-01T00:00:00Z"))
	if len(feed) != 1 || feed[0]["deleted_at"] == nil || feed[0]["id"] != id.String() {
		t.Errorf("sync feed: %v", feed)
	}
	// The delete moved server_updated_at, so a client that had pulled the live
	// plan gets the deletion on its next pull.
	since := before["server_updated_at"].(string)
	if pulled := planItems(t, a.list(a.user, "updated_since="+url.QueryEscape(since))); len(pulled) != 1 || pulled[0]["deleted_at"] == nil {
		t.Errorf("pull after the delete = %v", pulled)
	}

	// A later PUT is a deleted conflict (no resurrection).
	apitest.RequireError(t, a.put(a.user, id, planBody("Back", planTime(0))), http.StatusConflict, "conflict")

	for name, tt := range map[string]struct {
		rec  func() *planResponse
		want int
		code string
	}{
		"unknown id":   {func() *planResponse { return a.del(a.user, uuid.New()) }, http.StatusNotFound, "not_found"},
		"bad id":       {func() *planResponse { return a.do(http.MethodDelete, "/v1/workout-plans/x", a.user, "") }, http.StatusBadRequest, "bad_request"},
		"another user": {func() *planResponse { return a.del(a.other, id) }, http.StatusNotFound, "not_found"},
	} {
		t.Run(name, func(t *testing.T) { apitest.RequireError(t, tt.rec(), tt.want, tt.code) })
	}
	// Another user cannot delete a live plan either, and it stays.
	live := uuid.New()
	planRequireOK(t, a.put(a.user, live, planBody("Live", planTime(0))), http.StatusCreated)
	apitest.RequireError(t, a.del(a.other, live), http.StatusNotFound, "not_found")
	planRequireOK(t, a.get(a.user, live), http.StatusOK)
	a.planRequireNoServerErrors()
}

// planItems returns the items of a 200 list response.
func planItems(t testing.TB, rec *planResponse) []map[string]any {
	t.Helper()
	body := planRequireOK(t, rec, http.StatusOK)
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items is %T, want an array: %s", body["items"], rec.Body.String())
	}
	out := make([]map[string]any, len(raw))
	for i, it := range raw {
		out[i], _ = it.(map[string]any)
	}
	return out
}

func planNextCursor(t testing.TB, rec *planResponse) string {
	t.Helper()
	next, _ := planJSONOf(t, rec)["next_cursor"].(string)
	return next
}

// planWalkHTTP follows next_cursor from the first page and returns the ids.
func planWalkHTTP(t *testing.T, a *planAPI, who planCaller, query string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for range 500 {
		q := query
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := a.list(who, q)
		for _, it := range planItems(t, rec) {
			ids = append(ids, it["id"].(string))
		}
		if cursor = planNextCursor(t, rec); cursor == "" {
			return ids
		}
	}
	t.Fatal("paging did not end")
	return nil
}

func TestPlansHTTPListDefaultOrderAndPaging(t *testing.T) {
	a := newPlanAPI(t)
	// Mixed case and equal names; ids ascend with creation.
	names := []string{"banana", "Apple", "apple", "cherry", "APPLE", "Cherryx", "apple", "Zebra", "banana"}
	byName := map[string]string{}
	var ids []string
	for _, name := range names {
		id, _ := uuid.NewV7()
		planRequireOK(t, a.put(a.user, id, planBody(name, planTime(-time.Hour))), http.StatusCreated)
		ids = append(ids, id.String())
		byName[id.String()] = name
	}
	a.seedPlan(a.other.id, testutil.WithPlanName("theirs"))
	a.seedPlan(a.user.id, testutil.WithPlanName("deleted"), testutil.WithPlanDeletedAt(planHTTPNow))

	want := slices.Clone(ids)
	slices.SortStableFunc(want, func(x, y string) int {
		if c := strings.Compare(strings.ToLower(byName[x]), strings.ToLower(byName[y])); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	for _, limit := range []int{1, 2, 4, len(names), 200} {
		if got := planWalkHTTP(t, a, a.user, fmt.Sprintf("limit=%d", limit)); !slices.Equal(got, want) {
			t.Errorf("limit %d:\n got  %v\n want %v", limit, got, want)
		}
	}
	// The default limit is 50; an exact page has no cursor, a short one neither.
	rec := a.list(a.user, fmt.Sprintf("limit=%d", len(names)))
	if planNextCursor(t, rec) != "" {
		t.Error("next_cursor set on the last page")
	}
	rec = a.list(a.user, "limit=1")
	if planNextCursor(t, rec) == "" {
		t.Error("next_cursor missing on a first page with more items")
	}
	if !strings.Contains(rec.Body.String(), `"next_cursor":"`) || strings.Contains(a.list(a.user, "").Body.String(), `"next_cursor":"`) {
		t.Error("next_cursor is not a string on a page with more items and null on the last one")
	}
}

func TestPlansHTTPListShapes(t *testing.T) {
	a := newPlanAPI(t)
	id := uuid.New()
	planRequireOK(t, a.put(a.user, id, planBody("Push", planTime(-time.Hour),
		planExercise(a.exercises[0], 0, `,"target_weight":80`), planExercise(a.exercises[1], 1, ""))), http.StatusCreated)
	empty := uuid.New()
	planRequireOK(t, a.put(a.user, empty, planBody("Empty", planTime(-time.Hour))), http.StatusCreated)

	t.Run("summaries", func(t *testing.T) {
		rec := a.list(a.user, "")
		body := planRequireOK(t, rec, http.StatusOK)
		if got, want := planKeys(body), []string{"items", "next_cursor"}; !slices.Equal(got, want) {
			t.Errorf("envelope keys = %v, want %v", got, want)
		}
		items := planItems(t, rec)
		if len(items) != 2 {
			t.Fatalf("items = %d", len(items))
		}
		wantKeys := []string{"created_at", "deleted_at", "description", "exercise_count", "id", "name", "server_updated_at", "updated_at"}
		for _, it := range items {
			if got := planKeys(it); !slices.Equal(got, wantKeys) {
				t.Errorf("summary keys = %v, want %v", got, wantKeys)
			}
		}
		if items[0]["name"] != "Empty" || fmt.Sprint(items[0]["exercise_count"]) != "0" || fmt.Sprint(items[1]["exercise_count"]) != "2" {
			t.Errorf("items = %v", items)
		}
		// The exact key order of the spec example.
		if !strings.Contains(rec.Body.String(), `{"items":[{"id":"`+empty.String()+`","name":"Empty","description":null,"exercise_count":0,"created_at":"`) {
			t.Errorf("body does not follow the key order of spec 06: %s", rec.Body.String())
		}
	})

	t.Run("expand=exercises returns the full plan, not exercise_count", func(t *testing.T) {
		rec := a.list(a.user, "expand=exercises")
		items := planItems(t, rec)
		if len(items) != 2 {
			t.Fatalf("items = %d", len(items))
		}
		wantKeys := []string{"created_at", "deleted_at", "description", "exercises", "id", "name", "server_updated_at", "updated_at"}
		for _, it := range items {
			if got := planKeys(it); !slices.Equal(got, wantKeys) {
				t.Errorf("expanded keys = %v, want %v", got, wantKeys)
			}
			// Each item is byte for byte what GET returns.
			got := planRequireOK(t, a.get(a.user, uuid.MustParse(it["id"].(string))), http.StatusOK)
			if !reflect.DeepEqual(got, it) {
				t.Errorf("expanded item differs from GET:\n%v\n%v", it, got)
			}
		}
		if exs, _ := items[0]["exercises"].([]any); exs == nil || len(exs) != 0 {
			t.Errorf("a plan without exercises must expand to [], got %v", items[0]["exercises"])
		}
		if exs, _ := items[1]["exercises"].([]any); len(exs) != 2 {
			t.Errorf("exercises = %v", items[1]["exercises"])
		}
		if strings.Contains(rec.Body.String(), "exercise_count") {
			t.Error("expand=exercises must not carry exercise_count")
		}
	})

	t.Run("an empty list is an empty array, not null", func(t *testing.T) {
		rec := a.list(a.other, "")
		planRequireStatus(t, rec, http.StatusOK)
		if strings.TrimSpace(rec.Body.String()) != `{"items":[],"next_cursor":null}` {
			t.Errorf("body = %s", rec.Body.String())
		}
		rec = a.list(a.other, "expand=exercises")
		if strings.TrimSpace(rec.Body.String()) != `{"items":[],"next_cursor":null}` {
			t.Errorf("expanded body = %s", rec.Body.String())
		}
	})
}

// TestPlansHTTPListQueryErrors: the statuses of the response table of spec 6.
func TestPlansHTTPListQueryErrors(t *testing.T) {
	a := newPlanAPI(t)
	a.seedPlan(a.user.id)
	a.seedPlan(a.user.id)
	validCursor := planNextCursor(t, a.list(a.user, "limit=1"))
	if validCursor == "" {
		t.Fatal("no cursor to reuse")
	}

	tests := []struct {
		name   string
		query  string
		status int
		code   string
		issue  string // "field:issue" for 422
	}{
		{"limit 0", "limit=0", http.StatusUnprocessableEntity, "validation_failed", "limit:out_of_range"},
		{"limit 201", "limit=201", http.StatusUnprocessableEntity, "validation_failed", "limit:out_of_range"},
		{"limit negative", "limit=-5", http.StatusUnprocessableEntity, "validation_failed", "limit:out_of_range"},
		{"limit text", "limit=abc", http.StatusUnprocessableEntity, "validation_failed", "limit:invalid_format"},
		{"limit empty", "limit=", http.StatusUnprocessableEntity, "validation_failed", "limit:invalid_format"},
		{"limit 200 is fine", "limit=200", http.StatusOK, "", ""},
		{"limit 1 is fine", "limit=1", http.StatusOK, "", ""},
		{"expand unknown", "expand=foo", http.StatusUnprocessableEntity, "validation_failed", "expand:invalid_value"},
		{"expand empty", "expand=", http.StatusUnprocessableEntity, "validation_failed", "expand:invalid_value"},
		{"expand upper case", "expand=Exercises", http.StatusUnprocessableEntity, "validation_failed", "expand:invalid_value"},
		{"expand exercises", "expand=exercises", http.StatusOK, "", ""},
		{"include_deleted text", "include_deleted=yes", http.StatusUnprocessableEntity, "validation_failed", "include_deleted:invalid_format"},
		{"include_deleted true", "include_deleted=true", http.StatusOK, "", ""},
		{"include_deleted false", "include_deleted=false", http.StatusOK, "", ""},
		{"updated_since garbage", "updated_since=yesterday", http.StatusBadRequest, "bad_request", ""},
		{"updated_since date only", "updated_since=2026-09-19", http.StatusBadRequest, "bad_request", ""},
		{"updated_since empty", "updated_since=", http.StatusBadRequest, "bad_request", ""},
		{"updated_since valid", "updated_since=2026-09-19T08:00:00Z", http.StatusOK, "", ""},
		{"updated_since with an encoded offset", "updated_since=" + url.QueryEscape("2026-09-19T10:00:00+02:00"), http.StatusOK, "", ""},
		{"cursor garbage", "cursor=garbage", http.StatusBadRequest, "bad_request", ""},
		{"cursor not ours", "cursor=AAAA", http.StatusBadRequest, "bad_request", ""},
		{"cursor of the default order in sync mode", "updated_since=1970-01-01T00:00:00Z&cursor=" + validCursor, http.StatusBadRequest, "bad_request", ""},
		{"cursor valid", "limit=1&cursor=" + validCursor, http.StatusOK, "", ""},
		{"limit and cursor errors: the limit is reported", "limit=0&cursor=garbage", http.StatusUnprocessableEntity, "validation_failed", "limit:out_of_range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := a.list(a.user, tt.query)
			if tt.code == "" {
				planRequireOK(t, rec, tt.status)
				return
			}
			apitest.RequireError(t, rec, tt.status, tt.code)
			if tt.issue != "" {
				if got := planIssueList(t, rec); !slices.Equal(got, []string{tt.issue}) {
					t.Errorf("details = %v, want [%s]", got, tt.issue)
				}
			}
		})
	}
	a.planRequireNoServerErrors()
}

// TestPlansHTTPSyncFeed follows the recipe of spec 05/06 for a phone: a first
// full pull in one call, then incremental pulls with the largest
// server_updated_at seen.
func TestPlansHTTPSyncFeed(t *testing.T) {
	a := newPlanAPI(t)
	ex := a.exercises
	put := func(id uuid.UUID, name, at string, exercises ...string) {
		t.Helper()
		if rec := a.put(a.user, id, planBody(name, at, exercises...)); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	p1, p2, p3 := uuid.New(), uuid.New(), uuid.New()
	// p1 was written offline long ago; it reaches the server just now.
	put(p1, "old client time", planTime(-72*time.Hour), planExercise(ex[0], 0, ""))
	put(p2, "second", planTime(-time.Hour), planExercise(ex[1], 0, ""))

	// First sync: everything with exercises in one call.
	rec := a.list(a.user, "updated_since=1970-01-01T00:00:00Z&expand=exercises&limit=200")
	items := planItems(t, rec)
	if len(items) != 2 || items[0]["id"] != p1.String() || items[1]["id"] != p2.String() {
		t.Fatalf("first pull = %v", items)
	}
	if planNextCursor(t, rec) != "" {
		t.Error("next_cursor on a single page")
	}
	if exs, _ := items[0]["exercises"].([]any); len(exs) != 1 {
		t.Errorf("the first pull has no exercises: %v", items[0])
	}
	since := items[1]["server_updated_at"].(string)

	// Nothing changed: an empty pull. (Strictly after: the last item is not repeated.)
	if items := planItems(t, a.list(a.user, "updated_since="+url.QueryEscape(since))); len(items) != 0 {
		t.Errorf("pull with nothing new = %v", items)
	}

	// An offline upload with an OLD client time still shows up (the feed uses
	// server_updated_at), and a delete shows as deleted_at.
	put(p3, "uploaded late", planTime(-100*time.Hour))
	if rec := a.del(a.user, p2); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	items = planItems(t, a.list(a.user, "updated_since="+url.QueryEscape(since)+"&expand=exercises"))
	if len(items) != 2 || items[0]["id"] != p3.String() || items[1]["id"] != p2.String() {
		t.Fatalf("second pull = %v", items)
	}
	if items[0]["deleted_at"] != nil || items[1]["deleted_at"] == nil {
		t.Errorf("deleted_at: %v / %v", items[0]["deleted_at"], items[1]["deleted_at"])
	}
	// Paged by cursor with a small limit, the same items in the same order.
	paged := planWalkHTTP(t, a, a.user, "updated_since="+url.QueryEscape(since)+"&limit=1")
	if !slices.Equal(paged, []string{p3.String(), p2.String()}) {
		t.Errorf("paged pull = %v", paged)
	}
	// The other user sees none of it.
	if items := planItems(t, a.list(a.other, "updated_since=1970-01-01T00:00:00Z")); len(items) != 0 {
		t.Errorf("another user's feed = %v", items)
	}
}

// TestPlansHTTPGoldenShapes pins the JSON of every response shape against the
// examples of specs 06, 07 and 10: key sets, key order, null handling.
func TestPlansHTTPGoldenShapes(t *testing.T) {
	a := newPlanAPI(t)
	exID := a.exercises[0]
	id := uuid.New()

	// The request example of spec 10.
	request := fmt.Sprintf(`{
	  "name": "Push",
	  "description": "Chest / shoulders / triceps",
	  "updated_at": "2026-09-19T09:00:00Z",
	  "exercises": [
	    {
	      "exercise_id": %q,
	      "position": 0,
	      "target_sets": 4,
	      "target_reps": 8,
	      "target_reps_max": 10,
	      "target_weight": 80.0,
	      "target_duration_seconds": null,
	      "target_distance_meters": null,
	      "rest_seconds": 120,
	      "notes": "Pause on chest"
	    }
	  ]
	}`, exID)
	created := a.put(a.user, id, request)
	planRequireOK(t, created, http.StatusCreated)

	exercise := fmt.Sprintf(`{"exercise_id":%q,"position":0,"target_sets":4,"target_reps":8,"target_reps_max":10,`+
		`"target_weight":80.0,"target_duration_seconds":null,"target_distance_meters":null,"rest_seconds":120,"notes":"Pause on chest"}`, exID)
	prefix := fmt.Sprintf(`{"id":%q,"name":"Push","description":"Chest / shoulders / triceps","updated_at":"2026-09-19T09:00:00Z","created_at":"`, id)
	for name, body := range map[string]string{"PUT": created.Body.String(), "GET": a.get(a.user, id).Body.String()} {
		if !strings.HasPrefix(body, prefix) {
			t.Errorf("%s body does not start like spec 07:\n%s\nwant prefix %s", name, body, prefix)
		}
		if !strings.Contains(body, `,"deleted_at":null,"exercises":[`+exercise+`]}`) {
			t.Errorf("%s body does not end like spec 07:\n%s", name, body)
		}
	}

	// A stale write: the error envelope of the conventions with current.
	stale := a.put(a.user, id, planBody("x", "2026-09-19T08:59:59Z"))
	eb := apitest.RequireError(t, stale, http.StatusConflict, "conflict")
	if eb.Error.Message == "" || len(eb.Error.Details) != 1 {
		t.Fatalf("error = %+v", eb.Error)
	}
	raw := planJSONOf(t, stale)
	errObj := raw["error"].(map[string]any)
	if got, want := planKeys(errObj), []string{"code", "details", "message"}; !slices.Equal(got, want) {
		t.Errorf("error keys = %v, want %v", got, want)
	}
	detail := errObj["details"].([]any)[0].(map[string]any)
	if got, want := planKeys(detail), []string{"current", "issue"}; !slices.Equal(got, want) {
		t.Errorf("details[0] keys = %v, want %v", got, want)
	}
	if got, want := planKeys(detail["current"].(map[string]any)), []string{"created_at", "deleted_at", "description", "exercises", "id", "name", "server_updated_at", "updated_at"}; !slices.Equal(got, want) {
		t.Errorf("current keys = %v, want %v (the GET shape)", got, want)
	}

	// A deleted conflict has only the issue.
	if rec := a.del(a.user, id); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	deleted := a.put(a.user, id, planBody("x", "2026-09-19T09:01:00Z"))
	apitest.RequireError(t, deleted, http.StatusConflict, "conflict")
	detail = planJSONOf(t, deleted)["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
	if got, want := planKeys(detail), []string{"issue"}; !slices.Equal(got, want) {
		t.Errorf("deleted details[0] keys = %v, want %v", got, want)
	}

	// A validation error names field and issue.
	invalid := a.put(a.user, uuid.New(), `{"name":"","updated_at":"2026-09-19T09:00:00Z","exercises":[]}`)
	apitest.RequireError(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	detail = planJSONOf(t, invalid)["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
	if got, want := planKeys(detail), []string{"field", "issue"}; !slices.Equal(got, want) {
		t.Errorf("validation details[0] keys = %v, want %v", got, want)
	}
	// The plan cap has an issue and no field (about the request as a whole).
	a.planRequireNoServerErrors()
}
