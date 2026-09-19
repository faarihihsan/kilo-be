package main

import (
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/migrations"
)

// configEnvVars is every variable config.Load reads. Tests that call the
// command line blank them all first, so the developer's shell cannot leak in.
var configEnvVars = []string{
	"APP_ENV", "HTTP_ADDR", "DATABASE_URL", "DB_MAX_CONNS", "MEDIA_DIR",
	"MEDIA_BASE_URL", "TRUSTED_PROXY_CIDRS", "TOKEN_TTL_DAYS",
	"LOGIN_MAX_FAILS_USER", "LOGIN_MAX_FAILS_IP", "LOGIN_LOCK_MINUTES",
	"ARGON2_MEMORY_KIB", "ARGON2_TIME", "ARGON2_PARALLELISM",
	"ARGON2_MAX_CONCURRENT", "LOG_LEVEL",
}

// setEnv replaces the process environment seen by config.Load with exactly
// overrides (everything else blank, which counts as unset).
func setEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	for _, name := range configEnvVars {
		t.Setenv(name, "")
	}
	for name, value := range overrides {
		t.Setenv(name, value)
	}
}

// devEnv is a valid development environment for databaseURL, with a private
// media directory.
func devEnv(t *testing.T, databaseURL string) map[string]string {
	t.Helper()
	return map[string]string{
		"APP_ENV":      "development",
		"DATABASE_URL": databaseURL,
		"MEDIA_DIR":    t.TempDir(),
		"HTTP_ADDR":    "127.0.0.1:0",
	}
}

// testConfig parses env the way config.Load does, without touching the
// process environment.
func testConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("test config: %v", err)
	}
	return cfg
}

// keepSlogDefault restores slog's default logger when the test ends, for tests
// of subcommands that replace it.
func keepSlogDefault(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func contains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, want it to contain %q", what, got, w)
		}
	}
}

// migrationCount is the number of embedded migrations, so tests keep working
// when the orchestrator adds 0006 and later.
func migrationCount(t *testing.T) int {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no embedded migrations: %v", err)
	}
	return len(files)
}
