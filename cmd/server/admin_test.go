package main

import (
	"os"
	"testing"
	"time"

	"workout-tracker-be/internal/auth"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

// withAdminStdin replaces os.Stdin with a pipe carrying input, so the password
// prompt path (non-terminal) can be tested.
func withAdminStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

func TestAdminCLIUsageErrors(t *testing.T) {
	setEnv(t, nil) // a bad command line must not need a valid configuration
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", []string{"admin"}, "needs a subcommand"},
		{"unknown subcommand", []string{"admin", "frobnicate"}, "unknown admin command"},
		{"create-user without username", []string{"admin", "create-user", "--password", "pw"}, "--username is required"},
		{"create-user bad role", []string{"admin", "create-user", "--username", "boss", "--password", "pw", "--role", "root"}, "--role must be"},
		{"create-user extra arguments", []string{"admin", "create-user", "--username", "boss", "--password", "pw", "extra"}, "unexpected arguments"},
		{"reset-password without username", []string{"admin", "reset-password", "--password", "pw"}, "--username is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tt.args...)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d; stderr: %s", code, exitUsage, stderr)
			}
			contains(t, "stderr", stderr, tt.want)
		})
	}
}

func TestAdminCLICreateUserAndResetPassword(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	keepSlogDefault(t)
	setEnv(t, devEnv(t, url))
	if code, _, stderr := runCLI(t, "migrate", "up"); code != exitOK {
		t.Fatalf("migrate up: exit %d, stderr: %s", code, stderr)
	}

	if code, _, stderr := runCLI(t, "admin", "create-user",
		"--username", "boss", "--password", "first", "--role", "admin"); code != exitOK {
		t.Fatalf("create-user: exit %d, stderr: %s", code, stderr)
	}

	db, err := store.Open(t.Context(), url, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := store.NewUsers(db).GetByUsername(t.Context(), "boss")
	if err != nil {
		t.Fatalf("created user not found: %v", err)
	}
	if user.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", user.Role)
	}
	_, tokenID := testutil.SeedToken(t, db, user.ID)

	if code, _, stderr := runCLI(t, "admin", "reset-password",
		"--username", "boss", "--password", "second"); code != exitOK {
		t.Fatalf("reset-password: exit %d, stderr: %s", code, stderr)
	}
	var revoked *time.Time
	if err := db.QueryRow(t.Context(), `SELECT revoked_at FROM auth_tokens WHERE id = $1`, tokenID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked == nil {
		t.Error("reset-password did not revoke the user's tokens")
	}
}

func TestAdminCLICreateUserBypassesReservedAndReadsStdin(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	keepSlogDefault(t)
	setEnv(t, devEnv(t, url))
	if code, _, stderr := runCLI(t, "migrate", "up"); code != exitOK {
		t.Fatalf("migrate up: exit %d, stderr: %s", code, stderr)
	}

	withAdminStdin(t, "fromstdin\n")
	if code, _, stderr := runCLI(t, "admin", "create-user", "--username", "admin"); code != exitOK {
		t.Fatalf("create-user: exit %d, stderr: %s", code, stderr)
	}

	db, err := store.Open(t.Context(), url, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := store.NewUsers(db).GetByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatalf("reserved name was not created: %v", err)
	}
	hasher, err := auth.NewHasher(auth.HasherConfig{MemoryKiB: 8192, Time: 1, Parallelism: 1, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := hasher.Verify(t.Context(), "fromstdin", user.PasswordHash); err != nil || !ok {
		t.Errorf("stdin password does not verify: %v, %v", ok, err)
	}
}
