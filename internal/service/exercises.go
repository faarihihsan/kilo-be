package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
)

// Exercises implements list, create, update and delete of exercises
// (endpoints 8, 9, 18, 19). Admin-versus-user is decided by the router before
// any method here runs; the methods take plain arguments, never a request.
//
// Every exercise it returns is in its response shape (image_url filled in) by
// newExerciseResponse, the one conversion that ExerciseImages (T6) uses too.
type Exercises struct {
	exercises    *store.Exercises
	clock        clock.Clock
	mediaBaseURL string
}

// NewExercises builds the exercises service.
func NewExercises(d Deps) *Exercises {
	return &Exercises{
		exercises:    store.NewExercises(d.DB),
		clock:        d.Clock,
		mediaBaseURL: d.Config.MediaBaseURL,
	}
}

// newExerciseResponse turns an exercise read from the store into the shape the
// API returns (specs 08, 09, 18, 20): it fills ImageURL from the stored image
// reference and makes sure the secondary muscle groups encode as [] and never
// as null.
//
// ImageURL is mediaBaseURL + "/media/" + media.Path(id, hash, ext), for example
//
//	https://api.example.com/media/exercises/{id}/{hash}.webp
//
// and nil when the exercise has no image. mediaBaseURL is config.MediaBaseURL
// (no trailing slash). The result is a copy; e is not changed. Every exercise
// that leaves the service layer goes through this function, so a new endpoint
// that returns an exercise (the image endpoint, T6) must call it too instead
// of encoding what the store returned.
func newExerciseResponse(mediaBaseURL string, e domain.Exercise) domain.Exercise {
	e.ImageURL = nil
	if e.Image != nil {
		if path := media.Path(e.ID, e.Image.Hash, e.Image.Ext); path != "" {
			url := mediaBaseURL + "/media/" + path
			e.ImageURL = &url
		}
	}
	if e.SecondaryMuscleGroups == nil {
		e.SecondaryMuscleGroups = []domain.MuscleGroup{}
	}
	return e
}

// ExerciseListParams are the query parameters of GET /v1/exercises (spec 08).
type ExerciseListParams struct {
	// Query is the name search; surrounding whitespace is ignored and an empty
	// text means no search.
	Query string
	// Category, PrimaryMuscleGroup and Equipment are filters, nil when the
	// parameter was not sent. A value outside the enum is a validation error.
	Category           *string
	PrimaryMuscleGroup *string
	Equipment          *string
	// Limit is the page size, 0 for the default. 1 to 200 otherwise.
	Limit int
	// Cursor is the opaque next_cursor of the previous page.
	Cursor string
	// UpdatedSince switches to sync mode; see store.ExerciseFilter.
	UpdatedSince   *time.Time
	IncludeDeleted bool
}

// filter validates the parameters and converts them to a store filter. All
// invalid values are reported together (422). The cursor is checked by the
// store, which knows the sort order it belongs to.
func (p ExerciseListParams) filter() (store.ExerciseFilter, error) {
	var v domain.ValidationError
	f := store.ExerciseFilter{
		Cursor:         p.Cursor,
		Limit:          p.Limit,
		UpdatedSince:   p.UpdatedSince,
		IncludeDeleted: p.IncludeDeleted,
	}

	if p.Limit == 0 {
		f.Limit = domain.DefaultPageLimit
	} else if p.Limit < domain.MinPageLimit || p.Limit > domain.MaxPageLimit {
		v.Add("limit", domain.IssueOutOfRange)
	}

	// A name is at most ExerciseNameMaxLen characters, so a longer search can
	// match nothing; reject it instead of sending it to the database.
	f.Query = strings.TrimSpace(p.Query)
	switch {
	case utf8.RuneCountInString(f.Query) > domain.ExerciseNameMaxLen:
		v.Add("q", domain.IssueTooLong)
	case !utf8.ValidString(f.Query) || strings.ContainsRune(f.Query, 0):
		v.Add("q", domain.IssueInvalidChars)
	}

	if p.Category != nil {
		f.Category = domain.Category(*p.Category)
		if !f.Category.IsValid() {
			v.Add("category", domain.IssueInvalidValue)
		}
	}
	if p.PrimaryMuscleGroup != nil {
		f.PrimaryMuscleGroup = domain.MuscleGroup(*p.PrimaryMuscleGroup)
		if !f.PrimaryMuscleGroup.IsValid() {
			v.Add("primary_muscle_group", domain.IssueInvalidValue)
		}
	}
	if p.Equipment != nil {
		f.Equipment = domain.Equipment(*p.Equipment)
		if !f.Equipment.IsValid() {
			v.Add("equipment", domain.IssueInvalidValue)
		}
	}

	if err := v.Err(); err != nil {
		return store.ExerciseFilter{}, err
	}
	return f, nil
}

// List returns one page of the exercise catalogue (spec 08). Without
// UpdatedSince it lists live exercises by name (deleted ones too with
// IncludeDeleted); with UpdatedSince it is the sync feed, ordered by
// (updated_at, id), including deleted exercises.
//
// Errors: Validation (limit, filters, q), BadRequest (cursor).
func (s *Exercises) List(ctx context.Context, p ExerciseListParams) (domain.ExercisePage, error) {
	f, err := p.filter()
	if err != nil {
		return domain.ExercisePage{}, err
	}
	items, next, err := s.exercises.List(ctx, f)
	if err != nil {
		return domain.ExercisePage{}, err
	}
	page := domain.ExercisePage{Items: make([]domain.Exercise, len(items))}
	for i, e := range items {
		page.Items[i] = newExerciseResponse(s.mediaBaseURL, e)
	}
	if next != "" {
		page.NextCursor = &next
	}
	return page, nil
}

// Create adds an exercise created by createdBy (the caller's user id) and
// reports whether it was created (HTTP 201) or, on an idempotent retry of the
// same id with identical content, already existed (HTTP 200). The id is the
// client's when given, else a new UUID v7.
//
// Errors: Validation, Conflict (already_exists with ExistingID, id_taken,
// deleted).
func (s *Exercises) Create(ctx context.Context, createdBy uuid.UUID, in domain.ExerciseCreateInput) (domain.Exercise, bool, error) {
	content, id, err := in.Validate()
	if err != nil {
		return domain.Exercise{}, false, err
	}
	if id == uuid.Nil {
		if id, err = uuid.NewV7(); err != nil {
			return domain.Exercise{}, false, err
		}
	}
	e, created, err := s.exercises.Create(ctx, id, createdBy, content, s.clock.Now())
	if err != nil {
		return domain.Exercise{}, false, err
	}
	return newExerciseResponse(s.mediaBaseURL, e), created, nil
}

// Update replaces the editable fields of the exercise (spec 18). Any user may
// edit any exercise, so there is no caller argument. The image is untouched.
//
// Errors: Validation, NotFound, Conflict (already_exists with ExistingID,
// deleted).
func (s *Exercises) Update(ctx context.Context, id uuid.UUID, in domain.ExerciseInput) (domain.Exercise, error) {
	content, err := in.Validate()
	if err != nil {
		return domain.Exercise{}, err
	}
	e, err := s.exercises.Update(ctx, id, content, s.clock.Now())
	if err != nil {
		return domain.Exercise{}, err
	}
	return newExerciseResponse(s.mediaBaseURL, e), nil
}

// Delete soft-deletes the exercise (spec 19). It is idempotent: deleting a
// deleted exercise succeeds. Error: NotFound when the id never existed.
func (s *Exercises) Delete(ctx context.Context, id uuid.UUID) error {
	return s.exercises.SoftDelete(ctx, id, s.clock.Now())
}
