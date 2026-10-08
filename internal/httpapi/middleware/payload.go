package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// maxLoggedPayload caps how many bytes of a request or response body are kept
// for logging, so a large or hostile body cannot exhaust memory. A body over
// the cap is logged as a placeholder instead of its content.
const maxLoggedPayload = 64 << 10 // 64 KiB

// captureWriter collects the first max bytes written to it and counts the
// rest, so callers can tell a truncated body from a complete one.
type captureWriter struct {
	buf  bytes.Buffer
	max  int
	seen int
}

func newCaptureWriter(max int) *captureWriter { return &captureWriter{max: max} }

func (c *captureWriter) Write(p []byte) (int, error) {
	c.seen += len(p)
	if room := c.max - c.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	return len(p), nil
}

// teeReadCloser passes reads through to the handler while capturing a copy.
type teeReadCloser struct {
	io.Reader
	io.Closer
}

// redactPayload renders a captured body for the log, replacing the values of
// password and token fields (and Authorization) with "***". A body that is not
// valid JSON is scrubbed with a regular expression so secrets cannot slip
// through, and one over the capture cap is never printed in full.
func redactPayload(c *captureWriter, contentType string) (string, bool) {
	if c == nil || c.seen == 0 || !strings.Contains(contentType, "json") {
		return "", false
	}
	raw := bytes.TrimSpace(c.buf.Bytes())
	if len(raw) == 0 {
		return "", false
	}
	if c.seen > c.max {
		return `"[payload exceeds 64 KiB]"`, true
	}
	return redactJSON(raw), true
}

func redactJSON(raw []byte) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return scrubJSON(raw)
	}
	redactValue(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return scrubJSON(raw)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if sensitiveKey(k) {
				t[k] = "***"
				continue
			}
			redactValue(val)
		}
	case []any:
		for _, e := range t {
			redactValue(e)
		}
	}
}

// jsonSecretRE finds "key": value pairs whose key names a password or token.
var jsonSecretRE = regexp.MustCompile(
	`(?i)"(password|[a-z0-9_]*token|authorization|secret)"\s*:\s*("(?:\\.|[^"\\])*"|null|true|false|-?\d+(?:\.\d+)?)`)

func scrubJSON(raw []byte) string {
	return jsonSecretRE.ReplaceAllString(string(raw), `"$1":"***"`)
}

// sensitiveKey reports whether a JSON key names a secret whose value must not
// be logged. Underscores, dashes and spaces are ignored so access_token,
// accessToken and access-token all match.
func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.NewReplacer("_", "", "-", "", " ", "").Replace(k)
	if strings.Contains(k, "password") {
		return true
	}
	switch k {
	case "authorization", "token", "accesstoken", "refreshtoken",
		"bearertoken", "authtoken", "secret", "clientsecret", "apikey":
		return true
	}
	return false
}
