package service

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// authTestNow is the fake instant all auth service tests run at.
var authTestNow = time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)

// authFixture wires the auth and admin services on a throwaway database with a
// fake clock, a cheap real hasher and a login limiter.
type authFixture struct {
	deps   Deps
	db     *store.DB
	clk    *clock.Fake
	auth   *Auth
	admin  *Admin
	hasher *auth.Hasher
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureWith(t, auth.LoginLimiterConfig{MaxFailsUser: 3, MaxFailsIP: 20})
}

func newAuthFixtureWith(t *testing.T, limiterCfg auth.LoginLimiterConfig) *authFixture {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(authTestNow)
	hasher, err := auth.NewHasher(auth.HasherConfig{
		MemoryKiB:     8192,
		Time:          1,
		Parallelism:   1,
		MaxConcurrent: 4,
	})
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	d := Deps{
		DB:      db,
		Clock:   clk,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Hasher:  hasher,
		Limiter: auth.NewLoginLimiter(clk, limiterCfg),
	}
	return &authFixture{
		deps:   d,
		db:     db,
		clk:    clk,
		auth:   NewAuth(d),
		admin:  NewAdmin(d),
		hasher: hasher,
	}
}

// seedUser inserts a user whose password verifies and returns the stored row.
func (f *authFixture) seedUser(t *testing.T, username, password string, role domain.Role) store.User {
	t.Helper()
	hash, err := f.hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	testutil.SeedUser(t, f.db, role, testutil.WithUsername(username), testutil.WithPasswordHash(hash))
	user, err := store.NewUsers(f.db).GetByUsername(t.Context(), username)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	return user
}

// tokenState reads a token row's revocation time.
func (f *authFixture) revokedAt(t *testing.T, tokenID uuid.UUID) *time.Time {
	t.Helper()
	var revoked *time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT revoked_at FROM auth_tokens WHERE id = $1`, tokenID).Scan(&revoked); err != nil {
		t.Fatalf("read token: %v", err)
	}
	return revoked
}

func authTestIssues(t *testing.T, err error) []string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v (%T), want a validation error", err, err)
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Field + ":" + is.Issue
	}
	return out
}

func authTestRateLimited(t *testing.T, err error) *domain.RateLimitedError {
	t.Helper()
	var rl *domain.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v (%T), want rate limited", err, err)
	}
	return rl
}

func TestAuthLoginIssuesToken(t *testing.T) {
	f := newAuthFixture(t)
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)

	res, err := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{
		Username:   "  Ihsan ",
		Password:   "secret",
		DeviceName: testutil.Ptr("Ihsan's iPhone"),
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", res.TokenType)
	}
	if !strings.HasPrefix(res.AccessToken, domain.TokenPrefix) {
		t.Errorf("access_token = %q, want the wt_ prefix", res.AccessToken)
	}
	if res.User.Username != "ihsan" || res.User.Role != domain.RoleUser {
		t.Errorf("user = %+v, want ihsan/user", res.User)
	}
	if want := authTestNow.Add(domain.DefaultTokenTTL); !res.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", res.ExpiresAt, want)
	}

	// The stored row is found by the SHA-256 hash and carries the device name.
	rec, err := store.NewAuthTokens(f.db).LookupByHash(t.Context(), auth.HashToken(res.AccessToken))
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	if rec.TokenID != res.TokenID || rec.UserID != res.User.ID {
		t.Errorf("stored token = %+v, want id %s and user %s", rec, res.TokenID, res.User.ID)
	}
	if rec.DeviceName == nil || *rec.DeviceName != "Ihsan's iPhone" {
		t.Errorf("device_name = %v, want Ihsan's iPhone", rec.DeviceName)
	}
}

func TestAuthLoginValidation(t *testing.T) {
	f := newAuthFixture(t)
	long := strings.Repeat("d", domain.DeviceNameMaxLen+1)

	for _, tt := range []struct {
		name string
		req  LoginRequest
		want []string
	}{
		{"missing username", LoginRequest{Password: "pw"}, []string{"username:required"}},
		{"blank username", LoginRequest{Username: "   ", Password: "pw"}, []string{"username:required"}},
		{"missing password", LoginRequest{Username: "ihsan"}, []string{"password:required"}},
		{"device_name too long", LoginRequest{Username: "ihsan", Password: "pw", DeviceName: &long}, []string{"device_name:too_long"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.auth.Login(t.Context(), "203.0.113.5", tt.req)
			got := authTestIssues(t, err)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthLoginRejectsBadCredentialsWithoutRevealingWhich(t *testing.T) {
	f := newAuthFixture(t)
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)

	_, wrong := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "nope"})
	_, unknown := f.auth.Login(t.Context(), "203.0.113.6", LoginRequest{Username: "ghost", Password: "nope"})

	for name, err := range map[string]error{"wrong password": wrong, "unknown user": unknown} {
		var ue *domain.UnauthorizedError
		if !errors.As(err, &ue) {
			t.Errorf("%s: err = %v (%T), want unauthorized", name, err, err)
		}
	}
}

func TestAuthLoginLocksUsernameEvenWithCorrectPassword(t *testing.T) {
	f := newAuthFixtureWith(t, auth.LoginLimiterConfig{MaxFailsUser: 3, MaxFailsIP: 20})
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)

	for i := range 3 {
		if _, err := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "wrong"}); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("failure %d: err = %v, want unauthorized", i+1, err)
		}
	}
	_, err := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "secret"})
	rl := authTestRateLimited(t, err)
	if rl.RetryAfterSeconds() <= 0 {
		t.Errorf("RetryAfter = %v, want positive", rl.RetryAfter)
	}
}

func TestAuthLoginBlocksAfterIPFailures(t *testing.T) {
	f := newAuthFixtureWith(t, auth.LoginLimiterConfig{MaxFailsUser: 100, MaxFailsIP: 3})
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)

	for _, name := range []string{"aone", "btwo", "cthree"} {
		if _, err := f.auth.Login(t.Context(), "203.0.113.9", LoginRequest{Username: name, Password: "wrong"}); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s: err = %v, want unauthorized", name, err)
		}
	}
	_, err := f.auth.Login(t.Context(), "203.0.113.9", LoginRequest{Username: "ihsan", Password: "secret"})
	authTestRateLimited(t, err)
}

func TestAuthLogoutRevokesCurrentToken(t *testing.T) {
	f := newAuthFixture(t)
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	res, err := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.auth.Logout(t.Context(), res.User.ID, res.TokenID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if f.revokedAt(t, res.TokenID) == nil {
		t.Error("token was not revoked")
	}
	// A second logout is an idempotent no-op at the store level.
	if err := f.auth.Logout(t.Context(), res.User.ID, res.TokenID); err != nil {
		t.Fatalf("second Logout: %v", err)
	}
}

func TestAuthRevoke(t *testing.T) {
	f := newAuthFixture(t)
	f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	first, _ := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "secret"})
	second, _ := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "secret"})
	third, _ := f.auth.Login(t.Context(), "203.0.113.5", LoginRequest{Username: "ihsan", Password: "secret"})

	// Revoke exactly the second token by id.
	got, err := f.auth.Revoke(t.Context(), first.User.ID, first.TokenID, RevokeRequest{TokenID: testutil.Ptr(second.TokenID.String())})
	if err != nil || got.RevokedCount != 1 {
		t.Fatalf("revoke by id = %+v, %v; want count 1", got, err)
	}
	if f.revokedAt(t, second.TokenID) == nil {
		t.Error("named token not revoked")
	}
	// Revoking it again is idempotent.
	if got, err := f.auth.Revoke(t.Context(), first.User.ID, first.TokenID, RevokeRequest{TokenID: testutil.Ptr(second.TokenID.String())}); err != nil || got.RevokedCount != 0 {
		t.Errorf("revoke again = %+v, %v; want count 0", got, err)
	}

	// scope others leaves the current token alone.
	if got, err := f.auth.Revoke(t.Context(), first.User.ID, first.TokenID, RevokeRequest{Scope: testutil.Ptr(ScopeOthers)}); err != nil || got.RevokedCount != 1 {
		t.Fatalf("revoke others = %+v, %v; want count 1", got, err)
	}
	if f.revokedAt(t, first.TokenID) != nil {
		t.Error("current token must survive scope others")
	}
	if f.revokedAt(t, third.TokenID) == nil {
		t.Error("other token not revoked")
	}

	// scope all includes the current token.
	if got, err := f.auth.Revoke(t.Context(), first.User.ID, first.TokenID, RevokeRequest{Scope: testutil.Ptr(ScopeAll)}); err != nil || got.RevokedCount != 1 {
		t.Fatalf("revoke all = %+v, %v; want count 1", got, err)
	}
	if f.revokedAt(t, first.TokenID) == nil {
		t.Error("current token must be revoked by scope all")
	}
}

func TestAuthRevokeErrors(t *testing.T) {
	f := newAuthFixture(t)
	user := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	other := f.seedUser(t, "other", "secret", domain.RoleUser)
	foreignRaw, foreignID := testutil.SeedToken(t, f.db, other.ID)
	_ = foreignRaw

	tests := []struct {
		name     string
		req      RevokeRequest
		want     []string
		notFound bool
	}{
		{"neither", RevokeRequest{}, []string{"token_id:required", "scope:required"}, false},
		{"both", RevokeRequest{TokenID: testutil.Ptr(uuid.NewString()), Scope: testutil.Ptr(ScopeOthers)}, []string{"token_id:invalid_value", "scope:invalid_value"}, false},
		{"bad scope", RevokeRequest{Scope: testutil.Ptr("some")}, []string{"scope:invalid_value"}, false},
		{"bad id", RevokeRequest{TokenID: testutil.Ptr("not-a-uuid")}, []string{"token_id:invalid_format"}, false},
		{"foreign id", RevokeRequest{TokenID: testutil.Ptr(foreignID.String())}, nil, true},
		{"unknown id", RevokeRequest{TokenID: testutil.Ptr(uuid.NewString())}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.auth.Revoke(t.Context(), user.ID, uuid.Nil, tt.req)
			if tt.notFound {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("err = %v (%T), want not found", err, err)
				}
				return
			}
			got := authTestIssues(t, err)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
		})
	}
	if f.revokedAt(t, foreignID) != nil {
		t.Error("a foreign token was revoked")
	}
}

func TestAuthListTokens(t *testing.T) {
	f := newAuthFixture(t)
	user := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	older := testutil.Ptr(authTestNow)
	newer := testutil.Ptr(authTestNow.Add(time.Minute))
	_, firstID := testutil.SeedToken(t, f.db, user.ID, testutil.WithTokenCreatedAt(*older), testutil.WithTokenDevice("iPhone"))
	_, secondID := testutil.SeedToken(t, f.db, user.ID, testutil.WithTokenCreatedAt(*newer), testutil.WithTokenDevice("iPad"))

	res, err := f.auth.ListTokens(t.Context(), user.ID, secondID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if res.Items[0].ID != secondID || !res.Items[0].Current {
		t.Errorf("items[0] = %+v, want the newer current token", res.Items[0])
	}
	if res.Items[1].ID != firstID || res.Items[1].Current {
		t.Errorf("items[1] = %+v, want the older non-current token", res.Items[1])
	}
	if res.Items[0].DeviceName == nil || *res.Items[0].DeviceName != "iPad" {
		t.Errorf("device_name = %v, want iPad", res.Items[0].DeviceName)
	}
}

func TestAuthListTokensExcludesExpiredAndRevoked(t *testing.T) {
	f := newAuthFixture(t)
	user := f.seedUser(t, "ihsan", "secret", domain.RoleUser)
	testutil.SeedToken(t, f.db, user.ID, testutil.WithTokenExpiresAt(authTestNow.Add(time.Minute)), testutil.WithTokenDevice("expires"))
	testutil.SeedToken(t, f.db, user.ID, testutil.WithTokenRevoked(authTestNow.Add(-time.Minute)))

	f.clk.Advance(2 * time.Minute)
	res, err := f.auth.ListTokens(t.Context(), user.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("items = %+v, want none", res.Items)
	}
}
