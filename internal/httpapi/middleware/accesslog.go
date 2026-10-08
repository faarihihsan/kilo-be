package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AccessLog writes two lines per request:
//
//	Request = method:GET, uri:/v1/exercises Request payload: {...}
//	Response = time:2 ms, Payload:{...}
//
// The uri keeps its query string and both payloads are the request and
// response bodies (JSON only), with password and token fields redacted to
// "***" (see redactPayload and redactedURI). Request bodies are captured as
// the handler reads them, so nothing is buffered twice and the per-route size
// limit still applies. The status is logged as an attribute, and the caller's
// user id when the request was authenticated. A request that ends in a panic
// is logged as a 500; the Recover middleware outside sends the response.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			holder := &callerHolder{}
			r = r.WithContext(context.WithValue(r.Context(), callerKey{}, holder))
			rec := recordResponse(w)

			reqCap := newCaptureWriter(maxLoggedPayload)
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = &teeReadCloser{Reader: io.TeeReader(r.Body, reqCap), Closer: r.Body}
			}

			completed := false
			defer func() {
				status := rec.status
				if !rec.wroteHeader {
					status = http.StatusOK
					if !completed {
						status = http.StatusInternalServerError
					}
				}
				ctx := r.Context()
				reqAttrs := []slog.Attr{}
				if id := RequestIDFrom(ctx); id != "" {
					reqAttrs = append(reqAttrs, slog.String("request_id", id))
				}
				if p := holder.p.Load(); p != nil {
					reqAttrs = append(reqAttrs, slog.String("user_id", p.UserID.String()))
				}
				logger.LogAttrs(ctx, slog.LevelInfo, requestLine(r, reqCap), reqAttrs...)

				level := slog.LevelInfo
				if status >= http.StatusInternalServerError {
					level = slog.LevelError
				}
				respAttrs := []slog.Attr{slog.Int("status", status)}
				if id := RequestIDFrom(ctx); id != "" {
					respAttrs = append(respAttrs, slog.String("request_id", id))
				}
				logger.LogAttrs(ctx, level, responseLine(time.Since(start), rec), respAttrs...)
			}()

			next.ServeHTTP(rec, r)
			completed = true
		})
	}
}

// requestLine builds the "Request = ..." message.
func requestLine(r *http.Request, body *captureWriter) string {
	var b strings.Builder
	b.WriteString("Request = method:")
	b.WriteString(r.Method)
	b.WriteString(", uri:")
	b.WriteString(redactedURI(r.URL))
	if payload, ok := redactPayload(body, r.Header.Get("Content-Type")); ok {
		b.WriteString(" Request payload: ")
		b.WriteString(payload)
	}
	return b.String()
}

// responseLine builds the "Response = ..." message.
func responseLine(d time.Duration, rec *responseRecorder) string {
	var b strings.Builder
	b.WriteString("Response = time:")
	b.WriteString(strconv.FormatInt(d.Milliseconds(), 10))
	b.WriteString(" ms, Payload:")
	if payload, ok := redactPayload(rec.body(), rec.Header().Get("Content-Type")); ok {
		b.WriteString(payload)
	}
	return b.String()
}

// redactedURI renders the request URI, replacing the value of every query
// parameter whose name is sensitive (password, token, ...) with "***". The
// order of the parameters is preserved.
func redactedURI(u *url.URL) string {
	uri := u.EscapedPath()
	if u.RawQuery == "" {
		return uri
	}
	parts := strings.Split(u.RawQuery, "&")
	for i, p := range parts {
		key, _, _ := strings.Cut(p, "=")
		if name, err := url.QueryUnescape(key); err == nil && sensitiveKey(name) {
			parts[i] = key + "=***"
		}
	}
	return uri + "?" + strings.Join(parts, "&")
}
