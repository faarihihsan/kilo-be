package service

import (
	"context"
	"fmt"
	"log/slog"
	"mime"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
)

// ExerciseImages implements setting and removing an exercise image (endpoints
// 20, 21).
//
// The order of a set is the heart of the crash safety (spec 20): inspect the
// bytes, write the new file to disk, update the exercise row in a transaction,
// and only then delete the file the row replaced. The database therefore never
// points at a missing file; the worst a crash can leave is an orphan file,
// which `media gc` removes.
type ExerciseImages struct {
	exercises    *store.Exercises
	files        *media.Store
	clock        clock.Clock
	mediaBaseURL string
	logger       *slog.Logger
}

// NewExerciseImages builds the exercise image service. It uses the exercises
// store directly, not the Exercises service.
func NewExerciseImages(d Deps) *ExerciseImages {
	return &ExerciseImages{
		exercises:    store.NewExercises(d.DB),
		files:        d.Media,
		clock:        d.Clock,
		mediaBaseURL: d.Config.MediaBaseURL,
		logger:       d.Logger,
	}
}

// Set stores data as the image of the exercise id (endpoint 20). The caller
// reads the body (media.ReadLimited enforces the 2 MiB cap); this method does
// the image pipeline: it sniffs the real type, checks it against contentType,
// reads the dimensions, hashes the bytes, writes the file, updates the row and
// deletes the replaced file.
//
// contentType is the Content-Type request header. It must agree with the type
// sniffed from the bytes (spec 20: the bytes decide, the header must match).
//
// Errors: Validation (422 invalid_image, too_large_dimensions), BadRequest
// (400 empty body), PayloadTooLarge (413), UnsupportedMediaType (415),
// NotFound (404), Conflict deleted (409).
func (s *ExerciseImages) Set(ctx context.Context, id uuid.UUID, contentType string, data []byte) (domain.Exercise, error) {
	info, err := media.Inspect(data)
	if err != nil {
		return domain.Exercise{}, err
	}
	if !exerciseImageContentTypeMatches(contentType, info.ContentType) {
		return domain.Exercise{}, domain.NewUnsupportedMediaType()
	}

	if err := s.files.Put(id, info.Hash, info.Ext, data); err != nil {
		return domain.Exercise{}, fmt.Errorf("store exercise image: %w", err)
	}

	e, old, err := s.exercises.SetImage(ctx, id, domain.ExerciseImage{
		Hash:      info.Hash,
		Ext:       info.Ext,
		SizeBytes: info.Size,
	}, s.clock.Now())
	if err != nil {
		// The new file is on disk but no row points at it (or the exercise was
		// deleted): it is an orphan and `media gc` will remove it.
		return domain.Exercise{}, err
	}
	if old != nil {
		s.deleteFile(ctx, id, *old)
	}
	return newExerciseResponse(s.mediaBaseURL, e), nil
}

// Delete removes the image of the exercise id (endpoint 21): it clears the
// image columns and then deletes the file. A missing image, or a soft-deleted
// exercise, is a no-op returning nil (204). A deleted exercise keeps its file
// (spec 19); only this method removes it.
//
// Error: NotFound when the exercise never existed.
func (s *ExerciseImages) Delete(ctx context.Context, id uuid.UUID) error {
	old, err := s.exercises.ClearImage(ctx, id, s.clock.Now())
	if err != nil {
		return err
	}
	if old != nil {
		s.deleteFile(ctx, id, *old)
	}
	return nil
}

// deleteFile removes a file that the database no longer points at. A failure
// leaves an orphan for `media gc`, so it is logged, not returned: the row is
// already committed and the request must not become a 500 after the fact.
func (s *ExerciseImages) deleteFile(ctx context.Context, id uuid.UUID, img domain.ExerciseImage) {
	if err := s.files.Delete(id, img.Hash, img.Ext); err != nil {
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(ctx, "exercise image: replaced file not removed",
			"exercise_id", id.String(), "image_hash", img.Hash, "image_ext", string(img.Ext), "error", err)
	}
}

// exerciseImageContentTypeMatches reports whether the Content-Type header
// agrees with the type sniffed from the bytes. An absent or malformed header
// does not match: the bytes are always trusted, the header never is.
func exerciseImageContentTypeMatches(header, sniffed string) bool {
	mt, _, err := mime.ParseMediaType(header)
	return err == nil && mt == sniffed
}
