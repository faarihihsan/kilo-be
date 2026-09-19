package middleware

import (
	"fmt"
	"net/http"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/render"
)

// BodyLimit caps the request body at max bytes; max <= 0 means
// domain.MaxBodyBytes (1 MiB). A declared Content-Length over the limit is
// answered with 413 at once, before the handler runs. Otherwise the body is
// wrapped in http.MaxBytesReader, so reading past the limit fails with
// *http.MaxBytesError, which render.WriteError and render.DecodeJSON turn into
// the same 413.
func BodyLimit(max int64) func(http.Handler) http.Handler {
	if max <= 0 {
		max = domain.MaxBodyBytes
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				render.WriteError(w, r, &domain.PayloadTooLargeError{
					Message: fmt.Sprintf("The request body must not exceed %d bytes.", max),
				})
				return
			}
			// Leave an absent body alone so handlers can still tell it is absent.
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
