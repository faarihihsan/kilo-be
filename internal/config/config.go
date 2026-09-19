// Package config loads the process configuration from environment variables
// and validates it once, at startup (docs/implementation-plan.md section 3).
//
// Load reads the real environment; Parse is the testable core that takes any
// lookup function. Validation never stops at the first problem: it collects
// every missing or invalid variable and returns them in a single *Error, so an
// operator fixes the env file in one pass.
//
// Secrets never appear in error text. DATABASE_URL is the only secret-bearing
// value; a bad one is reported without echoing it, and Config's LogValue,
// String and GoString all redact its password. Always pass *Config (not a
// Config copy) to loggers so those methods apply.
package config

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"workout-tracker-be/internal/domain"
)

// Env is the deployment mode selected by APP_ENV.
type Env string

const (
	// EnvProduction is strict: MEDIA_BASE_URL is required and must be https,
	// MEDIA_DIR must be an absolute path. It is the default when APP_ENV is
	// unset, so a forgotten variable fails closed.
	EnvProduction Env = "production"
	// EnvDevelopment relaxes the above: MEDIA_BASE_URL is optional (derived
	// from HTTP_ADDR) and may be http://, MEDIA_DIR may be relative.
	EnvDevelopment Env = "development"
)

// IsProduction reports whether e is EnvProduction.
func (e Env) IsProduction() bool { return e == EnvProduction }

// Defaults for optional variables (plan section 3). Values that also exist in
// package domain (token TTL) are taken from there so they are defined once.
const (
	defaultHTTPAddr            = "127.0.0.1:8080"
	defaultDBMaxConns          = 10
	defaultTrustedProxyCIDR    = "127.0.0.1/32"
	defaultLoginMaxFailsUser   = 5
	defaultLoginMaxFailsIP     = 20
	defaultLoginLockMinutes    = 15
	defaultArgon2MemoryKiB     = 64 * 1024
	defaultArgon2Time          = 2
	defaultArgon2Parallelism   = 1
	defaultArgon2MaxConcurrent = 2
	defaultLogLevel            = slog.LevelInfo
)

// Bounds. Upper limits exist where a value would overflow its Go type
// (durations, uint8/uint32 argon2 parameters) or is clearly a typo.
const (
	minDBMaxConns = 1
	maxDBMaxConns = 100

	maxTokenTTLDays   = 3650 // 10 years; also keeps time.Duration from overflowing
	maxLoginLockMins  = 1440 // 24 hours
	minArgon2MemKiB   = 8192 // 8 MiB, the floor for a meaningful argon2id hash
	maxArgon2Parallel = math.MaxUint8

	// noMax marks a variable with no upper bound beyond its Go type.
	noMax = math.MaxInt32
)

// Config is the validated process configuration. Every field is filled; there
// are no "unset" states after a successful Parse.
type Config struct {
	// Env is APP_ENV.
	Env Env
	// HTTPAddr is HTTP_ADDR, a host:port listen address.
	HTTPAddr string
	// DatabaseURL is DATABASE_URL, a postgres:// URL. It contains the database
	// password: never log it directly, use LogValue or RedactURL.
	DatabaseURL string
	// DBMaxConns is DB_MAX_CONNS, the pgx pool size (1..100).
	DBMaxConns int
	// MediaDir is MEDIA_DIR, the persistent directory for exercise images.
	MediaDir string
	// MediaBaseURL is MEDIA_BASE_URL without a trailing slash; image URLs are
	// MediaBaseURL + "/media/exercises/{id}/{hash}.{ext}".
	MediaBaseURL string
	// TrustedProxyCIDRs is TRUSTED_PROXY_CIDRS: only peers inside these ranges
	// may set X-Forwarded-For. Empty (not nil-checked) means trust no proxy.
	TrustedProxyCIDRs []netip.Prefix
	// TokenTTL is TOKEN_TTL_DAYS as a duration.
	TokenTTL time.Duration
	// LoginMaxFailsUser is LOGIN_MAX_FAILS_USER: failed logins per username
	// in the window before that username is locked.
	LoginMaxFailsUser int
	// LoginMaxFailsIP is LOGIN_MAX_FAILS_IP: failed logins per client IP in the
	// window before that IP is locked.
	LoginMaxFailsIP int
	// LoginLock is LOGIN_LOCK_MINUTES as a duration.
	LoginLock time.Duration
	// Argon2MemoryKiB, Argon2Time and Argon2Parallelism are ARGON2_MEMORY_KIB,
	// ARGON2_TIME and ARGON2_PARALLELISM. Their types match the arguments of
	// argon2.IDKey so callers pass them straight through.
	Argon2MemoryKiB   uint32
	Argon2Time        uint32
	Argon2Parallelism uint8
	// Argon2MaxConcurrent is ARGON2_MAX_CONCURRENT, the cap on parallel hashes.
	Argon2MaxConcurrent int
	// LogLevel is LOG_LEVEL.
	LogLevel slog.Level
}

// Error is returned by Parse and Load when the environment is invalid. It
// lists every problem found, in variable order.
type Error struct {
	Problems []string
}

// Error joins all problems into one line, e.g.
// `config: DATABASE_URL is required; DB_MAX_CONNS must be a positive integer, got "abc"`.
func (e *Error) Error() string {
	return "config: " + strings.Join(e.Problems, "; ")
}

// Load reads and validates the configuration from the process environment.
func Load() (*Config, error) {
	return Parse(os.LookupEnv)
}

// Parse builds a Config from lookup, which has the shape of os.LookupEnv.
// Surrounding whitespace is trimmed and a blank value counts as unset (so it
// takes the default, or is reported missing when required). The one exception
// is TRUSTED_PROXY_CIDRS, where a set-but-blank value means "trust no proxy".
//
// On any problem it returns a nil Config and an *Error listing all of them.
func Parse(lookup func(string) (string, bool)) (*Config, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	p := &parser{lookup: lookup}
	cfg := &Config{}

	cfg.Env = p.env()
	cfg.HTTPAddr = p.httpAddr()
	cfg.DatabaseURL = p.databaseURL()
	cfg.DBMaxConns = int(p.integer("DB_MAX_CONNS", defaultDBMaxConns, minDBMaxConns, maxDBMaxConns))
	cfg.MediaDir = p.mediaDir(cfg.Env)
	cfg.MediaBaseURL = p.mediaBaseURL(cfg.Env, cfg.HTTPAddr)
	cfg.TrustedProxyCIDRs = p.trustedProxies()
	cfg.TokenTTL = time.Duration(p.integer("TOKEN_TTL_DAYS", domain.DefaultTokenTTLDays, 1, maxTokenTTLDays)) * 24 * time.Hour
	cfg.LoginMaxFailsUser = int(p.integer("LOGIN_MAX_FAILS_USER", defaultLoginMaxFailsUser, 1, noMax))
	cfg.LoginMaxFailsIP = int(p.integer("LOGIN_MAX_FAILS_IP", defaultLoginMaxFailsIP, 1, noMax))
	cfg.LoginLock = time.Duration(p.integer("LOGIN_LOCK_MINUTES", defaultLoginLockMinutes, 1, maxLoginLockMins)) * time.Minute
	cfg.Argon2MemoryKiB = uint32(p.integer("ARGON2_MEMORY_KIB", defaultArgon2MemoryKiB, minArgon2MemKiB, math.MaxUint32))
	cfg.Argon2Time = uint32(p.integer("ARGON2_TIME", defaultArgon2Time, 1, math.MaxUint32))
	cfg.Argon2Parallelism = uint8(p.integer("ARGON2_PARALLELISM", defaultArgon2Parallelism, 1, maxArgon2Parallel))
	cfg.Argon2MaxConcurrent = int(p.integer("ARGON2_MAX_CONCURRENT", defaultArgon2MaxConcurrent, 1, noMax))
	cfg.LogLevel = p.logLevel()

	if len(p.problems) > 0 {
		return nil, &Error{Problems: p.problems}
	}
	return cfg, nil
}

// parser accumulates problems while reading variables. Each method returns a
// usable (default or zero) value even after recording a problem, so all
// variables are always examined.
type parser struct {
	lookup   func(string) (string, bool)
	problems []string
}

func (p *parser) fail(format string, args ...any) {
	p.problems = append(p.problems, fmt.Sprintf(format, args...))
}

// get returns the trimmed value of name; ok is false when it is unset or blank.
func (p *parser) get(name string) (string, bool) {
	v, ok := p.lookup(name)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// integer reads an integer in [lo, hi], or def when unset.
func (p *parser) integer(name string, def, lo, hi int64) int64 {
	raw, ok := p.get(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// Every integer variable has lo >= 1, so "positive" is always right.
		p.fail("%s must be a positive integer, got %q", name, raw)
		return def
	}
	if n < lo || n > hi {
		switch {
		case hi >= noMax && lo == 1:
			p.fail("%s must be a positive integer, got %d", name, n)
		case hi >= noMax:
			p.fail("%s must be at least %d, got %d", name, lo, n)
		default:
			p.fail("%s must be between %d and %d, got %d", name, lo, hi, n)
		}
		return def
	}
	return n
}

func (p *parser) env() Env {
	raw, ok := p.get("APP_ENV")
	if !ok {
		return EnvProduction
	}
	switch e := Env(strings.ToLower(raw)); e {
	case EnvProduction, EnvDevelopment:
		return e
	}
	p.fail("APP_ENV must be %q or %q, got %q", EnvProduction, EnvDevelopment, raw)
	// Keep validating the rest under the strict rules.
	return EnvProduction
}

func (p *parser) httpAddr() string {
	raw, ok := p.get("HTTP_ADDR")
	if !ok {
		return defaultHTTPAddr
	}
	_, port, err := net.SplitHostPort(raw)
	if err != nil {
		p.fail("HTTP_ADDR must be host:port, e.g. %s, got %q", defaultHTTPAddr, raw)
		return raw
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		p.fail("HTTP_ADDR port must be a number between 0 and 65535, got %q", raw)
	}
	return raw
}

// databaseURL never echoes the value in a problem: it carries the password.
func (p *parser) databaseURL() string {
	raw, ok := p.get("DATABASE_URL")
	if !ok {
		p.fail("DATABASE_URL is required")
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		// err's text embeds the URL, so it is deliberately dropped.
		p.fail("DATABASE_URL is not a valid URL (percent-encode special characters in the password)")
		return raw
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		p.fail("DATABASE_URL must be a postgres:// or postgresql:// URL")
	}
	return raw
}

func (p *parser) mediaDir(env Env) string {
	raw, ok := p.get("MEDIA_DIR")
	if !ok {
		p.fail("MEDIA_DIR is required")
		return ""
	}
	dir := filepath.Clean(raw)
	if env.IsProduction() && !filepath.IsAbs(dir) {
		p.fail("MEDIA_DIR must be an absolute path in production, got %q", raw)
	}
	return dir
}

func (p *parser) mediaBaseURL(env Env, httpAddr string) string {
	raw, ok := p.get("MEDIA_BASE_URL")
	if !ok {
		if env.IsProduction() {
			p.fail("MEDIA_BASE_URL is required in production (e.g. https://api.example.com)")
			return ""
		}
		return devMediaBaseURL(httpAddr)
	}

	u, err := url.Parse(raw)
	switch {
	case err != nil:
		// Not echoed: it may have carried credentials.
		p.fail("MEDIA_BASE_URL must be an absolute http(s) URL, e.g. https://api.example.com")
	case u.User != nil:
		p.fail("MEDIA_BASE_URL must not contain credentials")
	case u.Scheme != "https" && (u.Scheme != "http" || env.IsProduction()):
		p.fail("MEDIA_BASE_URL must start with %s, got %q", schemeHint(env), raw)
	case u.Host == "" || u.Hostname() == "":
		p.fail("MEDIA_BASE_URL must include a host, got %q", raw)
	case u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "?") || strings.Contains(raw, "#"):
		p.fail("MEDIA_BASE_URL must not contain a query or fragment, got %q", raw)
	}
	return strings.TrimRight(raw, "/")
}

// schemeHint names the URL schemes MEDIA_BASE_URL may use in env.
func schemeHint(env Env) string {
	if env.IsProduction() {
		return "https://"
	}
	return "http:// or https://"
}

// devMediaBaseURL is the development default: the server's own address. It
// returns "" if addr is not a valid host:port (HTTP_ADDR is reported already).
func devMediaBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// trustedProxies parses a comma-separated list of CIDRs or bare IPs. Unset
// means the default (loopback); set but blank means no trusted proxy at all.
func (p *parser) trustedProxies() []netip.Prefix {
	raw, ok := p.lookup("TRUSTED_PROXY_CIDRS")
	if !ok {
		return []netip.Prefix{netip.MustParsePrefix(defaultTrustedProxyCIDR)}
	}
	out := []netip.Prefix{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := parsePrefix(item)
		switch {
		case err != nil:
			p.fail("TRUSTED_PROXY_CIDRS entry %q is not a valid CIDR (e.g. 127.0.0.1/32) or IP address", item)
		case prefix.Bits() == 0:
			p.fail("TRUSTED_PROXY_CIDRS entry %q would trust every client; use a narrower range", item)
		default:
			out = append(out, prefix)
		}
	}
	return out
}

// parsePrefix accepts "10.0.0.0/8" or a bare address ("127.0.0.1", meaning
// /32 or /128) and returns the masked prefix.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("zone not allowed")
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func (p *parser) logLevel() slog.Level {
	raw, ok := p.get("LOG_LEVEL")
	if !ok {
		return defaultLogLevel
	}
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	p.fail("LOG_LEVEL must be one of debug, info, warn, error, got %q", raw)
	return defaultLogLevel
}

const (
	redactedPassword = "REDACTED"
	redactedURL      = "[redacted]"
)

// sensitiveQueryKeys are libpq URL parameters that carry secrets.
var sensitiveQueryKeys = []string{"password", "sslpassword"}

// RedactURL returns raw with the password replaced by REDACTED, for logs. The
// password may sit in the userinfo (postgres://user:secret@host/db) or in the
// query (?password=secret). If raw cannot be parsed at all the whole value is
// replaced, since nothing about it can be trusted to be safe.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redactedURL
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), redactedPassword)
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for _, key := range sensitiveQueryKeys {
			if q.Has(key) {
				q.Set(key, redactedPassword)
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

// attrs lists every setting in a log-safe form (secrets redacted). Keys are the
// lower-cased variable names.
func (c *Config) attrs() []slog.Attr {
	proxies := make([]string, len(c.TrustedProxyCIDRs))
	for i, prefix := range c.TrustedProxyCIDRs {
		proxies[i] = prefix.String()
	}
	return []slog.Attr{
		slog.String("app_env", string(c.Env)),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("database_url", RedactURL(c.DatabaseURL)),
		slog.Int("db_max_conns", c.DBMaxConns),
		slog.String("media_dir", c.MediaDir),
		slog.String("media_base_url", c.MediaBaseURL),
		slog.Any("trusted_proxy_cidrs", proxies),
		slog.Duration("token_ttl", c.TokenTTL),
		slog.Int("login_max_fails_user", c.LoginMaxFailsUser),
		slog.Int("login_max_fails_ip", c.LoginMaxFailsIP),
		slog.Duration("login_lock", c.LoginLock),
		slog.Uint64("argon2_memory_kib", uint64(c.Argon2MemoryKiB)),
		slog.Uint64("argon2_time", uint64(c.Argon2Time)),
		slog.Uint64("argon2_parallelism", uint64(c.Argon2Parallelism)),
		slog.Int("argon2_max_concurrent", c.Argon2MaxConcurrent),
		slog.String("log_level", c.LogLevel.String()),
	}
}

// LogValue implements slog.LogValuer: logging a *Config emits a group of all
// settings with the DATABASE_URL password redacted.
func (c *Config) LogValue() slog.Value {
	if c == nil {
		return slog.StringValue("<nil>")
	}
	return slog.GroupValue(c.attrs()...)
}

// String returns the same redacted key=value listing as LogValue, so printing
// a *Config with %v or %s cannot leak the database password.
func (c *Config) String() string {
	if c == nil {
		return "<nil>"
	}
	attrs := c.attrs()
	parts := make([]string, len(attrs))
	for i, a := range attrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, " ")
}

// GoString covers %#v with the same redacted output.
func (c *Config) GoString() string { return c.String() }
