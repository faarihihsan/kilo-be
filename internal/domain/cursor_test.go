package domain

import (
	"encoding/base64"
	"errors"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var base64URLNoPad = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func mustUUID(t testing.TB, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad test uuid %q: %v", s, err)
	}
	return id
}

// rawCursor builds a cursor string from raw (pre-base64) bytes.
func rawCursor(b ...byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func wantInvalidCursor(t *testing.T, err error) {
	t.Helper()
	var bre *BadRequestError
	if err == nil {
		t.Fatal("got nil error, want an invalid cursor error")
	}
	if !errors.As(err, &bre) || !errors.Is(err, ErrBadRequest) {
		t.Fatalf("error = %v (%T), want *BadRequestError", err, err)
	}
	if bre.Message != invalidCursorMessage {
		t.Fatalf("message = %q, want %q", bre.Message, invalidCursorMessage)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := mustUUID(t, "0195f3a2-cccc-7000-8000-000000000100")
	maxUUID := mustUUID(t, "ffffffff-ffff-ffff-ffff-ffffffffffff")

	cases := []struct {
		name string
		at   time.Time
	}{
		{"typical", time.Date(2026, 9, 19, 8, 55, 4, 123456000, time.UTC)},
		{"unix epoch", time.Unix(0, 0).UTC()},
		{"1 µs after epoch", time.Unix(0, 1000).UTC()},
		{"1 µs before epoch", time.Unix(0, -1000).UTC()},
		{"whole second", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"999999 µs", time.Date(2026, 1, 1, 0, 0, 0, 999_999_000, time.UTC)},
		{"000001 µs", time.Date(2026, 1, 1, 0, 0, 0, 1000, time.UTC)},
		{"zero time", time.Time{}},
		{"year 1 plus 1 µs", time.Time{}.Add(time.Microsecond)},
		{"year 1000", time.Date(1000, 6, 15, 12, 0, 0, 1000, time.UTC)},
		{"year 1900", time.Date(1900, 1, 1, 0, 0, 0, 999_999_000, time.UTC)},
		{"year 2262", time.Date(2262, 4, 12, 0, 0, 0, 0, time.UTC)}, // past UnixNano's range
		{"year 5000", time.Date(5000, 1, 1, 0, 0, 0, 1000, time.UTC)},
		{"last accepted instant", time.Date(9999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)},
		{"other zone", time.Date(2026, 9, 19, 15, 55, 4, 5000, time.FixedZone("UTC+7", 7*3600))},
	}
	for _, tc := range cases {
		for _, cid := range []uuid.UUID{id, uuid.Nil, maxUUID} {
			t.Run(tc.name+"/"+cid.String()[:8], func(t *testing.T) {
				in := Cursor{At: tc.at, ID: cid}
				enc := EncodeCursor(in)
				got, err := DecodeCursor(enc)
				if err != nil {
					t.Fatalf("DecodeCursor(%q): %v", enc, err)
				}
				if !got.At.Equal(tc.at) {
					t.Fatalf("At = %v (%d µs), want %v (%d µs)", got.At, got.At.UnixMicro(), tc.at, tc.at.UnixMicro())
				}
				if got.At.Location() != time.UTC {
					t.Fatalf("At location = %v, want UTC", got.At.Location())
				}
				if got.ID != cid {
					t.Fatalf("ID = %v, want %v", got.ID, cid)
				}
				if again := EncodeCursor(got); again != enc {
					t.Fatalf("re-encode = %q, want %q", again, enc)
				}
			})
		}
	}
}

func TestCursorRoundTripRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test data
	lo, hi := minCursorMicro, maxCursorMicro
	for i := 0; i < 5000; i++ {
		var us int64
		switch i % 4 {
		case 0: // anywhere in the accepted range
			us = lo + rng.Int63n(hi-lo+1)
		case 1: // around now
			us = time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC).UnixMicro() + rng.Int63n(1e12) - 5e11
		case 2: // sub-second edges
			us = time.Date(2000+rng.Intn(100), time.Month(1+rng.Intn(12)), 1+rng.Intn(28), rng.Intn(24), rng.Intn(60), rng.Intn(60), 0, time.UTC).UnixMicro()
			us += []int64{0, 1, 999_999, 500_000}[rng.Intn(4)]
		default: // the extremes
			us = []int64{lo, lo + 1, hi, hi - 1, 0, 1, -1}[rng.Intn(7)]
		}
		var id uuid.UUID
		rng.Read(id[:])
		want := time.UnixMicro(us).UTC()

		// Add sub-µs noise: it must be dropped by flooring, never carried.
		noisy := want.Add(time.Duration(rng.Intn(1000)) * time.Nanosecond)

		enc := EncodeCursor(Cursor{At: noisy, ID: id})
		if !base64URLNoPad.MatchString(enc) || len(enc) > 80 {
			t.Fatalf("encoded cursor %q is not compact base64url", enc)
		}
		got, err := DecodeCursor(enc)
		if err != nil {
			t.Fatalf("DecodeCursor(%q) for %d µs: %v", enc, us, err)
		}
		if got.At.UnixMicro() != us || !got.At.Equal(want) || got.ID != id {
			t.Fatalf("round trip of %d µs / %v gave %d µs / %v", us, id, got.At.UnixMicro(), got.ID)
		}
	}
}

func TestCursorEncodingIsDeterministicAndOpaque(t *testing.T) {
	c := Cursor{At: time.Date(2026, 9, 19, 8, 55, 4, 123456000, time.UTC), ID: mustUUID(t, "0195f3a2-cccc-7000-8000-000000000100")}
	enc := EncodeCursor(c)
	if enc != EncodeCursor(c) {
		t.Fatal("encoding is not deterministic")
	}
	if enc != EncodeCursor(Cursor{At: c.At.In(time.FixedZone("x", 3600)), ID: c.ID}) {
		t.Fatal("encoding depends on the location of At")
	}
	if !base64URLNoPad.MatchString(enc) || strings.ContainsAny(enc, "=+/") {
		t.Fatalf("cursor %q is not base64url without padding", enc)
	}
	if len(enc) != 72 {
		t.Fatalf("len(cursor) = %d, want 72 for a current-era timestamp", len(enc))
	}
	// Stable across releases: this exact string is what the current encoder
	// produces, so a format change is a conscious decision, not an accident.
	const golden = "EDE3ODk4MDgxMDQxMjM0NTYkMDE5NWYzYTItY2NjYy03MDAwLTgwMDAtMDAwMDAwMDAwMTAw"
	if enc != golden {
		t.Fatalf("cursor format changed: got %q, want %q", enc, golden)
	}
	// Nothing readable leaks the layout: it is not plain base64 of a
	// human-readable "time|id" string.
	if raw, err := base64.RawURLEncoding.DecodeString(enc); err != nil || strings.Contains(string(raw), "|") {
		t.Fatalf("unexpected raw form %q (%v)", raw, err)
	}
}

func TestEncodeCursorFloorsBelowMicrosecond(t *testing.T) {
	id := uuid.MustParse("0195f3a2-cccc-7000-8000-000000000100")
	base := time.Date(2026, 9, 19, 8, 0, 0, 5000, time.UTC)
	if EncodeCursor(Cursor{At: base.Add(999 * time.Nanosecond), ID: id}) != EncodeCursor(Cursor{At: base, ID: id}) {
		t.Fatal("999 ns above a µs must encode like the µs")
	}
	// Before the epoch the floor goes down, like time.Truncate and Postgres.
	pre := time.Unix(-1, 999_999_999).UTC()
	got, err := DecodeCursor(EncodeCursor(Cursor{At: pre, ID: id}))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(-1, 999_999_000).UTC(); !got.At.Equal(want) {
		t.Fatalf("pre-epoch At = %v, want %v", got.At, want)
	}
}

func TestDecodeCursorMalformed(t *testing.T) {
	goodID := "0195f3a2-cccc-7000-8000-000000000100"
	good := EncodeKeyCursor("1758268504123456", goodID)
	if _, err := DecodeCursor(good); err != nil {
		t.Fatalf("control cursor must decode: %v", err)
	}

	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace", " "},
		{"newline", "\n"},
		{"not base64", "not a cursor!"},
		{"bad alphabet", "@@@@"},
		{"standard alphabet plus", "ab+d"},
		{"standard alphabet slash", "ab/d"},
		{"padded", good + "=="},
		{"leading space", " " + good},
		{"trailing space", good + " "},
		{"trailing newline", good + "\n"},
		{"nul byte", good + "\x00"},
		{"unicode", "ñandú"},
		{"one char", "A"},
		{"truncated", good[:len(good)-1]},
		{"truncated a lot", good[:10]},
		{"extra char", good + "A"},
		{"non-canonical trailing bits", nonCanonicalTrailingBits(good)},

		{"one part", EncodeKeyCursor("1758268504123456")},
		{"three parts", EncodeKeyCursor("1758268504123456", goodID, "x")},
		{"zero parts", EncodeKeyCursor()},
		{"empty parts", EncodeKeyCursor("", "")},
		{"trailing bytes after two parts", rawCursor(append(rawBytes(t, good), 0)...)},
		{"length longer than data", rawCursor(5, 'a', 'b')},
		{"length overflows int", rawCursor(0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 'a')},
		{"varint too long", rawCursor(0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01)},
		{"non-minimal varint", rawCursor(0x81, 0x00, 'a', 0x00)},
		{"truncated varint", rawCursor(0x80)},

		{"time not a number", EncodeKeyCursor("abc", goodID)},
		{"time empty", EncodeKeyCursor("", goodID)},
		{"time float", EncodeKeyCursor("1.5e15", goodID)},
		{"time decimal point", EncodeKeyCursor("1758268504.123456", goodID)},
		{"time plus sign", EncodeKeyCursor("+1758268504123456", goodID)},
		{"time leading zero", EncodeKeyCursor("01758268504123456", goodID)},
		{"time negative zero", EncodeKeyCursor("-0", goodID)},
		{"time spaces", EncodeKeyCursor(" 1758268504123456", goodID)},
		{"time hex", EncodeKeyCursor("0x10", goodID)},
		{"time overflows int64", EncodeKeyCursor("9223372036854775808", goodID)},
		{"time min int64 minus one", EncodeKeyCursor("-9223372036854775809", goodID)},
		{"time max int64", EncodeKeyCursor("9223372036854775807", goodID)},
		{"time min int64", EncodeKeyCursor("-9223372036854775808", goodID)},
		{"time below year 1", EncodeKeyCursor(itoa(minCursorMicro-1), goodID)},
		{"time above year 9999", EncodeKeyCursor(itoa(maxCursorMicro+1), goodID)},
		{"time year 300000", EncodeKeyCursor("9000000000000000000", goodID)},

		{"uuid empty", EncodeKeyCursor("1758268504123456", "")},
		{"uuid short", EncodeKeyCursor("1758268504123456", "0195f3a2-cccc")},
		{"uuid no hyphens", EncodeKeyCursor("1758268504123456", strings.ReplaceAll(goodID, "-", ""))},
		{"uuid braces", EncodeKeyCursor("1758268504123456", "{"+goodID+"}")},
		{"uuid urn", EncodeKeyCursor("1758268504123456", "urn:uuid:"+goodID)},
		{"uuid uppercase", EncodeKeyCursor("1758268504123456", strings.ToUpper(goodID))},
		{"uuid not hex", EncodeKeyCursor("1758268504123456", "zzzzzzzz-cccc-7000-8000-000000000100")},
		{"uuid too long", EncodeKeyCursor("1758268504123456", goodID+"0")},
		{"uuid with nul", EncodeKeyCursor("1758268504123456", goodID+"\x00")},

		{"huge, valid alphabet", strings.Repeat("A", MaxCursorLen+1)},
		{"1 MiB of A", strings.Repeat("A", 1<<20)},
		{"1 MiB of a valid-looking cursor", strings.Repeat(good, (1<<20)/len(good))},
		{"just over the limit", EncodeKeyCursor(strings.Repeat("x", 800), goodID)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := DecodeCursor(tc.in)
			wantInvalidCursor(t, err)
			if c != (Cursor{}) {
				t.Fatalf("cursor on error = %+v, want zero", c)
			}
		})
	}
}

func rawBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// nonCanonicalTrailingBits returns a string that base64-decodes to the same
// bytes as s in lenient mode but has different (non-zero) padding bits.
func nonCanonicalTrailingBits(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	rem := len(s) % 4 // characters in the last group: 2 (4 spare bits) or 3 (2 spare bits)
	if rem != 2 && rem != 3 {
		return s + "A" // not applicable: still an invalid cursor
	}
	last := strings.IndexByte(alphabet, s[len(s)-1])
	spare := 4
	if rem == 3 {
		spare = 2
	}
	return s[:len(s)-1] + string(alphabet[last|1<<(spare-1)])
}

func TestDecodeCursorRejectsTampering(t *testing.T) {
	// Changing any single character must either be rejected or yield a
	// different cursor that is itself canonical. Never a panic, never a
	// non-canonical acceptance.
	c := Cursor{At: time.Date(2026, 9, 19, 8, 55, 4, 123456000, time.UTC), ID: mustUUID(t, "0195f3a2-cccc-7000-8000-000000000100")}
	enc := EncodeCursor(c)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	accepted, rejected := 0, 0
	for i := 0; i < len(enc); i++ {
		for _, r := range alphabet {
			if byte(r) == enc[i] {
				continue
			}
			mut := enc[:i] + string(r) + enc[i+1:]
			got, err := DecodeCursor(mut)
			if err != nil {
				wantInvalidCursor(t, err)
				rejected++
				continue
			}
			accepted++
			if EncodeCursor(got) != mut {
				t.Fatalf("accepted non-canonical cursor %q", mut)
			}
			if got == c {
				t.Fatalf("mutation %q decoded to the original cursor", mut)
			}
		}
	}
	if rejected == 0 {
		t.Fatal("no mutation was rejected; strictness is not working")
	}
	t.Logf("single-character mutations: %d rejected, %d decoded to another canonical cursor", rejected, accepted)
}

func TestDecodeCursorNeverPanicsOnRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(2)) //nolint:gosec // deterministic test data
	for i := 0; i < 20000; i++ {
		b := make([]byte, rng.Intn(100))
		rng.Read(b)
		// Both raw garbage and garbage that is valid base64url.
		for _, s := range []string{string(b), base64.RawURLEncoding.EncodeToString(b)} {
			if c, err := DecodeCursor(s); err == nil && EncodeCursor(c) != s {
				t.Fatalf("accepted non-canonical %q", s)
			}
		}
	}
}

func TestKeyCursorRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
	}{
		{"one part", []string{"bench press"}},
		{"name and id", []string{"bench press", "0195f3a2-bbbb-7000-8000-000000000010"}},
		{"three parts", []string{"a", "b", "c"}},
		{"empty parts", []string{"", "", ""}},
		{"empty and non-empty", []string{"", "x", ""}},
		{"unicode", []string{"Übung ✓ 練習", "🏋️"}},
		{"nul and control bytes", []string{"a\x00b", "\n\t\r"}},
		{"invalid utf-8", []string{"\xff\xfe\xfd", "\xc3\x28"}},
		{"separator lookalikes", []string{"a|b", "c,d", "e:f", "="}},
		{"long part", []string{strings.Repeat("é", 200), "id"}},
		{"127 and 128 bytes (varint boundary)", []string{strings.Repeat("a", 127), strings.Repeat("b", 128)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enc := EncodeKeyCursor(tc.parts...)
			if !base64URLNoPad.MatchString(enc) {
				t.Fatalf("cursor %q is not base64url without padding", enc)
			}
			if enc != EncodeKeyCursor(tc.parts...) {
				t.Fatal("not deterministic")
			}
			got, err := DecodeKeyCursor(enc, len(tc.parts))
			if err != nil {
				t.Fatalf("DecodeKeyCursor: %v", err)
			}
			if len(got) != len(tc.parts) {
				t.Fatalf("got %d parts, want %d", len(got), len(tc.parts))
			}
			for i := range got {
				if got[i] != tc.parts[i] {
					t.Fatalf("part %d = %q, want %q", i, got[i], tc.parts[i])
				}
			}
			// A different arity never decodes.
			for _, n := range []int{len(tc.parts) - 1, len(tc.parts) + 1, len(tc.parts) + 5} {
				if _, err := DecodeKeyCursor(enc, n); err == nil {
					t.Fatalf("decoded as %d parts, want error", n)
				} else {
					wantInvalidCursor(t, err)
				}
			}
		})
	}
}

func TestKeyCursorSplitsAreUnambiguous(t *testing.T) {
	// ("ab","c") and ("a","bc") must not collide: lengths are part of the
	// encoding, unlike a naive join.
	a := EncodeKeyCursor("ab", "c")
	b := EncodeKeyCursor("a", "bc")
	if a == b {
		t.Fatal("distinct tuples encode identically")
	}
	if got, err := DecodeKeyCursor(a, 2); err != nil || got[0] != "ab" || got[1] != "c" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDecodeKeyCursorBadArguments(t *testing.T) {
	enc := EncodeKeyCursor("a", "b")
	for _, n := range []int{0, -1, math.MinInt, math.MaxInt, 1000} {
		if _, err := DecodeKeyCursor(enc, n); err == nil {
			t.Fatalf("n = %d: want error", n)
		} else {
			wantInvalidCursor(t, err)
		}
	}
	if _, err := DecodeKeyCursor("", 1); err == nil {
		t.Fatal("empty cursor must be rejected")
	}
	if _, err := DecodeKeyCursor(strings.Repeat("A", MaxCursorLen+1), 1); err == nil {
		t.Fatal("oversized cursor must be rejected")
	}
}

func TestKeyCursorSizeLimit(t *testing.T) {
	// The longest plan-name key still round-trips: 100 four-byte runes + a uuid.
	name := strings.Repeat("😀", 100)
	id := "0195f3a2-bbbb-7000-8000-000000000010"
	enc := EncodeKeyCursor(name, id)
	if len(enc) > MaxCursorLen {
		t.Fatalf("worst-case plan cursor is %d chars, over MaxCursorLen %d", len(enc), MaxCursorLen)
	}
	got, err := DecodeKeyCursor(enc, 2)
	if err != nil || got[0] != name || got[1] != id {
		t.Fatalf("worst-case plan cursor did not round trip: %v", err)
	}
}

func TestDecodeKeyCursorDoesNotAllocateFromLengthField(t *testing.T) {
	// A tiny cursor claiming a gigantic part must fail fast, not allocate.
	s := rawCursor(0xff, 0xff, 0xff, 0xff, 0x0f) // length 4 GiB - 1
	if _, err := DecodeKeyCursor(s, 1); err == nil {
		t.Fatal("want error")
	}
	if got := testing.AllocsPerRun(20, func() { _, _ = DecodeKeyCursor(s, 1) }); got > 20 {
		t.Fatalf("allocs = %v, want a handful", got)
	}
}

func FuzzDecodeCursor(f *testing.F) {
	good := EncodeCursor(Cursor{At: time.Date(2026, 9, 19, 8, 55, 4, 123456000, time.UTC), ID: uuid.MustParse("0195f3a2-cccc-7000-8000-000000000100")})
	seeds := []string{
		"", "A", "=", "!!!", " ", "\x00", good, good + "=", good[:len(good)-1], good + "A",
		strings.ToUpper(good), strings.Repeat("A", 2000),
		EncodeKeyCursor("1758268504123456", "0195f3a2-cccc-7000-8000-000000000100"),
		EncodeKeyCursor("1", "x"), EncodeKeyCursor("a"), EncodeKeyCursor("a", "b", "c"),
		EncodeCursor(Cursor{}), EncodeCursor(Cursor{At: time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC), ID: uuid.Max}),
		EncodeKeyCursor("9223372036854775807", "0195f3a2-cccc-7000-8000-000000000100"),
		rawCursor(0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := DecodeCursor(s)
		if err != nil {
			if !errors.Is(err, ErrBadRequest) {
				t.Fatalf("error %v (%T) is not a bad request", err, err)
			}
			if c != (Cursor{}) {
				t.Fatalf("non-zero cursor %+v with error", c)
			}
			return
		}
		// Anything accepted is canonical, in range, and round-trips exactly.
		if enc := EncodeCursor(c); enc != s {
			t.Fatalf("accepted %q but it re-encodes to %q", s, enc)
		}
		if us := c.At.UnixMicro(); us < minCursorMicro || us > maxCursorMicro {
			t.Fatalf("accepted out-of-range time %v", c.At)
		}
		if c.At.Nanosecond()%1000 != 0 || c.At.Location() != time.UTC {
			t.Fatalf("At = %v is not a UTC µs instant", c.At)
		}
		if len(s) > MaxCursorLen {
			t.Fatalf("accepted %d chars, over MaxCursorLen", len(s))
		}
	})
}

func FuzzDecodeKeyCursor(f *testing.F) {
	f.Add(EncodeKeyCursor("a", "b"), 2)
	f.Add(EncodeKeyCursor("", ""), 2)
	f.Add(EncodeKeyCursor("x"), 1)
	f.Add("", 1)
	f.Add("AAAA", 0)
	f.Add(rawCursor(0x80, 0x00), 1)
	f.Add(strings.Repeat("A", 3000), math.MaxInt)
	f.Fuzz(func(t *testing.T, s string, n int) {
		parts, err := DecodeKeyCursor(s, n)
		if err != nil {
			if !errors.Is(err, ErrBadRequest) || parts != nil {
				t.Fatalf("bad error result: %v %v", err, parts)
			}
			return
		}
		if len(parts) != n || EncodeKeyCursor(parts...) != s {
			t.Fatalf("accepted %q as %d parts %q but it does not round trip", s, n, parts)
		}
	})
}
