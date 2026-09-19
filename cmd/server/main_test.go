package main

import (
	"fmt"
	"strings"
	"testing"

	"workout-tracker-be/internal/testutil"
)

// runCLI runs the command line in-process and returns exit code, stdout and
// stderr.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRunUsageAndHelp(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{"no arguments", nil, exitUsage, nil, []string{"Usage: server <command>"}},
		{"unknown command", []string{"frobnicate"}, exitUsage, nil, []string{`unknown command "frobnicate"`, "Usage: server"}},
		{"help", []string{"help"}, exitOK, []string{"Usage: server <command>", "serve", "migrate up", "admin", "media gc"}, nil},
		{"--help", []string{"--help"}, exitOK, []string{"Usage: server"}, nil},
		{"version", []string{"version"}, exitOK, []string{"workout-tracker"}, nil},
		{"serve with arguments", []string{"serve", "now"}, exitUsage, nil, []string{"serve takes no arguments"}},
		{"migrate without subcommand", []string{"migrate"}, exitUsage, nil, []string{"migrate needs exactly one of"}},
		{"migrate sideways", []string{"migrate", "sideways"}, exitUsage, nil, []string{`unknown migrate command "sideways"`}},
		{"migrate too many arguments", []string{"migrate", "up", "now"}, exitUsage, nil, []string{"migrate needs exactly one of"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// None of these may need a valid configuration.
			setEnv(t, nil)
			code, stdout, stderr := runCLI(t, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr)
			}
			contains(t, "stdout", stdout, tt.wantStdout...)
			contains(t, "stderr", stderr, tt.wantStderr...)
		})
	}
}

func TestRunStubbedSubcommandsFailClearly(t *testing.T) {
	setEnv(t, nil)
	// admin is implemented by T5; media gc is still T6's stub.
	for _, args := range [][]string{{"media", "gc"}} {
		code, _, stderr := runCLI(t, args...)
		if code != exitError {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitError)
		}
		contains(t, strings.Join(args, " ")+" stderr", stderr, "not implemented yet")
	}
}

func TestSubcommandsReportAllConfigProblems(t *testing.T) {
	setEnv(t, nil) // DATABASE_URL and MEDIA_DIR are required
	for _, args := range [][]string{{"serve"}, {"migrate", "up"}, {"migrate", "status"}} {
		code, _, stderr := runCLI(t, args...)
		if code != exitError {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitError)
		}
		contains(t, strings.Join(args, " ")+" stderr", stderr, "DATABASE_URL is required", "MEDIA_DIR is required")
	}
}

func TestMigrateCommands(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	keepSlogDefault(t)
	setEnv(t, devEnv(t, url))

	// Empty database: everything pending.
	code, stdout, stderr := runCLI(t, "migrate", "status")
	if code != exitOK {
		t.Fatalf("migrate status: exit %d, stderr: %s", code, stderr)
	}
	latest := migrationCount(t)
	contains(t, "status before up", stdout, "0001_users.sql", "pending",
		fmt.Sprintf("database at version 0, binary at version %d", latest))
	if strings.Contains(stdout, "applied") {
		t.Errorf("status before up shows applied migrations:\n%s", stdout)
	}

	// migrate up applies everything, and again is a no-op.
	for range 2 {
		if code, _, stderr := runCLI(t, "migrate", "up"); code != exitOK {
			t.Fatalf("migrate up: exit %d, stderr: %s", code, stderr)
		}
	}
	code, stdout, stderr = runCLI(t, "migrate", "status")
	if code != exitOK {
		t.Fatalf("migrate status: exit %d, stderr: %s", code, stderr)
	}
	contains(t, "status after up", stdout, "0005_progress.sql", "applied",
		fmt.Sprintf("database at version %[1]d, binary at version %[1]d", latest))
	if strings.Contains(stdout, "pending") {
		t.Errorf("status after up still shows pending migrations:\n%s", stdout)
	}

	// Development may roll back one step.
	if code, _, stderr := runCLI(t, "migrate", "down"); code != exitOK {
		t.Fatalf("migrate down: exit %d, stderr: %s", code, stderr)
	}
	_, stdout, _ = runCLI(t, "migrate", "status")
	contains(t, "status after down", stdout,
		fmt.Sprintf("database at version %d, binary at version %d", latest-1, latest))
}

func TestMigrateDownIsRefusedInProduction(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	keepSlogDefault(t)
	setEnv(t, map[string]string{
		"APP_ENV":        "production",
		"DATABASE_URL":   url,
		"MEDIA_DIR":      t.TempDir(),
		"MEDIA_BASE_URL": "https://api.example.com",
	})
	if code, _, stderr := runCLI(t, "migrate", "up"); code != exitOK {
		t.Fatalf("migrate up: exit %d, stderr: %s", code, stderr)
	}

	code, _, stderr := runCLI(t, "migrate", "down")
	if code != exitError {
		t.Errorf("migrate down in production: exit %d, want %d", code, exitError)
	}
	contains(t, "stderr", stderr, "development only")

	// Nothing was rolled back.
	_, stdout, _ := runCLI(t, "migrate", "status")
	latest := migrationCount(t)
	contains(t, "status", stdout, fmt.Sprintf("database at version %[1]d, binary at version %[1]d", latest))
}
