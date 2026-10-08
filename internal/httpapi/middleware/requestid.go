package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"workout-tracker-be/internal/httpapi/render"
	"workout-tracker-be/internal/logging"
)

// RequestIDHeader carries the request id, inbound and outbound.
const RequestIDHeader = "X-Request-Id"

const maxRequestIDLen = 64

type requestIDKey struct{}

// RequestID gives every request an id: the caller's X-Request-Id when it is 1
// to 64 characters from [A-Za-z0-9._-], otherwise a fresh UUID v7. The id goes
// into the response header, the request context (RequestIDFrom) and the
// request-scoped logger (render.LoggerFrom) as attribute request_id. logger
// is the base logger; nil means slog.Default().
func RequestID(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID(id) {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			// The trace id makes every log line written with this context
			// show the same trace-id, whatever logger is used.
			ctx = logging.WithTraceID(ctx, id)
			ctx = render.WithLogger(ctx, logger.With(slog.String("request_id", id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFrom returns the id RequestID assigned, or "" outside that
// middleware.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return id.String()
}
