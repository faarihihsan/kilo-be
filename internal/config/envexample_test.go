package config

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// envExamplePath is the operator-facing template. It lives outside this
// package, so this test is what keeps it in step with Parse.
var envExamplePath = filepath.Join("..", "..", "deploy", "env.example")

// readEnvExample returns the active KEY=value lines of deploy/env.example that
// belong to the server. Commented-out lines are skipped, and so are the
// BACKUP_* and RCLONE_* settings, which only deploy/backup.sh reads.
func readEnvExample(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(envExamplePath)
	if err != nil {
		t.Fatalf("open %s: %v", envExamplePath, err)
	}
	defer f.Close()

	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("env.example: line %q is not KEY=value", line)
		}
		if strings.HasPrefix(key, "BACKUP_") || strings.HasPrefix(key, "RCLONE_") {
			continue
		}
		if _, dup := vars[key]; dup {
			t.Errorf("env.example: %s is set twice", key)
		}
		vars[key] = value
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return vars
}

// TestEnvExampleMatchesConfig fails when a variable is added to or removed from
// Parse without updating deploy/env.example, or when the example stops being
// a valid configuration.
func TestEnvExampleMatchesConfig(t *testing.T) {
	example := readEnvExample(t)

	// Every variable Parse looks up, found by recording the lookups.
	read := map[string]bool{}
	_, _ = Parse(func(name string) (string, bool) {
		read[name] = true
		return "", false
	})

	var undocumented, stale []string
	for name := range read {
		if _, ok := example[name]; !ok {
			undocumented = append(undocumented, name)
		}
	}
	for name := range example {
		if !read[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(stale)
	if len(undocumented) > 0 {
		t.Errorf("deploy/env.example lacks config variables: %v", undocumented)
	}
	if len(stale) > 0 {
		t.Errorf("deploy/env.example has variables config does not read: %v", stale)
	}

	got, err := Parse(lookupOf(example))
	if err != nil {
		t.Fatalf("deploy/env.example is not a valid production configuration: %v", err)
	}
	if !got.Env.IsProduction() {
		t.Errorf("env.example Env = %q, want production", got.Env)
	}

	// Apart from the three required values, the example spells out the defaults.
	minimal := mustParse(t, prodEnv())
	got.DatabaseURL, got.MediaDir, got.MediaBaseURL = minimal.DatabaseURL, minimal.MediaDir, minimal.MediaBaseURL
	if !reflect.DeepEqual(got, minimal) {
		t.Errorf("env.example differs from the defaults\n got: %#v\nwant: %#v", got, minimal)
	}

	if strings.Contains(example["DATABASE_URL"], "@") && !strings.Contains(example["DATABASE_URL"], "CHANGE_ME") {
		t.Errorf("env.example DATABASE_URL must use the CHANGE_ME placeholder, not a real password")
	}
}
