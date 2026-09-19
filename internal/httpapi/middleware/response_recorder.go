package middleware

import (
	"net/http"
)

// responseRecorder wraps a ResponseWriter to remember the status and the number of
// body bytes written. Optional interfaces (Flush, Hijack, ...) stay reachable
// through Unwrap, which http.ResponseController uses.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

// recordResponse returns w as a *responseRecorder, reusing it when an outer
// middleware already wrapped it.
func recordResponse(w http.ResponseWriter) *responseRecorder {
	if rec, ok := w.(*responseRecorder); ok {
		return rec
	}
	return &responseRecorder{ResponseWriter: w}
}

func (r *responseRecorder) WriteHeader(code int) {
	// 1xx responses are interim; the final status comes later.
	if !r.wroteHeader && (code < 100 || code > 199 || code == http.StatusSwitchingProtocols) {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush lets handlers that assert http.Flusher keep streaming.
func (r *responseRecorder) Flush() {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
