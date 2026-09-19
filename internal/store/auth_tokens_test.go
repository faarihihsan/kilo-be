package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// authTokenState reads revoked_at and last_used_at straight from the table.
func authTokenState(t *testing.T, db *store.DB, id uuid.UUID) (revoked, lastUsed *time.Time) {
	t.Helper()
	err := db.QueryRow(t.Context(),
		`SELECT revoked_at, last_used_at FROM auth_tokens WHERE id = $1`, id).Scan(&revoked, &lastUsed)
	if err != nil {
		t.Fatalf("read token %v: %v", id, err)
	}
	return revoked, lastUsed
}

func authTokenCount(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM auth_tokens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func authTimeEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func authStr(s string) *string { return &s }

func TestAuthTokensCreate(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()
	raw, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}

	info, err := tokens.Create(t.Context(), store.TokenCreate{
		UserID: uid, Hash: hash, DeviceName: authStr("Ihsan's iPhone"),
		CreatedAt: now, ExpiresAt: now.Add(domain.DefaultTokenTTL),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID == uuid.Nil || info.ID.Version() != 7 {
		t.Errorf("id = %v, want a UUID v7", info.ID)
	}
	if info.DeviceName == nil || *info.DeviceName != "Ihsan's iPhone" || info.LastUsedAt != nil ||
		!info.CreatedAt.Equal(now) || !info.ExpiresAt.Equal(now.Add(domain.DefaultTokenTTL)) {
		t.Errorf("Create returned %+v", info)
	}

	// The row is found by the token's hash and carries the user's role.
	rec, err := tokens.LookupByHash(t.Context(), auth.HashToken(raw))
	if err != nil {
		t.Fatalf("LookupByHash: %v", err)
	}
	if rec.TokenID != info.ID || rec.UserID != uid || rec.Role != domain.RoleUser ||
		!authTimeEq(&rec.CreatedAt, &info.CreatedAt) || !authTimeEq(&rec.ExpiresAt, &info.ExpiresAt) ||
		rec.RevokedAt != nil || rec.LastUsedAt != nil {
		t.Errorf("stored record %+v differs from created %+v", rec, info)
	}

	// Only the hash is stored: the raw token appears nowhere in the row.
	var rowText string
	if err := db.QueryRow(t.Context(), `SELECT t::text FROM auth_tokens t WHERE id = $1`, info.ID).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, raw) || strings.Contains(rowText, strings.TrimPrefix(raw, domain.TokenPrefix)) {
		t.Error("the raw token is stored in auth_tokens")
	}
}

func TestAuthTokensCreateOptionalDeviceAndErrors(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()
	newHash := func() []byte { _, h, _ := auth.NewToken(); return h }

	// No device name is NULL, not "".
	info, err := tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: newHash(), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil || info.DeviceName != nil {
		t.Fatalf("Create without device: %+v, %v", info, err)
	}
	list, _ := tokens.ListByUser(t.Context(), uid, now)
	if len(list) != 1 || list[0].DeviceName != nil {
		t.Errorf("listed device name = %v, want nil", list)
	}

	// Times are truncated to what the database stores, and the returned value
	// is the stored one.
	at := time.Date(2027, 1, 2, 3, 4, 5, 987654321, time.UTC)
	info, err = tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: newHash(), CreatedAt: at, ExpiresAt: at.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if want := at.Truncate(time.Microsecond); !info.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want %v", info.CreatedAt, want)
	}
	revoked, _ := authTokenState(t, db, info.ID)
	if revoked != nil {
		t.Error("new token is revoked")
	}

	// Duplicate hash.
	h := newHash()
	if _, err := tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: h, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, err = tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: h, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if name, ok := store.IsUniqueViolation(err); !ok || name != "auth_tokens_token_hash_uniq" {
		t.Errorf("duplicate hash: err = %v, want auth_tokens_token_hash_uniq", err)
	}

	// Unknown user.
	_, err = tokens.Create(t.Context(), store.TokenCreate{UserID: uuid.New(), Hash: newHash(), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if _, ok := store.IsForeignKeyViolation(err); !ok {
		t.Errorf("unknown user: err = %v, want a foreign key violation", err)
	}

	// Device name longer than 100 characters.
	_, err = tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: newHash(), DeviceName: authStr(strings.Repeat("x", 101)), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if _, ok := store.IsCheckViolation(err); !ok {
		t.Errorf("long device name: err = %v, want a check violation", err)
	}
	// Exactly 100 is fine, counted in characters.
	_, err = tokens.Create(t.Context(), store.TokenCreate{UserID: uid, Hash: newHash(), DeviceName: authStr(strings.Repeat("é", 100)), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Errorf("100 character device name: %v", err)
	}
}

func TestAuthTokensLookupByHash(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	adminID, _ := testutil.SeedUser(t, db, domain.RoleAdmin)
	now := authBase()
	revokedAt := now.Add(-time.Hour)
	lastUsed := now.Add(-2 * time.Hour)

	rawActive, activeID := testutil.SeedToken(t, db, uid, testutil.WithTokenDevice("phone"), testutil.WithTokenLastUsedAt(lastUsed))
	rawAdmin, adminTokenID := testutil.SeedToken(t, db, adminID)
	rawExpired, expiredID := testutil.SeedToken(t, db, uid, testutil.WithTokenExpired())
	rawRevoked, revokedID := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(revokedAt))

	rec, err := tokens.LookupByHash(t.Context(), testutil.HashToken(rawActive))
	if err != nil {
		t.Fatal(err)
	}
	if rec.TokenID != activeID || rec.UserID != uid || rec.Role != domain.RoleUser ||
		rec.DeviceName == nil || *rec.DeviceName != "phone" ||
		!authTimeEq(rec.LastUsedAt, &lastUsed) || rec.RevokedAt != nil {
		t.Errorf("active token record = %+v", rec)
	}
	for name, tm := range map[string]time.Time{"created_at": rec.CreatedAt, "expires_at": rec.ExpiresAt} {
		if tm.Location() != time.UTC {
			t.Errorf("%s is not in UTC: %v", name, tm.Location())
		}
	}
	if rec.LastUsedAt.Location() != time.UTC {
		t.Error("last_used_at is not in UTC")
	}

	admin, err := tokens.LookupByHash(t.Context(), testutil.HashToken(rawAdmin))
	if err != nil || admin.TokenID != adminTokenID || admin.UserID != adminID || admin.Role != domain.RoleAdmin {
		t.Errorf("admin token record = %+v, %v", admin, err)
	}

	// Expired and revoked tokens are returned as they are; the caller decides.
	expired, err := tokens.LookupByHash(t.Context(), testutil.HashToken(rawExpired))
	if err != nil || expired.TokenID != expiredID || !expired.ExpiresAt.Before(now) || expired.RevokedAt != nil {
		t.Errorf("expired token record = %+v, %v", expired, err)
	}
	revoked, err := tokens.LookupByHash(t.Context(), testutil.HashToken(rawRevoked))
	if err != nil || revoked.TokenID != revokedID || !authTimeEq(revoked.RevokedAt, &revokedAt) || revoked.RevokedAt.Location() != time.UTC {
		t.Errorf("revoked token record = %+v, %v", revoked, err)
	}

	// Unknown and malformed hashes are not found.
	_, unknownHash, _ := auth.NewToken()
	for name, h := range map[string][]byte{
		"unknown":     unknownHash,
		"empty":       nil,
		"short":       {1, 2, 3},
		"the raw one": []byte(rawActive), // the token itself is not its hash
	} {
		_, err := tokens.LookupByHash(t.Context(), h)
		var nf *domain.NotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("%s: err = %v, want *domain.NotFoundError", name, err)
		}
	}
}

func TestAuthTokensTouchLastUsed(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()
	_, id := testutil.SeedToken(t, db, uid)

	// Never used: set.
	changed, err := tokens.TouchLastUsed(t.Context(), id, now)
	if err != nil || !changed {
		t.Fatalf("first touch = %v, %v; want true", changed, err)
	}
	if _, lu := authTokenState(t, db, id); !authTimeEq(lu, &now) {
		t.Errorf("last_used_at = %v, want %v", lu, now)
	}

	// Within the hour: untouched, however often it is asked.
	for _, later := range []time.Duration{time.Second, 30 * time.Minute, time.Hour - time.Microsecond} {
		changed, err := tokens.TouchLastUsed(t.Context(), id, now.Add(later))
		if err != nil || changed {
			t.Errorf("touch after %v = %v, %v; want false (at most hourly)", later, changed, err)
		}
	}
	if _, lu := authTokenState(t, db, id); !authTimeEq(lu, &now) {
		t.Errorf("last_used_at moved to %v within the hour", lu)
	}

	// Exactly one hour later and after: updated.
	next := now.Add(time.Hour)
	if changed, err := tokens.TouchLastUsed(t.Context(), id, next); err != nil || !changed {
		t.Errorf("touch after exactly 1h = %v, %v; want true", changed, err)
	}
	if _, lu := authTokenState(t, db, id); !authTimeEq(lu, &next) {
		t.Errorf("last_used_at = %v, want %v", lu, next)
	}
	far := next.Add(48 * time.Hour)
	if changed, _ := tokens.TouchLastUsed(t.Context(), id, far); !changed {
		t.Error("touch two days later did not update")
	}

	// Unknown token: nothing to update.
	if changed, err := tokens.TouchLastUsed(t.Context(), uuid.New(), now); err != nil || changed {
		t.Errorf("touch of an unknown token = %v, %v; want false, nil", changed, err)
	}
}

func TestAuthTokensListByUser(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	otherID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()

	oldest := now.Add(-72 * time.Hour)
	middle := now.Add(-48 * time.Hour)
	newest := now.Add(-24 * time.Hour)
	lastUsed := now.Add(-time.Hour)
	// Inserted out of order to prove the ordering is by created_at DESC.
	_, midID := testutil.SeedToken(t, db, uid, testutil.WithTokenCreatedAt(middle), testutil.WithTokenDevice("tablet"))
	_, newID := testutil.SeedToken(t, db, uid, testutil.WithTokenCreatedAt(newest), testutil.WithTokenDevice("phone"), testutil.WithTokenLastUsedAt(lastUsed))
	_, oldID := testutil.SeedToken(t, db, uid, testutil.WithTokenCreatedAt(oldest))
	// Not listed: revoked, expired, expiring exactly now, another user's.
	testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(now.Add(-time.Minute)))
	testutil.SeedToken(t, db, uid, testutil.WithTokenExpiresAt(now.Add(-time.Second)))
	testutil.SeedToken(t, db, uid, testutil.WithTokenExpiresAt(now))
	testutil.SeedToken(t, db, otherID, testutil.WithTokenCreatedAt(now.Add(-time.Hour)))

	got, err := tokens.ListByUser(t.Context(), uid, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != newID || got[1].ID != midID || got[2].ID != oldID {
		t.Fatalf("listed %+v, want [%v %v %v] (newest first, active only, own only)", got, newID, midID, oldID)
	}
	first := got[0]
	if first.DeviceName == nil || *first.DeviceName != "phone" || !authTimeEq(first.LastUsedAt, &lastUsed) ||
		!first.CreatedAt.Equal(newest) || first.ExpiresAt.Before(now) || first.CreatedAt.Location() != time.UTC {
		t.Errorf("first item = %+v", first)
	}
	if got[2].DeviceName != nil || got[2].LastUsedAt != nil {
		t.Errorf("oldest item has device %v, last used %v; want nil, nil", got[2].DeviceName, got[2].LastUsedAt)
	}

	// Tokens expire as time moves on.
	later, _ := tokens.ListByUser(t.Context(), uid, now.Add(2*domain.DefaultTokenTTL))
	if len(later) != 0 || later == nil {
		t.Errorf("list after everything expired = %#v, want an empty non-nil slice", later)
	}
	none, err := tokens.ListByUser(t.Context(), uuid.New(), now)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("list of an unknown user = %#v, %v; want an empty slice", none, err)
	}
}

func TestAuthTokensListOrdersByIDWhenCreatedAtTies(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()
	at := now.Add(-time.Hour)
	for range 5 {
		testutil.SeedToken(t, db, uid, testutil.WithTokenCreatedAt(at))
	}
	got, err := tokens.ListByUser(t.Context(), uid, now)
	if err != nil || len(got) != 5 {
		t.Fatalf("list = %d items, %v", len(got), err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID.String() < got[i].ID.String() {
			t.Errorf("items %d and %d are not in descending id order", i-1, i)
		}
	}
}

func TestAuthTokensRevokeByID(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	otherUID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()

	_, activeID := testutil.SeedToken(t, db, uid)
	_, otherActive := testutil.SeedToken(t, db, uid)
	_, expiredID := testutil.SeedToken(t, db, uid, testutil.WithTokenExpired())
	earlier := now.Add(-time.Hour)
	_, revokedID := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(earlier))
	_, foreignID := testutil.SeedToken(t, db, otherUID)

	n, err := tokens.RevokeByID(t.Context(), uid, activeID, now)
	if err != nil || n != 1 {
		t.Fatalf("revoke own active token = %d, %v; want 1", n, err)
	}
	if rev, _ := authTokenState(t, db, activeID); !authTimeEq(rev, &now) {
		t.Errorf("revoked_at = %v, want %v", rev, now)
	}
	if rev, _ := authTokenState(t, db, otherActive); rev != nil {
		t.Error("another token of the user was revoked too")
	}

	// Idempotent: already revoked or expired tokens count 0 and are left alone.
	later := now.Add(time.Minute)
	for name, id := range map[string]uuid.UUID{"already revoked": activeID, "revoked earlier": revokedID, "expired": expiredID} {
		n, err := tokens.RevokeByID(t.Context(), uid, id, later)
		if err != nil || n != 0 {
			t.Errorf("revoke %s token = %d, %v; want 0, nil", name, n, err)
		}
	}
	if rev, _ := authTokenState(t, db, activeID); !authTimeEq(rev, &now) {
		t.Errorf("revoking twice moved revoked_at to %v", rev)
	}
	if rev, _ := authTokenState(t, db, revokedID); !authTimeEq(rev, &earlier) {
		t.Errorf("revoked_at of an old revoked token changed to %v", rev)
	}
	if rev, _ := authTokenState(t, db, expiredID); rev != nil {
		t.Errorf("an expired token was marked revoked: %v", rev)
	}

	// Ownership: another user's token and an unknown id are both "not found",
	// and the foreign token stays valid.
	for name, id := range map[string]uuid.UUID{"another user's": foreignID, "unknown": uuid.New()} {
		n, err := tokens.RevokeByID(t.Context(), uid, id, now)
		var nf *domain.NotFoundError
		if !errors.As(err, &nf) || n != 0 {
			t.Errorf("revoke %s token = %d, %v; want *domain.NotFoundError", name, n, err)
		}
	}
	if rev, _ := authTokenState(t, db, foreignID); rev != nil {
		t.Error("a token of another user was revoked through this user's id")
	}
	// And the owner can revoke it.
	if n, err := tokens.RevokeByID(t.Context(), otherUID, foreignID, now); err != nil || n != 1 {
		t.Errorf("owner revoke = %d, %v", n, err)
	}
}

func TestAuthTokensRevokeOthers(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	otherUID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()

	_, current := testutil.SeedToken(t, db, uid)
	_, a := testutil.SeedToken(t, db, uid)
	_, b := testutil.SeedToken(t, db, uid)
	earlier := now.Add(-time.Hour)
	_, alreadyRevoked := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(earlier))
	_, expired := testutil.SeedToken(t, db, uid, testutil.WithTokenExpired())
	_, foreign := testutil.SeedToken(t, db, otherUID)

	n, err := tokens.RevokeOthers(t.Context(), uid, current, now)
	if err != nil || n != 2 {
		t.Fatalf("RevokeOthers = %d, %v; want 2 (only the active others)", n, err)
	}
	for name, id := range map[string]uuid.UUID{"a": a, "b": b} {
		if rev, _ := authTokenState(t, db, id); !authTimeEq(rev, &now) {
			t.Errorf("token %s revoked_at = %v, want %v", name, rev, now)
		}
	}
	if rev, _ := authTokenState(t, db, current); rev != nil {
		t.Error("the current token was revoked")
	}
	if rev, _ := authTokenState(t, db, foreign); rev != nil {
		t.Error("another user's token was revoked")
	}
	if rev, _ := authTokenState(t, db, alreadyRevoked); !authTimeEq(rev, &earlier) {
		t.Errorf("an already revoked token was revoked again: %v", rev)
	}
	if rev, _ := authTokenState(t, db, expired); rev != nil {
		t.Errorf("an expired token was marked revoked: %v", rev)
	}

	if n, err := tokens.RevokeOthers(t.Context(), uid, current, now); err != nil || n != 0 {
		t.Errorf("second RevokeOthers = %d, %v; want 0", n, err)
	}
	// A current id that does not exist revokes everything active.
	if n, err := tokens.RevokeOthers(t.Context(), uid, uuid.New(), now); err != nil || n != 1 {
		t.Errorf("RevokeOthers with an unknown current id = %d, %v; want 1 (the former current)", n, err)
	}
}

func TestAuthTokensRevokeAllForUser(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	otherUID, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()

	_, a := testutil.SeedToken(t, db, uid)
	_, b := testutil.SeedToken(t, db, uid)
	_, c := testutil.SeedToken(t, db, uid)
	earlier := now.Add(-time.Hour)
	_, alreadyRevoked := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(earlier))
	_, expired := testutil.SeedToken(t, db, uid, testutil.WithTokenExpired())
	_, foreign := testutil.SeedToken(t, db, otherUID)

	n, err := tokens.RevokeAllForUser(t.Context(), uid, now)
	if err != nil || n != 3 {
		t.Fatalf("RevokeAllForUser = %d, %v; want 3", n, err)
	}
	for name, id := range map[string]uuid.UUID{"a": a, "b": b, "c": c} {
		if rev, _ := authTokenState(t, db, id); !authTimeEq(rev, &now) {
			t.Errorf("token %s revoked_at = %v, want %v", name, rev, now)
		}
	}
	if rev, _ := authTokenState(t, db, foreign); rev != nil {
		t.Error("another user's token was revoked")
	}
	if rev, _ := authTokenState(t, db, alreadyRevoked); !authTimeEq(rev, &earlier) {
		t.Errorf("an already revoked token changed: %v", rev)
	}
	if rev, _ := authTokenState(t, db, expired); rev != nil {
		t.Errorf("an expired token was marked revoked: %v", rev)
	}

	// Idempotent, and a user without tokens is fine.
	if n, err := tokens.RevokeAllForUser(t.Context(), uid, now.Add(time.Minute)); err != nil || n != 0 {
		t.Errorf("second RevokeAllForUser = %d, %v; want 0", n, err)
	}
	if n, err := tokens.RevokeAllForUser(t.Context(), uuid.New(), now); err != nil || n != 0 {
		t.Errorf("RevokeAllForUser of an unknown user = %d, %v; want 0, nil", n, err)
	}
	if active, _ := tokens.ListByUser(t.Context(), uid, now); len(active) != 0 {
		t.Errorf("%d tokens still active", len(active))
	}
}

func TestAuthTokensPurgeExpired(t *testing.T) {
	db := testutil.NewDB(t)
	tokens := store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	now := authBase()
	cutoff := now.Add(-domain.TokenPurgeAfter)
	day := 24 * time.Hour

	// Purged.
	_, expiredLongAgo := testutil.SeedToken(t, db, uid, testutil.WithTokenExpiresAt(cutoff.Add(-day)))
	_, revokedLongAgo := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(cutoff.Add(-day)))
	_, revokedLongAgoStillValid := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(cutoff.Add(-time.Second)))
	// Kept.
	_, expiredRecently := testutil.SeedToken(t, db, uid, testutil.WithTokenExpiresAt(cutoff.Add(day)))
	_, revokedRecently := testutil.SeedToken(t, db, uid, testutil.WithTokenRevoked(cutoff.Add(day)))
	_, expiresAtCutoff := testutil.SeedToken(t, db, uid, testutil.WithTokenExpiresAt(cutoff))
	_, active := testutil.SeedToken(t, db, uid)
	_, activeUsed := testutil.SeedToken(t, db, uid, testutil.WithTokenLastUsedAt(cutoff.Add(-90*day)))

	n, err := tokens.PurgeExpired(t.Context(), cutoff)
	if err != nil || n != 3 {
		t.Fatalf("PurgeExpired = %d, %v; want 3", n, err)
	}
	exists := func(id uuid.UUID) bool {
		var found bool
		if err := db.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM auth_tokens WHERE id = $1)`, id).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	for name, id := range map[string]uuid.UUID{"expired long ago": expiredLongAgo, "revoked long ago": revokedLongAgo, "revoked just before the cutoff": revokedLongAgoStillValid} {
		if exists(id) {
			t.Errorf("%s token was not purged", name)
		}
	}
	for name, id := range map[string]uuid.UUID{"expired recently": expiredRecently, "revoked recently": revokedRecently, "expires at the cutoff": expiresAtCutoff, "active": active, "active, used long ago": activeUsed} {
		if !exists(id) {
			t.Errorf("%s token was purged", name)
		}
	}
	if n, err := tokens.PurgeExpired(t.Context(), cutoff); err != nil || n != 0 {
		t.Errorf("second PurgeExpired = %d, %v; want 0", n, err)
	}
	if got := authTokenCount(t, db); got != 5 {
		t.Errorf("%d tokens left, want 5", got)
	}
}

// Every store method must work through the real login shape: create, look up
// by the hash of the raw token, use, revoke, look up again.
func TestAuthTokensLifecycle(t *testing.T) {
	db := testutil.NewDB(t)
	users, tokens := store.NewUsers(db), store.NewAuthTokens(db)
	now := authBase()
	user, err := users.Create(t.Context(), store.UserCreate{Username: "ihsan", PasswordHash: "h", Role: domain.RoleUser, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, _ := auth.NewToken()
	info, err := tokens.Create(t.Context(), store.TokenCreate{UserID: user.ID, Hash: hash, DeviceName: authStr("phone"), CreatedAt: now, ExpiresAt: now.Add(domain.DefaultTokenTTL)})
	if err != nil {
		t.Fatal(err)
	}

	rec, err := tokens.LookupByHash(t.Context(), auth.HashToken(raw))
	if err != nil || rec.TokenID != info.ID || rec.Role != domain.RoleUser {
		t.Fatalf("lookup = %+v, %v", rec, err)
	}
	if listed, _ := tokens.ListByUser(t.Context(), user.ID, now); len(listed) != 1 || listed[0].ID != info.ID {
		t.Fatalf("listed = %+v", listed)
	}
	if n, err := tokens.RevokeByID(t.Context(), user.ID, rec.TokenID, now); err != nil || n != 1 {
		t.Fatalf("logout = %d, %v", n, err)
	}
	rec, _ = tokens.LookupByHash(t.Context(), auth.HashToken(raw))
	if rec.RevokedAt == nil {
		t.Error("token is not revoked after logout")
	}
	if listed, _ := tokens.ListByUser(t.Context(), user.ID, now); len(listed) != 0 {
		t.Errorf("revoked token still listed: %+v", listed)
	}
}
