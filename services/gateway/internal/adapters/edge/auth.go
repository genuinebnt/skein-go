package edge

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// UserIDMetadataKey is the gRPC metadata key carrying the verified user ID
// to backends that read it instead of verifying the token themselves (the
// agent service).
const UserIDMetadataKey = "x-skein-user-id"

// BearerToken returns the token from "Authorization: Bearer <token>", or
// "" when there is none.
func BearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// VerifyBearer verifies token and returns ctx carrying its claims (see the
// package documentation). The error is UNAUTHENTICATED with an ErrorInfo
// reason.
func VerifyBearer(ctx context.Context, v auth.TokenVerifier, token string) (context.Context, auth.Claims, error) {
	if token == "" {
		return ctx, auth.Claims{}, Unauthenticated(ReasonTokenInvalid, "missing bearer token")
	}
	claims, err := v.Verify(ctx, token)
	if err != nil {
		reason := ReasonTokenInvalid
		if errors.Is(err, auth.ErrExpired) {
			reason = ReasonTokenExpired
		}
		// The verifier's message is about the caller's own token, never
		// about the server, so it is safe to return.
		return ctx, auth.Claims{}, Unauthenticated(reason, err.Error())
	}
	ctx = auth.WithClaims(ctx, claims, token)
	return logging.WithSubject(ctx, claims.Subject.UserID, claims.Subject.AgentRunID), claims, nil
}

// OutgoingSubject returns metadata naming the verified caller, for backend
// calls the gateway makes itself (WebSocket streams). grpc-gateway calls
// get the same through the mux's metadata annotator.
func OutgoingSubject(ctx context.Context) context.Context {
	if s, ok := auth.SubjectFromContext(ctx); ok {
		return metadata.AppendToOutgoingContext(ctx, UserIDMetadataKey, s.UserID)
	}
	return ctx
}

// SubjectMetadata is the annotator for grpc-gateway: the verified user ID,
// or nothing for anonymous calls.
func SubjectMetadata(ctx context.Context, _ *http.Request) metadata.MD {
	if s, ok := auth.SubjectFromContext(ctx); ok {
		return metadata.Pairs(UserIDMetadataKey, s.UserID)
	}
	return nil
}

// ClientIPFunc finds the client's address for a request.
type ClientIPFunc func(r *http.Request) netip.Addr

// ClientIPBehind returns a ClientIPFunc that trusts X-Forwarded-For from
// the given proxies only (domain.ClientIP). An unparseable peer, which a
// real listener never produces, counts as the unspecified address.
func ClientIPBehind(trusted []netip.Prefix) ClientIPFunc {
	return func(r *http.Request) netip.Addr {
		addr, ok := domain.ClientIP(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), trusted)
		if !ok {
			return netip.IPv4Unspecified()
		}
		return addr
	}
}
