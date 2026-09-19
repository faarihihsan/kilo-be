package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"

	"workout-tracker-be/internal/domain"
)

// Disk layout under the media root (MEDIA_DIR):
//
//	exercises/{exercise_id}/{hash}.{ext}     the served files
//	exercises/{exercise_id}/.tmp-*           writes in progress (see Put)
//
// The same relative path, prefixed with /media/, is the public URL layout that
// the reverse proxy serves straight from disk (docs/api/endpoints/20-*.md).
// File names are content hashes, so a file never changes once written.
const (
	exercisesDir = "exercises"

	// TempPrefix starts the name of a file that Put is still writing. Sweep
	// removes such files once they are old enough to be leftovers of a crash.
	TempPrefix = ".tmp-"

	dirPerm  fs.FileMode = 0o755 // before umask; the proxy needs to traverse
	filePerm fs.FileMode = 0o644 // set explicitly: os.CreateTemp makes 0600

	// putAttempts bounds the retries of Put when the exercise directory is
	// removed under it by another process (media gc running Sweep).
	putAttempts = 5

	// lockStripes is the number of mutexes that serialise directory creation
	// and removal within the process (see Store).
	lockStripes = 64
)

var (
	// ErrInvalidName is returned for a hash or extension that is not exactly
	// 16 lowercase hex characters and one of jpg, png, webp. Names are never
	// joined into a path without passing this check.
	ErrInvalidName = errors.New("media: invalid file name")
	// ErrHashMismatch is returned by Put when the bytes do not hash to the
	// given name. Files are named after their content, so this is a caller bug.
	ErrHashMismatch = errors.New("media: content does not match hash")
)

// Validate reports whether hash and ext can name a stored file: hash is
// domain.ImageHashLen lowercase hex characters and ext is a valid ImageExt.
// It returns an error wrapping ErrInvalidName otherwise.
func Validate(hash string, ext domain.ImageExt) error {
	if len(hash) != domain.ImageHashLen {
		return fmt.Errorf("%w: hash must be %d hex characters", ErrInvalidName, domain.ImageHashLen)
	}
	for i := 0; i < len(hash); i++ {
		if c := hash[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: hash must be lowercase hex", ErrInvalidName)
		}
	}
	if !ext.IsValid() {
		return fmt.Errorf("%w: unknown extension", ErrInvalidName)
	}
	return nil
}

// Path returns the relative, slash-separated location of an exercise image,
// "exercises/{id}/{hash}.{ext}". The public URL is MEDIA_BASE_URL + "/media/" +
// Path(...). It returns "" when hash or ext is invalid (see Validate); values
// read from the exercises table are always valid.
func Path(id uuid.UUID, hash string, ext domain.ImageExt) string {
	if Validate(hash, ext) != nil {
		return ""
	}
	return exercisesDir + "/" + id.String() + "/" + hash + "." + string(ext)
}

// Store keeps exercise images under a root directory. It is safe for
// concurrent use. Its only state is a set of mutexes that keep Put from
// creating a file in an exercise directory at the moment Delete removes that
// directory as empty; they are held for a few system calls, never across the
// write and fsync of an image. A Store must not be copied.
type Store struct {
	root  string
	fs    filesystem
	locks [lockStripes]sync.Mutex
}

// dirLock returns the mutex that guards the directory of exercise id.
func (s *Store) dirLock(id uuid.UUID) *sync.Mutex { return &s.locks[int(id[15])%lockStripes] }

// NewStore opens the store at root (MEDIA_DIR), creating root and its exercises
// directory when missing. root is made absolute.
func NewStore(root string) (*Store, error) { return newStore(root, osFS{}) }

func newStore(root string, fsys filesystem) (*Store, error) {
	if root == "" {
		return nil, errors.New("media: empty root directory")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("media: resolve root: %w", err)
	}
	if err := fsys.MkdirAll(filepath.Join(abs, exercisesDir), dirPerm); err != nil {
		return nil, fmt.Errorf("media: create root: %w", err)
	}
	return &Store{root: abs, fs: fsys}, nil
}

// Root returns the absolute root directory.
func (s *Store) Root() string { return s.root }

// Path is the package-level Path.
func (s *Store) Path(id uuid.UUID, hash string, ext domain.ImageExt) string {
	return Path(id, hash, ext)
}

// Abs returns the absolute file system path of an image, or an error wrapping
// ErrInvalidName.
func (s *Store) Abs(id uuid.UUID, hash string, ext domain.ImageExt) (string, error) {
	if err := Validate(hash, ext); err != nil {
		return "", err
	}
	return filepath.Join(s.dir(id), hash+"."+string(ext)), nil
}

func (s *Store) dir(id uuid.UUID) string {
	return filepath.Join(s.root, exercisesDir, id.String())
}

// Put stores data as exercises/{id}/{hash}.{ext} and never leaves a partial
// file at that name. Steps: create the exercise directory (0755), write a
// temporary file next to the target (.tmp-*, mode 0644), fsync it, close it,
// rename it onto the final name, then fsync the directory (and its parent) so
// the rename survives a crash.
//
// data must be the bytes that hash to hash (ErrHashMismatch otherwise), so the
// name always matches the content. If the target already holds identical bytes
// nothing is written and Put returns nil (uploading the same image twice is a
// no-op); a target with different bytes (damaged) is replaced atomically.
// Concurrent Puts of the same target are safe: each writes its own temp file
// and the last rename wins with identical content. A Put racing a Delete of
// the same exercise's last file is safe too; a directory removed by another
// process (media gc) between the steps makes Put start over, a few times.
//
// On any error the temp file is removed. The final name is either untouched or
// complete; the one case where it exists after an error is a failed directory
// fsync after a successful rename, where the file is whole but not yet
// guaranteed durable. Callers update the database only after Put returns nil,
// so such a file is at worst an orphan for Sweep.
func (s *Store) Put(id uuid.UUID, hash string, ext domain.ImageExt, data []byte) error {
	if err := Validate(hash, ext); err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("media: empty data")
	}
	if got := Hash(data); got != hash {
		return fmt.Errorf("%w: named %s, hashes to %s", ErrHashMismatch, hash, got)
	}
	var err error
	for attempt := 0; attempt < putAttempts; attempt++ {
		if err = s.put(id, hash, ext, data); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		// The directory vanished between creating it and using it: a
		// concurrent Delete/Sweep removed it as empty. Try again.
	}
	return err
}

func (s *Store) put(id uuid.UUID, hash string, ext domain.ImageExt, data []byte) error {
	dir := s.dir(id)
	final := filepath.Join(dir, hash+"."+string(ext))

	if cur, err := s.fs.ReadFile(final); err == nil && bytes.Equal(cur, data) {
		return nil // already stored
	}

	tmp, err := s.createTemp(id, dir)
	if err != nil {
		return err
	}
	name := tmp.Name()
	closed, renamed := false, false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !renamed {
			_ = s.fs.Remove(name)
		}
	}()

	if err := tmp.Chmod(filePerm); err != nil {
		return fmt.Errorf("media: chmod temp file: %w", err)
	}
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("media: write temp file: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("media: write temp file: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("media: sync temp file: %w", err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("media: close temp file: %w", err)
	}
	if err := s.fs.Rename(name, final); err != nil {
		return fmt.Errorf("media: rename into place: %w", err)
	}
	renamed = true
	if err := s.fs.SyncDir(dir); err != nil {
		return fmt.Errorf("media: sync directory: %w", err)
	}
	if err := s.fs.SyncDir(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("media: sync parent directory: %w", err)
	}
	return nil
}

// createTemp makes the exercise directory if needed and a temp file in it.
// Once the temp file exists the directory cannot be removed as empty, until
// the file is renamed to its final name, which keeps it non-empty for good.
func (s *Store) createTemp(id uuid.UUID, dir string) (tempFile, error) {
	mu := s.dirLock(id)
	mu.Lock()
	defer mu.Unlock()
	if err := s.fs.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("media: create directory: %w", err)
	}
	tmp, err := s.fs.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("media: create temp file: %w", err)
	}
	return tmp, nil
}

// Delete removes an image file. A missing file is not an error (deleting is
// idempotent). It then removes the exercise directory if that left it empty;
// that part is best effort and never fails the call.
func (s *Store) Delete(id uuid.UUID, hash string, ext domain.ImageExt) error {
	path, err := s.Abs(id, hash, ext)
	if err != nil {
		return err
	}
	mu := s.dirLock(id)
	mu.Lock()
	defer mu.Unlock()
	if err := s.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("media: delete: %w", err)
	}
	// Remove fails on a non-empty directory, which is exactly what we want.
	_ = s.fs.Remove(filepath.Dir(path))
	return nil
}

// Exists reports whether the image file is present (a regular file).
func (s *Store) Exists(id uuid.UUID, hash string, ext domain.ImageExt) (bool, error) {
	path, err := s.Abs(id, hash, ext)
	if err != nil {
		return false, err
	}
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("media: stat: %w", err)
	}
	return fi.Mode().IsRegular(), nil
}

// Sweep is the package-level Sweep on this store's root.
func (s *Store) Sweep(referenced func(Ref) bool, opt SweepOptions) (SweepResult, error) {
	return Sweep(s.root, referenced, opt)
}

// filesystem is the seam through which Put and Delete touch the disk, so tests
// can inject a failure between any two steps.
type filesystem interface {
	MkdirAll(path string, perm fs.FileMode) error
	ReadFile(name string) ([]byte, error)
	CreateTemp(dir, pattern string) (tempFile, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	SyncDir(dir string) error
}

// tempFile is the part of *os.File that Put uses.
type tempFile interface {
	Name() string
	Write(p []byte) (int, error)
	Chmod(mode fs.FileMode) error
	Sync() error
	Close() error
}

type osFS struct{}

func (osFS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (osFS) ReadFile(name string) ([]byte, error)         { return os.ReadFile(name) }
func (osFS) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error                     { return os.Remove(name) }

func (osFS) CreateTemp(dir, pattern string) (tempFile, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err // a plain nil, not a nil *os.File in the interface
	}
	return f, nil
}

func (osFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
