package handlers_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workout-tracker-be/internal/clock"
	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi"
	"workout-tracker-be/internal/httpapi/apitest"
	"workout-tracker-be/internal/httpapi/handlers"
	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/service"
	"workout-tracker-be/internal/testutil"
)

// These tests drive the real router with the real service, a real database and
// a real media store in a temp directory. Reuses the fake auth and the time of
// exercises_helpers_test.go (same package).

// imageAPI is the harness for the image endpoints.
type imageAPI struct {
	t     *testing.T
	h     http.Handler
	deps  service.Deps
	files *media.Store
	clk   *clock.Fake
	user  uuid.UUID
	admin uuid.UUID
}

func imageNewAPI(t *testing.T) *imageAPI {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{
		"APP_ENV":        "development",
		"DATABASE_URL":   "postgres://unused@localhost/unused",
		"MEDIA_DIR":      dir,
		"MEDIA_BASE_URL": exerciseAPIBaseURL,
		"HTTP_ADDR":      "127.0.0.1:0",
	}
	cfg, err := config.Parse(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	clk := clock.NewFake(exerciseAPIT0)
	files, err := media.NewStore(dir)
	if err != nil {
		t.Fatalf("media store: %v", err)
	}
	deps := service.Deps{DB: testutil.NewDB(t), Clock: clk, Config: cfg, Logger: logger, Media: files}

	hd := handlers.Deps{Config: cfg, Logger: logger, Clock: clk}
	images := handlers.NewExerciseImages(service.NewExerciseImages(deps), hd)
	exercises := handlers.NewExercises(service.NewExercises(deps), hd)
	router := httpapi.NewRouter(httpapi.RouterConfig{
		Handlers: httpapi.Handlers{
			ListExercises:       exercises.List,
			SetExerciseImage:    images.Set,
			DeleteExerciseImage: images.Delete,
		},
		Authenticator: exerciseAPIFakeAuth,
		Logger:        logger,
	})

	user, _ := testutil.SeedUser(t, deps.DB, domain.RoleUser)
	admin, _ := testutil.SeedUser(t, deps.DB, domain.RoleAdmin)
	return &imageAPI{t: t, h: router, deps: deps, files: files, clk: clk, user: user, admin: admin}
}

// do sends a request. role == "" means an anonymous request.
func (a *imageAPI) do(user uuid.UUID, role domain.Role, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if role != "" {
		req.Header.Set(exerciseAPIUserHdr, user.String())
		req.Header.Set(exerciseAPIRoleHdr, string(role))
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func imagePath(id uuid.UUID) string { return "/v1/exercises/" + id.String() + "/image" }

func (a *imageAPI) put(id uuid.UUID, data []byte, contentType string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(a.user, domain.RoleUser, http.MethodPut, imagePath(id), bytes.NewReader(data), contentType)
}

func (a *imageAPI) putStream(id uuid.UUID, data []byte, contentType string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(a.user, domain.RoleUser, http.MethodPut, imagePath(id), imageUnknownLenReader{bytes.NewReader(data)}, contentType)
}

func (a *imageAPI) del(id uuid.UUID) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.do(a.user, domain.RoleUser, http.MethodDelete, imagePath(id), nil, "")
}

// seedExercise inserts an exercise created by the default user.
func (a *imageAPI) seedExercise(opts ...testutil.ExerciseOption) uuid.UUID {
	a.t.Helper()
	return testutil.SeedExercise(a.t, a.deps.DB, a.user, opts...)
}

// imageColumns reads the three image columns of an exercise; "" means NULL.
func (a *imageAPI) imageColumns(id uuid.UUID) (hash, ext string, size *int64) {
	a.t.Helper()
	var h, e *string
	if err := a.deps.DB.QueryRow(a.t.Context(),
		`SELECT image_hash, image_ext, image_size_bytes FROM exercises WHERE id = $1`, id).
		Scan(&h, &e, &size); err != nil {
		a.t.Fatalf("read image columns: %v", err)
	}
	if h != nil {
		hash = *h
	}
	if e != nil {
		ext = *e
	}
	return hash, ext, size
}

// requireFileExists asserts the stored file for hash/ext is present.
func (a *imageAPI) requireFileExists(id uuid.UUID, hash string, ext domain.ImageExt, want bool) {
	a.t.Helper()
	got, err := a.files.Exists(id, hash, ext)
	if err != nil {
		a.t.Fatalf("exists %s/%s.%s: %v", id, hash, ext, err)
	}
	if got != want {
		a.t.Errorf("file %s.%s exists = %v, want %v", hash, ext, got, want)
	}
}

func imageJSON(t testing.TB, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object: %v: %s", err, rec.Body.String())
	}
	return m
}

func imageRequireStatus(t testing.TB, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

func imageRequireError(t testing.TB, rec *httptest.ResponseRecorder, status int, code string) render.ErrorBody {
	t.Helper()
	return apitest.RequireError(t, rec, status, code)
}

// --- image bytes ------------------------------------------------------------

func imageJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imagePNG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageGIF(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	pal := color.Palette{color.Black, color.White}
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, w, h), pal), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// imageUnknownLenReader hides the length from httptest, so the request arrives
// chunked and the body limit is enforced while reading, not from Content-Length.
type imageUnknownLenReader struct{ r io.Reader }

func (r imageUnknownLenReader) Read(p []byte) (int, error) { return r.r.Read(p) }

// --- tests ------------------------------------------------------------------

func TestExerciseImageSetUploads(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	data := imageJPEG(t, 40, 30)
	rec := a.put(id, data, "image/jpeg")
	imageRequireStatus(t, rec, http.StatusOK)

	body := imageJSON(t, rec)
	hash := media.Hash(data)
	wantURL := exerciseAPIBaseURL + "/media/exercises/" + id.String() + "/" + hash + ".jpg"
	if body["image_url"] != wantURL {
		t.Errorf("image_url = %v, want %q", body["image_url"], wantURL)
	}
	if body["id"] != id.String() {
		t.Errorf("id = %v, want %s", body["id"], id)
	}

	gotHash, gotExt, size := a.imageColumns(id)
	if gotHash != hash || gotExt != "jpg" || size == nil || int(*size) != len(data) {
		t.Errorf("db image = %q/%q/%v, want %q/jpg/%d", gotHash, gotExt, size, hash, len(data))
	}
	a.requireFileExists(id, hash, domain.ImageExtJPG, true)
}

func TestExerciseImageSetReplacesAndDeletesOld(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	first := imageJPEG(t, 10, 10)
	rec := a.put(id, first, "image/jpeg")
	imageRequireStatus(t, rec, http.StatusOK)
	firstHash := media.Hash(first)
	firstUpdated := imageJSON(t, rec)["updated_at"]

	a.clk.Advance(time.Hour)

	second := imagePNG(t, 10, 10)
	rec = a.put(id, second, "image/png")
	imageRequireStatus(t, rec, http.StatusOK)
	secondHash := media.Hash(second)

	if got := imageJSON(t, rec)["updated_at"]; got == firstUpdated {
		t.Errorf("updated_at did not change on replace: %v", got)
	}
	a.requireFileExists(id, secondHash, domain.ImageExtPNG, true)
	a.requireFileExists(id, firstHash, domain.ImageExtJPG, false)

	gotHash, gotExt, _ := a.imageColumns(id)
	if gotHash != secondHash || gotExt != "png" {
		t.Errorf("db image = %q/%q, want %q/png", gotHash, gotExt, secondHash)
	}
}

func TestExerciseImageSetIdenticalIsNoOp(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	data := imageJPEG(t, 20, 20)
	first := a.put(id, data, "image/jpeg")
	imageRequireStatus(t, first, http.StatusOK)
	firstBody := imageJSON(t, first)

	a.clk.Advance(48 * time.Hour)

	second := a.put(id, data, "image/jpeg")
	imageRequireStatus(t, second, http.StatusOK)
	secondBody := imageJSON(t, second)

	if secondBody["image_url"] != firstBody["image_url"] {
		t.Errorf("image_url changed: %v -> %v", firstBody["image_url"], secondBody["image_url"])
	}
	if secondBody["updated_at"] != firstBody["updated_at"] {
		t.Errorf("updated_at bumped on an identical upload: %v -> %v", firstBody["updated_at"], secondBody["updated_at"])
	}
	a.requireFileExists(id, media.Hash(data), domain.ImageExtJPG, true)
}

func TestExerciseImageSetUnsupportedAndMismatch(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	jpg := imageJPEG(t, 8, 8)
	gif := imageGIF(t, 8, 8)

	for _, tt := range []struct {
		name        string
		data        []byte
		contentType string
	}{
		{"gif by sniff", gif, "image/gif"},
		{"jpeg with png header", jpg, "image/png"},
		{"jpeg with text header", jpg, "text/plain"},
		{"jpeg with no header", jpg, ""},
		{"png with jpeg header", imagePNG(t, 8, 8), "image/jpeg"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := a.put(id, tt.data, tt.contentType)
			imageRequireError(t, rec, http.StatusUnsupportedMediaType, "unsupported_media_type")
		})
	}
}

func TestExerciseImageSetEmptyBody(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	rec := a.do(a.user, domain.RoleUser, http.MethodPut, imagePath(id), strings.NewReader(""), "")
	imageRequireError(t, rec, http.StatusBadRequest, "bad_request")
}

func TestExerciseImageSetOversize(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	big := make([]byte, domain.MaxImageBytes+1)

	t.Run("declared content length", func(t *testing.T) {
		rec := a.put(id, big, "image/jpeg")
		imageRequireError(t, rec, http.StatusRequestEntityTooLarge, "payload_too_large")
	})
	t.Run("streamed", func(t *testing.T) {
		rec := a.putStream(id, big, "image/jpeg")
		imageRequireError(t, rec, http.StatusRequestEntityTooLarge, "payload_too_large")
	})
}

func TestExerciseImageSetCorruptAndTooLargeDimensions(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	t.Run("corrupt image", func(t *testing.T) {
		corrupt := append([]byte("\x89PNG\r\n\x1a\n"), []byte("not a real png")...)
		rec := a.put(id, corrupt, "image/png")
		body := imageRequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != domain.IssueInvalidImage {
			t.Errorf("details = %+v, want issue %q", body.Error.Details, domain.IssueInvalidImage)
		}
	})

	t.Run("too large dimensions", func(t *testing.T) {
		big := imagePNG(t, domain.MaxImageDimension+1, 1)
		rec := a.put(id, big, "image/png")
		body := imageRequireError(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != domain.IssueTooLargeDimensions {
			t.Errorf("details = %+v, want issue %q", body.Error.Details, domain.IssueTooLargeDimensions)
		}
	})
}

func TestExerciseImageSetMissingAndDeleted(t *testing.T) {
	a := imageNewAPI(t)
	jpg := imageJPEG(t, 8, 8)

	t.Run("unknown exercise", func(t *testing.T) {
		rec := a.put(uuid.New(), jpg, "image/jpeg")
		imageRequireError(t, rec, http.StatusNotFound, "not_found")
	})

	t.Run("soft-deleted exercise", func(t *testing.T) {
		id := a.seedExercise(testutil.WithExerciseDeletedAt(exerciseAPIT0))
		rec := a.put(id, jpg, "image/jpeg")
		body := imageRequireError(t, rec, http.StatusConflict, "conflict")
		if len(body.Error.Details) != 1 || body.Error.Details[0].Issue != domain.IssueDeleted {
			t.Errorf("details = %+v, want issue %q", body.Error.Details, domain.IssueDeleted)
		}
	})
}

func TestExerciseImageDelete(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	jpg := imageJPEG(t, 12, 12)
	hash := media.Hash(jpg)

	rec := a.put(id, jpg, "image/jpeg")
	imageRequireStatus(t, rec, http.StatusOK)

	if gotHash, _, _ := a.imageColumns(id); gotHash != hash {
		t.Fatalf("image not set: %q", gotHash)
	}
	before := a.updatedAt(id)

	a.clk.Advance(time.Hour)
	if rec := a.del(id); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if gotHash, gotExt, size := a.imageColumns(id); gotHash != "" || gotExt != "" || size != nil {
		t.Errorf("db image columns not cleared: %q/%q/%v", gotHash, gotExt, size)
	}
	a.requireFileExists(id, hash, domain.ImageExtJPG, false)
	if after := a.updatedAt(id); !after.After(before) {
		t.Errorf("updated_at not bumped by the clear: %v -> %v", before, after)
	}

	// Idempotent: the second delete succeeds and does not bump updated_at.
	afterFirst := a.updatedAt(id)
	if rec := a.del(id); rec.Code != http.StatusNoContent {
		t.Errorf("second delete status = %d, want 204", rec.Code)
	}
	if got := a.updatedAt(id); !got.Equal(afterFirst) {
		t.Errorf("second delete bumped updated_at: %v -> %v", afterFirst, got)
	}
}

// updatedAt reads the exercise's updated_at.
func (a *imageAPI) updatedAt(id uuid.UUID) time.Time {
	a.t.Helper()
	var at time.Time
	if err := a.deps.DB.QueryRow(a.t.Context(),
		`SELECT updated_at FROM exercises WHERE id = $1`, id).Scan(&at); err != nil {
		a.t.Fatalf("read updated_at: %v", err)
	}
	return at.UTC()
}

func TestExerciseImageDeleteNoImageIsIdempotent(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	if rec := a.del(id); rec.Code != http.StatusNoContent {
		t.Errorf("delete without image status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestExerciseImageDeleteKeepsDeletedExerciseFile(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	jpg := imageJPEG(t, 9, 9)
	hash := media.Hash(jpg)
	if rec := a.put(id, jpg, "image/jpeg"); rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", rec.Code)
	}

	if _, err := a.deps.DB.Exec(a.t.Context(),
		`UPDATE exercises SET deleted_at = $2 WHERE id = $1`, id, exerciseAPIT0); err != nil {
		t.Fatal(err)
	}
	if rec := a.del(id); rec.Code != http.StatusNoContent {
		t.Errorf("delete of a soft-deleted exercise status = %d, want 204", rec.Code)
	}
	// The image of a deleted exercise is kept (spec 19, 21).
	if gotHash, _, _ := a.imageColumns(id); gotHash != hash {
		t.Errorf("image of a deleted exercise changed: %q, want %q", gotHash, hash)
	}
	a.requireFileExists(id, hash, domain.ImageExtJPG, true)
}

func TestExerciseImageDeleteUnknownIsNotFound(t *testing.T) {
	a := imageNewAPI(t)
	rec := a.del(uuid.New())
	imageRequireError(t, rec, http.StatusNotFound, "not_found")
}

func TestExerciseImageAccessControl(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()
	jpg := imageJPEG(t, 8, 8)

	t.Run("admin is forbidden on set", func(t *testing.T) {
		rec := a.do(a.admin, domain.RoleAdmin, http.MethodPut, imagePath(id), bytes.NewReader(jpg), "image/jpeg")
		imageRequireError(t, rec, http.StatusForbidden, "forbidden")
	})
	t.Run("anonymous is unauthorized on set", func(t *testing.T) {
		rec := a.do(uuid.Nil, "", http.MethodPut, imagePath(id), bytes.NewReader(jpg), "image/jpeg")
		imageRequireError(t, rec, http.StatusUnauthorized, "unauthorized")
	})
	t.Run("admin is forbidden on delete", func(t *testing.T) {
		rec := a.do(a.admin, domain.RoleAdmin, http.MethodDelete, imagePath(id), nil, "")
		imageRequireError(t, rec, http.StatusForbidden, "forbidden")
	})
}

// TestExerciseImageCrashSafety checks the invariant that matters (spec 20): the
// database never points at a file that is not on disk. After every write the
// row's hash/ext must name a present file, and a failed write must leave the
// previous pointer (and its file) untouched.
func TestExerciseImageCrashSafety(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise()

	first := imageJPEG(t, 14, 9)
	if rec := a.put(id, first, "image/jpeg"); rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", rec.Code)
	}
	hash, ext, _ := a.imageColumns(id)
	a.requireFileExists(id, hash, domain.ImageExt(ext), true)

	second := imagePNG(t, 14, 9)
	if rec := a.put(id, second, "image/png"); rec.Code != http.StatusOK {
		t.Fatalf("replace status = %d", rec.Code)
	}
	hash, ext, _ = a.imageColumns(id)
	a.requireFileExists(id, hash, domain.ImageExt(ext), true)
	a.requireFileExists(id, media.Hash(first), domain.ImageExtJPG, false)

	// Soft-delete after the replace, then a failed upload: the row must keep
	// pointing at the file it already has, which is still present.
	if _, err := a.deps.DB.Exec(a.t.Context(),
		`UPDATE exercises SET deleted_at = $2 WHERE id = $1`, id, exerciseAPIT0); err != nil {
		t.Fatal(err)
	}
	third := imageJPEG(t, 30, 30)
	if rec := a.put(id, third, "image/jpeg"); rec.Code != http.StatusConflict {
		t.Fatalf("upload to a deleted exercise status = %d, want 409", rec.Code)
	}
	hash, ext, _ = a.imageColumns(id)
	a.requireFileExists(id, hash, domain.ImageExt(ext), true)
	if hash == media.Hash(third) {
		t.Error("database points at the new file of a rejected upload")
	}
}

// TestExerciseImageURLInListAndSync proves the uploaded image_url shows up on
// the exercise list and the sync feed.
func TestExerciseImageURLInListAndSync(t *testing.T) {
	a := imageNewAPI(t)
	id := a.seedExercise(testutil.WithExerciseUpdatedAt(exerciseAPIT0))
	jpg := imageJPEG(t, 11, 11)
	if rec := a.put(id, jpg, "image/jpeg"); rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", rec.Code)
	}
	want := exerciseAPIBaseURL + "/media/exercises/" + id.String() + "/" + media.Hash(jpg) + ".jpg"

	for _, path := range []string{
		"/v1/exercises",
		"/v1/exercises?updated_since=2026-08-01T00:00:00Z",
	} {
		rec := a.do(a.user, domain.RoleUser, http.MethodGet, path, nil, "")
		imageRequireStatus(t, rec, http.StatusOK)
		items, _ := imageJSON(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%s: items = %d, want 1", path, len(items))
		}
		if got := items[0].(map[string]any)["image_url"]; got != want {
			t.Errorf("%s: image_url = %v, want %q", path, got, want)
		}
	}
}
