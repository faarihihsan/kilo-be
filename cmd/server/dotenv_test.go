package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv unsets keys for the duration of the test and restores them after, so
// loadDotEnv sees a clean slate without leaking into other tests.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		prev, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDotEnvReadsFile(t *testing.T) {
	keys := []string{"APP_ENV", "DATABASE_URL", "MEDIA_DIR", "QUOTED", "HASH", "SPACED"}
	clearEnv(t, keys...)

	path := writeEnvFile(t, `# a comment
APP_ENV=development

export DATABASE_URL=postgres://u:p@localhost:55432/workout_dev?sslmode=disable
MEDIA_DIR="./var/media"
QUOTED='single'
HASH=abc#def
SPACED = value with spaces
`)
	t.Setenv("ENV_FILE", path)

	if err := loadDotEnv(); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}

	want := map[string]string{
		"APP_ENV":      "development",
		"DATABASE_URL": "postgres://u:p@localhost:55432/workout_dev?sslmode=disable",
		"MEDIA_DIR":    "./var/media",
		"QUOTED":       "single",
		"HASH":         "abc#def", // no inline comments
		"SPACED":       "value with spaces",
	}
	for k, w := range want {
		if got := os.Getenv(k); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
}

func TestLoadDotEnvRealEnvironmentWins(t *testing.T) {
	path := writeEnvFile(t, "APP_ENV=development\nMEDIA_DIR=./from-file\n")
	t.Setenv("ENV_FILE", path)
	t.Setenv("APP_ENV", "production") // already set: must not be overwritten
	clearEnv(t, "MEDIA_DIR")          // absent: taken from the file

	if err := loadDotEnv(); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}
	if got := os.Getenv("APP_ENV"); got != "production" {
		t.Errorf("APP_ENV = %q, want production (real env wins)", got)
	}
	if got := os.Getenv("MEDIA_DIR"); got != "./from-file" {
		t.Errorf("MEDIA_DIR = %q, want ./from-file", got)
	}
}

func TestLoadDotEnvMissingDefaultIsNotAnError(t *testing.T) {
	clearEnv(t, "ENV_FILE")
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	if err := loadDotEnv(); err != nil {
		t.Fatalf("loadDotEnv with no .env = %v, want nil", err)
	}
}

func TestLoadDotEnvMissingExplicitFileIsAnError(t *testing.T) {
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "nope"))
	if err := loadDotEnv(); err == nil {
		t.Fatal("loadDotEnv with a missing ENV_FILE = nil, want an error")
	}
}

func TestLoadDotEnvRejectsMalformedLines(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"no equals", "NOPE\n", "KEY=value"},
		{"invalid name", "1BAD=x\n", "invalid variable name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", writeEnvFile(t, tt.content))
			err := loadDotEnv()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
