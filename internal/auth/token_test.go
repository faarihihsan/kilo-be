package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/testutil"
)

func TestNewTokenFormat(t *testing.T) {
	raw, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(raw, domain.TokenPrefix) {
		t.Errorf("token %q lacks prefix %q", raw, domain.TokenPrefix)
	}
	if len(raw) != TokenLen || TokenLen != 46 {
		t.Errorf("len = %d, TokenLen = %d; want 46 (wt_ + 43 chars)", len(raw), TokenLen)
	}
	body := strings.TrimPrefix(raw, domain.TokenPrefix)
	secret, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(secret) != domain.TokenRandomBytes {
		t.Errorf("body is not %d bytes of unpadded base64url: %d bytes, %v", domain.TokenRandomBytes, len(secret), err)
	}
	if strings.ContainsAny(raw, "=+/") {
		t.Errorf("token %q is not URL-safe unpadded base64", raw)
	}
	if want := sha256.Sum256([]byte(raw)); !bytes.Equal(hash, want[:]) {
		t.Error("returned hash is not the SHA-256 of the full token string")
	}
	if got, ok := ParseBearer("Bearer " + raw); !ok || got != raw {
		t.Error("ParseBearer rejects a token from NewToken")
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	const n = 20000
	raws := make(map[string]struct{}, n)
	hashes := make(map[string]struct{}, n)
	for range n {
		raw, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		raws[raw] = struct{}{}
		hashes[string(hash)] = struct{}{}
	}
	if len(raws) != n || len(hashes) != n {
		t.Errorf("%d unique tokens and %d unique hashes out of %d", len(raws), len(hashes), n)
	}
}

// The hash stored by NewToken must be the one the test seeds store, or the
// authenticator could not find tokens made either way.
func TestHashTokenMatchesTestutil(t *testing.T) {
	for _, raw := range []string{"wt_abc", "", domain.TokenPrefix + strings.Repeat("A", 43)} {
		if !bytes.Equal(HashToken(raw), testutil.HashToken(raw)) {
			t.Errorf("HashToken(%q) differs from testutil.HashToken", raw)
		}
	}
}

func TestParseBearer(t *testing.T) {
	valid, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(valid, domain.TokenPrefix)

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"valid", "Bearer " + valid, true},
		{"lowercase scheme", "bearer " + valid, true},
		{"uppercase scheme", "BEARER " + valid, true},
		{"mixed case scheme", "bEaReR " + valid, true},

		{"empty header", "", false},
		{"scheme only", "Bearer", false},
		{"scheme and space only", "Bearer ", false},
		{"token only", valid, false},
		{"basic auth", "Basic dXNlcjpwYXNz", false},
		{"basic with a valid-looking token", "Basic " + valid, false},
		{"token scheme", "Token " + valid, false},
		{"scheme prefix", "Bearerx " + valid, false},
		{"two spaces", "Bearer  " + valid, false},
		{"tab separator", "Bearer\t" + valid, false},
		{"leading space", " Bearer " + valid, false},
		{"trailing space", "Bearer " + valid + " ", false},
		{"trailing newline", "Bearer " + valid + "\n", false},
		{"trailing CR", "Bearer " + valid + "\r", false},
		{"two tokens", "Bearer " + valid + " " + valid, false},
		{"comma list", "Bearer " + valid + ",Bearer " + valid, false},
		{"wrong prefix", "Bearer xx_" + body, false},
		{"no prefix", "Bearer " + body + "abc", false},
		{"uppercase prefix", "Bearer WT_" + body, false},
		{"prefix only", "Bearer wt_", false},
		{"too short", "Bearer " + valid[:len(valid)-1], false},
		{"too long by one", "Bearer " + valid + "A", false},
		{"way too long", "Bearer " + valid + strings.Repeat("A", 100_000), false},
		{"padded", "Bearer " + valid[:len(valid)-1] + "=", false},
		{"standard base64 plus", "Bearer " + valid[:10] + "+" + valid[11:], false},
		{"standard base64 slash", "Bearer " + valid[:10] + "/" + valid[11:], false},
		{"non ascii", "Bearer " + valid[:10] + "é" + valid[12:], false},
		{"NUL byte", "Bearer " + valid[:10] + "\x00" + valid[11:], false},
		{"sql-ish", "Bearer wt_' OR '1'='1" + strings.Repeat("A", 25), false},
		{"looks like a JWT", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseBearer(tc.header)
			if ok != tc.want {
				t.Fatalf("ParseBearer ok = %v, want %v", ok, tc.want)
			}
			if ok && got != valid {
				t.Errorf("token = %q, want %q", got, valid)
			}
			if !ok && got != "" {
				t.Errorf("token = %q on failure, want empty", got)
			}
		})
	}
}
