package media

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
)

var sweepNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// entry is a file to create: its content and how old it is.
type entry struct {
	content string
	age     time.Duration // how long before sweepNow it was modified
}

func build(t *testing.T, root string, files map[string]entry, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, e := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(e.content), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := sweepNow.Add(-e.age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// Creating files touched their directories. Make every directory look old
	// (deepest first, so touching a child does not touch its parent again) so
	// that tests decide the age of a directory explicitly.
	var dirPaths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirPaths = append(dirPaths, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	old := sweepNow.Add(-40 * 24 * time.Hour)
	for i := len(dirPaths) - 1; i >= 0; i-- {
		if err := os.Chtimes(dirPaths[i], old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// tree lists every file and directory under root (directories with a trailing
// slash), relative and sorted.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

const (
	hRef    = "aaaaaaaaaaaaaaaa"
	hOrph1  = "bbbbbbbbbbbbbbbb"
	hOrph2  = "cccccccccccccccc"
	hYoung  = "dddddddddddddddd"
	hOther  = "eeeeeeeeeeeeeeee"
	hOnlyOr = "0123456789abcdef"
)

var (
	idA = uuid.MustParse("00000000-0000-7000-8000-00000000000a")
	idB = uuid.MustParse("00000000-0000-7000-8000-00000000000b")
	idC = uuid.MustParse("00000000-0000-7000-8000-00000000000c")
	idD = uuid.MustParse("00000000-0000-7000-8000-00000000000d")
)

func ex(id uuid.UUID, name string) string { return "exercises/" + id.String() + "/" + name }

// Ids of directories the fixture needs. idU has letters, so its upper-case
// spelling is a non-canonical directory name; no lower-case twin of it exists,
// which keeps the fixture valid on case-insensitive file systems.
var (
	idE = uuid.MustParse("00000000-0000-7000-8000-0000000000ee")
	idU = uuid.MustParse("0000000a-0000-7000-8000-00000000000a")
)

func upper(id uuid.UUID) string { return strings.ToUpper(id.String()) }

// sweepFixture builds a media root that exercises every branch of Sweep.
func sweepFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	day := 24 * time.Hour
	build(t, root, map[string]entry{
		// exercise A: one referenced file, orphans (old and young), temp files, junk
		ex(idA, hRef+".jpg"):                    {"referenced", 30 * day},
		ex(idA, hOrph1+".png"):                  {"orphan-old-1", 10 * day},
		ex(idA, hOrph2+".webp"):                 {"orphan-old-22", 2 * time.Hour},
		ex(idA, hYoung+".jpg"):                  {"young-orphan", time.Minute},
		ex(idA, ".tmp-123456"):                  {"stale-temp", 3 * time.Hour},
		ex(idA, ".tmp-987654"):                  {"fresh-temp", 5 * time.Minute},
		ex(idA, "notes.txt"):                    {"junk", 40 * day},
		ex(idA, strings.ToUpper(hOther)+".jpg"): {"upper-case hash is not ours", 40 * day},
		ex(idA, hOther+".gif"):                  {"unknown extension", 40 * day},
		// exercise B: only orphans, so its directory goes too
		ex(idB, hOnlyOr+".jpg"): {"orphan-b", 5 * day},
		ex(idB, ".tmp-1"):       {"stale-temp-b", 5 * day},
		// exercise D has the same hash as A's referenced file, but references are per exercise
		ex(idD, hRef+".jpg"): {"same hash, other exercise, unreferenced", 5 * day},
		// things that are not ours
		"exercises/not-a-uuid/" + hOrph1 + ".jpg":         {"foreign dir", 99 * day},
		"exercises/loose.jpg":                             {"loose file", 99 * day},
		"exercises/" + upper(idU) + "/" + hOrph1 + ".jpg": {"non-canonical dir name", 99 * day},
		"elsewhere/" + hOrph1 + ".jpg":                    {"outside exercises", 99 * day},
		"stray.tmp":                                       {"outside exercises", 99 * day},
	},
		"exercises/"+idE.String(),        // an empty exercise directory
		"exercises/"+idC.String()+"/sub", // a directory holding only a sub-directory
	)
	return root
}

func refs(rs ...Ref) func(Ref) bool { return NewRefSet(rs...).Has }

func TestSweepRemovesOrphansTempsAndEmptyDirs(t *testing.T) {
	root := sweepFixture(t)
	opt := SweepOptions{Clock: clock.NewFake(sweepNow)} // default grace period: 1 hour

	res, err := Sweep(root, refs(Ref{ID: idA, Hash: hRef, Ext: domain.ImageExtJPG}), opt)
	if err != nil {
		t.Fatal(err)
	}

	wantPaths := []string{
		ex(idA, hOrph1+".png"),
		ex(idA, hOrph2+".webp"),
		ex(idA, ".tmp-123456"),
		ex(idB, ".tmp-1"),
		ex(idB, hOnlyOr+".jpg"),
		ex(idD, hRef+".jpg"),
	}
	if got := sorted(slices.Clone(res.Paths)); !slices.Equal(got, sorted(slices.Clone(wantPaths))) {
		t.Fatalf("removed paths\n got  %v\n want %v", got, sorted(wantPaths))
	}
	if res.Removed != len(wantPaths) {
		t.Fatalf("Removed = %d, want %d", res.Removed, len(wantPaths))
	}
	wantBytes := int64(len("orphan-old-1") + len("orphan-old-22") + len("stale-temp") + len("stale-temp-b") + len("orphan-b") + len("same hash, other exercise, unreferenced"))
	if res.Bytes != wantBytes {
		t.Fatalf("Bytes = %d, want %d", res.Bytes, wantBytes)
	}
	if res.DryRun {
		t.Fatal("DryRun set on a real run")
	}
	wantDirs := []string{"exercises/" + idB.String(), "exercises/" + idD.String(), "exercises/" + idE.String()}
	if got := sorted(slices.Clone(res.RemovedDirs)); !slices.Equal(got, wantDirs) {
		t.Fatalf("RemovedDirs = %v, want %v", got, wantDirs)
	}

	wantLeft := []string{
		"elsewhere/",
		"elsewhere/" + hOrph1 + ".jpg",
		"exercises/",
		"exercises/" + upper(idU) + "/",
		"exercises/" + upper(idU) + "/" + hOrph1 + ".jpg",
		"exercises/" + idA.String() + "/",
		ex(idA, ".tmp-987654"),
		ex(idA, strings.ToUpper(hOther)+".jpg"),
		ex(idA, hOther+".gif"),
		ex(idA, hRef+".jpg"),
		ex(idA, hYoung+".jpg"),
		ex(idA, "notes.txt"),
		"exercises/" + idC.String() + "/",
		"exercises/" + idC.String() + "/sub/",
		"exercises/loose.jpg",
		"exercises/not-a-uuid/",
		"exercises/not-a-uuid/" + hOrph1 + ".jpg",
		"stray.tmp",
	}
	if got := tree(t, root); !slices.Equal(got, sorted(wantLeft)) {
		t.Fatalf("tree after sweep\n got  %v\n want %v", got, sorted(wantLeft))
	}

	wantSkipped := []string{
		ex(idA, hOther+".gif"),
		ex(idA, strings.ToUpper(hOther)+".jpg"),
		ex(idA, "notes.txt"),
		"exercises/" + idC.String() + "/sub",
		"exercises/" + upper(idU),
		"exercises/loose.jpg",
		"exercises/not-a-uuid",
	}
	if got := sorted(slices.Clone(res.Skipped)); !slices.Equal(got, sorted(wantSkipped)) {
		t.Fatalf("Skipped\n got  %v\n want %v", got, sorted(wantSkipped))
	}
	// Scanned counts files (kept, removed or skipped), not directories: 9 in A,
	// 2 in B, 1 in D, and loose.jpg directly under exercises/.
	if res.Scanned != 13 {
		t.Fatalf("Scanned = %d, want 13", res.Scanned)
	}
}

func sorted(s []string) []string { slices.Sort(s); return s }

func TestSweepDryRunRemovesNothingAndReportsTheSame(t *testing.T) {
	root := sweepFixture(t)
	before := tree(t, root)
	set := refs(Ref{ID: idA, Hash: hRef, Ext: domain.ImageExtJPG})

	dry, err := Sweep(root, set, SweepOptions{DryRun: true, Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	if after := tree(t, root); !slices.Equal(before, after) {
		t.Fatalf("dry run changed the tree:\nbefore %v\nafter  %v", before, after)
	}
	if !dry.DryRun || dry.Removed == 0 {
		t.Fatalf("dry run result = %+v", dry)
	}
	// File contents and times are untouched too.
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(ex(idA, hOrph1+".png"))))
	if err != nil || !fi.ModTime().Before(sweepNow.Add(-9*24*time.Hour)) {
		t.Fatalf("dry run touched a file: %v %v", fi, err)
	}

	real, err := Sweep(root, set, SweepOptions{Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	real.DryRun = true
	if !reflect.DeepEqual(dry, real) {
		t.Fatalf("dry run and real run disagree:\ndry  %+v\nreal %+v", dry, real)
	}
}

func TestSweepIsIdempotent(t *testing.T) {
	root := sweepFixture(t)
	opt := SweepOptions{Clock: clock.NewFake(sweepNow)}
	set := refs(Ref{ID: idA, Hash: hRef, Ext: domain.ImageExtJPG})
	if _, err := Sweep(root, set, opt); err != nil {
		t.Fatal(err)
	}
	after := tree(t, root)
	res, err := Sweep(root, set, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 || len(res.Paths) != 0 || len(res.RemovedDirs) != 0 || res.Bytes != 0 {
		t.Fatalf("second sweep removed things: %+v", res)
	}
	if !slices.Equal(tree(t, root), after) {
		t.Fatal("second sweep changed the tree")
	}
}

func TestSweepKeepsEverythingReferenced(t *testing.T) {
	root := t.TempDir()
	old := 100 * 24 * time.Hour
	files := map[string]entry{
		ex(idA, hRef+".jpg"):   {"a", old},
		ex(idA, hOrph1+".png"): {"b", old},
		ex(idB, hRef+".webp"):  {"c", old},
	}
	build(t, root, files)
	all := refs(
		Ref{ID: idA, Hash: hRef, Ext: domain.ImageExtJPG},
		Ref{ID: idA, Hash: hOrph1, Ext: domain.ImageExtPNG},
		Ref{ID: idB, Hash: hRef, Ext: domain.ImageExtWebP},
	)
	res, err := Sweep(root, all, SweepOptions{Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 || res.Scanned != 3 || len(res.RemovedDirs) != 0 {
		t.Fatalf("result = %+v", res)
	}
	// The extension is part of the reference: a jpg reference does not keep a png.
	res, err = Sweep(root, refs(Ref{ID: idB, Hash: hRef, Ext: domain.ImageExtJPG}), SweepOptions{Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 3 {
		t.Fatalf("Removed = %d, want 3 (nothing matches the references)", res.Removed)
	}
}

func TestSweepGracePeriod(t *testing.T) {
	newRoot := func() string {
		root := t.TempDir()
		build(t, root, map[string]entry{
			ex(idA, hOrph1+".jpg"):  {"orphan 30 minutes old", 30 * time.Minute},
			ex(idA, hOrph2+".jpg"):  {"orphan 2 hours old", 2 * time.Hour},
			ex(idA, ".tmp-1"):       {"temp 30 minutes old", 30 * time.Minute},
			ex(idA, ".tmp-2"):       {"temp 2 hours old", 2 * time.Hour},
			ex(idA, hOrph1+".webp"): {"orphan from the future", -time.Hour},
		})
		return root
	}
	none := refs()

	t.Run("zero means the default of one hour", func(t *testing.T) {
		root := newRoot()
		res, err := Sweep(root, none, SweepOptions{Clock: clock.NewFake(sweepNow)})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{ex(idA, ".tmp-2"), ex(idA, hOrph2+".jpg")}
		if !slices.Equal(sorted(res.Paths), sorted(want)) {
			t.Fatalf("removed %v, want %v", res.Paths, want)
		}
	})
	t.Run("explicit age", func(t *testing.T) {
		root := newRoot()
		res, err := Sweep(root, none, SweepOptions{MinAge: 10 * time.Minute, Clock: clock.NewFake(sweepNow)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Removed != 4 {
			t.Fatalf("Removed = %d (%v), want 4 (all but the future-dated one)", res.Removed, res.Paths)
		}
	})
	t.Run("negative disables the grace period", func(t *testing.T) {
		root := newRoot()
		res, err := Sweep(root, none, SweepOptions{MinAge: -1, Clock: clock.NewFake(sweepNow)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Removed != 5 {
			t.Fatalf("Removed = %d, want 5", res.Removed)
		}
		if names := tree(t, root); !slices.Equal(names, []string{"exercises/"}) {
			t.Fatalf("tree = %v, want only exercises/", names)
		}
	})
	t.Run("the default clock is the real one", func(t *testing.T) {
		root := t.TempDir()
		build(t, root, map[string]entry{ex(idA, hOrph1+".jpg"): {"x", 0}})
		p := filepath.Join(root, filepath.FromSlash(ex(idA, hOrph1+".jpg")))
		now := time.Now()
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
		res, err := Sweep(root, none, SweepOptions{})
		if err != nil || res.Removed != 0 {
			t.Fatalf("a file written just now was swept: %+v %v", res, err)
		}
		old := now.Add(-2 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		res, err = Sweep(root, none, SweepOptions{})
		if err != nil || res.Removed != 1 {
			t.Fatalf("a two-hour-old orphan was kept: %+v %v", res, err)
		}
	})
}

func TestSweepLeavesYoungEmptyDirectories(t *testing.T) {
	// An upload creates the directory a moment before its first file: an empty
	// directory that was just touched must survive a sweep, an old one must not.
	root := t.TempDir()
	build(t, root, nil, "exercises/"+idA.String(), "exercises/"+idB.String())
	young := sweepNow.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(root, "exercises", idB.String()), young, young); err != nil {
		t.Fatal(err)
	}

	res, err := Sweep(root, refs(), SweepOptions{Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"exercises/" + idA.String()}; !slices.Equal(res.RemovedDirs, want) {
		t.Fatalf("RemovedDirs = %v, want %v", res.RemovedDirs, want)
	}
	if want := []string{"exercises/", "exercises/" + idB.String() + "/"}; !slices.Equal(tree(t, root), want) {
		t.Fatalf("tree = %v, want %v", tree(t, root), want)
	}
}

func TestSweepRemovesADirectoryItsOwnRemovalsEmptied(t *testing.T) {
	// Sweeping the last orphan out of an old directory removes the directory in
	// the same run, even though deleting the file just touched it.
	root := t.TempDir()
	build(t, root, map[string]entry{ex(idA, hOrph1+".jpg"): {"orphan", 9 * 24 * time.Hour}})
	res, err := Sweep(root, refs(), SweepOptions{Clock: clock.NewFake(sweepNow)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || len(res.RemovedDirs) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if want := []string{"exercises/"}; !slices.Equal(tree(t, root), want) {
		t.Fatalf("tree = %v", tree(t, root))
	}
}

func TestSweepRefusesANilReferencedSet(t *testing.T) {
	root := t.TempDir()
	build(t, root, map[string]entry{ex(idA, hOrph1+".jpg"): {"x", 99 * 24 * time.Hour}})
	if _, err := Sweep(root, nil, SweepOptions{Clock: clock.NewFake(sweepNow)}); err == nil {
		t.Fatal("nil referenced must be an error")
	}
	if len(tree(t, root)) != 3 {
		t.Fatal("files were removed although the call was refused")
	}
}

func TestSweepMissingExercisesDirectory(t *testing.T) {
	root := t.TempDir()
	res, err := Sweep(root, refs(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 0 || res.Removed != 0 {
		t.Fatalf("result = %+v", res)
	}
	res, err = Sweep(filepath.Join(root, "does-not-exist"), refs(), SweepOptions{})
	if err != nil || res.Scanned != 0 {
		t.Fatalf("missing root: %+v, %v", res, err)
	}
}

func TestSweepNeverFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	build(t, outside, map[string]entry{"victim.jpg": {"do not touch", 99 * 24 * time.Hour}})
	victim := filepath.Join(outside, "victim.jpg")

	root := t.TempDir()
	build(t, root, map[string]entry{ex(idA, hOrph1+".jpg"): {"orphan", 99 * 24 * time.Hour}})
	// A symlinked exercise directory, and a symlinked file inside a real one.
	if err := os.Symlink(outside, filepath.Join(root, "exercises", idB.String())); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(root, filepath.FromSlash(ex(idA, hOrph2+".jpg")))); err != nil {
		t.Fatal(err)
	}

	res, err := Sweep(root, refs(), SweepOptions{MinAge: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file behind a symlink was removed: %v", err)
	}
	if res.Removed != 1 || res.Paths[0] != ex(idA, hOrph1+".jpg") {
		t.Fatalf("result = %+v, want only the real orphan removed", res)
	}
	want := []string{ex(idA, hOrph2+".jpg"), "exercises/" + idB.String()}
	if !slices.Equal(sorted(slices.Clone(res.Skipped)), sorted(want)) {
		t.Fatalf("Skipped = %v, want %v", res.Skipped, want)
	}
	// The exercise directory with the symlink left in it must survive.
	if _, err := os.Lstat(filepath.Join(root, "exercises", idA.String())); err != nil {
		t.Fatalf("directory holding a skipped entry was removed: %v", err)
	}
}

func TestSweepRefusesASymlinkedExercisesDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	build(t, outside, map[string]entry{idA.String() + "/" + hOrph1 + ".jpg": {"x", 99 * 24 * time.Hour}})
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "exercises")); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}
	if _, err := Sweep(root, refs(), SweepOptions{MinAge: -1}); err == nil {
		t.Fatal("a symlinked exercises directory must be refused")
	}
	if len(tree(t, outside)) != 2 {
		t.Fatal("files behind the symlink were removed")
	}
}

func TestSweepThroughStoreAgainstStoredFiles(t *testing.T) {
	s, root := newTestStore(t)
	keep, keepHash, ext := testImage(t, 16, 16)
	drop, dropHash, _ := testImage(t, 17, 17)
	if err := s.Put(idA, keepHash, ext, keep); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(idA, dropHash, ext, drop); err != nil {
		t.Fatal(err)
	}

	res, err := s.Sweep(NewRefSet(Ref{ID: idA, Hash: keepHash, Ext: ext}).Has, SweepOptions{MinAge: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Paths, []string{Path(idA, dropHash, ext)}) {
		t.Fatalf("Paths = %v", res.Paths)
	}
	if ok, _ := s.Exists(idA, keepHash, ext); !ok {
		t.Fatal("referenced file was swept")
	}
	if ok, _ := s.Exists(idA, dropHash, ext); ok {
		t.Fatal("orphan survived")
	}
	// Once nothing references the exercise any more, its directory goes too.
	if _, err := s.Sweep(refs(), SweepOptions{MinAge: -1}); err != nil {
		t.Fatal(err)
	}
	if names := listDir(t, filepath.Join(root, "exercises")); len(names) != 0 {
		t.Fatalf("exercises holds %v", names)
	}
}

func TestSweepWithGracePeriodRacingWithPutNeverHurtsIt(t *testing.T) {
	// The upload writes the file, and only then commits the row that
	// references it, so a sweep running in between sees a fresh unreferenced
	// file. The default grace period must protect it, along with the temp file
	// still being written and the directory that holds them.
	s, root := newTestStore(t)
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
			if _, err := Sweep(root, refs(), SweepOptions{}); err != nil {
				t.Errorf("Sweep: %v", err)
				return
			}
		}
	}()

	const n = 50
	images := make([][]byte, n)
	for i := 0; i < n; i++ {
		images[i] = jpegBytes(t, 10+i, 10)
		if err := s.Put(idA, Hash(images[i]), domain.ImageExtJPG, images[i]); err != nil {
			close(stop)
			<-done
			t.Fatalf("Put %d during a sweep: %v", i, err)
		}
	}
	close(stop)
	<-done
	for i, data := range images {
		if ok, err := s.Exists(idA, Hash(data), domain.ImageExtJPG); err != nil || !ok {
			t.Fatalf("image %d was swept although it was fresh: %v %v", i, ok, err)
		}
	}
}
