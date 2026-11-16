package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

// Authenticate requires "Authorization: Bearer <token>" and puts the
// verified claims in the request context, from where grpcx clients forward
// the token to the services. WebSocket upgrades from browsers cannot set
// headers; the gateway passes their token another way and calls the
// verifier itself.
func Authenticate(v auth.TokenVerifier, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			token = strings.TrimSpace(token)
			if !strings.EqualFold(scheme, "bearer") || token == "" {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				WriteError(w, r, log, status.Error(codes.Unauthenticated, "missing bearer token"))
				return
			}
			claims, err := v.Verify(r.Context(), token)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				WriteError(w, r, log, status.Error(codes.Unauthenticated, err.Error()))
				return
			}
			ctx := auth.WithClaims(r.Context(), claims, token)
			ctx = logging.WithSubject(ctx, claims.Subject.UserID, claims.Subject.AgentRunID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// KeyFunc picks the rate limit key for a request.
type KeyFunc func(r *http.Request) string

// BySubjectOrPeer keys by authenticated user, or by the connecting peer's
// IP for anonymous requests. Behind a proxy the peer is the proxy, so the
// gateway supplies its own KeyFunc that reads X-Forwarded-For from the
// proxies it trusts; trusting that header from anyone would let clients
// pick their own key.
func BySubjectOrPeer(r *http.Request) string {
	if s, ok := auth.SubjectFromContext(r.Context()); ok {
		return "user:" + s.UserID
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// RateLimit enforces limit per key and sets the rate limit headers on
// every response. If the limiter fails, the request proceeds and the
// failure is logged: a Redis outage must not take the site down.
func RateLimit(l ratelimit.Limiter, limit ratelimit.Limit, key KeyFunc, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d, err := l.Allow(r.Context(), key(r), limit, 1)
			if err != nil {
				if log != nil {
					log.WarnContext(r.Context(), "rate limiter failed; allowing request", slog.String("error", err.Error()))
				}
				next.ServeHTTP(w, r)
				return
			}
			WriteRateLimitHeaders(w, d)
			if !d.Allowed {
				WriteError(w, r, log, grpcx.ResourceExhausted(d.RetryAfter))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteRateLimitHeaders sets RateLimit-Limit, RateLimit-Remaining,
// RateLimit-Reset and, for a denial, Retry-After.
func WriteRateLimitHeaders(w http.ResponseWriter, d ratelimit.Decision) {
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.FormatInt(d.Limit, 10))
	h.Set("RateLimit-Remaining", strconv.FormatInt(d.Remaining, 10))
	h.Set("RateLimit-Reset", retryAfterSeconds(d.Reset))
	if !d.Allowed {
		h.Set("Retry-After", retryAfterSeconds(d.RetryAfter))
	}
}
