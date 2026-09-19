package domain

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Username and password rules shared by register, admin change-password and
// the admin CLI (specs 01, 02, 14).

// NormalizeUsername trims and lowercases a username. Apply it before
// validating, storing or looking one up.
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateUsername checks a normalized username against UsernamePattern and
// reports the specific issue (too_short, too_long, invalid_chars) on the field
// "username". It returns nil when the username is valid. It does not check the
// reserved list: the HTTP register endpoint does that with IsReservedUsername,
// the admin CLI does not.
func ValidateUsername(normalized string) error {
	n := utf8.RuneCountInString(normalized)
	switch {
	case n < UsernameMinLen:
		return NewValidation("username", IssueTooShort)
	case n > UsernameMaxLen:
		return NewValidation("username", IssueTooLong)
	}
	for _, r := range normalized {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return NewValidation("username", IssueInvalidChars)
		}
	}
	return nil
}

// IsReservedUsername reports whether the name, once trimmed and lowercased,
// is in ReservedUsernames.
func IsReservedUsername(name string) bool {
	return slices.Contains(ReservedUsernames, NormalizeUsername(name))
}

// ValidatePassword enforces the only password rules: non-empty and at most
// PasswordMaxBytes bytes. It returns a validation error on the field
// "password" (required, too_long) or nil. The password is never trimmed.
func ValidatePassword(password string) error {
	switch {
	case password == "":
		return NewValidation("password", IssueRequired)
	case len(password) > PasswordMaxBytes:
		return NewValidation("password", IssueTooLong)
	}
	return nil
}
