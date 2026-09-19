package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
)

func TestRequireRoles(t *testing.T) {
	user := &domain.Principal{UserID: uuid.New(), Role: domain.RoleUser}
	admin := &domain.Principal{UserID: uuid.New(), Role: domain.RoleAdmin}

	tests := []struct {
		name       string
		roles      []domain.Role
		principal  *domain.Principal
		wantStatus int
		wantCode   string
	}{
		{"anonymous on user route", []domain.Role{domain.RoleUser}, nil, 401, "unauthorized"},
		{"anonymous on admin route", []domain.Role{domain.RoleAdmin}, nil, 401, "unauthorized"},
		{"user on user route", []domain.Role{domain.RoleUser}, user, 200, ""},
		{"admin on user route", []domain.Role{domain.RoleUser}, admin, 403, "forbidden"},
		{"admin on admin route", []domain.Role{domain.RoleAdmin}, admin, 200, ""},
		{"user on admin route", []domain.Role{domain.RoleAdmin}, user, 403, "forbidden"},
		{"user on both-roles route", []domain.Role{domain.RoleUser, domain.RoleAdmin}, user, 200, ""},
		{"admin on both-roles route", []domain.Role{domain.RoleUser, domain.RoleAdmin}, admin, 200, ""},
		{"no roles configured fails closed", nil, admin, 403, "forbidden"},
		{"unknown role is rejected", []domain.Role{domain.RoleUser}, &domain.Principal{Role: "root"}, 403, "forbidden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := RequireRoles(tc.roles...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/x", nil)
			if tc.principal != nil {
				req = req.WithContext(WithPrincipal(req.Context(), *tc.principal))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if tc.wantStatus == 200 {
				if !called || rec.Code != 200 {
					t.Fatalf("handler called = %v, status = %d; want called with 200", called, rec.Code)
				}
				return
			}
			if called {
				t.Error("handler must not run")
			}
			apitest.RequireError(t, rec, tc.wantStatus, tc.wantCode)
			wantAuth := ""
			if tc.wantStatus == 401 {
				wantAuth = "Bearer"
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != wantAuth {
				t.Errorf("WWW-Authenticate = %q, want %q", got, wantAuth)
			}
		})
	}
}

func TestRequireRolesCopiesItsArguments(t *testing.T) {
	roles := []domain.Role{domain.RoleUser}
	mw := RequireRoles(roles...)
	roles[0] = domain.RoleAdmin // must not change the middleware
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(WithPrincipal(req.Context(), domain.Principal{Role: domain.RoleAdmin}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
