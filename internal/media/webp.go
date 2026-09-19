package media

import "encoding/binary"

// WebP has no decoder in the standard library, so the canvas size is read from
// the header by hand. Layout (https://developers.google.com/speed/webp/docs/riff_container):
//
//	"RIFF" | file size - 8 (u32 LE) | "WEBP" | chunk...
//	chunk = FourCC | payload size (u32 LE) | payload | 1 pad byte if the size is odd
//
// The first chunk decides the format:
//
//	"VP8 "  lossy:    payload = 3-byte frame tag, 9D 01 2A, width (14 bits + 2 scale bits), height (same)
//	"VP8L"  lossless: payload = 0x2F, then 32 bits LE: (width-1) 14 | (height-1) 14 | alpha 1 | version 3
//	"VP8X"  extended: payload = flags (1), reserved (3), canvas width-1 (u24 LE), canvas height-1 (u24 LE)
//
// Only the size fields and the fixed markers are checked. The file must not be
// cut short: the RIFF size and the first chunk's size must fit in the data.
// Bytes after the RIFF payload are ignored, as decoders do.

const (
	riffHeaderLen  = 12 // "RIFF" + size + "WEBP"
	chunkHeaderLen = 8  // FourCC + size
)

// webpConfig returns the width and height of a WebP file whose first 12 bytes
// were already sniffed as RIFF....WEBP. Any structural problem is errCorrupt.
func webpConfig(data []byte) (width, height int, err error) {
	if len(data) < riffHeaderLen+chunkHeaderLen {
		return 0, 0, errCorrupt
	}
	riffSize := int64(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < riffHeaderLen-8+chunkHeaderLen || 8+riffSize > int64(len(data)) {
		return 0, 0, errCorrupt // too small to hold a chunk, or truncated
	}
	body := data[riffHeaderLen : 8+riffSize]

	fourCC := string(body[0:4])
	size := int64(binary.LittleEndian.Uint32(body[4:8]))
	payload := body[chunkHeaderLen:]
	if size > int64(len(payload)) {
		return 0, 0, errCorrupt
	}
	payload = payload[:size]

	switch fourCC {
	case "VP8 ":
		return vp8Size(payload)
	case "VP8L":
		return vp8lSize(payload)
	case "VP8X":
		return vp8xSize(payload)
	}
	return 0, 0, errCorrupt // a WebP starts with VP8, VP8L or VP8X
}

// vp8Size reads a lossy key frame header (RFC 6386 section 9.1).
func vp8Size(p []byte) (int, int, error) {
	if len(p) < 10 {
		return 0, 0, errCorrupt
	}
	tag := uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16
	isInterFrame := tag&1 != 0
	version := (tag >> 1) & 7
	if isInterFrame || version > 3 { // a still image is a key frame
		return 0, 0, errCorrupt
	}
	if p[3] != 0x9D || p[4] != 0x01 || p[5] != 0x2A {
		return 0, 0, errCorrupt // key frame start code
	}
	// The top 2 bits of each 16-bit field are an upscaling hint, not size.
	w := int(binary.LittleEndian.Uint16(p[6:8]) & 0x3FFF)
	h := int(binary.LittleEndian.Uint16(p[8:10]) & 0x3FFF)
	return w, h, nil
}

// vp8lSize reads a lossless header.
func vp8lSize(p []byte) (int, int, error) {
	if len(p) < 5 || p[0] != 0x2F {
		return 0, 0, errCorrupt
	}
	bits := binary.LittleEndian.Uint32(p[1:5])
	if bits>>29 != 0 { // version must be 0
		return 0, 0, errCorrupt
	}
	w := int(bits&0x3FFF) + 1
	h := int((bits>>14)&0x3FFF) + 1
	return w, h, nil
}

// vp8xSize reads the canvas size of an extended file (also used for animated
// files; the canvas is the image size seen by the client).
func vp8xSize(p []byte) (int, int, error) {
	if len(p) < 10 {
		return 0, 0, errCorrupt
	}
	w := int(uint32(p[4])|uint32(p[5])<<8|uint32(p[6])<<16) + 1
	h := int(uint32(p[7])|uint32(p[8])<<8|uint32(p[9])<<16) + 1
	return w, h, nil
}
