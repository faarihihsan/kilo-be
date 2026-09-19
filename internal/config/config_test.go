package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testPassword = "s3cr3t-hunter2"
	testDBURL    = "postgres://tracker:" + testPassword + "@db.internal:5432/workout?sslmode=disable"
)

// lookupOf turns a map into a lookup function; absent keys are unset.
func lookupOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// merge returns base overlaid with extra. A key mapped to "\x00" is deleted.
func merge(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if v == "\x00" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

const unset = "\x00"

// prodEnv is the smallest valid production environment.
func prodEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":   testDBURL,
		"MEDIA_DIR":      "/var/lib/workout-tracker/media",
		"MEDIA_BASE_URL": "https://api.example.com",
	}
}

// devEnv is the smallest valid development environment.
func devEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      "development",
		"DATABASE_URL": testDBURL,
		"MEDIA_DIR":    "media",
	}
}

func mustParse(t *testing.T, env map[string]string) *Config {
	t.Helper()
	cfg, err := Parse(lookupOf(env))
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	return cfg
}

func parseErr(t *testing.T, env map[string]string) *Error {
	t.Helper()
	cfg, err := Parse(lookupOf(env))
	if err == nil {
		t.Fatalf("Parse() = %+v, want error", cfg)
	}
	if cfg != nil {
		t.Errorf("Parse() returned non-nil Config alongside an error")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("Parse() error type = %T, want *Error", err)
	}
	return ce
}

func TestParseDefaults(t *testing.T) {
	got := mustParse(t, prodEnv())

	want := &Config{
		Env:                 EnvProduction,
		HTTPAddr:            "127.0.0.1:8080",
		DatabaseURL:         testDBURL,
		DBMaxConns:          10,
		MediaDir:            "/var/lib/workout-tracker/media",
		MediaBaseURL:        "https://api.example.com",
		TrustedProxyCIDRs:   []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		TokenTTL:            365 * 24 * time.Hour,
		LoginMaxFailsUser:   5,
		LoginMaxFailsIP:     20,
		LoginLock:           15 * time.Minute,
		Argon2MemoryKiB:     65536,
		Argon2Time:          2,
		Argon2Parallelism:   1,
		Argon2MaxConcurrent: 2,
		LogLevel:            slog.LevelInfo,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseAllVariablesSet(t *testing.T) {
	got := mustParse(t, map[string]string{
		"APP_ENV":               "development",
		"HTTP_ADDR":             "0.0.0.0:9090",
		"DATABASE_URL":          "postgresql://u:p@localhost/db",
		"DB_MAX_CONNS":          "25",
		"MEDIA_DIR":             "/srv/media",
		"MEDIA_BASE_URL":        "http://cdn.example.com/",
		"TRUSTED_PROXY_CIDRS":   "10.0.0.0/8, 192.168.1.5,::1/128",
		"TOKEN_TTL_DAYS":        "30",
		"LOGIN_MAX_FAILS_USER":  "3",
		"LOGIN_MAX_FAILS_IP":    "50",
		"LOGIN_LOCK_MINUTES":    "60",
		"ARGON2_MEMORY_KIB":     "19456",
		"ARGON2_TIME":           "3",
		"ARGON2_PARALLELISM":    "4",
		"ARGON2_MAX_CONCURRENT": "1",
		"LOG_LEVEL":             "debug",
	})

	want := &Config{
		Env:          EnvDevelopment,
		HTTPAddr:     "0.0.0.0:9090",
		DatabaseURL:  "postgresql://u:p@localhost/db",
		DBMaxConns:   25,
		MediaDir:     "/srv/media",
		MediaBaseURL: "http://cdn.example.com",
		TrustedProxyCIDRs: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("192.168.1.5/32"),
			netip.MustParsePrefix("::1/128"),
		},
		TokenTTL:            30 * 24 * time.Hour,
		LoginMaxFailsUser:   3,
		LoginMaxFailsIP:     50,
		LoginLock:           time.Hour,
		Argon2MemoryKiB:     19456,
		Argon2Time:          3,
		Argon2Parallelism:   4,
		Argon2MaxConcurrent: 1,
		LogLevel:            slog.LevelDebug,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseNilLookup(t *testing.T) {
	// A nil lookup behaves like an empty environment instead of panicking.
	cfg, err := Parse(nil)
	var ce *Error
	if cfg != nil || !errors.As(err, &ce) {
		t.Fatalf("Parse(nil) = %v, %v; want nil and *Error", cfg, err)
	}
	if !strings.Contains(ce.Error(), "DATABASE_URL is required") {
		t.Errorf("error = %q, want it to report DATABASE_URL", ce)
	}
}

func TestParseRequired(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string // exact problems
	}{
		{
			name: "everything missing in production",
			env:  map[string]string{},
			want: []string{
				"DATABASE_URL is required",
				"MEDIA_DIR is required",
				"MEDIA_BASE_URL is required in production (e.g. https://api.example.com)",
			},
		},
		{
			name: "DATABASE_URL missing",
			env:  merge(prodEnv(), map[string]string{"DATABASE_URL": unset}),
			want: []string{"DATABASE_URL is required"},
		},
		{
			name: "DATABASE_URL blank",
			env:  merge(prodEnv(), map[string]string{"DATABASE_URL": "   "}),
			want: []string{"DATABASE_URL is required"},
		},
		{
			name: "MEDIA_DIR missing",
			env:  merge(prodEnv(), map[string]string{"MEDIA_DIR": unset}),
			want: []string{"MEDIA_DIR is required"},
		},
		{
			name: "MEDIA_DIR blank",
			env:  merge(prodEnv(), map[string]string{"MEDIA_DIR": ""}),
			want: []string{"MEDIA_DIR is required"},
		},
		{
			name: "MEDIA_BASE_URL missing in production",
			env:  merge(prodEnv(), map[string]string{"MEDIA_BASE_URL": unset}),
			want: []string{"MEDIA_BASE_URL is required in production (e.g. https://api.example.com)"},
		},
		{
			name: "explicit APP_ENV=production also requires MEDIA_BASE_URL",
			env:  merge(prodEnv(), map[string]string{"APP_ENV": "production", "MEDIA_BASE_URL": unset}),
			want: []string{"MEDIA_BASE_URL is required in production (e.g. https://api.example.com)"},
		},
		{
			name: "development still requires DATABASE_URL and MEDIA_DIR",
			env:  map[string]string{"APP_ENV": "development"},
			want: []string{"DATABASE_URL is required", "MEDIA_DIR is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := parseErr(t, tt.env)
			if !reflect.DeepEqual(ce.Problems, tt.want) {
				t.Errorf("problems = %q\n   want = %q", ce.Problems, tt.want)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // exact single problem
	}{
		// APP_ENV
		{"APP_ENV unknown", map[string]string{"APP_ENV": "staging"},
			`APP_ENV must be "production" or "development", got "staging"`},

		// HTTP_ADDR
		{"HTTP_ADDR without port", map[string]string{"HTTP_ADDR": "localhost"},
			`HTTP_ADDR must be host:port, e.g. 127.0.0.1:8080, got "localhost"`},
		{"HTTP_ADDR named port", map[string]string{"HTTP_ADDR": "127.0.0.1:http"},
			`HTTP_ADDR port must be a number between 0 and 65535, got "127.0.0.1:http"`},
		{"HTTP_ADDR port too large", map[string]string{"HTTP_ADDR": "127.0.0.1:70000"},
			`HTTP_ADDR port must be a number between 0 and 65535, got "127.0.0.1:70000"`},

		// DATABASE_URL: the value (which holds a password) must not be echoed.
		{"DATABASE_URL wrong scheme", map[string]string{"DATABASE_URL": "mysql://u:" + testPassword + "@h/db"},
			"DATABASE_URL must be a postgres:// or postgresql:// URL"},
		{"DATABASE_URL key-value DSN", map[string]string{"DATABASE_URL": "host=h user=u password=" + testPassword},
			"DATABASE_URL must be a postgres:// or postgresql:// URL"},
		{"DATABASE_URL unparsable", map[string]string{"DATABASE_URL": "postgres://u:" + testPassword + "@h:notaport/db"},
			"DATABASE_URL is not a valid URL (percent-encode special characters in the password)"},

		// DB_MAX_CONNS
		{"DB_MAX_CONNS not a number", map[string]string{"DB_MAX_CONNS": "abc"},
			`DB_MAX_CONNS must be a positive integer, got "abc"`},
		{"DB_MAX_CONNS float", map[string]string{"DB_MAX_CONNS": "10.5"},
			`DB_MAX_CONNS must be a positive integer, got "10.5"`},
		{"DB_MAX_CONNS zero", map[string]string{"DB_MAX_CONNS": "0"},
			"DB_MAX_CONNS must be between 1 and 100, got 0"},
		{"DB_MAX_CONNS negative", map[string]string{"DB_MAX_CONNS": "-5"},
			"DB_MAX_CONNS must be between 1 and 100, got -5"},
		{"DB_MAX_CONNS too large", map[string]string{"DB_MAX_CONNS": "101"},
			"DB_MAX_CONNS must be between 1 and 100, got 101"},

		// MEDIA_DIR
		{"MEDIA_DIR relative in production", map[string]string{"MEDIA_DIR": "media"},
			`MEDIA_DIR must be an absolute path in production, got "media"`},

		// MEDIA_BASE_URL
		{"MEDIA_BASE_URL http in production", map[string]string{"MEDIA_BASE_URL": "http://api.example.com"},
			`MEDIA_BASE_URL must start with https://, got "http://api.example.com"`},
		{"MEDIA_BASE_URL no scheme", map[string]string{"MEDIA_BASE_URL": "api.example.com"},
			`MEDIA_BASE_URL must start with https://, got "api.example.com"`},
		{"MEDIA_BASE_URL ftp", map[string]string{"MEDIA_BASE_URL": "ftp://api.example.com"},
			`MEDIA_BASE_URL must start with https://, got "ftp://api.example.com"`},
		{"MEDIA_BASE_URL no host", map[string]string{"MEDIA_BASE_URL": "https://"},
			`MEDIA_BASE_URL must include a host, got "https://"`},
		{"MEDIA_BASE_URL query", map[string]string{"MEDIA_BASE_URL": "https://api.example.com?x=1"},
			`MEDIA_BASE_URL must not contain a query or fragment, got "https://api.example.com?x=1"`},
		{"MEDIA_BASE_URL fragment", map[string]string{"MEDIA_BASE_URL": "https://api.example.com/#top"},
			`MEDIA_BASE_URL must not contain a query or fragment, got "https://api.example.com/#top"`},
		{"MEDIA_BASE_URL credentials not echoed", map[string]string{"MEDIA_BASE_URL": "https://u:" + testPassword + "@api.example.com"},
			"MEDIA_BASE_URL must not contain credentials"},
		{"MEDIA_BASE_URL unparsable not echoed", map[string]string{"MEDIA_BASE_URL": "https://u:" + testPassword + "@api.example.com:port"},
			"MEDIA_BASE_URL must be an absolute http(s) URL, e.g. https://api.example.com"},

		// TRUSTED_PROXY_CIDRS
		{"TRUSTED_PROXY_CIDRS garbage", map[string]string{"TRUSTED_PROXY_CIDRS": "nope"},
			`TRUSTED_PROXY_CIDRS entry "nope" is not a valid CIDR (e.g. 127.0.0.1/32) or IP address`},
		{"TRUSTED_PROXY_CIDRS bad prefix length", map[string]string{"TRUSTED_PROXY_CIDRS": "10.0.0.0/33"},
			`TRUSTED_PROXY_CIDRS entry "10.0.0.0/33" is not a valid CIDR (e.g. 127.0.0.1/32) or IP address`},
		{"TRUSTED_PROXY_CIDRS zone", map[string]string{"TRUSTED_PROXY_CIDRS": "fe80::1%eth0"},
			`TRUSTED_PROXY_CIDRS entry "fe80::1%eth0" is not a valid CIDR (e.g. 127.0.0.1/32) or IP address`},
		{"TRUSTED_PROXY_CIDRS trust everyone v4", map[string]string{"TRUSTED_PROXY_CIDRS": "0.0.0.0/0"},
			`TRUSTED_PROXY_CIDRS entry "0.0.0.0/0" would trust every client; use a narrower range`},
		{"TRUSTED_PROXY_CIDRS trust everyone v6", map[string]string{"TRUSTED_PROXY_CIDRS": "127.0.0.1/32,::/0"},
			`TRUSTED_PROXY_CIDRS entry "::/0" would trust every client; use a narrower range`},

		// TOKEN_TTL_DAYS
		{"TOKEN_TTL_DAYS not a number", map[string]string{"TOKEN_TTL_DAYS": "a year"},
			`TOKEN_TTL_DAYS must be a positive integer, got "a year"`},
		{"TOKEN_TTL_DAYS zero", map[string]string{"TOKEN_TTL_DAYS": "0"},
			"TOKEN_TTL_DAYS must be between 1 and 3650, got 0"},
		{"TOKEN_TTL_DAYS overflow guard", map[string]string{"TOKEN_TTL_DAYS": "999999999"},
			"TOKEN_TTL_DAYS must be between 1 and 3650, got 999999999"},

		// Login limits
		{"LOGIN_MAX_FAILS_USER not a number", map[string]string{"LOGIN_MAX_FAILS_USER": "five"},
			`LOGIN_MAX_FAILS_USER must be a positive integer, got "five"`},
		{"LOGIN_MAX_FAILS_USER zero", map[string]string{"LOGIN_MAX_FAILS_USER": "0"},
			"LOGIN_MAX_FAILS_USER must be a positive integer, got 0"},
		{"LOGIN_MAX_FAILS_IP negative", map[string]string{"LOGIN_MAX_FAILS_IP": "-1"},
			"LOGIN_MAX_FAILS_IP must be a positive integer, got -1"},
		{"LOGIN_LOCK_MINUTES zero", map[string]string{"LOGIN_LOCK_MINUTES": "0"},
			"LOGIN_LOCK_MINUTES must be between 1 and 1440, got 0"},
		{"LOGIN_LOCK_MINUTES too large", map[string]string{"LOGIN_LOCK_MINUTES": "1441"},
			"LOGIN_LOCK_MINUTES must be between 1 and 1440, got 1441"},

		// Argon2
		{"ARGON2_MEMORY_KIB not a number", map[string]string{"ARGON2_MEMORY_KIB": "64MiB"},
			`ARGON2_MEMORY_KIB must be a positive integer, got "64MiB"`},
		{"ARGON2_MEMORY_KIB below floor", map[string]string{"ARGON2_MEMORY_KIB": "8191"},
			"ARGON2_MEMORY_KIB must be at least 8192, got 8191"},
		{"ARGON2_MEMORY_KIB zero", map[string]string{"ARGON2_MEMORY_KIB": "0"},
			"ARGON2_MEMORY_KIB must be at least 8192, got 0"},
		{"ARGON2_MEMORY_KIB overflows uint32", map[string]string{"ARGON2_MEMORY_KIB": "4294967296"},
			"ARGON2_MEMORY_KIB must be at least 8192, got 4294967296"},
		{"ARGON2_TIME zero", map[string]string{"ARGON2_TIME": "0"},
			"ARGON2_TIME must be a positive integer, got 0"},
		{"ARGON2_TIME negative", map[string]string{"ARGON2_TIME": "-2"},
			"ARGON2_TIME must be a positive integer, got -2"},
		{"ARGON2_PARALLELISM zero", map[string]string{"ARGON2_PARALLELISM": "0"},
			"ARGON2_PARALLELISM must be between 1 and 255, got 0"},
		{"ARGON2_PARALLELISM overflows uint8", map[string]string{"ARGON2_PARALLELISM": "256"},
			"ARGON2_PARALLELISM must be between 1 and 255, got 256"},
		{"ARGON2_MAX_CONCURRENT zero", map[string]string{"ARGON2_MAX_CONCURRENT": "0"},
			"ARGON2_MAX_CONCURRENT must be a positive integer, got 0"},

		// LOG_LEVEL
		{"LOG_LEVEL unknown", map[string]string{"LOG_LEVEL": "verbose"},
			`LOG_LEVEL must be one of debug, info, warn, error, got "verbose"`},
		{"LOG_LEVEL offset syntax not accepted", map[string]string{"LOG_LEVEL": "info+2"},
			`LOG_LEVEL must be one of debug, info, warn, error, got "info+2"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := parseErr(t, merge(prodEnv(), tt.env))
			if !reflect.DeepEqual(ce.Problems, []string{tt.want}) {
				t.Errorf("problems = %q\n   want = %q", ce.Problems, []string{tt.want})
			}
			if got, want := ce.Error(), "config: "+tt.want; got != want {
				t.Errorf("Error() = %q, want %q", got, want)
			}
			if strings.Contains(ce.Error(), testPassword) {
				t.Errorf("error text leaks a secret: %q", ce.Error())
			}
		})
	}
}

func TestParseAggregatesAllProblems(t *testing.T) {
	env := map[string]string{
		"APP_ENV":               "staging",
		"HTTP_ADDR":             "nope",
		"DB_MAX_CONNS":          "abc",
		"MEDIA_BASE_URL":        "ftp://x",
		"TRUSTED_PROXY_CIDRS":   "1.2.3.4/99, 10.0.0.0/8, bogus",
		"TOKEN_TTL_DAYS":        "0",
		"LOGIN_MAX_FAILS_USER":  "-1",
		"LOGIN_MAX_FAILS_IP":    "x",
		"LOGIN_LOCK_MINUTES":    "2000",
		"ARGON2_MEMORY_KIB":     "1024",
		"ARGON2_TIME":           "0",
		"ARGON2_PARALLELISM":    "300",
		"ARGON2_MAX_CONCURRENT": "0",
		"LOG_LEVEL":             "loud",
	}
	ce := parseErr(t, env)

	want := []string{
		`APP_ENV must be "production" or "development", got "staging"`,
		`HTTP_ADDR must be host:port, e.g. 127.0.0.1:8080, got "nope"`,
		"DATABASE_URL is required",
		`DB_MAX_CONNS must be a positive integer, got "abc"`,
		"MEDIA_DIR is required",
		`MEDIA_BASE_URL must start with https://, got "ftp://x"`,
		`TRUSTED_PROXY_CIDRS entry "1.2.3.4/99" is not a valid CIDR (e.g. 127.0.0.1/32) or IP address`,
		`TRUSTED_PROXY_CIDRS entry "bogus" is not a valid CIDR (e.g. 127.0.0.1/32) or IP address`,
		"TOKEN_TTL_DAYS must be between 1 and 3650, got 0",
		"LOGIN_MAX_FAILS_USER must be a positive integer, got -1",
		`LOGIN_MAX_FAILS_IP must be a positive integer, got "x"`,
		"LOGIN_LOCK_MINUTES must be between 1 and 1440, got 2000",
		"ARGON2_MEMORY_KIB must be at least 8192, got 1024",
		"ARGON2_TIME must be a positive integer, got 0",
		"ARGON2_PARALLELISM must be between 1 and 255, got 300",
		"ARGON2_MAX_CONCURRENT must be a positive integer, got 0",
		`LOG_LEVEL must be one of debug, info, warn, error, got "loud"`,
	}
	if !reflect.DeepEqual(ce.Problems, want) {
		t.Errorf("problems mismatch\n got: %q\nwant: %q", ce.Problems, want)
	}
	if got, wantStr := ce.Error(), "config: "+strings.Join(want, "; "); got != wantStr {
		t.Errorf("Error() =\n%q\nwant\n%q", got, wantStr)
	}
}

func TestParseErrorMessageShape(t *testing.T) {
	// The documented example: several problems in one sentence-like line.
	ce := parseErr(t, map[string]string{
		"MEDIA_DIR":      "/m",
		"MEDIA_BASE_URL": "https://a.example",
		"DB_MAX_CONNS":   "abc",
	})
	want := `config: DATABASE_URL is required; DB_MAX_CONNS must be a positive integer, got "abc"`
	if ce.Error() != want {
		t.Errorf("Error() = %q, want %q", ce.Error(), want)
	}
}

func TestParseDevelopment(t *testing.T) {
	t.Run("relaxed requirements and defaults", func(t *testing.T) {
		got := mustParse(t, devEnv())
		if got.Env != EnvDevelopment || got.Env.IsProduction() {
			t.Errorf("Env = %q, want development", got.Env)
		}
		if got.MediaDir != "media" {
			t.Errorf("MediaDir = %q, want relative path kept", got.MediaDir)
		}
		if got.MediaBaseURL != "http://127.0.0.1:8080" {
			t.Errorf("MediaBaseURL = %q, want derived from default HTTP_ADDR", got.MediaBaseURL)
		}
	})

	t.Run("http MEDIA_BASE_URL allowed", func(t *testing.T) {
		got := mustParse(t, merge(devEnv(), map[string]string{"MEDIA_BASE_URL": "http://localhost:3000/"}))
		if got.MediaBaseURL != "http://localhost:3000" {
			t.Errorf("MediaBaseURL = %q", got.MediaBaseURL)
		}
	})

	t.Run("https MEDIA_BASE_URL still allowed", func(t *testing.T) {
		got := mustParse(t, merge(devEnv(), map[string]string{"MEDIA_BASE_URL": "https://dev.example.com"}))
		if got.MediaBaseURL != "https://dev.example.com" {
			t.Errorf("MediaBaseURL = %q", got.MediaBaseURL)
		}
	})

	t.Run("invalid MEDIA_BASE_URL scheme lists both options", func(t *testing.T) {
		ce := parseErr(t, merge(devEnv(), map[string]string{"MEDIA_BASE_URL": "ftp://x"}))
		want := []string{`MEDIA_BASE_URL must start with http:// or https://, got "ftp://x"`}
		if !reflect.DeepEqual(ce.Problems, want) {
			t.Errorf("problems = %q, want %q", ce.Problems, want)
		}
	})

	t.Run("APP_ENV is case-insensitive and trimmed", func(t *testing.T) {
		got := mustParse(t, merge(devEnv(), map[string]string{"APP_ENV": "  Development "}))
		if got.Env != EnvDevelopment {
			t.Errorf("Env = %q", got.Env)
		}
	})

	// The derived default follows HTTP_ADDR; unspecified hosts become localhost.
	derived := []struct{ addr, want string }{
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{":9000", "http://localhost:9000"},
		{"0.0.0.0:8080", "http://localhost:8080"},
		{"[::]:8080", "http://localhost:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
		{"localhost:3000", "http://localhost:3000"},
	}
	for _, tt := range derived {
		t.Run("derived from "+tt.addr, func(t *testing.T) {
			got := mustParse(t, merge(devEnv(), map[string]string{"HTTP_ADDR": tt.addr}))
			if got.MediaBaseURL != tt.want {
				t.Errorf("MediaBaseURL = %q, want %q", got.MediaBaseURL, tt.want)
			}
		})
	}

	t.Run("invalid HTTP_ADDR is reported once, no derived default", func(t *testing.T) {
		ce := parseErr(t, merge(devEnv(), map[string]string{"HTTP_ADDR": "nope"}))
		want := []string{`HTTP_ADDR must be host:port, e.g. 127.0.0.1:8080, got "nope"`}
		if !reflect.DeepEqual(ce.Problems, want) {
			t.Errorf("problems = %q, want %q", ce.Problems, want)
		}
	})
}

func TestParseProductionIsDefault(t *testing.T) {
	// No APP_ENV at all must behave as production: relative MEDIA_DIR fails.
	ce := parseErr(t, merge(prodEnv(), map[string]string{"MEDIA_DIR": "./media"}))
	if len(ce.Problems) != 1 || !strings.Contains(ce.Problems[0], "MEDIA_DIR must be an absolute path in production") {
		t.Errorf("problems = %q", ce.Problems)
	}
	// An invalid APP_ENV also validates strictly (relative MEDIA_DIR still fails).
	ce = parseErr(t, merge(prodEnv(), map[string]string{"APP_ENV": "qa", "MEDIA_DIR": "./media"}))
	if len(ce.Problems) != 2 {
		t.Errorf("problems = %q, want APP_ENV and MEDIA_DIR", ce.Problems)
	}
}

func TestParseTrimsAndTreatsBlankAsUnset(t *testing.T) {
	got := mustParse(t, merge(prodEnv(), map[string]string{
		"HTTP_ADDR":      "  ",
		"DB_MAX_CONNS":   " 7 ",
		"LOG_LEVEL":      "",
		"MEDIA_BASE_URL": "  https://api.example.com/  ",
	}))
	if got.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr = %q, want default for blank", got.HTTPAddr)
	}
	if got.DBMaxConns != 7 {
		t.Errorf("DBMaxConns = %d, want 7", got.DBMaxConns)
	}
	if got.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info for blank", got.LogLevel)
	}
	if got.MediaBaseURL != "https://api.example.com" {
		t.Errorf("MediaBaseURL = %q", got.MediaBaseURL)
	}
}

func TestParseMediaBaseURLNormalised(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://api.example.com", "https://api.example.com"},
		{"https://api.example.com/", "https://api.example.com"},
		{"https://api.example.com///", "https://api.example.com"},
		{"https://cdn.example.com/static/", "https://cdn.example.com/static"},
		{"https://api.example.com:8443", "https://api.example.com:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := mustParse(t, merge(prodEnv(), map[string]string{"MEDIA_BASE_URL": tt.in}))
			if got.MediaBaseURL != tt.want {
				t.Errorf("MediaBaseURL = %q, want %q", got.MediaBaseURL, tt.want)
			}
		})
	}
}

func TestParseMediaDirCleaned(t *testing.T) {
	got := mustParse(t, merge(prodEnv(), map[string]string{"MEDIA_DIR": "/var/lib/wt//media/../media/"}))
	if got.MediaDir != "/var/lib/wt/media" {
		t.Errorf("MediaDir = %q", got.MediaDir)
	}
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	pfx := netip.MustParsePrefix
	tests := []struct {
		name string
		set  bool
		in   string
		want []netip.Prefix
	}{
		{"unset defaults to loopback", false, "", []netip.Prefix{pfx("127.0.0.1/32")}},
		{"single CIDR", true, "10.0.0.0/8", []netip.Prefix{pfx("10.0.0.0/8")}},
		{"multiple with spaces", true, " 10.0.0.0/8 , 172.16.0.0/12 ", []netip.Prefix{pfx("10.0.0.0/8"), pfx("172.16.0.0/12")}},
		{"bare IPv4 becomes /32", true, "203.0.113.7", []netip.Prefix{pfx("203.0.113.7/32")}},
		{"bare IPv6 becomes /128", true, "2001:db8::1", []netip.Prefix{pfx("2001:db8::1/128")}},
		{"IPv6 CIDR", true, "fd00::/8", []netip.Prefix{pfx("fd00::/8")}},
		{"host bits are masked", true, "10.1.2.3/8", []netip.Prefix{pfx("10.0.0.0/8")}},
		{"trailing and doubled commas ignored", true, "10.0.0.0/8,,", []netip.Prefix{pfx("10.0.0.0/8")}},
		{"set but empty means trust nobody", true, "", []netip.Prefix{}},
		{"set but whitespace means trust nobody", true, "  ", []netip.Prefix{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := prodEnv()
			if tt.set {
				env["TRUSTED_PROXY_CIDRS"] = tt.in
			}
			got := mustParse(t, env).TrustedProxyCIDRs
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TrustedProxyCIDRs = %v, want %v", got, tt.want)
			}
			if got == nil {
				t.Errorf("TrustedProxyCIDRs is nil, want a non-nil slice")
			}
		})
	}
}

func TestParseLogLevels(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
		{" Error ", slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := mustParse(t, merge(prodEnv(), map[string]string{"LOG_LEVEL": tt.in}))
			if got.LogLevel != tt.want {
				t.Errorf("LogLevel = %v, want %v", got.LogLevel, tt.want)
			}
		})
	}
}

func TestParseBoundaryValues(t *testing.T) {
	got := mustParse(t, merge(prodEnv(), map[string]string{
		"DB_MAX_CONNS":          "1",
		"TOKEN_TTL_DAYS":        "3650",
		"LOGIN_LOCK_MINUTES":    "1440",
		"ARGON2_MEMORY_KIB":     "8192",
		"ARGON2_PARALLELISM":    "255",
		"ARGON2_TIME":           "4294967295",
		"LOGIN_MAX_FAILS_USER":  "1",
		"ARGON2_MAX_CONCURRENT": "1",
	}))
	if got.DBMaxConns != 1 || got.TokenTTL != 3650*24*time.Hour || got.LoginLock != 1440*time.Minute ||
		got.Argon2MemoryKiB != 8192 || got.Argon2Parallelism != 255 || got.Argon2Time != 4294967295 {
		t.Errorf("boundary values not accepted as-is: %#v", got)
	}

	got = mustParse(t, merge(prodEnv(), map[string]string{"DB_MAX_CONNS": "100"}))
	if got.DBMaxConns != 100 {
		t.Errorf("DBMaxConns = %d, want 100", got.DBMaxConns)
	}
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	for k, v := range merge(prodEnv(), map[string]string{"DB_MAX_CONNS": "12", "LOG_LEVEL": "warn"}) {
		t.Setenv(k, v)
	}
	// Unset every other variable so a developer's shell cannot leak into the
	// test. t.Setenv first, so the original value is restored afterwards.
	for _, k := range []string{
		"APP_ENV", "HTTP_ADDR", "TRUSTED_PROXY_CIDRS", "TOKEN_TTL_DAYS", "LOGIN_MAX_FAILS_USER",
		"LOGIN_MAX_FAILS_IP", "LOGIN_LOCK_MINUTES", "ARGON2_MEMORY_KIB", "ARGON2_TIME",
		"ARGON2_PARALLELISM", "ARGON2_MAX_CONCURRENT",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DBMaxConns != 12 || cfg.LogLevel != slog.LevelWarn || cfg.DatabaseURL != testDBURL {
		t.Errorf("Load() = %v", cfg)
	}
}

func TestLoadReportsProblems(t *testing.T) {
	for _, k := range []string{"DATABASE_URL", "MEDIA_DIR", "MEDIA_BASE_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("APP_ENV", "production")
	_, err := Load()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("Load() error = %v, want *Error", err)
	}
	if len(ce.Problems) != 3 {
		t.Errorf("problems = %q, want 3", ce.Problems)
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"userinfo password", "postgres://tracker:hunter2@db:5432/workout", "postgres://tracker:REDACTED@db:5432/workout"},
		{"keeps query", "postgres://u:p@h/db?sslmode=disable", "postgres://u:REDACTED@h/db?sslmode=disable"},
		{"no password", "postgres://u@h/db", "postgres://u@h/db"},
		{"no userinfo", "postgres://h/db", "postgres://h/db"},
		{"unix socket", "postgres:///db?host=/var/run/postgresql", "postgres:///db?host=/var/run/postgresql"},
		{"empty password", "postgres://u:@h/db", "postgres://u:REDACTED@h/db"},
		{"password in query", "postgres://h/db?password=hunter2&sslmode=disable", "postgres://h/db?password=REDACTED&sslmode=disable"},
		{"sslpassword in query", "postgres://h/db?sslpassword=hunter2", "postgres://h/db?sslpassword=REDACTED"},
		{"percent-encoded password", "postgres://u:p%40ss%2Fw@h/db", "postgres://u:REDACTED@h/db"},
		{"unparsable is fully hidden", "postgres://u:hunter2@h:notaport/db", "[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURL(tt.in)
			if got != tt.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "hunter2") {
				t.Errorf("RedactURL leaks the password: %q", got)
			}
		})
	}
}

func TestConfigRedactsDatabasePassword(t *testing.T) {
	cfg := mustParse(t, prodEnv())

	t.Run("slog JSON", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("config loaded", "config", cfg)
		out := buf.String()
		assertRedacted(t, out)
		for _, want := range []string{
			`"database_url":"postgres://tracker:REDACTED@db.internal:5432/workout?sslmode=disable"`,
			`"app_env":"production"`,
			`"db_max_conns":10`,
			`"media_base_url":"https://api.example.com"`,
			`"trusted_proxy_cidrs":["127.0.0.1/32"]`,
			`"log_level":"INFO"`,
			`"argon2_memory_kib":65536`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("log output missing %s\n%s", want, out)
			}
		}
	})

	t.Run("slog text", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(slog.NewTextHandler(&buf, nil)).Info("config loaded", "config", cfg)
		assertRedacted(t, buf.String())
	})

	t.Run("slog group attr", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("config loaded", slog.Any("cfg", cfg))
		assertRedacted(t, buf.String())
	})

	t.Run("formatting verbs", func(t *testing.T) {
		for _, format := range []string{"%v", "%+v", "%s", "%#v"} {
			assertRedacted(t, fmt.Sprintf(format, cfg))
		}
		assertRedacted(t, fmt.Sprint(cfg))
		assertRedacted(t, cfg.String())
	})

	t.Run("field is still usable", func(t *testing.T) {
		// Redaction only affects rendering, never the value the pool needs.
		if cfg.DatabaseURL != testDBURL {
			t.Errorf("DatabaseURL = %q, want the raw URL", cfg.DatabaseURL)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		var nilCfg *Config
		if got := nilCfg.String(); got != "<nil>" {
			t.Errorf("String() = %q", got)
		}
		if got := nilCfg.LogValue().String(); got != "<nil>" {
			t.Errorf("LogValue() = %q", got)
		}
	})
}

func assertRedacted(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testPassword) {
		t.Errorf("output leaks the database password: %s", s)
	}
	if !strings.Contains(s, "REDACTED") {
		t.Errorf("output lacks the redaction marker: %s", s)
	}
}

func TestConfigImplementsLogValuer(t *testing.T) {
	var _ slog.LogValuer = (*Config)(nil)
	var _ fmt.Stringer = (*Config)(nil)
	var _ fmt.GoStringer = (*Config)(nil)
	var _ error = (*Error)(nil)
}
