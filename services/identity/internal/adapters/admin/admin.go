// Package admin serves identity's admin HTTP port (IDENTITY_ADMIN_ADDR,
// default :8080): the public key set other services verify access tokens
// with, and liveness and readiness probes. It is not routed by the gateway;
// verifiers inside the cluster fetch the key set from it directly.
//
//	GET /.well-known/jwks.json   JWKS (RFC 7517) of every published key:
//	                             staged, active and retiring. Cache-Control
//	                             max-age is the JWKS cache period C of the
//	                             rotation procedure (package keys), 5 minutes.
//	GET /livez                   200 while the process runs.
//	GET /readyz                  200 when Ready returns nil (database
//	                             reachable), 503 otherwise.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
)

// JWKSPath is where the key set is served.
const JWKSPath = "/.well-known/jwks.json"

// JWKSCachePeriod is how long verifiers may cache the key set; the
// rotation procedure waits at least this long between staging and
// activating a key.
const JWKSCachePeriod = 5 * time.Minute

// KeySet publishes keys.
type KeySet interface {
	JWKS() auth.JWKS
}

// Handler returns the admin routes.
func Handler(keys KeySet, ready func(context.Context) error, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+JWKSPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(JWKSCachePeriod.Seconds())))
		if err := json.NewEncoder(w).Encode(keys.JWKS()); err != nil {
			log.Warn("write jwks", "error", err)
		}
	})
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if err := ready(ctx); err != nil {
				log.WarnContext(ctx, "not ready", "error", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
