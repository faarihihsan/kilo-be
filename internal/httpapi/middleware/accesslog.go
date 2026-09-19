package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// AccessLog logs one line per request when it finishes: method, path (no
// query string), route pattern, status, duration_ms, response bytes,
// request_id and, when the request was authenticated, user_id.
//
// It never logs request bodies, query strings or headers, so tokens and
// passwords cannot leak through it. A request that ends in a panic is logged
// as 500; the Recover middleware outside sends the response.
//
// It runs before the authenticator, so the user id reaches it through the
// context holder that WithPrincipal fills in.
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

			completed := false
			defer func() {
				status := rec.status
				if !rec.wroteHeader {
					status = http.StatusOK
					if !completed {
						status = http.StatusInternalServerError
					}
				}
				attrs := make([]slog.Attr, 0, 9)
				attrs = append(attrs,
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
				if r.Pattern != "" {
					attrs = append(attrs, slog.String("route", r.Pattern))
				}
				attrs = append(attrs,
					slog.Int("status", status),
					slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
					slog.Int64("bytes", rec.bytes),
				)
				if id := RequestIDFrom(r.Context()); id != "" {
					attrs = append(attrs, slog.String("request_id", id))
				}
				if p := holder.p.Load(); p != nil {
					attrs = append(attrs, slog.String("user_id", p.UserID.String()))
				}
				level := slog.LevelInfo
				if status >= http.StatusInternalServerError {
					level = slog.LevelError
				}
				logger.LogAttrs(r.Context(), level, "http request", attrs...)
			}()

			next.ServeHTTP(rec, r)
			completed = true
		})
	}
}
