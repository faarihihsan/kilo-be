package media

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

var testID = uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010")

// newTestStore returns a store in a fresh directory nested one level below the
// temp dir, so tests can check that nothing escapes the root.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media")
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// testImage is a small valid JPEG with its hash and extension.
func testImage(t testing.TB, w, h int) (data []byte, hash string, ext domain.ImageExt) {
	t.Helper()
	data = jpegBytes(t, w, h)
	return data, Hash(data), domain.ImageExtJPG
}

// listDir returns the names in dir, or nil when it does not exist.
func listDir(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPathLayout(t *testing.T) {
	const hash = "9f2c4e1ab37d05c6"
	want := "exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp"
	if got := Path(testID, hash, domain.ImageExtWebP); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	for ext, suffix := range map[domain.ImageExt]string{
		domain.ImageExtJPG: ".jpg", domain.ImageExtPNG: ".png", domain.ImageExtWebP: ".webp",
	} {
		got := Path(testID, hash, ext)
		if !strings.HasSuffix(got, "/"+hash+suffix) || !strings.HasPrefix(got, "exercises/"+testID.String()+"/") {
			t.Fatalf("Path(%s) = %q", ext, got)
		}
	}
	s, root := newTestStore(t)
	if s.Path(testID, hash, domain.ImageExtWebP) != want {
		t.Fatal("Store.Path differs from Path")
	}
	abs, err := s.Abs(testID, hash, domain.ImageExtWebP)
	if err != nil || abs != filepath.Join(root, filepath.FromSlash(want)) {
		t.Fatalf("Abs = %q, %v", abs, err)
	}
	if Path(testID, "../etc/passwd", domain.ImageExtWebP) != "" || Path(testID, hash, "gif") != "" {
		t.Fatal("Path must return an empty string for invalid names")
	}
}

func TestNewStoreCreatesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b", "media")
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "exercises")); err != nil || !fi.IsDir() {
		t.Fatalf("exercises dir not created: %v", err)
	}
	if !filepath.IsAbs(s.Root()) {
		t.Fatalf("Root %q is not absolute", s.Root())
	}
	if _, err := NewStore(root); err != nil { // opening an existing root is fine
		t.Fatal(err)
	}
	if _, err := NewStore(""); err == nil {
		t.Fatal("empty root must be rejected")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(file); err == nil {
		t.Fatal("a root that is a regular file must be rejected")
	}
}

func TestPutExistsDeleteRoundTrip(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)

	if ok, err := s.Exists(testID, hash, ext); err != nil || ok {
		t.Fatalf("Exists before Put = %v, %v", ok, err)
	}
	if err := s.Put(testID, hash, ext, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ok, err := s.Exists(testID, hash, ext); err != nil || !ok {
		t.Fatalf("Exists after Put = %v, %v", ok, err)
	}

	final := filepath.Join(root, "exercises", testID.String(), hash+".jpg")
	if !bytes.Equal(mustRead(t, final), data) {
		t.Fatal("stored bytes differ from the input")
	}
	fi, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("file mode = %v, want 0644 (the proxy must be able to read it)", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(final))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm()&0o500 != 0o500 {
		t.Fatalf("directory mode = %v, want owner read+execute at least", di.Mode().Perm())
	}
	if names := listDir(t, filepath.Dir(final)); !slices.Equal(names, []string{hash + ".jpg"}) {
		t.Fatalf("directory holds %v, want only the image (no temp files)", names)
	}

	if err := s.Delete(testID, hash, ext); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := s.Exists(testID, hash, ext); ok {
		t.Fatal("still exists after Delete")
	}
	if names := listDir(t, filepath.Join(root, "exercises")); len(names) != 0 {
		t.Fatalf("empty exercise directory not removed: %v", names)
	}
	if _, err := os.Stat(filepath.Join(root, "exercises")); err != nil {
		t.Fatalf("Delete must not remove the exercises directory itself: %v", err)
	}
}

func TestPutAllFormats(t *testing.T) {
	s, root := newTestStore(t)
	for _, data := range [][]byte{jpegBytes(t, 8, 8), pngBytes(t, 8, 8), webpVP8(8, 8)} {
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Put(testID, info.Hash, info.Ext, data); err != nil {
			t.Fatalf("Put %s: %v", info.Ext, err)
		}
		got := mustRead(t, filepath.Join(root, filepath.FromSlash(Path(testID, info.Hash, info.Ext))))
		if !bytes.Equal(got, data) {
			t.Fatalf("%s bytes differ", info.Ext)
		}
	}
	if n := len(listDir(t, filepath.Join(root, "exercises", testID.String()))); n != 3 {
		t.Fatalf("%d files, want 3", n)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)
	dir := filepath.Join(root, "exercises", testID.String())
	final := filepath.Join(dir, hash+".jpg")

	if err := s.Put(testID, hash, ext, data); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(final, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := s.Put(testID, hash, ext, data); err != nil {
			t.Fatalf("repeat Put %d: %v", i, err)
		}
	}
	after, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		t.Fatal("an identical Put rewrote the file")
	}
	if names := listDir(t, dir); !slices.Equal(names, []string{hash + ".jpg"}) {
		t.Fatalf("directory holds %v", names)
	}
}

func TestPutReplacesADamagedFile(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)
	dir := filepath.Join(root, "exercises", testID.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, hash+".jpg")
	if err := os.WriteFile(final, []byte("half a fi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testID, hash, ext, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, final), data) {
		t.Fatal("damaged file was not replaced")
	}
}

func TestPutRejectsBadArguments(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)

	if err := s.Put(testID, "0000000000000000", ext, data); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("wrong hash: %v, want ErrHashMismatch", err)
	}
	if err := s.Put(testID, hash, ext, nil); err == nil {
		t.Fatal("empty data must be rejected")
	}
	if err := s.Put(testID, hash, ext, data[:len(data)-1]); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("truncated data: %v, want ErrHashMismatch", err)
	}
	if names := listDir(t, filepath.Join(root, "exercises")); len(names) != 0 {
		t.Fatalf("rejected Puts created %v", names)
	}
}

func TestDeleteMissingIsNotAnError(t *testing.T) {
	s, root := newTestStore(t)
	_, hash, ext := testImage(t, 16, 16)

	if err := s.Delete(testID, hash, ext); err != nil {
		t.Fatalf("Delete of a never-stored image: %v", err)
	}
	// Twice, and with the directory present but the file missing.
	if err := os.MkdirAll(filepath.Join(root, "exercises", testID.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Delete(testID, hash, ext); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
	if names := listDir(t, filepath.Join(root, "exercises")); len(names) != 0 {
		t.Fatalf("empty exercise directory left behind: %v", names)
	}
}

func TestDeleteKeepsOtherFilesAndDirectory(t *testing.T) {
	s, root := newTestStore(t)
	a, hashA, _ := testImage(t, 16, 16)
	b, hashB, _ := testImage(t, 17, 17)
	for _, p := range []struct {
		data []byte
		hash string
	}{{a, hashA}, {b, hashB}} {
		if err := s.Put(testID, p.hash, domain.ImageExtJPG, p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(testID, hashA, domain.ImageExtJPG); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "exercises", testID.String())
	if names := listDir(t, dir); !slices.Equal(names, []string{hashB + ".jpg"}) {
		t.Fatalf("directory holds %v, want only the other image", names)
	}
	// Another exercise is untouched.
	other := uuid.New()
	if err := s.Put(other, hashA, domain.ImageExtJPG, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(testID, hashB, domain.ImageExtJPG); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(other, hashA, domain.ImageExtJPG); !ok {
		t.Fatal("deleting one exercise's image removed another's")
	}
}

func TestInvalidNamesAreRejectedEverywhere(t *testing.T) {
	s, root := newTestStore(t)
	parent := filepath.Dir(root)
	sentinel := filepath.Join(parent, "sentinel.jpg")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.jpg")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, parent)

	const goodHash = "0123456789abcdef"
	badHashes := []string{
		"", "..", ".", "../", "../..", "../../sentinel", "../../../etc/passwd",
		"..\\..\\x", "/etc/passwd", "a/b", "0123456789abcde/", "/123456789abcdef",
		"0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "0123456789abcdeg",
		"0123456789abcdé", " 123456789abcdef", "0123456789abcde\n", "0123456789abcde\x00",
		strings.Repeat("a", 1000), "0123456789abcdef/../../x", "%2e%2e%2f%2e%2e%2fx1",
	}
	badExts := []domain.ImageExt{
		"", ".", "..", "../x", "jpg/../../x", "JPG", "Png", "jpeg", "gif", "webp ", " webp",
		"jpg\x00", "svg", "html", "jpg.php", "exe",
	}
	data, _, _ := testImage(t, 8, 8)

	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrInvalidName) {
			t.Fatalf("%s: error = %v, want ErrInvalidName", what, err)
		}
	}
	for _, h := range badHashes {
		check("Put hash "+h, s.Put(testID, h, domain.ImageExtJPG, data))
		check("Delete hash "+h, s.Delete(testID, h, domain.ImageExtJPG))
		_, err := s.Exists(testID, h, domain.ImageExtJPG)
		check("Exists hash "+h, err)
		_, err = s.Abs(testID, h, domain.ImageExtJPG)
		check("Abs hash "+h, err)
		if Path(testID, h, domain.ImageExtJPG) != "" {
			t.Fatalf("Path accepted hash %q", h)
		}
	}
	for _, e := range badExts {
		check("Put ext "+string(e), s.Put(testID, goodHash, e, data))
		check("Delete ext "+string(e), s.Delete(testID, goodHash, e))
		_, err := s.Exists(testID, goodHash, e)
		check("Exists ext "+string(e), err)
		if Path(testID, goodHash, e) != "" {
			t.Fatalf("Path accepted ext %q", e)
		}
	}

	if after := snapshot(t, parent); !slices.Equal(before, after) {
		t.Fatalf("the file tree around the root changed:\nbefore %v\nafter  %v", before, after)
	}
	if !bytes.Equal(mustRead(t, sentinel), []byte("keep")) || !bytes.Equal(mustRead(t, outside), []byte("keep")) {
		t.Fatal("a file outside the exercises directory was modified")
	}
}

// snapshot lists every path under dir with its size, for before/after checks.
func snapshot(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, fmt.Sprintf("%s %s %d", rel, info.Mode(), info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ---- fault injection ---------------------------------------------------

var errBoom = errors.New("injected failure")

// faultFS wraps the real file system and fails one named step. Steps, in the
// order Put performs them: mkdir, createtemp, chmod, write (partialwrite and
// shortwrite are variants of it), sync, close, rename, syncdir, syncparent.
type faultFS struct {
	filesystem
	failAt string
	// vanish makes the first N CreateTemp calls fail as if another process had
	// removed the directory just after it was created.
	vanish int

	mu       sync.Mutex
	syncs    int
	removals []string
}

func (f *faultFS) fails(step string) bool { return f.failAt == step }

func (f *faultFS) MkdirAll(path string, perm fs.FileMode) error {
	if f.fails("mkdir") {
		return errBoom
	}
	return f.filesystem.MkdirAll(path, perm)
}

func (f *faultFS) CreateTemp(dir, pattern string) (tempFile, error) {
	if f.fails("createtemp") {
		return nil, errBoom
	}
	f.mu.Lock()
	vanish := f.vanish > 0
	if vanish {
		f.vanish--
	}
	f.mu.Unlock()
	if vanish {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrNotExist}
	}
	tf, err := f.filesystem.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultFile{tempFile: tf, failAt: f.failAt}, nil
}

func (f *faultFS) Rename(oldpath, newpath string) error {
	if f.fails("rename") {
		return errBoom
	}
	return f.filesystem.Rename(oldpath, newpath)
}

func (f *faultFS) Remove(name string) error {
	f.mu.Lock()
	f.removals = append(f.removals, name)
	f.mu.Unlock()
	return f.filesystem.Remove(name)
}

func (f *faultFS) SyncDir(dir string) error {
	f.mu.Lock()
	f.syncs++
	n := f.syncs
	f.mu.Unlock()
	if (f.fails("syncdir") && n == 1) || (f.fails("syncparent") && n == 2) {
		return errBoom
	}
	return f.filesystem.SyncDir(dir)
}

type faultFile struct {
	tempFile
	failAt string
}

func (f *faultFile) Chmod(mode fs.FileMode) error {
	if f.failAt == "chmod" {
		return errBoom
	}
	return f.tempFile.Chmod(mode)
}

func (f *faultFile) Write(p []byte) (int, error) {
	switch f.failAt {
	case "write":
		return 0, errBoom
	case "partialwrite": // half the bytes reach the disk, then the write fails
		n, _ := f.tempFile.Write(p[:len(p)/2])
		return n, errBoom
	case "shortwrite": // a writer that reports fewer bytes and no error
		n, err := f.tempFile.Write(p[:len(p)/2])
		return n, err
	}
	return f.tempFile.Write(p)
}

func (f *faultFile) Sync() error {
	if f.failAt == "sync" {
		return errBoom
	}
	return f.tempFile.Sync()
}

func (f *faultFile) Close() error {
	err := f.tempFile.Close()
	if f.failAt == "close" {
		return errBoom
	}
	return err
}

func newFaultStore(t *testing.T, failAt string) (*Store, *faultFS, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media")
	ffs := &faultFS{filesystem: osFS{}} // healthy while the store is opened
	s, err := newStore(root, ffs)
	if err != nil {
		t.Fatal(err)
	}
	ffs.failAt = failAt
	return s, ffs, root
}

func TestPutFailureLeavesNoPartialFinalFileAndNoTemp(t *testing.T) {
	// Every step up to and including the rename: the target must not exist
	// and the directory must hold nothing.
	steps := []string{"mkdir", "createtemp", "chmod", "write", "partialwrite", "shortwrite", "sync", "close", "rename"}
	data, hash, ext := testImage(t, 32, 32)

	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			s, ffs, root := newFaultStore(t, step)
			dir := filepath.Join(root, "exercises", testID.String())

			err := s.Put(testID, hash, ext, data)
			if err == nil {
				t.Fatalf("Put succeeded although %s failed", step)
			}
			if step != "shortwrite" && !errors.Is(err, errBoom) {
				t.Fatalf("error = %v, want it to wrap the injected failure", err)
			}
			if names := listDir(t, dir); len(names) != 0 {
				t.Fatalf("directory holds %v after a failed Put, want nothing (no partial file, no temp file)", names)
			}
			if ok, _ := s.Exists(testID, hash, ext); ok {
				t.Fatal("final file exists after a failed Put")
			}
			// The store still works once the fault is gone.
			ffs.failAt = ""
			if err := s.Put(testID, hash, ext, data); err != nil {
				t.Fatalf("Put after the failure: %v", err)
			}
			if !bytes.Equal(mustRead(t, filepath.Join(dir, hash+".jpg")), data) {
				t.Fatal("retry stored the wrong bytes")
			}
		})
	}
}

func TestPutFailureKeepsAnExistingFileIntact(t *testing.T) {
	// A damaged file at the target is replaced only by a complete one: when
	// the replacement fails the old file is left exactly as it was.
	data, hash, ext := testImage(t, 32, 32)
	for _, step := range []string{"createtemp", "chmod", "write", "partialwrite", "sync", "close", "rename"} {
		t.Run(step, func(t *testing.T) {
			s, _, root := newFaultStore(t, step)
			dir := filepath.Join(root, "exercises", testID.String())
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(dir, hash+".jpg")
			if err := os.WriteFile(final, []byte("previous"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := s.Put(testID, hash, ext, data); err == nil {
				t.Fatal("Put succeeded")
			}
			if got := mustRead(t, final); string(got) != "previous" {
				t.Fatalf("existing file changed to %d bytes", len(got))
			}
			if names := listDir(t, dir); !slices.Equal(names, []string{hash + ".jpg"}) {
				t.Fatalf("directory holds %v", names)
			}
		})
	}
}

func TestPutFailingDirectorySyncLeavesACompleteFile(t *testing.T) {
	// After the rename the name holds the whole image, never a partial one.
	// The error tells the caller not to reference it yet.
	data, hash, ext := testImage(t, 32, 32)
	for _, step := range []string{"syncdir", "syncparent"} {
		t.Run(step, func(t *testing.T) {
			s, _, root := newFaultStore(t, step)
			dir := filepath.Join(root, "exercises", testID.String())
			err := s.Put(testID, hash, ext, data)
			if !errors.Is(err, errBoom) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			if names := listDir(t, dir); !slices.Equal(names, []string{hash + ".jpg"}) {
				t.Fatalf("directory holds %v, want only the finished file", names)
			}
			if !bytes.Equal(mustRead(t, filepath.Join(dir, hash+".jpg")), data) {
				t.Fatal("file is not the complete image")
			}
		})
	}
}

func TestPutCleansTheTempFileByRemovingItsName(t *testing.T) {
	data, hash, ext := testImage(t, 32, 32)
	s, ffs, _ := newFaultStore(t, "write")
	if err := s.Put(testID, hash, ext, data); err == nil {
		t.Fatal("want error")
	}
	if len(ffs.removals) != 1 || !strings.HasPrefix(filepath.Base(ffs.removals[0]), TempPrefix) {
		t.Fatalf("removals = %v, want exactly the temp file", ffs.removals)
	}
}

func TestPutStartsOverWhenTheDirectoryVanishes(t *testing.T) {
	// media gc (another process) may remove an empty exercise directory between
	// Put creating it and creating the temp file in it.
	data, hash, ext := testImage(t, 32, 32)

	t.Run("recovers", func(t *testing.T) {
		s, ffs, root := newFaultStore(t, "")
		ffs.vanish = putAttempts - 1
		if err := s.Put(testID, hash, ext, data); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if !bytes.Equal(mustRead(t, filepath.Join(root, "exercises", testID.String(), hash+".jpg")), data) {
			t.Fatal("wrong bytes")
		}
	})
	t.Run("gives up eventually", func(t *testing.T) {
		s, ffs, root := newFaultStore(t, "")
		ffs.vanish = putAttempts
		err := s.Put(testID, hash, ext, data)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Put = %v, want the not-exist error after %d attempts", err, putAttempts)
		}
		if names := listDir(t, filepath.Join(root, "exercises", testID.String())); len(names) != 0 {
			t.Fatalf("directory holds %v", names)
		}
	})
}

func TestPutIntoReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for this user")
	}
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)
	dir := filepath.Join(root, "exercises", testID.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := s.Put(testID, hash, ext, data)
	if err == nil {
		t.Fatal("Put into a read-only directory succeeded")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want a permission error", err)
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Fatalf("directory holds %v", names)
	}
}

func TestPutIntoReadOnlyRoot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for this user")
	}
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 16, 16)
	exercises := filepath.Join(root, "exercises")
	if err := os.Chmod(exercises, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(exercises, 0o755) })

	if err := s.Put(testID, hash, ext, data); err == nil {
		t.Fatal("Put with a read-only exercises directory succeeded")
	}
	if names := listDir(t, exercises); len(names) != 0 {
		t.Fatalf("exercises holds %v", names)
	}
}

// ---- concurrency -------------------------------------------------------

func TestConcurrentPutOfTheSameTarget(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 64, 64)
	dir := filepath.Join(root, "exercises", testID.String())

	const workers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.Put(testID, hash, ext, data)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}
	if names := listDir(t, dir); !slices.Equal(names, []string{hash + ".jpg"}) {
		t.Fatalf("directory holds %v, want only the image", names)
	}
	if !bytes.Equal(mustRead(t, filepath.Join(dir, hash+".jpg")), data) {
		t.Fatal("stored bytes differ")
	}
}

func TestConcurrentPutsOfDifferentImagesForOneExercise(t *testing.T) {
	s, root := newTestStore(t)
	dir := filepath.Join(root, "exercises", testID.String())

	const n = 16
	images := make([][]byte, n)
	var wg sync.WaitGroup
	for i := range images {
		images[i] = jpegBytes(t, 8+i, 8)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(testID, Hash(images[i]), domain.ImageExtJPG, images[i]); err != nil {
				t.Errorf("Put %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if got := len(listDir(t, dir)); got != n {
		t.Fatalf("%d files, want %d", got, n)
	}
	for _, data := range images {
		if !bytes.Equal(mustRead(t, filepath.Join(dir, Hash(data)+".jpg")), data) {
			t.Fatal("wrong bytes")
		}
	}
}

func TestPutSurvivesConcurrentRemovalOfTheEmptyDirectory(t *testing.T) {
	// Delete removes an exercise directory that its last file left empty; a
	// Put for the same exercise running at that moment must not fail.
	s, root := newTestStore(t)
	_, otherHash, ext := testImage(t, 9, 9)
	dir := filepath.Join(root, "exercises", testID.String())

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = s.Delete(testID, otherHash, ext) // nothing to delete: only removes the empty directory
			time.Sleep(100 * time.Microsecond)
		}
	}()

	for i := 0; i < 100; i++ {
		data := jpegBytes(t, 10+i, 10)
		if err := s.Put(testID, Hash(data), ext, data); err != nil {
			close(stop)
			<-done
			t.Fatalf("Put %d while the directory was being removed: %v", i, err)
		}
		if !bytes.Equal(mustRead(t, filepath.Join(dir, Hash(data)+".jpg")), data) {
			close(stop)
			<-done
			t.Fatalf("Put %d lost its file", i)
		}
	}
	close(stop)
	<-done
}

func TestConcurrentPutAndDeleteOfTheSameTarget(t *testing.T) {
	s, root := newTestStore(t)
	data, hash, ext := testImage(t, 64, 64)
	dir := filepath.Join(root, "exercises", testID.String())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := s.Put(testID, hash, ext, data); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := s.Delete(testID, hash, ext); err != nil {
					t.Errorf("Delete: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Whatever the interleaving: never a partial file and never a leftover temp.
	for _, name := range listDir(t, dir) {
		if name != hash+".jpg" {
			t.Fatalf("unexpected entry %q", name)
		}
		if !bytes.Equal(mustRead(t, filepath.Join(dir, name)), data) {
			t.Fatal("partial or wrong file after concurrent Put and Delete")
		}
	}
}

func TestEndToEndUploadFlow(t *testing.T) {
	// ReadLimited -> Inspect -> Put -> Path, as the upload service will do it.
	s, root := newTestStore(t)
	body := bytes.NewReader(pngBytes(t, 300, 200))
	data, err := ReadLimited(body, domain.MaxImageBytes)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testID, info.Hash, info.Ext, data); err != nil {
		t.Fatal(err)
	}
	rel := Path(testID, info.Hash, info.Ext)
	if want := "exercises/" + testID.String() + "/" + info.Hash + ".png"; rel != want {
		t.Fatalf("Path = %q, want %q", rel, want)
	}
	if !bytes.Equal(mustRead(t, filepath.Join(root, filepath.FromSlash(rel))), data) {
		t.Fatal("served file differs from the upload")
	}
}
