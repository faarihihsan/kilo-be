package main

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
	"workout-tracker-be/internal/testutil"
)

func TestMediaCLIUsageErrors(t *testing.T) {
	setEnv(t, nil) // a bad command line must not need a valid configuration
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", []string{"media"}, "needs a subcommand"},
		{"unknown subcommand", []string{"media", "frobnicate"}, "unknown media command"},
		{"unexpected arguments", []string{"media", "gc", "now"}, "unexpected arguments"},
		{"unknown flag", []string{"media", "gc", "--frobnicate"}, "media gc:"},
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

// TestMediaGC creates one referenced file and one orphan, then checks that
// --dry-run removes nothing and a real run removes only the orphan.
func TestMediaGC(t *testing.T) {
	url := testutil.NewEmptyDatabase(t)
	mediaDir := t.TempDir()
	keepSlogDefault(t)
	env := devEnv(t, url)
	env["MEDIA_DIR"] = mediaDir
	setEnv(t, env)
	if code, _, stderr := runCLI(t, "migrate", "up"); code != exitOK {
		t.Fatalf("migrate up: exit %d, stderr: %s", code, stderr)
	}

	db, err := store.Open(t.Context(), url, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, _ := testutil.SeedUser(t, db, domain.RoleUser)

	files, err := media.NewStore(mediaDir)
	if err != nil {
		t.Fatal(err)
	}

	// A file referenced by an exercise row.
	refData := []byte("referenced image bytes, content does not matter to gc")
	refHash := media.Hash(refData)
	refID := testutil.SeedExercise(t, db, user,
		testutil.WithExerciseImage(refHash, domain.ImageExtJPG, len(refData)))
	if err := files.Put(refID, refHash, domain.ImageExtJPG, refData); err != nil {
		t.Fatalf("put referenced file: %v", err)
	}

	// A file no row points at.
	orphanData := []byte("orphan image bytes")
	orphanHash := media.Hash(orphanData)
	orphanID := uuid.New()
	if err := files.Put(orphanID, orphanHash, domain.ImageExtPNG, orphanData); err != nil {
		t.Fatalf("put orphan file: %v", err)
	}

	// Both files must be older than the sweep grace period or they are kept as
	// uploads in flight.
	mediaAgeFilesToOld(t, files, refID, refHash, domain.ImageExtJPG)
	mediaAgeFilesToOld(t, files, orphanID, orphanHash, domain.ImageExtPNG)

	if code, _, stderr := runCLI(t, "media", "gc", "--dry-run"); code != exitOK {
		t.Fatalf("media gc --dry-run: exit %d, stderr: %s", code, stderr)
	}
	mediaRequireExists(t, files, refID, refHash, domain.ImageExtJPG, true)
	mediaRequireExists(t, files, orphanID, orphanHash, domain.ImageExtPNG, true)

	if code, _, stderr := runCLI(t, "media", "gc"); code != exitOK {
		t.Fatalf("media gc: exit %d, stderr: %s", code, stderr)
	}
	mediaRequireExists(t, files, refID, refHash, domain.ImageExtJPG, true)
	mediaRequireExists(t, files, orphanID, orphanHash, domain.ImageExtPNG, false)
}

// mediaAgeFilesToOld backdates a stored file past media.DefaultSweepMinAge.
func mediaAgeFilesToOld(t *testing.T, files *media.Store, id uuid.UUID, hash string, ext domain.ImageExt) {
	t.Helper()
	path, err := files.Abs(id, hash, ext)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * media.DefaultSweepMinAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age %s: %v", path, err)
	}
}

func mediaRequireExists(t *testing.T, files *media.Store, id uuid.UUID, hash string, ext domain.ImageExt, want bool) {
	t.Helper()
	got, err := files.Exists(id, hash, ext)
	if err != nil {
		t.Fatalf("exists %s/%s.%s: %v", id, hash, ext, err)
	}
	if got != want {
		t.Errorf("file %s/%s.%s exists = %v, want %v", id, hash, ext, got, want)
	}
}
