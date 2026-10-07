package service

import (
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

const exerciseTestBaseURL = "https://api.example.com"

var exerciseTestT0 = time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)

// exerciseTestDeps builds Deps on a throwaway database with a fake clock.
func exerciseTestDeps(t *testing.T, baseURL string) (Deps, *clock.Fake) {
	t.Helper()
	env := map[string]string{
		"APP_ENV":        "development",
		"DATABASE_URL":   "postgres://unused@localhost/unused",
		"MEDIA_DIR":      t.TempDir(),
		"MEDIA_BASE_URL": baseURL,
		"HTTP_ADDR":      "127.0.0.1:0",
	}
	cfg, err := config.Parse(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	clk := clock.NewFake(exerciseTestT0)
	return Deps{
		DB:     testutil.NewDB(t),
		Clock:  clk,
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}, clk
}

func exerciseTestService(t *testing.T) (*Exercises, Deps, *clock.Fake, uuid.UUID) {
	t.Helper()
	d, clk := exerciseTestDeps(t, exerciseTestBaseURL)
	user, _ := testutil.SeedUser(t, d.DB, domain.RoleUser)
	return NewExercises(d), d, clk, user
}

func exerciseTestContent(name string) domain.ExerciseInput {
	return domain.ExerciseInput{
		Name:                  name,
		Category:              domain.CategoryStrength,
		PrimaryMuscleGroup:    domain.MuscleGroupChest,
		SecondaryMuscleGroups: []domain.MuscleGroup{domain.MuscleGroupTriceps},
		Equipment:             domain.EquipmentBarbell,
		MeasurementType:       domain.MeasurementTypeRepsWeight,
	}
}

func exerciseTestStr(s string) *string { return &s }

func exerciseTestIssues(t *testing.T, err error) []string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v (%T), want a validation error", err, err)
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Field + ":" + is.Issue
	}
	return out
}

func exerciseTestConflict(t *testing.T, err error, issue string) *domain.ConflictError {
	t.Helper()
	var c *domain.ConflictError
	if !errors.As(err, &c) || c.Issue != issue {
		t.Fatalf("err = %v, want a conflict with issue %q", err, issue)
	}
	return c
}

func TestNewExerciseResponse(t *testing.T) {
	id := uuid.MustParse("0195f3a2-bbbb-7000-8000-000000000010")
	withImage := domain.Exercise{
		ID:                    id,
		SecondaryMuscleGroups: []domain.MuscleGroup{domain.MuscleGroupTriceps},
		Image:                 &domain.ExerciseImage{Hash: "9f2c4e1ab37d05c6", Ext: domain.ImageExtWebP, SizeBytes: 100},
	}

	tests := []struct {
		name    string
		base    string
		in      domain.Exercise
		wantURL string // "" means nil
	}{
		{"webp image", exerciseTestBaseURL, withImage,
			"https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp"},
		{"base url with a port and a path prefix", "http://localhost:8080/api", withImage,
			"http://localhost:8080/api/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp"},
		{"jpg", exerciseTestBaseURL, func() domain.Exercise {
			e := withImage
			e.Image = &domain.ExerciseImage{Hash: "0123456789abcdef", Ext: domain.ImageExtJPG}
			return e
		}(), "https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/0123456789abcdef.jpg"},
		{"a deleted exercise keeps its image url", exerciseTestBaseURL, func() domain.Exercise {
			e := withImage
			now := exerciseTestT0
			e.DeletedAt = &now
			return e
		}(), "https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp"},
		{"no image", exerciseTestBaseURL, domain.Exercise{ID: id}, ""},
		{"a corrupt reference gives no url", exerciseTestBaseURL, func() domain.Exercise {
			e := withImage
			e.Image = &domain.ExerciseImage{Hash: "short", Ext: domain.ImageExtWebP}
			return e
		}(), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.in
			got := newExerciseResponse(tt.base, tt.in)
			switch {
			case tt.wantURL == "" && got.ImageURL != nil:
				t.Errorf("ImageURL = %q, want nil", *got.ImageURL)
			case tt.wantURL != "" && (got.ImageURL == nil || *got.ImageURL != tt.wantURL):
				t.Errorf("ImageURL = %v, want %q", got.ImageURL, tt.wantURL)
			}
			if before.ImageURL != nil || tt.in.ImageURL != nil {
				t.Error("the input was modified")
			}
			if got.SecondaryMuscleGroups == nil {
				t.Error("SecondaryMuscleGroups is nil, want a non-nil slice so it encodes as []")
			}
		})
	}

	t.Run("a stale image_url is replaced", func(t *testing.T) {
		e := domain.Exercise{ID: id, ImageURL: exerciseTestStr("https://old.example.com/x.png")}
		if got := newExerciseResponse(exerciseTestBaseURL, e); got.ImageURL != nil {
			t.Errorf("ImageURL = %q, want nil for an exercise without an image", *got.ImageURL)
		}
	})
}

func TestExercisesServiceCreate(t *testing.T) {
	t.Run("sets created_by from the caller and the times from the clock", func(t *testing.T) {
		s, _, _, user := exerciseTestService(t)
		e, created, err := s.Create(t.Context(), user, domain.ExerciseCreateInput{ExerciseInput: exerciseTestContent("  Bench Press ")})
		if err != nil || !created {
			t.Fatalf("Create: created=%v err=%v", created, err)
		}
		if e.CreatedBy != user || e.Name != "Bench Press" {
			t.Errorf("created_by = %v name = %q", e.CreatedBy, e.Name)
		}
		if !e.CreatedAt.Equal(exerciseTestT0) || !e.UpdatedAt.Equal(exerciseTestT0) {
			t.Errorf("created_at = %v updated_at = %v, want the fake clock %v", e.CreatedAt, e.UpdatedAt, exerciseTestT0)
		}
		if e.ID == uuid.Nil || e.ID.Version() != 7 {
			t.Errorf("id = %v, want a generated UUID v7", e.ID)
		}
		if e.ImageURL != nil {
			t.Errorf("image_url = %q, want nil", *e.ImageURL)
		}
	})

	t.Run("uses the client's id", func(t *testing.T) {
		s, _, _, user := exerciseTestService(t)
		id := "0195f3a2-bbbb-7000-8000-000000000011"
		e, _, err := s.Create(t.Context(), user, domain.ExerciseCreateInput{ID: &id, ExerciseInput: exerciseTestContent("Bench Press")})
		if err != nil {
			t.Fatal(err)
		}
		if e.ID.String() != id {
			t.Errorf("id = %v, want %v", e.ID, id)
		}
	})

	t.Run("a retry is not created again and keeps the first creator", func(t *testing.T) {
		s, d, clk, user := exerciseTestService(t)
		other, _ := testutil.SeedUser(t, d.DB, domain.RoleUser)
		id := "0195f3a2-bbbb-7000-8000-000000000011"
		in := domain.ExerciseCreateInput{ID: &id, ExerciseInput: exerciseTestContent("Bench Press")}
		first, _, err := s.Create(t.Context(), user, in)
		if err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour)
		again, created, err := s.Create(t.Context(), other, in)
		if err != nil || created {
			t.Fatalf("retry: created=%v err=%v, want created=false", created, err)
		}
		if again.CreatedBy != user || !again.UpdatedAt.Equal(first.UpdatedAt) {
			t.Errorf("retry changed the row: %+v", again)
		}
	})

	t.Run("validation reports every issue and nothing is stored", func(t *testing.T) {
		s, _, _, user := exerciseTestService(t)
		bad := "not-a-uuid"
		_, _, err := s.Create(t.Context(), user, domain.ExerciseCreateInput{
			ID:            &bad,
			ExerciseInput: domain.ExerciseInput{Name: " ", Category: "yoga", PrimaryMuscleGroup: domain.MuscleGroupChest, Equipment: domain.EquipmentBarbell, MeasurementType: domain.MeasurementTypeReps},
		})
		want := []string{"id:invalid_format", "name:required", "category:invalid_value"}
		if got := exerciseTestIssues(t, err); !slices.Equal(got, want) {
			t.Errorf("issues = %v, want %v", got, want)
		}
		page, err := s.List(t.Context(), ExerciseListParams{})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("list after a failed create: %v %v", page.Items, err)
		}
	})

	t.Run("conflicts", func(t *testing.T) {
		s, _, _, user := exerciseTestService(t)
		first, _, err := s.Create(t.Context(), user, domain.ExerciseCreateInput{ExerciseInput: exerciseTestContent("Bench Press")})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Create(t.Context(), user, domain.ExerciseCreateInput{ExerciseInput: exerciseTestContent("bench press")})
		if c := exerciseTestConflict(t, err, domain.IssueAlreadyExists); c.ExistingID != first.ID {
			t.Errorf("ExistingID = %v, want %v", c.ExistingID, first.ID)
		}

		id := first.ID.String()
		changed := exerciseTestContent("Bench Press")
		changed.Equipment = domain.EquipmentDumbbell
		_, _, err = s.Create(t.Context(), user, domain.ExerciseCreateInput{ID: &id, ExerciseInput: changed})
		_ = exerciseTestConflict(t, err, domain.IssueIDTaken)

		if err := s.Delete(t.Context(), first.ID); err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Create(t.Context(), user, domain.ExerciseCreateInput{ID: &id, ExerciseInput: exerciseTestContent("Bench Press")})
		_ = exerciseTestConflict(t, err, domain.IssueDeleted)
	})
}

func TestExercisesServiceUpdate(t *testing.T) {
	t.Run("updates from the clock and leaves the image and its url alone", func(t *testing.T) {
		s, d, clk, user := exerciseTestService(t)
		id := testutil.SeedExercise(t, d.DB, user,
			testutil.WithExerciseName("Bench Press"),
			testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtWebP, 100))
		clk.Advance(time.Hour)

		e, err := s.Update(t.Context(), id, exerciseTestContent("Barbell Bench Press"))
		if err != nil {
			t.Fatal(err)
		}
		if e.Name != "Barbell Bench Press" || !e.UpdatedAt.Equal(exerciseTestT0.Add(time.Hour)) {
			t.Errorf("name = %q updated_at = %v", e.Name, e.UpdatedAt)
		}
		if e.CreatedBy != user {
			t.Errorf("created_by = %v, want unchanged %v", e.CreatedBy, user)
		}
		want := exerciseTestBaseURL + "/media/exercises/" + id.String() + "/9f2c4e1ab37d05c6.webp"
		if e.ImageURL == nil || *e.ImageURL != want {
			t.Errorf("image_url = %v, want %q", e.ImageURL, want)
		}
	})

	t.Run("validation comes before the lookup", func(t *testing.T) {
		s, _, _, _ := exerciseTestService(t)
		_, err := s.Update(t.Context(), uuid.New(), domain.ExerciseInput{Name: "x"})
		if got := exerciseTestIssues(t, err); len(got) != 4 {
			t.Errorf("issues = %v, want the four missing required fields", got)
		}
	})

	t.Run("not found and deleted", func(t *testing.T) {
		s, _, _, user := exerciseTestService(t)
		if _, err := s.Update(t.Context(), uuid.New(), exerciseTestContent("x")); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("err = %v, want not found", err)
		}
		e, _, _ := s.Create(t.Context(), user, domain.ExerciseCreateInput{ExerciseInput: exerciseTestContent("Bench Press")})
		_ = s.Delete(t.Context(), e.ID)
		_, err := s.Update(t.Context(), e.ID, exerciseTestContent("Bench Press"))
		_ = exerciseTestConflict(t, err, domain.IssueDeleted)
	})
}

func TestExercisesServiceDelete(t *testing.T) {
	s, _, clk, user := exerciseTestService(t)
	e, _, err := s.Create(t.Context(), user, domain.ExerciseCreateInput{ExerciseInput: exerciseTestContent("Bench Press")})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := s.Delete(t.Context(), e.ID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := s.Delete(t.Context(), e.ID); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if err := s.Delete(t.Context(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want not found", err)
	}

	page, err := s.List(t.Context(), ExerciseListParams{IncludeDeleted: true})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %v %v", page.Items, err)
	}
	got := page.Items[0]
	if got.DeletedAt == nil || !got.DeletedAt.Equal(exerciseTestT0.Add(time.Hour)) || !got.UpdatedAt.Equal(exerciseTestT0.Add(time.Hour)) {
		t.Errorf("deleted_at = %v updated_at = %v, want the first delete's time", got.DeletedAt, got.UpdatedAt)
	}
	if got.Name != "Bench Press" {
		t.Errorf("a deleted exercise keeps its fields: %+v", got)
	}
}

func TestExercisesServiceListValidation(t *testing.T) {
	s, _, _, _ := exerciseTestService(t)
	tests := []struct {
		name string
		p    ExerciseListParams
		want []string
	}{
		{"limit below the minimum", ExerciseListParams{Limit: -1}, []string{"limit:out_of_range"}},
		{"limit above the maximum", ExerciseListParams{Limit: 201}, []string{"limit:out_of_range"}},
		{"unknown category", ExerciseListParams{Category: exerciseTestStr("yoga")}, []string{"category:invalid_value"}},
		{"empty category", ExerciseListParams{Category: exerciseTestStr("")}, []string{"category:invalid_value"}},
		{"unknown muscle group", ExerciseListParams{PrimaryMuscleGroup: exerciseTestStr("wings")}, []string{"primary_muscle_group:invalid_value"}},
		{"unknown equipment", ExerciseListParams{Equipment: exerciseTestStr("trampoline")}, []string{"equipment:invalid_value"}},
		{"q too long", ExerciseListParams{Query: strings.Repeat("a", 101)}, []string{"q:too_long"}},
		{"q with NUL", ExerciseListParams{Query: "a\x00b"}, []string{"q:invalid_chars"}},
		{"q with invalid UTF-8", ExerciseListParams{Query: "a\xffb"}, []string{"q:invalid_chars"}},
		{"everything at once", ExerciseListParams{
			Limit: 500, Query: "\x00", Category: exerciseTestStr("x"), PrimaryMuscleGroup: exerciseTestStr("y"), Equipment: exerciseTestStr("z"),
		}, []string{"limit:out_of_range", "q:invalid_chars", "category:invalid_value", "primary_muscle_group:invalid_value", "equipment:invalid_value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.List(t.Context(), tt.p)
			if got := exerciseTestIssues(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("issues = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("q of exactly 100 characters is accepted, surrounding space is ignored", func(t *testing.T) {
		if _, err := s.List(t.Context(), ExerciseListParams{Query: "  " + strings.Repeat("a", 100) + "  "}); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a bad cursor is a bad request", func(t *testing.T) {
		_, err := s.List(t.Context(), ExerciseListParams{Cursor: "garbage!"})
		var br *domain.BadRequestError
		if !errors.As(err, &br) {
			t.Errorf("err = %v, want a bad request", err)
		}
	})
}

func TestExercisesServiceList(t *testing.T) {
	s, d, _, user := exerciseTestService(t)
	for _, name := range []string{"apple", "banana", "cherry"} {
		testutil.SeedExercise(t, d.DB, user, testutil.WithExerciseName(name))
	}
	withImage := testutil.SeedExercise(t, d.DB, user, testutil.WithExerciseName("date"),
		testutil.WithExerciseImage("9f2c4e1ab37d05c6", domain.ImageExtPNG, 10))

	page, err := s.List(t.Context(), ExerciseListParams{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatalf("first page: %d items, cursor %v", len(page.Items), page.NextCursor)
	}
	for _, e := range page.Items {
		if e.ImageURL != nil || e.SecondaryMuscleGroups == nil {
			t.Errorf("%s: image_url = %v secondary = %v", e.Name, e.ImageURL, e.SecondaryMuscleGroups)
		}
	}

	page, err = s.List(t.Context(), ExerciseListParams{Limit: 3, Cursor: *page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("last page: %d items, cursor %v", len(page.Items), page.NextCursor)
	}
	want := exerciseTestBaseURL + "/media/exercises/" + withImage.String() + "/9f2c4e1ab37d05c6.png"
	if got := page.Items[0].ImageURL; got == nil || *got != want {
		t.Errorf("image_url = %v, want %q", got, want)
	}

	t.Run("an empty result is an empty list, not null", func(t *testing.T) {
		page, err := s.List(t.Context(), ExerciseListParams{Query: "zzz"})
		if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
			t.Errorf("page = %+v err = %v", page, err)
		}
	})
	t.Run("the default limit applies for zero", func(t *testing.T) {
		if _, err := s.List(t.Context(), ExerciseListParams{Limit: 0}); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}
