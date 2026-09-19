package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"workout-tracker-be/internal/domain"
)

// Exercises is the data access for the exercises table (specs 08, 09, 18, 19,
// and the image columns of 20 and 21).
//
// Every method returns domain.Exercise values with Image set and ImageURL nil:
// the public URL needs MEDIA_BASE_URL, which the service adds (see
// newExerciseResponse in package service). Times are UTC.
//
// Writes take the timestamp to store as `now`, so the service's clock decides
// created_at, updated_at and deleted_at. Content is validated and normalised
// by domain.ExerciseInput.Validate before it reaches the store; the CHECK
// constraints are a backstop, and a violation surfaces as an unexpected error.
//
// Errors are domain errors where a client can cause them: NotFound, and
// Conflict with the issues already_exists (carrying ExistingID), id_taken and
// deleted.
type Exercises struct{ db *DB }

// NewExercises returns the Exercises store on db.
func NewExercises(db *DB) *Exercises { return &Exercises{db: db} }

const (
	// exerciseNameUniq is the partial unique index on lower(name) among live
	// exercises (migrations/0003_exercises.sql).
	exerciseNameUniq = "exercises_name_lower_uniq"

	// exerciseColumns is the column list every query returns, in the order
	// exerciseScan reads it.
	exerciseColumns = `id, name, category, primary_muscle_group, secondary_muscle_groups,
		equipment, measurement_type, instructions, image_hash, image_ext, image_size_bytes,
		created_by, created_at, updated_at, deleted_at`

	// exerciseNameRetries bounds how often a name conflict is re-examined when
	// the conflicting exercise disappears (is deleted or renamed) between the
	// failed write and the lookup of its id.
	exerciseNameRetries = 3
)

// exerciseScanner is the Scan method of pgx.Row and pgx.Rows.
type exerciseScanner interface {
	Scan(dest ...any) error
}

// exerciseScan reads one row of exerciseColumns, then the extra destinations.
func exerciseScan(row exerciseScanner, extra ...any) (domain.Exercise, error) {
	var (
		e                    domain.Exercise
		category, primary    string
		equipment, measure   string
		secondary            []string
		imgHash, imgExt      *string
		imgSize              *int64
		createdAt, updatedAt time.Time
		deletedAt            *time.Time
	)
	dest := []any{
		&e.ID, &e.Name, &category, &primary, &secondary,
		&equipment, &measure, &e.Instructions, &imgHash, &imgExt, &imgSize,
		&e.CreatedBy, &createdAt, &updatedAt, &deletedAt,
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return domain.Exercise{}, err
	}
	e.Category = domain.Category(category)
	e.PrimaryMuscleGroup = domain.MuscleGroup(primary)
	e.Equipment = domain.Equipment(equipment)
	e.MeasurementType = domain.MeasurementType(measure)
	e.SecondaryMuscleGroups = make([]domain.MuscleGroup, len(secondary))
	for i, g := range secondary {
		e.SecondaryMuscleGroups[i] = domain.MuscleGroup(g)
	}
	// pgx returns timestamptz in the local zone.
	e.CreatedAt, e.UpdatedAt = createdAt.UTC(), updatedAt.UTC()
	if deletedAt != nil {
		t := deletedAt.UTC()
		e.DeletedAt = &t
	}
	// The three image columns are all NULL or all set (CHECK constraint).
	if imgHash != nil && imgExt != nil && imgSize != nil {
		e.Image = &domain.ExerciseImage{Hash: *imgHash, Ext: domain.ImageExt(*imgExt), SizeBytes: *imgSize}
	}
	return e, nil
}

// exerciseSecondary is the text[] argument for the secondary muscle groups,
// never nil (the column is NOT NULL).
func exerciseSecondary(groups []domain.MuscleGroup) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = string(g)
	}
	return out
}

// exerciseGet reads one exercise, including a deleted one. forUpdate locks the
// row until the transaction ends; q must then be a transaction.
func exerciseGet(ctx context.Context, q Querier, id uuid.UUID, forUpdate bool) (domain.Exercise, error) {
	sql := `SELECT ` + exerciseColumns + ` FROM exercises WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	e, err := exerciseScan(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Exercise{}, domain.NewNotFound()
	}
	if err != nil {
		return domain.Exercise{}, fmt.Errorf("store: get exercise: %w", err)
	}
	return e, nil
}

// Get returns the exercise with the id, including a soft-deleted one (the
// caller decides what a deleted row means), or a NotFound error.
func (s *Exercises) Get(ctx context.Context, id uuid.UUID) (domain.Exercise, error) {
	return exerciseGet(ctx, s.db, id, false)
}

// exerciseLiveIDByName returns the id of the live (not deleted) exercise whose
// name equals name ignoring case, other than `except`, and whether there is one.
func exerciseLiveIDByName(ctx context.Context, q Querier, name string, except uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM exercises
		WHERE lower(name) = lower($1) AND deleted_at IS NULL AND id <> $2`, name, except).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("store: find exercise by name: %w", err)
	}
	return id, true, nil
}

// exerciseIsNameViolation reports whether err is a unique violation on the
// live-name index.
func exerciseIsNameViolation(err error) bool {
	c, ok := IsUniqueViolation(err)
	return ok && c == exerciseNameUniq
}

// exerciseNameTaken is the already_exists conflict for a name held by the live
// exercise `owner`.
func exerciseNameTaken(owner uuid.UUID) error {
	return domain.NewConflict(domain.IssueAlreadyExists, domain.WithExistingID(owner), domain.OnField("name"))
}

// Create inserts a new exercise with the client- or server-chosen id, created
// by createdBy, with created_at and updated_at set to now. It returns the
// stored row and whether it was created.
//
// Retries are idempotent (spec 09): when the id already exists and the row has
// exactly the content of in, Create returns that row with created=false and
// changes nothing. Conflicts:
//
//	the id exists with different content   id_taken
//	the id exists and is soft-deleted      deleted
//	the name is taken by another live      already_exists on field "name",
//	exercise (case-insensitive)            ExistingID = that exercise
//
// The id is examined before the name, so a retry whose name has since been
// taken by a third exercise is still id_taken, not already_exists.
func (s *Exercises) Create(ctx context.Context, id, createdBy uuid.UUID, in domain.ExerciseInput, now time.Time) (e domain.Exercise, created bool, err error) {
	for range exerciseNameRetries {
		e, err = exerciseScan(s.db.QueryRow(ctx, `
			INSERT INTO exercises (id, name, category, primary_muscle_group, secondary_muscle_groups,
				equipment, measurement_type, instructions, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			ON CONFLICT (id) DO NOTHING
			RETURNING `+exerciseColumns,
			id, in.Name, string(in.Category), string(in.PrimaryMuscleGroup), exerciseSecondary(in.SecondaryMuscleGroups),
			string(in.Equipment), string(in.MeasurementType), in.Instructions, createdBy, now))
		switch {
		case err == nil:
			return e, true, nil

		case errors.Is(err, pgx.ErrNoRows): // DO NOTHING: the id exists
			existing, err := exerciseGet(ctx, s.db, id, false)
			switch {
			case err != nil:
				return domain.Exercise{}, false, err
			case existing.DeletedAt != nil:
				return domain.Exercise{}, false, domain.NewConflict(domain.IssueDeleted)
			case in.Matches(existing):
				return existing, false, nil
			}
			return domain.Exercise{}, false, domain.NewConflict(domain.IssueIDTaken)
		}

		if !exerciseIsNameViolation(err) {
			return domain.Exercise{}, false, fmt.Errorf("store: create exercise: %w", err)
		}
		owner, found, lookupErr := exerciseLiveIDByName(ctx, s.db, in.Name, uuid.Nil)
		if lookupErr != nil {
			return domain.Exercise{}, false, lookupErr
		}
		if found {
			return domain.Exercise{}, false, exerciseNameTaken(owner)
		}
		// The name's owner vanished (deleted or renamed) between the insert and
		// the lookup: try again.
	}
	return domain.Exercise{}, false, fmt.Errorf("store: create exercise: name conflict did not settle: %w", err)
}

// Update replaces the editable fields of a live exercise and sets updated_at
// to now. created_by, created_at and the image are untouched. It returns the
// stored row.
//
// Last write wins, there is no stale check (spec 18). When in has exactly the
// stored content nothing is written and updated_at stays. Errors: NotFound
// when the id never existed, Conflict deleted when the exercise is
// soft-deleted (checked before the name), Conflict already_exists (with
// ExistingID) when another live exercise has the new name.
func (s *Exercises) Update(ctx context.Context, id uuid.UUID, in domain.ExerciseInput, now time.Time) (domain.Exercise, error) {
	var lastErr error
	for range exerciseNameRetries {
		var out domain.Exercise
		err := s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			cur, err := exerciseGet(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if cur.DeletedAt != nil {
				return domain.NewConflict(domain.IssueDeleted)
			}
			if in.Matches(cur) {
				out = cur
				return nil
			}
			out, err = exerciseScan(tx.QueryRow(ctx, `
				UPDATE exercises SET name = $2, category = $3, primary_muscle_group = $4,
					secondary_muscle_groups = $5, equipment = $6, measurement_type = $7,
					instructions = $8, updated_at = $9
				WHERE id = $1
				RETURNING `+exerciseColumns,
				id, in.Name, string(in.Category), string(in.PrimaryMuscleGroup), exerciseSecondary(in.SecondaryMuscleGroups),
				string(in.Equipment), string(in.MeasurementType), in.Instructions, now))
			if err != nil {
				return fmt.Errorf("store: update exercise: %w", err)
			}
			return nil
		})
		if err == nil {
			return out, nil
		}
		if !exerciseIsNameViolation(err) {
			return domain.Exercise{}, err // NotFound, deleted or unexpected
		}
		owner, found, lookupErr := exerciseLiveIDByName(ctx, s.db, in.Name, id)
		if lookupErr != nil {
			return domain.Exercise{}, lookupErr
		}
		if found {
			return domain.Exercise{}, exerciseNameTaken(owner)
		}
		lastErr = err // the name's owner vanished: try again
	}
	return domain.Exercise{}, fmt.Errorf("store: update exercise: name conflict did not settle: %w", lastErr)
}

// SoftDelete sets deleted_at and updated_at to now (spec 19). It is idempotent:
// an exercise that is already deleted is left as it is (updated_at is not
// bumped again) and nil is returned. NotFound when the id never existed. The
// image file and columns are kept.
func (s *Exercises) SoftDelete(ctx context.Context, id uuid.UUID, now time.Time) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE exercises SET deleted_at = $2, updated_at = $2 WHERE id = $1 AND deleted_at IS NULL`, id, now)
	if err != nil {
		return fmt.Errorf("store: delete exercise: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM exercises WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: delete exercise: %w", err)
	}
	if !exists {
		return domain.NewNotFound()
	}
	return nil // already deleted
}

// ExerciseFilter selects and pages the exercise list (spec 08). The zero
// value lists live exercises by name.
type ExerciseFilter struct {
	// Query is a case-insensitive substring of the name; "" means no search.
	// It is matched literally (LIKE wildcards in it are escaped) and must be
	// valid text without NUL.
	Query string
	// Category, PrimaryMuscleGroup and Equipment are exact filters; "" means
	// any. Callers validate them against the enum lists.
	Category           domain.Category
	PrimaryMuscleGroup domain.MuscleGroup
	Equipment          domain.Equipment
	// IncludeDeleted also lists soft-deleted exercises. It is implied by
	// UpdatedSince.
	IncludeDeleted bool
	// UpdatedSince switches to sync mode: exercises with updated_at strictly
	// after it, deleted ones included, ordered by (updated_at, id).
	UpdatedSince *time.Time
	// Cursor is the NextCursor of the previous page, "" for the first page.
	// A malformed cursor is a *domain.BadRequestError. A cursor belongs to the
	// mode and filters that issued it and is not checked against them: a
	// name-order cursor in sync mode is rejected (its first part is not a
	// timestamp), but the reverse just starts at an arbitrary place.
	Cursor string
	// Limit is the page size, 1 to domain.MaxPageLimit; anything else is
	// replaced by domain.DefaultPageLimit or domain.MaxPageLimit.
	Limit int
}

// List returns one page of exercises and the cursor of the next page, "" on the
// last page.
//
// Default order is lower(name), id. In sync mode (UpdatedSince set) it is
// updated_at, id, which the index exercises_updated_at_id_idx serves. Paging is
// keyset-based, so rows added or removed between requests never repeat or
// shift a row that was already returned. One more row than Limit is fetched to
// know whether a next page exists.
func (s *Exercises) List(ctx context.Context, f ExerciseFilter) (items []domain.Exercise, nextCursor string, err error) {
	limit := f.Limit
	if limit < 1 {
		limit = domain.DefaultPageLimit
	}
	limit = min(limit, domain.MaxPageLimit)

	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	syncMode := f.UpdatedSince != nil
	if !f.IncludeDeleted && !syncMode {
		where = append(where, "deleted_at IS NULL")
	}
	if f.Query != "" {
		// lower(name) with LIKE '%...%' is what the trigram index serves.
		where = append(where, "lower(name) LIKE '%' || lower("+arg(exerciseLikeEscape(f.Query))+") || '%'")
	}
	if f.Category != "" {
		where = append(where, "category = "+arg(string(f.Category)))
	}
	if f.PrimaryMuscleGroup != "" {
		where = append(where, "primary_muscle_group = "+arg(string(f.PrimaryMuscleGroup)))
	}
	if f.Equipment != "" {
		where = append(where, "equipment = "+arg(string(f.Equipment)))
	}

	var order string
	if syncMode {
		where = append(where, "updated_at > "+arg(f.UpdatedSince.UTC()))
		order = "updated_at, id"
		if f.Cursor != "" {
			c, err := domain.DecodeCursor(f.Cursor)
			if err != nil {
				return nil, "", err
			}
			where = append(where, "(updated_at, id) > ("+arg(c.At)+", "+arg(c.ID)+")")
		}
	} else {
		order = "lower(name), id"
		if f.Cursor != "" {
			name, id, err := exerciseDecodeNameCursor(f.Cursor)
			if err != nil {
				return nil, "", err
			}
			where = append(where, "(lower(name), id) > ("+arg(name)+", "+arg(id)+")")
		}
	}

	sql := `SELECT ` + exerciseColumns + `, lower(name) FROM exercises`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, " AND ")
	}
	sql += ` ORDER BY ` + order + ` LIMIT ` + arg(limit+1)

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list exercises: %w", err)
	}
	defer rows.Close()

	items = make([]domain.Exercise, 0, min(limit+1, 64))
	var sortNames []string // lower(name) as PostgreSQL computed it, for the cursor
	for rows.Next() {
		var sortName string
		e, err := exerciseScan(rows, &sortName)
		if err != nil {
			return nil, "", fmt.Errorf("store: list exercises: %w", err)
		}
		items = append(items, e)
		sortNames = append(sortNames, sortName)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list exercises: %w", err)
	}

	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		if syncMode {
			nextCursor = domain.EncodeCursor(domain.Cursor{At: last.UpdatedAt, ID: last.ID})
		} else {
			nextCursor = domain.EncodeKeyCursor(sortNames[limit-1], last.ID.String())
		}
	}
	return items, nextCursor, nil
}

// exerciseDecodeNameCursor decodes a cursor of the name order: (lower(name),
// id). Both parts must be safe to send to PostgreSQL: a forged cursor with a
// NUL or invalid UTF-8 would otherwise become a 500.
func exerciseDecodeNameCursor(cursor string) (name string, id uuid.UUID, err error) {
	parts, err := domain.DecodeKeyCursor(cursor, 2)
	if err != nil {
		return "", uuid.Nil, err
	}
	name = parts[0]
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", uuid.Nil, domain.NewBadRequest("invalid cursor")
	}
	id, err = uuid.Parse(parts[1])
	if err != nil || id.String() != parts[1] {
		return "", uuid.Nil, domain.NewBadRequest("invalid cursor")
	}
	return name, id, nil
}

// exerciseLikeEscape escapes the LIKE wildcards of a search text, so it is
// matched literally. Backslash is LIKE's default escape character.
func exerciseLikeEscape(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// The image hooks below back endpoints 20 and 21 (service.ExerciseImages). The
// caller writes the new file first, updates the row here, and only after this
// method returned deletes the previous file, so the database never points at a
// missing file. Both methods lock the row (SELECT ... FOR UPDATE) in a
// transaction of their own, so two concurrent changes cannot both believe they
// replaced the same old image.

// SetImage stores img as the exercise's image and sets updated_at to now. It
// returns the updated exercise and the image it replaced.
//
// old is non-nil only when a different file was replaced: that file is now
// unreferenced and the caller may delete it. It is nil when the exercise had no
// image, and also when it already had exactly this image (same hash and
// extension, so the same file): then nothing is written, updated_at stays
// (spec 20: identical bytes are a no-op) and the file must NOT be deleted.
//
// Errors: NotFound when the id never existed, Conflict deleted when the
// exercise is soft-deleted.
func (s *Exercises) SetImage(ctx context.Context, id uuid.UUID, img domain.ExerciseImage, now time.Time) (e domain.Exercise, old *domain.ExerciseImage, err error) {
	err = s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := exerciseGet(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return domain.NewConflict(domain.IssueDeleted)
		}
		if cur.Image != nil && cur.Image.Hash == img.Hash && cur.Image.Ext == img.Ext {
			e = cur
			return nil
		}
		e, err = exerciseScan(tx.QueryRow(ctx, `
			UPDATE exercises SET image_hash = $2, image_ext = $3, image_size_bytes = $4, updated_at = $5
			WHERE id = $1
			RETURNING `+exerciseColumns,
			id, img.Hash, string(img.Ext), img.SizeBytes, now))
		if err != nil {
			return fmt.Errorf("store: set exercise image: %w", err)
		}
		old = cur.Image
		return nil
	})
	if err != nil {
		return domain.Exercise{}, nil, err
	}
	return e, old, nil
}

// ClearImage removes the exercise's image columns and sets updated_at to now
// (spec 21). It returns the image that was removed, whose file the caller
// deletes after this method returned.
//
// It is idempotent: it returns (nil, nil) and writes nothing when the exercise
// has no image and also when the exercise is soft-deleted. A deleted exercise
// keeps its image, like all its other fields (specs 08, 19, 21). NotFound only
// when the id never existed.
func (s *Exercises) ClearImage(ctx context.Context, id uuid.UUID, now time.Time) (old *domain.ExerciseImage, err error) {
	err = s.db.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := exerciseGet(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil || cur.Image == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE exercises SET image_hash = NULL, image_ext = NULL, image_size_bytes = NULL, updated_at = $2
			WHERE id = $1`, id, now); err != nil {
			return fmt.Errorf("store: clear exercise image: %w", err)
		}
		old = cur.Image
		return nil
	})
	if err != nil {
		return nil, err
	}
	return old, nil
}

// ImageRefs calls fn for every exercise that has an image, soft-deleted
// exercises included: their files are kept (spec 19), so they are referenced.
// It is the referenced set for `media gc`. Rows are streamed in no particular
// order; fn must not use the database connection pool for long-running work,
// as one connection stays busy until the last row. An error from fn stops the
// scan and is returned.
func (s *Exercises) ImageRefs(ctx context.Context, fn func(id uuid.UUID, img domain.ExerciseImage) error) error {
	rows, err := s.db.Query(ctx, `
		SELECT id, image_hash, image_ext, image_size_bytes FROM exercises WHERE image_hash IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("store: list exercise images: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  uuid.UUID
			ext string
			img domain.ExerciseImage
		)
		if err := rows.Scan(&id, &img.Hash, &ext, &img.SizeBytes); err != nil {
			return fmt.Errorf("store: list exercise images: %w", err)
		}
		img.Ext = domain.ImageExt(ext)
		if err := fn(id, img); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: list exercise images: %w", err)
	}
	return nil
}
