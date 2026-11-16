// Package admin serves the documents service's admin HTTP port (8080 by
// default, never exposed by the gateway):
//
//	GET /healthz  200 while the process runs (liveness)
//	GET /readyz   200 when every readiness check passes (database, Redis),
//	              503 with the failing check's name otherwise
//
// Metrics and traces leave over OTLP (platform/telemetry), not here.
package admin

import (
	"context"
	"net/http"
	"time"
)

// Check is one readiness dependency.
type Check struct {
	Name  string
	Ready func(ctx context.Context) error
}

// Handler returns the admin routes.
func Handler(checks ...Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for _, c := range checks {
			if err := c.Ready(ctx); err != nil {
				http.Error(w, c.Name+" not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
