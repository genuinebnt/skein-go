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
//	POST /dev/sessions           only when IDENTITY_DEV_SESSIONS=true (dev
//	                             and test only): body {"userId": "..."},
//	                             answers a SignInResponse for that user
//	                             without a credential. Seed users have no
//	                             passwords; end-to-end tests sign in as them
//	                             this way (docs/local-native.md). 404 for an
//	                             unknown user. The route does not exist
//	                             otherwise, and the gateway never routes the
//	                             admin port.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
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

// DevSessionPath is the test-only session route (package documentation).
const DevSessionPath = "/dev/sessions"

// DevSessions starts sessions without a credential (app.Service).
type DevSessions interface {
	StartDevSession(ctx context.Context, userID string) (domain.User, app.SessionTokens, error)
}

// Handler returns the admin routes. devSessions is nil unless
// IDENTITY_DEV_SESSIONS is on; then DevSessionPath is not served at all.
func Handler(keys KeySet, ready func(context.Context) error, log *slog.Logger, devSessions DevSessions) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	if devSessions != nil {
		mux.HandleFunc("POST "+DevSessionPath, func(w http.ResponseWriter, r *http.Request) {
			serveDevSession(w, r, devSessions, log)
		})
	}
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

func serveDevSession(w http.ResponseWriter, r *http.Request, devSessions DevSessions, log *slog.Logger) {
	var body struct {
		UserID string `json:"userId"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(data, &body) != nil || body.UserID == "" {
		http.Error(w, `body must be {"userId": "<user id>"}`, http.StatusBadRequest)
		return
	}
	user, tokens, err := devSessions.StartDevSession(r.Context(), body.UserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, "no such user", http.StatusNotFound)
		return
	case err != nil:
		log.ErrorContext(r.Context(), "dev session failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := &skeinv1.SignInResponse{
		User: &skeinv1.User{Id: string(user.ID), Email: user.Email, DisplayName: user.DisplayName, CreatedAt: timestamppb.New(user.CreatedAt)},
		Session: &skeinv1.Session{AccessToken: tokens.AccessToken, AccessExpiresAt: timestamppb.New(tokens.AccessExpiresAt),
			RefreshToken: tokens.RefreshToken, RefreshExpiresAt: timestamppb.New(tokens.RefreshExpiresAt)},
	}
	out, err := protojson.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}
