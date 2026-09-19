package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// defaultEnvFile is the file loadDotEnv reads unless ENV_FILE names another one.
// It is resolved relative to the working directory, so run the binary from the
// project root (or set ENV_FILE).
const defaultEnvFile = ".env"

// loadDotEnv loads KEY=value pairs from a .env file into the process
// environment, so a local `go build && ./bin/server serve` works without
// exporting variables by hand. It runs once at startup, before any
// config.Load call.
//
// A variable already present in the real environment always wins: it is left
// untouched even when empty, so the shell, `make run` and systemd keep
// precedence over the file. A missing default .env is not an error; a missing
// ENV_FILE is.
//
// Format: one KEY=value per line. Blank lines and lines starting with "#" are
// ignored, a leading "export " is allowed, surrounding whitespace is trimmed
// and matching single or double quotes around the value are removed. Values
// are literal: no shell expansion, no escape processing, no inline comments
// (a "#" inside a value is kept).
func loadDotEnv() error {
	path := os.Getenv("ENV_FILE")
	explicit := path != ""
	if !explicit {
		path = defaultEnvFile
	}

	f, err := os.Open(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("env file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		key, value, ok, err := parseDotEnvLine(scanner.Text())
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue // the real environment wins
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, line, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("env file %s: %w", path, err)
	}
	return nil
}

// parseDotEnvLine parses one line of a .env file. ok is false for blank lines
// and comments (which are not errors).
func parseDotEnvLine(raw string) (key, value string, ok bool, err error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", false, fmt.Errorf("line is not KEY=value: %q", raw)
	}
	key = strings.TrimSpace(line[:eq])
	value = dotEnvUnquote(strings.TrimSpace(line[eq+1:]))

	if !validEnvName(key) {
		return "", "", false, fmt.Errorf("invalid variable name %q", key)
	}
	return key, value, true, nil
}

// dotEnvUnquote removes one pair of matching surrounding single or double
// quotes. An unmatched quote is left as is.
func dotEnvUnquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// validEnvName reports whether s looks like a portable environment variable
// name: [A-Za-z_][A-Za-z0-9_]*.
func validEnvName(s string) bool {
	if s == "" || !isEnvNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isEnvNameChar(s[i]) {
			return false
		}
	}
	return true
}

func isEnvNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isEnvNameChar(c byte) bool {
	return isEnvNameStart(c) || (c >= '0' && c <= '9')
}
