package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"workout-tracker-be/internal/httpapi/render"
)

// Recover turns a panic in a handler into a 500 `internal` JSON response. The
// panic value and stack are logged; the client sees only the generic message.
// It is the outermost middleware. If the response was already started there is
// nothing sensible to send, so it aborts the connection (http.ErrAbortHandler)
// instead of ending a truncated body as if it were complete.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := recordResponse(w)
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v) // the standard way for a handler to abort; not a bug
				}
				attrs := []slog.Attr{
					slog.String("panic", fmt.Sprint(v)),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				}
				// RequestID sits inside Recover, so its id is only visible on
				// the shared response header.
				if id := w.Header().Get(RequestIDHeader); id != "" {
					attrs = append(attrs, slog.String("request_id", id))
				}
				logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered", attrs...)

				if rec.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				render.WriteErrorResponse(rec, http.StatusInternalServerError, render.CodeInternal, render.MsgInternal)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
