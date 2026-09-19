package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

func TestAdminRegister(t *testing.T) {
	f := newAuthFixture(t)

	res, err := f.admin.Register(t.Context(), RegisterRequest{Username: "  NewUser ", Password: "123"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.Username != "newuser" || res.Role != domain.RoleUser {
		t.Errorf("user = %+v, want newuser/user", res)
	}
	if !res.CreatedAt.Equal(authTestNow) {
		t.Errorf("created_at = %v, want %v", res.CreatedAt, authTestNow)
	}
	stored, err := store.NewUsers(f.db).GetByUsername(t.Context(), "newuser")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if ok, err := f.hasher.Verify(t.Context(), "123", stored.PasswordHash); err != nil || !ok {
		t.Errorf("stored password does not verify: %v, %v", ok, err)
	}
}

func TestAdminRegisterValidation(t *testing.T) {
	f := newAuthFixture(t)

	for _, tt := range []struct {
		name string
		req  RegisterRequest
		want []string
	}{
		{"too short", RegisterRequest{Username: "ab", Password: "pw"}, []string{"username:too_short"}},
		{"too long", RegisterRequest{Username: strings.Repeat("a", domain.UsernameMaxLen+1), Password: "pw"}, []string{"username:too_long"}},
		{"invalid chars", RegisterRequest{Username: "has space", Password: "pw"}, []string{"username:invalid_chars"}},
		{"reserved", RegisterRequest{Username: "admin", Password: "pw"}, []string{"username:reserved"}},
		{"empty password", RegisterRequest{Username: "valid_name", Password: ""}, []string{"password:required"}},
		{"long password", RegisterRequest{Username: "valid_name", Password: strings.Repeat("a", domain.PasswordMaxBytes+1)}, []string{"password:too_long"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.admin.Register(t.Context(), tt.req)
			got := authTestIssues(t, err)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdminRegisterDuplicateUsername(t *testing.T) {
	f := newAuthFixture(t)
	f.seedUser(t, "taken", "pw", domain.RoleUser)

	_, err := f.admin.Register(t.Context(), RegisterRequest{Username: "taken", Password: "pw"})
	var c *domain.ConflictError
	if !errors.As(err, &c) || c.Issue != domain.IssueAlreadyTaken || c.Field != "username" {
		t.Fatalf("err = %v (%T), want a conflict already_taken on username", err, err)
	}
}

func TestAdminRevokeLogin(t *testing.T) {
	f := newAuthFixture(t)
	target := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, first := testutil.SeedToken(t, f.db, target.ID)
	_, second := testutil.SeedToken(t, f.db, target.ID)

	res, err := f.admin.RevokeLogin(t.Context(), "Ihsan")
	if err != nil {
		t.Fatalf("RevokeLogin: %v", err)
	}
	if res.RevokedCount != 2 {
		t.Errorf("revoked_count = %d, want 2", res.RevokedCount)
	}
	for _, id := range []uuid.UUID{first, second} {
		if f.revokedAt(t, id) == nil {
			t.Errorf("token %s not revoked", id)
		}
	}
	if _, err := f.admin.RevokeLogin(t.Context(), "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown user: err = %v, want not found", err)
	}
}

func TestAdminChangePasswordRevokesAllTargetTokens(t *testing.T) {
	f := newAuthFixture(t)
	target := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	admin := f.seedUser(t, "boss", "secret", domain.RoleAdmin)
	_, first := testutil.SeedToken(t, f.db, target.ID)
	_, second := testutil.SeedToken(t, f.db, target.ID)
	_, adminToken := testutil.SeedToken(t, f.db, admin.ID)

	if err := f.admin.ChangePassword(t.Context(), admin.ID, adminToken, "ihsan", "newpass"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	for _, id := range []uuid.UUID{first, second} {
		if f.revokedAt(t, id) == nil {
			t.Errorf("target token %s not revoked", id)
		}
	}
	if f.revokedAt(t, adminToken) != nil {
		t.Error("the admin's own token must not be revoked when changing another user's password")
	}
	stored, _ := store.NewUsers(f.db).GetByUsername(t.Context(), "ihsan")
	if ok, err := f.hasher.Verify(t.Context(), "newpass", stored.PasswordHash); err != nil || !ok {
		t.Errorf("new password does not verify: %v, %v", ok, err)
	}
}

func TestAdminChangePasswordSelfKeepsCurrentToken(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.seedUser(t, "boss", "secret", domain.RoleAdmin)
	_, current := testutil.SeedToken(t, f.db, admin.ID)
	_, other := testutil.SeedToken(t, f.db, admin.ID)

	if err := f.admin.ChangePassword(t.Context(), admin.ID, current, "boss", "newpass"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if f.revokedAt(t, current) != nil {
		t.Error("the current token must be kept on a self password change")
	}
	if f.revokedAt(t, other) == nil {
		t.Error("other tokens must be revoked on a self password change")
	}
}

func TestAdminChangePasswordErrors(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.seedUser(t, "boss", "secret", domain.RoleAdmin)

	if err := f.admin.ChangePassword(t.Context(), admin.ID, uuid.Nil, "boss", ""); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("empty password: err = %v, want validation", err)
	}
	if err := f.admin.ChangePassword(t.Context(), admin.ID, uuid.Nil, "ghost", "pw"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown user: err = %v, want not found", err)
	}
}

func TestAdminCreateUserCLIAllowsAdminAndReservedNames(t *testing.T) {
	f := newAuthFixture(t)

	res, err := f.admin.CreateUser(t.Context(), CreateUserRequest{Username: "admin", Password: "pw", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if res.Username != "admin" || res.Role != domain.RoleAdmin {
		t.Errorf("user = %+v, want admin/admin", res)
	}
	if _, err := f.admin.CreateUser(t.Context(), CreateUserRequest{Username: "nobody", Password: "pw", Role: "root"}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad role: err = %v, want validation", err)
	}
}

func TestAdminResetPasswordRevokesAllTokens(t *testing.T) {
	f := newAuthFixture(t)
	target := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	_, first := testutil.SeedToken(t, f.db, target.ID)
	_, second := testutil.SeedToken(t, f.db, target.ID)

	if err := f.admin.ResetPassword(t.Context(), "ihsan", "newpass"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	for _, id := range []uuid.UUID{first, second} {
		if f.revokedAt(t, id) == nil {
			t.Errorf("token %s not revoked", id)
		}
	}
	stored, _ := store.NewUsers(f.db).GetByUsername(t.Context(), "ihsan")
	if ok, err := f.hasher.Verify(t.Context(), "newpass", stored.PasswordHash); err != nil || !ok {
		t.Errorf("new password does not verify: %v, %v", ok, err)
	}
	if err := f.admin.ResetPassword(t.Context(), "ghost", "newpass"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown user: err = %v, want not found", err)
	}
}
