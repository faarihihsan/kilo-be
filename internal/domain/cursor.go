package domain

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Keyset cursors for the list endpoints (docs/api/conventions.md#pagination).
//
// A cursor is the sort key of the last row of a page, so the next page is
// `WHERE (key1, key2) > ($1, $2) ORDER BY key1, key2 LIMIT n`. It is opaque to
// clients: base64url without padding of a compact binary encoding. It is NOT
// signed or encrypted; tampering only moves the position, and every list query
// is already scoped to the caller, so a forged cursor cannot expose anything.
// It is tied to the sort order and filters it was issued for (spec 05 notes);
// nothing here checks that.
//
// A malformed cursor is a 400 bad_request (specs 05, 06 and 08), so decoding
// returns a *BadRequestError. All decoders are strict: exactly one string is
// accepted for a given value (canonical form), and any garbage, trailing bytes,
// tampering that breaks the structure, or oversized input is rejected. They
// never panic.

// MaxCursorLen is the longest cursor string the decoders accept. Encoders do
// not enforce it; a cursor that exceeds it (parts totalling about 750 bytes)
// could not be decoded again. The (At, ID) cursor is 72 characters and
// a plan name (100 characters, up to 400 bytes) still fits.
const MaxCursorLen = 1024

// invalidCursorMessage is the client-visible text of the bad-cursor error.
const invalidCursorMessage = "invalid cursor"

func errInvalidCursor() error { return NewBadRequest(invalidCursorMessage) }

// EncodeKeyCursor encodes an ordered tuple of opaque strings (any bytes,
// including empty ones) as a cursor. Wire format before base64: for each part,
// its byte length as an unsigned varint followed by the bytes. The number of
// parts is not stored; the decoder is told how many to expect.
func EncodeKeyCursor(parts ...string) string {
	size := 0
	for _, p := range parts {
		size += binary.MaxVarintLen64 + len(p)
	}
	buf := make([]byte, 0, size)
	for _, p := range parts {
		buf = binary.AppendUvarint(buf, uint64(len(p)))
		buf = append(buf, p...)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// DecodeKeyCursor is the strict inverse of EncodeKeyCursor: it returns exactly
// n parts (n >= 1), or a *BadRequestError when s is not the canonical encoding
// of n parts.
func DecodeKeyCursor(s string, n int) ([]string, error) {
	if n < 1 || s == "" || len(s) > MaxCursorLen {
		return nil, errInvalidCursor()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || n > len(raw) { // every part needs at least its length byte
		return nil, errInvalidCursor()
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, w := binary.Uvarint(raw)
		if w <= 0 || l > uint64(len(raw)-w) {
			return nil, errInvalidCursor()
		}
		raw = raw[w:]
		parts = append(parts, string(raw[:l]))
		raw = raw[l:]
	}
	if len(raw) != 0 {
		return nil, errInvalidCursor()
	}
	// Reject non-canonical forms (for example a varint with redundant
	// continuation bytes) so that decode(s) == parts implies encode(parts) == s.
	if EncodeKeyCursor(parts...) != s {
		return nil, errInvalidCursor()
	}
	return parts, nil
}

// Cursor is the keyset position (At, ID) of the sync feeds and any other list
// ordered by a timestamp with the id as tie-break: (server_updated_at, id) for
// updated_since pulls, (started_at, id) for progress, (updated_at, id) for
// exercises.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// The range of At that DecodeCursor accepts: years 1 to 9999. It keeps a forged
// cursor from carrying a value PostgreSQL would reject (which would surface as
// a 500) and covers time.Time{}.
var (
	minCursorMicro = time.Time{}.UnixMicro()
	maxCursorMicro = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC).UnixMicro()
)

// EncodeCursor returns the opaque cursor for c. At is floored to the
// microsecond (PostgreSQL's precision) and encoded as an integer count of
// microseconds since the Unix epoch, so it round-trips exactly and never goes
// through a float. It is an instant: the location of At is irrelevant. An At
// outside years 1-9999 encodes to a cursor that DecodeCursor rejects.
func EncodeCursor(c Cursor) string {
	return EncodeKeyCursor(strconv.FormatInt(c.At.UnixMicro(), 10), c.ID.String())
}

// DecodeCursor is the strict inverse of EncodeCursor. The returned At is in UTC
// with microsecond precision. Any invalid input returns a *BadRequestError.
func DecodeCursor(s string) (Cursor, error) {
	parts, err := DecodeKeyCursor(s, 2)
	if err != nil {
		return Cursor{}, err
	}
	us, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || us < minCursorMicro || us > maxCursorMicro {
		return Cursor{}, errInvalidCursor()
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return Cursor{}, errInvalidCursor()
	}
	c := Cursor{At: time.UnixMicro(us).UTC(), ID: id}
	// Canonical form only: no "+5", "007", uppercase or braced uuids.
	if EncodeCursor(c) != s {
		return Cursor{}, errInvalidCursor()
	}
	return c, nil
}
