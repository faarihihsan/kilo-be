// Package httpapi assembles the HTTP API: the route table, the middleware
// pipeline and the router. Handlers are supplied from outside through
// Handlers, so this package imports neither services nor handlers.
//
// Import graph (no cycles):
//
//	domain
//	  ^
//	render        (JSON, error mapping, parameter parsing)     -> domain
//	  ^
//	middleware    (recover, request id, access log, body limit,
//	  ^            principal, roles)                           -> domain, render
//	httpapi       (router, route table, health)                -> domain, render, middleware
//	handlers      (later; per-resource handlers)               -> domain, render, middleware
//	cmd/server    (wiring)                                     -> httpapi, handlers
package httpapi

import (
	"log/slog"
	"net/http"

	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/httpapi/middleware"
	"workout-tracker-be/internal/httpapi/render"
)

// RouterConfig is everything NewRouter needs.
type RouterConfig struct {
	// Handlers serve the routes; a nil field answers 501.
	Handlers Handlers
	// Authenticator validates the bearer token and puts the Principal in the
	// request context (middleware.WithPrincipal), or answers 401 itself. It
	// wraps every non-anonymous route. Nil means every such route answers 401.
	Authenticator func(http.Handler) http.Handler
	// RateLimit is the generic per-IP limiter for the routes flagged
	// AuthRateLimited. Nil means no limiting.
	RateLimit func(http.Handler) http.Handler
	// Logger receives access, panic and unexpected-error logs; nil means
	// slog.Default().
	Logger *slog.Logger
}

// NewRouter returns the API handler. The pipeline, outermost first
// (docs/implementation-plan.md section 4):
//
//	Recover -> RequestID -> AccessLog -> route match ->
//	BodyLimit -> RateLimit (flagged routes) -> Authenticator (not anonymous) ->
//	RequireRoles (not anonymous) -> handler
//
// Unknown paths answer 404 `not_found` and known paths with the wrong method
// 405 `method_not_allowed` (with Allow), both in the JSON error format and
// both inside Recover, RequestID and AccessLog.
func NewRouter(cfg RouterConfig) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	authenticate := cfg.Authenticator
	if authenticate == nil {
		authenticate = rejectAll
	}
	rateLimit := cfg.RateLimit
	if rateLimit == nil {
		rateLimit = passThrough
	}
	handlers := cfg.Handlers
	handlers.fillDefaults()

	mux := http.NewServeMux()
	for _, rt := range table {
		var h http.Handler = *rt.field(&handlers)
		if !rt.Anonymous {
			h = middleware.RequireRoles(rt.Roles...)(h)
			h = authenticate(h)
		}
		if rt.AuthRateLimited {
			h = rateLimit(h)
		}
		h = middleware.BodyLimit(rt.MaxBodyBytes)(h)
		mux.Handle(rt.Pattern(), h)
	}

	var root http.Handler = unmatched{mux}
	root = middleware.AccessLog(logger)(root)
	root = middleware.RequestID(logger)(root)
	root = middleware.Recover(logger)(root)
	return root
}

// rejectAll is the default Authenticator: without a real one nobody is
// authenticated.
func rejectAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render.WriteError(w, r, domain.NewUnauthorized())
	})
}

func passThrough(next http.Handler) http.Handler { return next }

// unmatched serves the mux, but answers requests that match no route with the
// JSON error format instead of ServeMux's plain-text 404 and 405.
type unmatched struct{ mux *http.ServeMux }

func (u unmatched) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := u.mux.Handler(r)
	if pattern != "" {
		u.mux.ServeHTTP(w, r)
		return
	}
	// No pattern: ServeMux's own 404 or 405 handler. Run it against a capture
	// to learn which one it is and to reuse the Allow header it computed.
	c := &capture{header: http.Header{}}
	h.ServeHTTP(c, r)
	if c.status == http.StatusMethodNotAllowed {
		if allow := c.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		render.WriteErrorResponse(w, http.StatusMethodNotAllowed, render.CodeMethodNotAllowed,
			"The method is not allowed for this route.")
		return
	}
	render.WriteErrorResponse(w, http.StatusNotFound, render.CodeNotFound, "No such route.")
}

// capture records the status and headers of a response and drops the body.
type capture struct {
	header http.Header
	status int
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}
func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return len(b), nil
}
