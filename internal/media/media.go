// Package media inspects uploaded exercise images and stores them on disk
// (docs/api/endpoints/20-set-exercise-image.md).
//
// It is a pure library: no database and no HTTP handlers. The server does no
// image processing (no resize, no re-encode), so everything here works on the
// raw bytes: sniff the real type, read the pixel size from the header, hash the
// content, and write it under MEDIA_DIR as exercises/{exercise_id}/{hash}.{ext}.
//
// Typical upload flow (service layer):
//
//	data, err := media.ReadLimited(body, domain.MaxImageBytes) // 400 empty, 413 too big
//	info, err := media.Inspect(data)                           // 415, 422 invalid_image / too_large_dimensions
//	err = store.Put(id, info.Hash, info.Ext, data)             // temp file, fsync, rename, fsync dir
//	// ... update the exercise row in a transaction, then store.Delete the old file.
//
// The image's Content-Type request header is not consulted here: the caller
// compares it with Info.ContentType (spec 20: the header must agree, but is
// never trusted alone).
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"

	"workout-tracker-be/internal/domain"
)

// Info describes an inspected image.
type Info struct {
	Ext         domain.ImageExt // from the sniffed bytes: jpg, png or webp
	ContentType string          // Ext.ContentType()
	Width       int
	Height      int
	Size        int64  // len(data)
	Hash        string // first domain.ImageHashLen (16) hex chars of the SHA-256 of the bytes
}

// Hash returns the content hash used in file names: the first 16 lowercase hex
// characters of the SHA-256 of data. Identical bytes always give the same hash,
// which is what makes re-uploading the same image a no-op.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:domain.ImageHashLen/2])
}

// ReadLimited reads r to the end but at most max bytes. A body longer than max
// returns domain.NewPayloadTooLarge (the HTTP layer also caps the body; this is
// defense in depth, and it maps http.MaxBytesReader's error the same way). An
// empty body returns a domain bad-request error (spec 20: 400 for an empty
// body). Other read errors are returned wrapped.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	if max < 0 {
		max = 0
	}
	limit := max
	if limit < math.MaxInt64 {
		limit++ // one extra byte tells "exactly max" from "more than max"
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, domain.NewPayloadTooLarge()
		}
		return nil, fmt.Errorf("media: read body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, domain.NewPayloadTooLarge()
	}
	if len(data) == 0 {
		return nil, errEmptyBody()
	}
	return data, nil
}

func errEmptyBody() error { return domain.NewBadRequest("empty body") }

// errCorrupt marks an image whose header cannot be parsed. Inspect turns it
// into the invalid_image validation error.
var errCorrupt = errors.New("media: corrupt image")

// Inspect validates the bytes of an uploaded image and describes it. The type
// comes only from the content (magic bytes), never from a header or file name.
//
// Errors, all domain errors, in the order they are checked:
//   - empty data: bad request (400)
//   - more than domain.MaxImageBytes: payload too large (413)
//   - not JPEG, PNG or WebP: unsupported media type (415)
//   - unreadable header, truncated WebP, or a zero dimension: validation
//     error, issue domain.IssueInvalidImage (422)
//   - width or height over domain.MaxImageDimension: validation error, issue
//     domain.IssueTooLargeDimensions (422)
//
// Only the header is read (image.DecodeConfig for JPEG and PNG, the RIFF chunk
// header for WebP); the pixels are never decoded. A JPEG or PNG that is intact
// up to its header but damaged or cut off later is therefore accepted, in
// keeping with spec 20 ("the server does no image processing").
//
// The validation errors have no field: the request body is the image.
func Inspect(data []byte) (Info, error) {
	if len(data) == 0 {
		return Info{}, errEmptyBody()
	}
	if int64(len(data)) > domain.MaxImageBytes {
		return Info{}, domain.NewPayloadTooLarge()
	}
	ext, ok := sniff(data)
	if !ok {
		return Info{}, domain.NewUnsupportedMediaType()
	}

	var width, height int
	var err error
	switch ext {
	case domain.ImageExtJPG:
		var cfg image.Config
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
		width, height = cfg.Width, cfg.Height
	case domain.ImageExtPNG:
		var cfg image.Config
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
		width, height = cfg.Width, cfg.Height
	case domain.ImageExtWebP:
		width, height, err = webpConfig(data)
	}
	if err != nil || width <= 0 || height <= 0 {
		return Info{}, domain.NewValidation("", domain.IssueInvalidImage)
	}
	if width > domain.MaxImageDimension || height > domain.MaxImageDimension {
		return Info{}, domain.NewValidation("", domain.IssueTooLargeDimensions)
	}

	return Info{
		Ext:         ext,
		ContentType: ext.ContentType(),
		Width:       width,
		Height:      height,
		Size:        int64(len(data)),
		Hash:        Hash(data),
	}, nil
}

var (
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	pngMagic  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
)

// sniff identifies the file type from its first bytes: JPEG (FF D8 FF), PNG
// (the 8-byte signature) or WebP ("RIFF", 4 size bytes, "WEBP").
func sniff(data []byte) (domain.ImageExt, bool) {
	switch {
	case bytes.HasPrefix(data, jpegMagic):
		return domain.ImageExtJPG, true
	case bytes.HasPrefix(data, pngMagic):
		return domain.ImageExtPNG, true
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return domain.ImageExtWebP, true
	}
	return "", false
}
