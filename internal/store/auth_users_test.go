package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// authBase is a microsecond-precision "now" for the auth store tests.
func authBase() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func TestUsersCreateAndGet(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	now := authBase()

	created, err := users.Create(t.Context(), store.UserCreate{
		Username: "ihsan", PasswordHash: "hash-1", Role: domain.RoleUser, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil || created.ID.Version() != 7 {
		t.Errorf("id = %v, want a UUID v7", created.ID)
	}
	want := store.User{ID: created.ID, Username: "ihsan", PasswordHash: "hash-1", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now}
	if created != want {
		t.Errorf("Create returned %+v, want %+v", created, want)
	}

	byName, err := users.GetByUsername(t.Context(), "ihsan")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	byID, err := users.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byName != want || byID != want {
		t.Errorf("stored user differs:\n by name %+v\n by id   %+v\n want    %+v", byName, byID, want)
	}
	if byName.CreatedAt.Location() != time.UTC || byName.UpdatedAt.Location() != time.UTC {
		t.Error("timestamps are not in UTC")
	}
}

func TestUsersCreateAdminAndTruncatesTimes(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	at := time.Date(2026, 9, 19, 8, 30, 0, 123456789, time.FixedZone("x", 3600))

	u, err := users.Create(t.Context(), store.UserCreate{Username: "boss", PasswordHash: "h", Role: domain.RoleAdmin, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != domain.RoleAdmin {
		t.Errorf("role = %q", u.Role)
	}
	stored, _ := users.GetByID(t.Context(), u.ID)
	if !stored.CreatedAt.Equal(at.Truncate(time.Microsecond)) || stored != u {
		t.Errorf("stored %+v, created %+v: want equal, times truncated to microseconds", stored, u)
	}
}

func TestUsersCreateDuplicateUsernameIsAConflict(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	in := store.UserCreate{Username: "ihsan", PasswordHash: "h", Role: domain.RoleUser, CreatedAt: authBase()}
	if _, err := users.Create(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	_, err := users.Create(t.Context(), in)
	var conflict *domain.ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want *domain.ConflictError", err)
	}
	if conflict.Issue != domain.IssueAlreadyTaken || conflict.Field != "username" {
		t.Errorf("conflict = %+v, want issue already_taken on field username (spec 01)", conflict)
	}
	if conflict.Message != "Username already taken" {
		t.Errorf("message = %q", conflict.Message)
	}

	// Also for a different role and hash: the username is what collides.
	in.Role = domain.RoleAdmin
	in.PasswordHash = "other"
	if _, err := users.Create(t.Context(), in); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("err = %v, want a conflict", err)
	}
}

func TestUsersCreateRejectsInvalidInput(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)

	_, err := users.Create(t.Context(), store.UserCreate{Username: "ihsan", PasswordHash: "h", Role: "root", CreatedAt: authBase()})
	if err == nil {
		t.Error("an unknown role was accepted")
	}
	// The CHECK constraints stay reachable for callers that skip validation;
	// they are not conflicts.
	_, err = users.Create(t.Context(), store.UserCreate{Username: "Not_Normalized", PasswordHash: "h", Role: domain.RoleUser, CreatedAt: authBase()})
	if name, ok := store.IsCheckViolation(err); !ok || name != "users_username_format_chk" {
		t.Errorf("err = %v, want a users_username_format_chk violation", err)
	}
	if errors.Is(err, domain.ErrConflict) {
		t.Error("a format violation is not a conflict")
	}
}

func TestUsersLookupsNotFound(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	testutil.SeedUser(t, db, domain.RoleUser, testutil.WithUsername("ihsan"))

	for name, get := range map[string]func() error{
		"unknown username": func() error { _, err := users.GetByUsername(t.Context(), "nobody"); return err },
		"empty username":   func() error { _, err := users.GetByUsername(t.Context(), ""); return err },
		"exact match only": func() error { _, err := users.GetByUsername(t.Context(), "IHSAN"); return err },
		"unknown id":       func() error { _, err := users.GetByID(t.Context(), uuid.New()); return err },
		"nil id":           func() error { _, err := users.GetByID(t.Context(), uuid.Nil); return err },
	} {
		err := get()
		var nf *domain.NotFoundError
		if !errors.As(err, &nf) || !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: err = %v, want *domain.NotFoundError", name, err)
		}
	}
}

func TestUsersReadSeededRows(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	aid, aname := testutil.SeedUser(t, db, domain.RoleAdmin)
	uid, uname := testutil.SeedUser(t, db, domain.RoleUser, testutil.WithPasswordHash("$argon2id$custom"))

	a, err := users.GetByUsername(t.Context(), aname)
	if err != nil || a.ID != aid || a.Role != domain.RoleAdmin {
		t.Errorf("admin = %+v, %v", a, err)
	}
	u, err := users.GetByID(t.Context(), uid)
	if err != nil || u.Username != uname || u.Role != domain.RoleUser || u.PasswordHash != "$argon2id$custom" {
		t.Errorf("user = %+v, %v", u, err)
	}
}

func TestUsersUpdatePassword(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	created := authBase().Add(-48 * time.Hour)
	u, err := users.Create(t.Context(), store.UserCreate{Username: "ihsan", PasswordHash: "old", Role: domain.RoleUser, CreatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := users.Create(t.Context(), store.UserCreate{Username: "other", PasswordHash: "keep", Role: domain.RoleUser, CreatedAt: created})

	now := authBase()
	if err := users.UpdatePassword(t.Context(), u.ID, "new", now); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, _ := users.GetByID(t.Context(), u.ID)
	if got.PasswordHash != "new" || !got.UpdatedAt.Equal(now) || !got.CreatedAt.Equal(created) {
		t.Errorf("after update: %+v; want new hash, updated_at = now, created_at unchanged", got)
	}
	if o, _ := users.GetByID(t.Context(), other.ID); o.PasswordHash != "keep" || !o.UpdatedAt.Equal(created) {
		t.Errorf("another user changed: %+v", o)
	}

	err = users.UpdatePassword(t.Context(), uuid.New(), "x", now)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want not found", err)
	}
}

// A password change and a token revocation must be able to share a
// transaction (spec 14), and roll back together.
func TestUsersAndTokensShareATransaction(t *testing.T) {
	db := testutil.NewDB(t)
	users, tokens := store.NewUsers(db), store.NewAuthTokens(db)
	uid, _ := testutil.SeedUser(t, db, domain.RoleUser)
	rawA, _ := testutil.SeedToken(t, db, uid)
	rawB, _ := testutil.SeedToken(t, db, uid)
	now := authBase()
	errRollback := errors.New("rollback")

	err := db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if err := users.Using(tx).UpdatePassword(ctx, uid, "changed", now); err != nil {
			return err
		}
		n, err := tokens.Using(tx).RevokeAllForUser(ctx, uid, now)
		if err != nil || n != 2 {
			t.Errorf("RevokeAllForUser in tx = %d, %v; want 2", n, err)
		}
		// Visible inside the transaction...
		if u, _ := users.Using(tx).GetByID(ctx, uid); u.PasswordHash != "changed" {
			t.Error("the transaction does not see its own password change")
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithTx err = %v", err)
	}
	// ...and gone after the rollback.
	if u, _ := users.GetByID(t.Context(), uid); u.PasswordHash == "changed" {
		t.Error("password change survived the rollback")
	}
	for _, raw := range []string{rawA, rawB} {
		if rec, _ := tokens.LookupByHash(t.Context(), testutil.HashToken(raw)); rec.RevokedAt != nil {
			t.Error("token revocation survived the rollback")
		}
	}

	err = db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if err := users.Using(tx).UpdatePassword(ctx, uid, "changed", now); err != nil {
			return err
		}
		_, err := tokens.Using(tx).RevokeAllForUser(ctx, uid, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := users.GetByID(t.Context(), uid); u.PasswordHash != "changed" {
		t.Error("committed password change is missing")
	}
	if rec, _ := tokens.LookupByHash(t.Context(), testutil.HashToken(rawA)); rec.RevokedAt == nil {
		t.Error("committed revocation is missing")
	}
}

func TestUsersCreateInsideATransactionIsRolledBack(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	errRollback := errors.New("rollback")
	_ = db.WithTx(t.Context(), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := users.Using(tx).Create(ctx, store.UserCreate{Username: "temp", PasswordHash: "h", Role: domain.RoleUser, CreatedAt: authBase()}); err != nil {
			t.Fatal(err)
		}
		return errRollback
	})
	if _, err := users.GetByUsername(t.Context(), "temp"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want the rolled back user to be gone", err)
	}
}

func TestUsersMethodsReportDatabaseFailuresAsInternalErrors(t *testing.T) {
	db := testutil.NewDB(t)
	users := store.NewUsers(db)
	db.Close()
	_, err := users.GetByUsername(t.Context(), "ihsan")
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want a wrapped database error (not a not-found)", err)
	}
	if err != nil && strings.Contains(err.Error(), "password") {
		t.Errorf("error text mentions passwords: %v", err)
	}
}
