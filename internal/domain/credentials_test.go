package domain

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestReservedUsernamesMatchSpec(t *testing.T) {
	// docs/api/endpoints/01-register.md, "Reserved usernames".
	want := []string{
		"admin", "administrator", "root", "system", "support",
		"api", "me", "null", "undefined", "anonymous",
	}
	if !slices.Equal(ReservedUsernames, want) {
		t.Errorf("ReservedUsernames = %v, want %v", ReservedUsernames, want)
	}
	// Spec quirk: "me" is reserved but only 2 characters, so it already fails
	// the length rule (too_short). Every other reserved name is well-formed, so
	// only the reserved check stops it.
	for _, name := range want {
		err := ValidateUsername(name)
		if name == "me" {
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Issues[0].Issue != IssueTooShort {
				t.Errorf("ValidateUsername(%q) = %v, want too_short", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("reserved name %q is not a valid username: %v", name, err)
		}
	}
}

func TestIsReservedUsername(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"admin", true},
		{"Admin", true},
		{"  ROOT ", true},
		{"anonymous", true},
		{"me", true},
		{"admin2", false},
		{"administrators", false},
		{"ihsan", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsReservedUsername(tc.in); got != tc.want {
			t.Errorf("IsReservedUsername(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ihsan", "ihsan"},
		{"  Ihsan  ", "ihsan"},
		{"\tUSER_1\n", "user_1"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := NormalizeUsername(tc.in); got != tc.want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantIssue string // "" means valid
	}{
		{"min length", "abc", ""},
		{"max length", strings.Repeat("a", 30), ""},
		{"digits and underscore", "user_01", ""},
		{"empty", "", IssueTooShort},
		{"one short", "ab", IssueTooShort},
		{"one long", strings.Repeat("a", 31), IssueTooLong},
		{"way too long", strings.Repeat("a", 500), IssueTooLong},
		{"space", "a b", IssueInvalidChars},
		{"dash", "ab-c", IssueInvalidChars},
		{"dot", "a.b", IssueInvalidChars},
		{"uppercase (not normalized)", "Abc", IssueInvalidChars},
		{"unicode letters, 3 runes", "äöü", IssueInvalidChars},
		{"unicode, 2 runes", "äö", IssueTooShort},
		{"emoji", "ab😀", IssueInvalidChars},
		{"trailing newline", "abc\n", IssueInvalidChars},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUsername(tc.in)
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("ValidateUsername(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateUsername(%q) = %v, want *ValidationError", tc.in, err)
			}
			want := []FieldIssue{{Field: "username", Issue: tc.wantIssue}}
			if !slices.Equal(ve.Issues, want) {
				t.Errorf("issues = %v, want %v", ve.Issues, want)
			}
		})
	}
}

// ValidateUsername and the SQL-facing UsernamePattern must accept exactly the
// same strings, because the DB CHECK constraint uses the pattern.
func TestValidateUsernameAgreesWithPattern(t *testing.T) {
	re := regexp.MustCompile(UsernamePattern)
	samples := []string{
		"", "a", "ab", "abc", "ABC", "a_b", "a-b", "a b", " abc", "abc ", "abc\n",
		"user_01", "ñandú", "日本語", "ab😀", "___", "000",
		strings.Repeat("a", 29), strings.Repeat("a", 30), strings.Repeat("a", 31),
		strings.Repeat("_", 30), strings.Repeat("9", 31),
	}
	for _, s := range samples {
		if got, want := ValidateUsername(s) == nil, re.MatchString(s); got != want {
			t.Errorf("%q: ValidateUsername ok = %v, pattern match = %v", s, got, want)
		}
	}
}

func TestUsernameLimitsAreConsistent(t *testing.T) {
	// The length constants and the pattern are written twice by necessity
	// (constants cannot be formatted); make sure they agree.
	want := regexp.MustCompile(`^\^\[a-z0-9_\]\{3,30\}\$$`)
	if !want.MatchString(UsernamePattern) {
		t.Fatalf("UsernamePattern = %q, unexpected shape", UsernamePattern)
	}
	if UsernameMinLen != 3 || UsernameMaxLen != 30 {
		t.Errorf("username length limits = %d..%d, want 3..30", UsernameMinLen, UsernameMaxLen)
	}
	re := regexp.MustCompile(UsernamePattern)
	if !re.MatchString(strings.Repeat("a", UsernameMinLen)) || re.MatchString(strings.Repeat("a", UsernameMinLen-1)) {
		t.Error("pattern does not match UsernameMinLen boundary")
	}
	if !re.MatchString(strings.Repeat("a", UsernameMaxLen)) || re.MatchString(strings.Repeat("a", UsernameMaxLen+1)) {
		t.Error("pattern does not match UsernameMaxLen boundary")
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantIssue string
	}{
		{"weak but valid", "123", ""},
		{"single char", "x", ""},
		{"whitespace only is allowed", "   ", ""},
		{"max bytes", strings.Repeat("a", PasswordMaxBytes), ""},
		{"empty", "", IssueRequired},
		{"one byte over", strings.Repeat("a", PasswordMaxBytes+1), IssueTooLong},
		// 43 three-byte runes = 129 bytes: the cap is bytes, not characters.
		{"multi-byte over in bytes", strings.Repeat("€", 43), IssueTooLong},
		{"multi-byte at cap", strings.Repeat("€", 42), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.in)
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("ValidatePassword = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidatePassword = %v, want *ValidationError", err)
			}
			want := []FieldIssue{{Field: "password", Issue: tc.wantIssue}}
			if !slices.Equal(ve.Issues, want) {
				t.Errorf("issues = %v, want %v", ve.Issues, want)
			}
		})
	}
}
