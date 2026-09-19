package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/service"
)

func TestRegisterHandler(t *testing.T) {
	a := newAuthAPI(t)
	adminID := a.seedUser(t, "boss", "secret", domain.RoleAdmin)
	p := &domain.Principal{UserID: adminID, Role: domain.RoleAdmin}

	rec := a.do(t, http.MethodPost, "/v1/auth/register", `{"username":"NewUser","password":"123"}`, p)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body)
	}
	var res service.UserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("body: %v: %s", err, rec.Body)
	}
	if res.Username != "newuser" || res.Role != domain.RoleUser {
		t.Errorf("response = %+v, want newuser/user", res)
	}
	id, role := a.userBy(t, "newuser")
	if id != res.ID || role != domain.RoleUser {
		t.Errorf("stored user = %s/%s, want %s/user", id, role, res.ID)
	}
}

func TestRegisterHandlerConflictsAndValidation(t *testing.T) {
	a := newAuthAPI(t)
	adminID := a.seedUser(t, "boss", "secret", domain.RoleAdmin)
	a.seedUser(t, "taken", "secret", domain.RoleUser)
	p := &domain.Principal{UserID: adminID, Role: domain.RoleAdmin}

	rec := a.do(t, http.MethodPost, "/v1/auth/register", `{"username":"taken","password":"123"}`, p)
	body := apitest.RequireError(t, rec, http.StatusConflict, "conflict")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "already_taken" || body.Error.Details[0].Field != "username" {
		t.Errorf("conflict details = %+v, want username already_taken", body.Error.Details)
	}

	rec = a.do(t, http.MethodPost, "/v1/auth/register", `{"username":"admin","password":"123"}`, p)
	body = apitest.RequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
	if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != "reserved" {
		t.Errorf("reserved details = %+v, want reserved", body.Error.Details)
	}

	apitest.RequireError(t,
		a.do(t, http.MethodPost, "/v1/auth/register", `{"username":"ab","password":"123"}`, p),
		http.StatusUnprocessableEntity, "validation_failed")
}

func TestAdminRevokeLoginHandler(t *testing.T) {
	a := newAuthAPI(t)
	adminID := a.seedUser(t, "boss", "secret", domain.RoleAdmin)
	targetID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, first := a.seedToken(t, targetID)
	_, second := a.seedToken(t, targetID)
	p := &domain.Principal{UserID: adminID, Role: domain.RoleAdmin}

	rec := a.do(t, http.MethodPost, "/v1/admin/users/ihsan/revoke-login", "", p)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var res service.RevokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.RevokedCount != 2 {
		t.Errorf("response = %+v, %v; want count 2", res, err)
	}
	for _, id := range []uuid.UUID{first, second} {
		if a.revokedAt(t, id) == nil {
			t.Errorf("token %s not revoked", id)
		}
	}

	apitest.RequireError(t,
		a.do(t, http.MethodPost, "/v1/admin/users/ghost/revoke-login", "", p),
		http.StatusNotFound, "not_found")
}

func TestAdminChangePasswordHandler(t *testing.T) {
	a := newAuthAPI(t)
	adminID := a.seedUser(t, "boss", "secret", domain.RoleAdmin)
	targetID := a.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, first := a.seedToken(t, targetID)
	_, second := a.seedToken(t, targetID)

	// A different admin resets the target: all target tokens go.
	rec := a.do(t, http.MethodPut, "/v1/admin/users/ihsan/password", `{"password":"newpass"}`,
		&domain.Principal{UserID: adminID, Role: domain.RoleAdmin})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body)
	}
	for _, id := range []uuid.UUID{first, second} {
		if a.revokedAt(t, id) == nil {
			t.Errorf("target token %s not revoked", id)
		}
	}

	// The same admin changes their own password: the current token survives.
	_, adminCurrent := a.seedToken(t, adminID)
	_, adminOther := a.seedToken(t, adminID)
	rec = a.do(t, http.MethodPut, "/v1/admin/users/boss/password", `{"password":"otherpass"}`,
		&domain.Principal{UserID: adminID, Role: domain.RoleAdmin, TokenID: adminCurrent})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("self change: status = %d, want 204; body: %s", rec.Code, rec.Body)
	}
	if a.revokedAt(t, adminCurrent) != nil {
		t.Error("self change revoked the current token")
	}
	if a.revokedAt(t, adminOther) == nil {
		t.Error("self change did not revoke the other token")
	}
}

func TestAdminChangePasswordHandlerErrors(t *testing.T) {
	a := newAuthAPI(t)
	adminID := a.seedUser(t, "boss", "secret", domain.RoleAdmin)
	p := &domain.Principal{UserID: adminID, Role: domain.RoleAdmin}

	apitest.RequireError(t,
		a.do(t, http.MethodPut, "/v1/admin/users/boss/password", `{"password":""}`, p),
		http.StatusUnprocessableEntity, "validation_failed")
	apitest.RequireError(t,
		a.do(t, http.MethodPut, "/v1/admin/users/ghost/password", `{"password":"pw"}`, p),
		http.StatusNotFound, "not_found")
}
