package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"workout-tracker-be/internal/httpapi/render"
)

// Pinger is what /healthz checks; the database pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// healthTimeout bounds the ping so a hung database cannot hang the probe.
const healthTimeout = 2 * time.Second

type healthResponse struct {
	Status string `json:"status"`
}

// HealthHandler answers 200 {"status":"ok"} while p.Ping succeeds within 2
// seconds and 503 {"status":"unavailable"} otherwise. A nil Pinger only
// reports that the process is serving. The router mounts it from
// Handlers.Healthz.
func HealthHandler(p Pinger) http.HandlerFunc {
	return healthHandler(p, healthTimeout)
}

func healthHandler(p Pinger, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if p != nil {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			if err := p.Ping(ctx); err != nil {
				render.LoggerFrom(r.Context()).WarnContext(r.Context(), "healthz: ping failed", slog.Any("error", err))
				render.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
				return
			}
		}
		render.WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	}
}
