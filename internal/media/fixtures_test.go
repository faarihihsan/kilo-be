package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// Real JPEG and PNG files come from the image encoders; WebP has no encoder in
// the standard library, so the minimal RIFF containers are built by hand.

func jpegBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, grayImage(w, h), &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, grayImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gifBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	p := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	if err := gif.Encode(&buf, p, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func grayImage(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	// A little variation so the file is not degenerate; the rest stays 0.
	for i := 0; i < w && i < 64; i++ {
		img.SetGray(i, 0, color.Gray{Y: uint8(i * 4)})
	}
	return img
}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le24(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16)} }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// riffChunk is FourCC + size + payload, padded to an even length.
func riffChunk(fourCC string, payload []byte) []byte {
	b := cat([]byte(fourCC), le32(uint32(len(payload))), payload)
	if len(payload)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// riffFile wraps chunks in "RIFF" size "WEBP".
func riffFile(chunks ...[]byte) []byte {
	body := cat(append([][]byte{[]byte("WEBP")}, chunks...)...)
	return cat([]byte("RIFF"), le32(uint32(len(body))), body)
}

// vp8Payload is a lossy key frame header for a w x h image. scaleBits go in the
// top 2 bits of both size fields.
func vp8Payload(w, h int, scaleBits uint16) []byte {
	tag := []byte{0x10, 0x00, 0x00} // key frame (bit 0 = 0), version 0, show_frame = 1
	return cat(tag, []byte{0x9D, 0x01, 0x2A}, le16(uint16(w)|scaleBits<<14), le16(uint16(h)|scaleBits<<14), make([]byte, 12))
}

// vp8lPayload is a lossless header for a w x h image.
func vp8lPayload(w, h int, version uint32) []byte {
	bits := uint32(w-1) | uint32(h-1)<<14 | 1<<28 | version<<29
	return cat([]byte{0x2F}, le32(bits), make([]byte, 8))
}

// vp8xPayload is an extended header with a w x h canvas.
func vp8xPayload(w, h int) []byte {
	return cat([]byte{0x10, 0, 0, 0}, le24(uint32(w-1)), le24(uint32(h-1)))
}

func webpVP8(w, h int) []byte  { return riffFile(riffChunk("VP8 ", vp8Payload(w, h, 0))) }
func webpVP8L(w, h int) []byte { return riffFile(riffChunk("VP8L", vp8lPayload(w, h, 0))) }

// webpVP8X is what encoders write for a WebP with metadata or alpha: a VP8X
// header chunk with the canvas size, then the image chunk.
func webpVP8X(w, h int) []byte {
	return riffFile(riffChunk("VP8X", vp8xPayload(w, h)), riffChunk("VP8L", vp8lPayload(w, h, 0)))
}

// padTo returns data extended with zero bytes to exactly n bytes.
func padTo(data []byte, n int) []byte {
	return append(bytes.Clone(data), make([]byte, n-len(data))...)
}
