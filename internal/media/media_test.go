package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"workout-tracker-be/internal/domain"
)

// wantIssue asserts err is a 422 validation error with exactly one issue.
func wantIssue(t *testing.T, err error, issue string) {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("error = %v (%T), want validation error %q", err, err, issue)
	}
	if len(ve.Issues) != 1 || ve.Issues[0].Issue != issue {
		t.Fatalf("issues = %+v, want one issue %q", ve.Issues, issue)
	}
}

func TestInspectAcceptsEachFormat(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		ext         domain.ImageExt
		contentType string
		w, h        int
	}{
		{"jpeg", jpegBytes(t, 37, 21), domain.ImageExtJPG, "image/jpeg", 37, 21},
		{"png", pngBytes(t, 40, 30), domain.ImageExtPNG, "image/png", 40, 30},
		{"webp lossy", webpVP8(64, 48), domain.ImageExtWebP, "image/webp", 64, 48},
		{"webp lossless", webpVP8L(50, 60), domain.ImageExtWebP, "image/webp", 50, 60},
		{"webp extended", webpVP8X(320, 200), domain.ImageExtWebP, "image/webp", 320, 200},
		{"jpeg 1x1", jpegBytes(t, 1, 1), domain.ImageExtJPG, "image/jpeg", 1, 1},
		{"png 1x1", pngBytes(t, 1, 1), domain.ImageExtPNG, "image/png", 1, 1},
		{"webp lossy 1x1", webpVP8(1, 1), domain.ImageExtWebP, "image/webp", 1, 1},
		{"webp lossless 1x1", webpVP8L(1, 1), domain.ImageExtWebP, "image/webp", 1, 1},
		{"webp extended 1x1", webpVP8X(1, 1), domain.ImageExtWebP, "image/webp", 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(tc.data)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			want := Info{Ext: tc.ext, ContentType: tc.contentType, Width: tc.w, Height: tc.h, Size: int64(len(tc.data)), Hash: Hash(tc.data)}
			if info != want {
				t.Fatalf("Inspect = %+v, want %+v", info, want)
			}
			if len(info.Hash) != domain.ImageHashLen {
				t.Fatalf("hash %q is not %d characters", info.Hash, domain.ImageHashLen)
			}
		})
	}
}

func TestInspectDimensionLimit(t *testing.T) {
	max := domain.MaxImageDimension
	if max != 2000 {
		t.Fatalf("MaxImageDimension = %d, the spec says 2000", max)
	}
	formats := []struct {
		name string
		make func(w, h int) []byte
	}{
		{"jpeg", func(w, h int) []byte { return jpegBytes(t, w, h) }},
		{"png", func(w, h int) []byte { return pngBytes(t, w, h) }},
		{"webp lossy", webpVP8},
		{"webp lossless", webpVP8L},
		{"webp extended", webpVP8X},
	}
	sizes := []struct {
		w, h int
		ok   bool
	}{
		{max, max, true}, // exactly 2000 x 2000 is allowed
		{max, 1, true},
		{1, max, true},
		{max + 1, 1, false},
		{1, max + 1, false},
		{max + 1, max + 1, false},
		{max, max + 1, false},
	}
	for _, f := range formats {
		for _, s := range sizes {
			t.Run(fmt.Sprintf("%s %dx%d", f.name, s.w, s.h), func(t *testing.T) {
				info, err := Inspect(f.make(s.w, s.h))
				if s.ok {
					if err != nil {
						t.Fatalf("Inspect: %v", err)
					}
					if info.Width != s.w || info.Height != s.h {
						t.Fatalf("size = %dx%d, want %dx%d", info.Width, info.Height, s.w, s.h)
					}
					return
				}
				wantIssue(t, err, domain.IssueTooLargeDimensions)
			})
		}
	}
}

func TestInspectHugeCanvasIsTooLarge(t *testing.T) {
	// The largest sizes each WebP variant can express are rejected, not
	// overflowed or accepted.
	tests := map[string][]byte{
		"VP8 16383x16383":  webpVP8(1<<14-1, 1<<14-1),
		"VP8L 16384x16384": webpVP8L(1<<14, 1<<14),
		"VP8X 2^24 x 2^24": riffFile(riffChunk("VP8X", vp8xPayload(1<<24, 1<<24))),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(data)
			wantIssue(t, err, domain.IssueTooLargeDimensions)
		})
	}
}

func TestInspectWebPScaleBitsAreNotSize(t *testing.T) {
	data := riffFile(riffChunk("VP8 ", vp8Payload(100, 50, 3)))
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 100 || info.Height != 50 {
		t.Fatalf("size = %dx%d, want 100x50 (scale bits ignored)", info.Width, info.Height)
	}
}

func TestInspectWebPIgnoresBytesAfterRIFFAndOddChunkPadding(t *testing.T) {
	base := webpVP8(10, 10)
	if _, err := Inspect(append(bytes.Clone(base), "trailing junk"...)); err != nil {
		t.Fatalf("bytes after the RIFF payload must be ignored: %v", err)
	}
	// An odd-sized payload is padded to even; the sizes must still line up.
	odd := riffFile(riffChunk("VP8L", append(vp8lPayload(9, 9, 0), 0xAA)))
	if info, err := Inspect(odd); err != nil || info.Width != 9 {
		t.Fatalf("odd-sized chunk: %+v, %v", info, err)
	}
}

func TestInspectWrongTypes(t *testing.T) {
	wav := cat([]byte("RIFF"), le32(4+8+16), []byte("WAVE"), riffChunk("fmt ", make([]byte, 16)))
	tests := []struct {
		name string
		data []byte
	}{
		{"gif", gifBytes(t, 8, 8)},
		{"text", []byte("hello, this is not an image")},
		{"html", []byte("<html><body>image</body></html>")},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)},
		{"pdf", []byte("%PDF-1.7\n%....")},
		{"bmp", append([]byte("BM"), make([]byte, 60)...)},
		{"riff wav", wav},
		{"riff without webp tag", cat([]byte("RIFF"), le32(4), []byte("AVI "))},
		{"just RIFF", []byte("RIFF")},
		{"single byte", []byte{0xFF}},
		{"two jpeg bytes", []byte{0xFF, 0xD8}},
		{"seven png bytes", pngMagic[:7]},
		{"zeros", make([]byte, 100)},
		{"png magic offset by one", append([]byte{0}, pngMagic...)},
		{"exif header of a jpeg without SOI", []byte("Exif\x00\x00")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Inspect(tc.data)
			var ue *domain.UnsupportedMediaTypeError
			if !errors.As(err, &ue) || !errors.Is(err, domain.ErrUnsupportedMediaType) {
				t.Fatalf("error = %v (%T), want unsupported media type", err, err)
			}
		})
	}
}

func TestInspectEmptyIsBadRequest(t *testing.T) {
	for _, data := range [][]byte{nil, {}} {
		_, err := Inspect(data)
		var be *domain.BadRequestError
		if !errors.As(err, &be) || !errors.Is(err, domain.ErrBadRequest) {
			t.Fatalf("Inspect(%v) = %v (%T), want bad request", data, err, err)
		}
	}
}

func TestInspectTrustsBytesNotNames(t *testing.T) {
	// A PNG is a PNG whatever the caller calls it: the only input is the bytes.
	info, err := Inspect(pngBytes(t, 5, 5))
	if err != nil || info.Ext != domain.ImageExtPNG || info.ContentType != "image/png" {
		t.Fatalf("got %+v, %v", info, err)
	}
	// JPEG magic followed by PNG content is corrupt, not a PNG.
	_, err = Inspect(append([]byte{0xFF, 0xD8, 0xFF}, pngBytes(t, 5, 5)...))
	wantIssue(t, err, domain.IssueInvalidImage)
}

func TestInspectCorruptAndTruncated(t *testing.T) {
	jpg := jpegBytes(t, 20, 20)
	pn := pngBytes(t, 20, 20)

	corruptPNGChecksum := bytes.Clone(pn)
	corruptPNGChecksum[16] ^= 0xFF // a width byte inside IHDR: the CRC no longer matches

	zeroWidthPNG := bytes.Clone(pn)
	copy(zeroWidthPNG[16:20], []byte{0, 0, 0, 0})

	tests := []struct {
		name string
		data []byte
	}{
		{"jpeg magic and nothing else", jpg[:3]},
		{"jpeg cut in the first marker", jpg[:10]},
		{"jpeg magic then garbage", append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte{0x41}, 100)...)},
		{"jpeg magic then zeros", append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 100)...)},
		{"png signature only", pn[:8]},
		{"png cut inside IHDR", pn[:20]},
		{"png signature then garbage", append(bytes.Clone(pngMagic), bytes.Repeat([]byte{0x41}, 100)...)},
		{"png with bad IHDR checksum", corruptPNGChecksum},
		{"png with zero width", zeroWidthPNG},
		{"webp riff header only", riffFile()},
		{"webp with an unknown first chunk", riffFile(riffChunk("JUNK", make([]byte, 20)))},
		{"webp with an ALPH first chunk", riffFile(riffChunk("ALPH", make([]byte, 20)))},
		{"webp VP8 too short", riffFile(riffChunk("VP8 ", make([]byte, 5)))},
		{"webp VP8 not a key frame", riffFile(riffChunk("VP8 ", cat([]byte{0x11, 0, 0, 0x9D, 0x01, 0x2A}, le16(10), le16(10), make([]byte, 8))))},
		{"webp VP8 bad version", riffFile(riffChunk("VP8 ", cat([]byte{0x10 | 4<<1, 0, 0, 0x9D, 0x01, 0x2A}, le16(10), le16(10), make([]byte, 8))))},
		{"webp VP8 bad start code", riffFile(riffChunk("VP8 ", cat([]byte{0x10, 0, 0, 0x9D, 0x01, 0x2B}, le16(10), le16(10), make([]byte, 8))))},
		{"webp VP8 zero width", riffFile(riffChunk("VP8 ", vp8Payload(0, 10, 0)))},
		{"webp VP8 zero height", riffFile(riffChunk("VP8 ", vp8Payload(10, 0, 0)))},
		{"webp VP8L too short", riffFile(riffChunk("VP8L", []byte{0x2F, 1, 2}))},
		{"webp VP8L bad signature", riffFile(riffChunk("VP8L", cat([]byte{0x2E}, le32(0), make([]byte, 4))))},
		{"webp VP8L bad version", riffFile(riffChunk("VP8L", vp8lPayload(10, 10, 1)))},
		{"webp VP8X too short", riffFile(riffChunk("VP8X", make([]byte, 9)))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Inspect(tc.data)
			wantIssue(t, err, domain.IssueInvalidImage)
		})
	}
}

func TestInspectTruncatedWebPIsNeverAccepted(t *testing.T) {
	for name, full := range map[string][]byte{
		"VP8":  webpVP8(30, 20),
		"VP8L": webpVP8L(30, 20),
		"VP8X": webpVP8X(30, 20),
	} {
		if _, err := Inspect(full); err != nil {
			t.Fatalf("%s: intact file rejected: %v", name, err)
		}
		for n := 1; n < len(full); n++ {
			_, err := Inspect(full[:n])
			if err == nil {
				t.Fatalf("%s truncated to %d of %d bytes was accepted", name, n, len(full))
			}
			switch {
			case n < 12: // too short to even be recognised as WebP
				if !errors.Is(err, domain.ErrUnsupportedMediaType) {
					t.Fatalf("%s truncated to %d: %v, want unsupported media type", name, n, err)
				}
			default:
				wantIssue(t, err, domain.IssueInvalidImage)
			}
		}
	}
}

func TestInspectWebPRIFFSizeLies(t *testing.T) {
	good := webpVP8(30, 20)
	for name, size := range map[string]uint32{
		"larger than the file": uint32(len(good)),
		"much larger":          math.MaxUint32,
		"zero":                 0,
		"below one chunk":      11,
	} {
		data := bytes.Clone(good)
		copy(data[4:8], le32(size))
		if _, err := Inspect(data); err == nil {
			t.Fatalf("RIFF size %s was accepted", name)
		} else {
			wantIssue(t, err, domain.IssueInvalidImage)
		}
	}
	// The first chunk claiming more than the RIFF holds.
	data := bytes.Clone(good)
	copy(data[16:20], le32(1<<20))
	_, err := Inspect(data)
	wantIssue(t, err, domain.IssueInvalidImage)
}

func TestInspectSizeLimit(t *testing.T) {
	const max = domain.MaxImageBytes
	if max != 2<<20 {
		t.Fatalf("MaxImageBytes = %d, the spec says 2 MiB", max)
	}
	// Trailing bytes after the image are ignored by the header parsers, so a
	// valid image can be padded to any length without changing its meaning.
	for name, base := range map[string][]byte{
		"jpeg": jpegBytes(t, 10, 10),
		"png":  pngBytes(t, 10, 10),
		"webp": webpVP8(10, 10),
	} {
		t.Run(name, func(t *testing.T) {
			info, err := Inspect(padTo(base, max))
			if err != nil {
				t.Fatalf("exactly %d bytes must be accepted: %v", max, err)
			}
			if info.Size != max {
				t.Fatalf("Size = %d, want %d", info.Size, max)
			}
			_, err = Inspect(padTo(base, max+1))
			var pe *domain.PayloadTooLargeError
			if !errors.As(err, &pe) || !errors.Is(err, domain.ErrPayloadTooLarge) {
				t.Fatalf("%d bytes: %v (%T), want payload too large", max+1, err, err)
			}
		})
	}
	// The size check comes before sniffing: a huge non-image is a 413, not a 415.
	_, err := Inspect(make([]byte, max+1))
	if !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Fatalf("oversized zeros: %v, want payload too large", err)
	}
}

func TestHash(t *testing.T) {
	// SHA-256("abc") = ba7816bf 8f01cfea 414140de 5dae2223 ...
	if got := Hash([]byte("abc")); got != "ba7816bf8f01cfea" {
		t.Fatalf("Hash(abc) = %q, want ba7816bf8f01cfea", got)
	}
	// The empty input's digest starts e3b0c44298fc1c14.
	if got := Hash(nil); got != "e3b0c44298fc1c14" {
		t.Fatalf("Hash(nil) = %q", got)
	}
	a, b := jpegBytes(t, 12, 12), jpegBytes(t, 13, 12)
	if Hash(a) != Hash(bytes.Clone(a)) {
		t.Fatal("hash is not stable for identical bytes")
	}
	if Hash(a) == Hash(b) {
		t.Fatal("different images have the same hash")
	}
	// One flipped bit changes it.
	c := bytes.Clone(a)
	c[len(c)-1] ^= 1
	if Hash(a) == Hash(c) {
		t.Fatal("hash ignores the last byte")
	}
	// Inspect reports the same hash, always 16 lowercase hex characters.
	info, err := Inspect(a)
	if err != nil || info.Hash != Hash(a) {
		t.Fatalf("Inspect hash %q, %v", info.Hash, err)
	}
	if err := Validate(info.Hash, info.Ext); err != nil {
		t.Fatalf("Inspect's hash does not pass Validate: %v", err)
	}
}

func TestReadLimited(t *testing.T) {
	data := []byte("0123456789")

	t.Run("under the limit", func(t *testing.T) {
		got, err := ReadLimited(bytes.NewReader(data), 11)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("exactly the limit", func(t *testing.T) {
		got, err := ReadLimited(bytes.NewReader(data), 10)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("one byte over the limit", func(t *testing.T) {
		_, err := ReadLimited(bytes.NewReader(data), 9)
		wantTooLarge(t, err)
	})
	t.Run("far over the limit stops reading", func(t *testing.T) {
		r := &countingReader{r: iotest.OneByteReader(bytes.NewReader(make([]byte, 1<<20)))}
		_, err := ReadLimited(r, 100)
		wantTooLarge(t, err)
		if r.n > 101 {
			t.Fatalf("read %d bytes past a 100 byte limit", r.n)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		for _, r := range []io.Reader{bytes.NewReader(nil), strings.NewReader("")} {
			_, err := ReadLimited(r, 100)
			var be *domain.BadRequestError
			if !errors.As(err, &be) || !errors.Is(err, domain.ErrBadRequest) {
				t.Fatalf("error = %v (%T), want bad request", err, err)
			}
		}
	})
	t.Run("zero limit", func(t *testing.T) {
		_, err := ReadLimited(bytes.NewReader(data), 0)
		wantTooLarge(t, err)
		_, err = ReadLimited(bytes.NewReader(nil), 0)
		if !errors.Is(err, domain.ErrBadRequest) {
			t.Fatalf("empty with zero limit: %v", err)
		}
	})
	t.Run("negative limit behaves like zero", func(t *testing.T) {
		_, err := ReadLimited(bytes.NewReader(data), -5)
		wantTooLarge(t, err)
	})
	t.Run("maximum limit does not overflow", func(t *testing.T) {
		got, err := ReadLimited(bytes.NewReader(data), math.MaxInt64)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("one byte at a time", func(t *testing.T) {
		got, err := ReadLimited(iotest.OneByteReader(bytes.NewReader(data)), 10)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("data with an early EOF error", func(t *testing.T) {
		got, err := ReadLimited(iotest.DataErrReader(bytes.NewReader(data)), 10)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("read error is passed on, not mapped", func(t *testing.T) {
		boom := errors.New("connection reset")
		_, err := ReadLimited(io.MultiReader(bytes.NewReader(data[:3]), iotest.ErrReader(boom)), 10)
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want it to wrap %v", err, boom)
		}
		for _, sentinel := range []error{domain.ErrBadRequest, domain.ErrPayloadTooLarge} {
			if errors.Is(err, sentinel) {
				t.Fatalf("an I/O error must be an internal error, got %v", err)
			}
		}
	})
	t.Run("http.MaxBytesReader over its limit", func(t *testing.T) {
		body := http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(make([]byte, 50))), 10)
		_, err := ReadLimited(body, 10)
		wantTooLarge(t, err)
	})
	t.Run("http.MaxBytesReader within its limit", func(t *testing.T) {
		body := http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(data)), 10)
		got, err := ReadLimited(body, 10)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func wantTooLarge(t *testing.T, err error) {
	t.Helper()
	var pe *domain.PayloadTooLargeError
	if !errors.As(err, &pe) || !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Fatalf("error = %v (%T), want payload too large", err, err)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestReadLimitedThenInspectAtTheSizeBoundary(t *testing.T) {
	base := jpegBytes(t, 10, 10)
	ok, err := ReadLimited(bytes.NewReader(padTo(base, domain.MaxImageBytes)), domain.MaxImageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ok); err != nil {
		t.Fatal(err)
	}
	_, err = ReadLimited(bytes.NewReader(padTo(base, domain.MaxImageBytes+1)), domain.MaxImageBytes)
	wantTooLarge(t, err)
}

func FuzzInspect(f *testing.F) {
	for _, seed := range [][]byte{
		nil, {}, {0xFF, 0xD8, 0xFF}, pngMagic, []byte("RIFF\x00\x00\x00\x00WEBP"),
		jpegBytes(f, 8, 8), pngBytes(f, 8, 8), gifBytes(f, 4, 4),
		webpVP8(8, 8), webpVP8L(8, 8), webpVP8X(8, 8),
		webpVP8(1<<14-1, 1<<14-1), webpVP8X(1<<24, 1<<24),
		pngBytes(f, 8, 8)[:20], jpegBytes(f, 8, 8)[:12], webpVP8L(8, 8)[:25],
		[]byte("RIFF\xff\xff\xff\xffWEBPVP8 \xff\xff\xff\xff"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Inspect(data)
		if err != nil {
			ok := errors.Is(err, domain.ErrBadRequest) || errors.Is(err, domain.ErrPayloadTooLarge) ||
				errors.Is(err, domain.ErrUnsupportedMediaType) || errors.Is(err, domain.ErrValidation)
			if !ok {
				t.Fatalf("error %v (%T) is not a domain error", err, err)
			}
			return
		}
		if !info.Ext.IsValid() || info.ContentType != info.Ext.ContentType() {
			t.Fatalf("bad type in %+v", info)
		}
		if info.Width < 1 || info.Height < 1 || info.Width > domain.MaxImageDimension || info.Height > domain.MaxImageDimension {
			t.Fatalf("accepted out-of-range size in %+v", info)
		}
		if info.Size != int64(len(data)) || info.Size > domain.MaxImageBytes || Validate(info.Hash, info.Ext) != nil {
			t.Fatalf("inconsistent info %+v for %d bytes", info, len(data))
		}
	})
}
