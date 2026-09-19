package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
)

// DefaultSweepMinAge is the grace period Sweep uses when SweepOptions.MinAge is
// zero. Put writes the file before the caller commits the database row that
// references it, so a brand-new file looks unreferenced for a moment; the
// grace period keeps Sweep (media gc, run while the server is up) from
// deleting it. It also keeps a temp file that is still being written.
const DefaultSweepMinAge = time.Hour

// Ref identifies one stored image file: the columns id, image_hash and
// image_ext of an exercise row.
type Ref struct {
	ID   uuid.UUID
	Hash string
	Ext  domain.ImageExt
}

// Path is the relative path of the referenced file, see Path.
func (r Ref) Path() string { return Path(r.ID, r.Hash, r.Ext) }

// RefSet is a set of Ref, the natural way to hand the exercises' image columns
// to Sweep: Sweep(root, media.NewRefSet(refs...).Has, opt).
type RefSet map[Ref]struct{}

// NewRefSet returns the set of refs.
func NewRefSet(refs ...Ref) RefSet {
	s := make(RefSet, len(refs))
	for _, r := range refs {
		s[r] = struct{}{}
	}
	return s
}

// Has reports whether r is in the set.
func (s RefSet) Has(r Ref) bool { _, ok := s[r]; return ok }

// SweepOptions tunes Sweep.
type SweepOptions struct {
	// DryRun reports what would be removed and removes nothing.
	DryRun bool
	// MinAge is how old (by modification time) an unreferenced or temporary
	// file must be before it is removed. Zero means DefaultSweepMinAge; a
	// negative value disables the grace period, which is only safe when
	// nothing can be uploading (tests, maintenance windows).
	MinAge time.Duration
	// Clock supplies "now" for the age check. Nil means the real clock.
	Clock clock.Clock
}

// SweepResult reports what Sweep did, or with DryRun what it would do.
type SweepResult struct {
	DryRun  bool
	Scanned int      // files examined under exercises/ (kept, removed or skipped)
	Removed int      // files removed
	Bytes   int64    // total size of the removed files
	Paths   []string // removed files, relative to root, slash-separated, in walk order
	// RemovedDirs are the exercise directories removed because they ended up empty.
	RemovedDirs []string
	// Skipped are entries that do not belong to the layout (unknown names,
	// symlinks, sub-directories). They are reported and never touched.
	Skipped []string
}

// Sweep is the garbage collector behind `media gc`. It walks
// root/exercises/{uuid}/ and removes
//
//   - image files ({16 hex}.{ext}) for which referenced returns false,
//   - leftover temp files (.tmp-*),
//   - exercise directories left empty,
//
// but only if they are at least MinAge old (files by their modification time,
// a directory by the last time an entry was added to or removed from it, so a
// directory an upload just created is left alone). Everything else is left in
// place and reported in Skipped: names outside the layout, symlinks and
// sub-directories. Nothing outside root/exercises is ever read or removed, and
// symlinks are never followed.
//
// referenced must describe every image the database still points at, loaded
// before calling Sweep. If it is incomplete, live files are deleted: build it
// from a successful, complete query and pass nothing on error. A nil
// referenced is refused. A missing exercises directory is an empty result.
//
// An I/O error aborts the sweep and is returned with the result so far.
func Sweep(root string, referenced func(Ref) bool, opt SweepOptions) (SweepResult, error) {
	res := SweepResult{DryRun: opt.DryRun}
	if referenced == nil {
		return res, errors.New("media: sweep needs a referenced set (nil would remove every image)")
	}
	minAge := opt.MinAge
	switch {
	case minAge == 0:
		minAge = DefaultSweepMinAge
	case minAge < 0:
		minAge = 0
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	base := filepath.Join(root, exercisesDir)
	fi, err := os.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("media: sweep: %w", err)
	}
	if !fi.IsDir() { // a symlink or a file: refuse rather than follow it out of root
		return res, fmt.Errorf("media: sweep: %s is not a directory", base)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return res, fmt.Errorf("media: sweep: %w", err)
	}

	for _, e := range entries {
		rel := exercisesDir + "/" + e.Name()
		id, ok := parseDirName(e.Name())
		if !e.IsDir() || !ok {
			if !e.IsDir() {
				res.Scanned++
			}
			res.Skipped = append(res.Skipped, rel)
			continue
		}
		if err := sweepDir(&res, filepath.Join(base, e.Name()), rel, id, referenced, minAge, now, opt.DryRun); err != nil {
			return res, err
		}
	}
	return res, nil
}

func sweepDir(res *SweepResult, dir, rel string, id uuid.UUID, referenced func(Ref) bool, minAge time.Duration, now time.Time, dryRun bool) error {
	// The directory's modification time is when an entry was last added or
	// removed. Read it before deleting anything: it decides below whether the
	// directory may go if it ends up empty.
	di, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // removed by someone else meanwhile
	}
	if err != nil {
		return fmt.Errorf("media: sweep: %w", err)
	}
	dirOldEnough := minAge <= 0 || now.Sub(di.ModTime()) >= minAge

	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("media: sweep: %w", err)
	}

	remaining := 0 // entries that stay in the directory
	for _, f := range files {
		frel := rel + "/" + f.Name()
		if !f.Type().IsRegular() { // sub-directories, symlinks, devices, ...
			res.Skipped = append(res.Skipped, frel)
			remaining++
			continue
		}
		res.Scanned++

		remove := false
		switch {
		case strings.HasPrefix(f.Name(), TempPrefix):
			remove = true
		default:
			hash, ext, ok := parseFileName(f.Name())
			if !ok {
				res.Skipped = append(res.Skipped, frel)
				remaining++
				continue
			}
			remove = !referenced(Ref{ID: id, Hash: hash, Ext: ext})
		}
		if !remove {
			remaining++
			continue
		}

		info, err := f.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // already gone
		}
		if err != nil {
			return fmt.Errorf("media: sweep: %w", err)
		}
		if minAge > 0 && now.Sub(info.ModTime()) < minAge {
			remaining++ // too young: maybe an upload in flight
			continue
		}
		if !dryRun {
			if err := os.Remove(filepath.Join(dir, f.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("media: sweep: %w", err)
			}
		}
		res.Removed++
		res.Bytes += info.Size()
		res.Paths = append(res.Paths, frel)
	}

	if remaining == 0 && dirOldEnough {
		if dryRun {
			res.RemovedDirs = append(res.RemovedDirs, rel)
		} else if err := os.Remove(dir); err == nil {
			res.RemovedDirs = append(res.RemovedDirs, rel)
		} // else: not empty after all (a concurrent upload) or already gone
	}
	return nil
}

// parseDirName accepts only the canonical lowercase hyphenated uuid, the form
// Store writes.
func parseDirName(name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(name)
	if err != nil || id.String() != name {
		return uuid.Nil, false
	}
	return id, true
}

// parseFileName splits "{hash}.{ext}" and validates both parts.
func parseFileName(name string) (string, domain.ImageExt, bool) {
	hash, ext, found := strings.Cut(name, ".")
	if !found || Validate(hash, domain.ImageExt(ext)) != nil {
		return "", "", false
	}
	return hash, domain.ImageExt(ext), true
}
