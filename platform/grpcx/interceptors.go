package grpcx

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

// Metadata keys used by the interceptors.
const (
	RequestIDHeader     = "x-request-id"
	AuthorizationHeader = "authorization"
)

const (
	healthService     = "/grpc.health.v1.Health/"
	reflectionService = "/grpc.reflection."
)

// RateLimitFunc decides whether a call may proceed. fullMethod is
// "/package.Service/Method". The claims of an authenticated caller are in
// ctx (auth.SubjectFromContext).
type RateLimitFunc func(ctx context.Context, fullMethod string) (ratelimit.Decision, error)

// RateLimitBySubject returns a RateLimitFunc that gives each user one
// budget under limit, and every unauthenticated caller of a public method
// one shared budget. Agent runs count against their user, so an agent
// cannot multiply a user's allowance.
func RateLimitBySubject(l ratelimit.Limiter, limit ratelimit.Limit) RateLimitFunc {
	return func(ctx context.Context, _ string) (ratelimit.Decision, error) {
		key := "anonymous"
		if s, ok := auth.SubjectFromContext(ctx); ok {
			key = "user:" + s.UserID
		}
		return l.Allow(ctx, key, limit, 1)
	}
}

type interceptors struct {
	logger         *slog.Logger
	verifier       auth.TokenVerifier
	public         map[string]bool
	defaultTimeout time.Duration
	rateLimit      RateLimitFunc
}

func (in *interceptors) isPublic(method string) bool {
	return in.public[method] || strings.HasPrefix(method, healthService) || strings.HasPrefix(method, reflectionService)
}

// withRequestID is step 1.
func withRequestID(ctx context.Context) context.Context {
	incoming := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(RequestIDHeader); len(v) > 0 {
			incoming = v[0]
		}
	}
	id := logging.RequestIDOrNew(incoming)
	// A trailer, not a header: once a client has received response headers
	// the call is committed and gRPC will not retry it, so echoing the ID in
	// headers would silently disable retries for every failed call.
	grpc.SetTrailer(ctx, metadata.Pairs(RequestIDHeader, id))
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("skein.request_id", id))
	return logging.WithRequestID(ctx, id)
}

// recoverPanic is step 2. It must be deferred directly by the interceptor.
func (in *interceptors) recoverPanic(ctx context.Context, method string, start time.Time, err *error) {
	p := recover()
	if p == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.RecordError(fmt.Errorf("panic: %v", p))
	span.SetStatus(otelcodes.Error, "panic")
	in.logger.ErrorContext(ctx, "grpc call panicked",
		slog.String("rpc.method", method),
		slog.String("code", codes.Internal.String()),
		slog.Float64("duration_ms", millis(time.Since(start))),
		slog.String("panic", fmt.Sprint(p)),
		slog.String("stack", string(debug.Stack())))
	*err = status.Error(codes.Internal, "internal error")
}

// serverFault reports whether a code means the server, not the caller, is
// at fault.
func serverFault(c codes.Code) bool {
	switch c {
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// logCall is step 3.
func (in *interceptors) logCall(ctx context.Context, method string, start time.Time, err error) {
	code := status.Code(err)
	level := slog.LevelInfo
	switch {
	case serverFault(code):
		level = slog.LevelError
	case strings.HasPrefix(method, healthService):
		level = slog.LevelDebug
	}
	attrs := []slog.Attr{
		slog.String("rpc.method", method),
		slog.String("code", code.String()),
		slog.Float64("duration_ms", millis(time.Since(start))),
	}
	if err != nil && code != codes.OK {
		attrs = append(attrs, slog.String("error", status.Convert(err).Message()))
	}
	in.logger.LogAttrs(ctx, level, "grpc call", attrs...)
}

func millis(d time.Duration) float64 { return math.Round(float64(d.Microseconds())) / 1000 }

// bearerToken extracts the token from "authorization: Bearer <token>".
func bearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get(AuthorizationHeader) {
		scheme, token, found := strings.Cut(v, " ")
		if found && strings.EqualFold(scheme, "bearer") {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// authenticate is step 4.
func (in *interceptors) authenticate(ctx context.Context, method string) (context.Context, error) {
	public := in.isPublic(method)
	token := bearerToken(ctx)
	if token == "" {
		if public {
			return ctx, nil
		}
		return ctx, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	if in.verifier == nil {
		if public {
			return ctx, nil
		}
		return ctx, status.Error(codes.Unauthenticated, "authentication is not configured")
	}
	claims, err := in.verifier.Verify(ctx, token)
	if err != nil {
		if public {
			return ctx, nil
		}
		// The reason is safe to return: it says why the caller's own token
		// failed (expired tells the client to refresh), not anything about
		// the server.
		return ctx, status.Error(codes.Unauthenticated, err.Error())
	}
	ctx = auth.WithClaims(ctx, claims, token)
	return logging.WithSubject(ctx, claims.Subject.UserID, claims.Subject.AgentRunID), nil
}

// withDefaultDeadline is step 5.
func (in *interceptors) withDefaultDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || in.defaultTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, in.defaultTimeout)
}

// checkRateLimit is step 6.
func (in *interceptors) checkRateLimit(ctx context.Context, method string) error {
	if in.rateLimit == nil || strings.HasPrefix(method, healthService) {
		return nil
	}
	d, err := in.rateLimit(ctx, method)
	if err != nil {
		in.logger.WarnContext(ctx, "rate limiter failed; allowing call", slog.String("rpc.method", method), slog.String("error", err.Error()))
		return nil
	}
	if d.Allowed {
		return nil
	}
	return ResourceExhausted(d.RetryAfter)
}

// ResourceExhausted returns the error for a rate-limited call: code
// ResourceExhausted with a RetryInfo detail carrying retryAfter.
func ResourceExhausted(retryAfter time.Duration) error {
	st := status.New(codes.ResourceExhausted, "rate limit exceeded")
	withDetail, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)})
	if err != nil {
		return st.Err()
	}
	return withDetail.Err()
}

// RetryAfter returns the RetryInfo delay carried by err, if any.
func RetryAfter(err error) (time.Duration, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return 0, false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.RetryInfo); ok && info.GetRetryDelay() != nil {
			return info.GetRetryDelay().AsDuration(), true
		}
	}
	return 0, false
}

func (in *interceptors) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	start := time.Now()
	ctx = withRequestID(ctx)
	defer in.recoverPanic(ctx, info.FullMethod, start, &err)

	// Logging wraps the inner steps but logs with the context they built,
	// so the line carries the authenticated user.
	resp, logCtx, err := in.unaryInner(ctx, req, info, handler)
	in.logCall(logCtx, info.FullMethod, start, err)
	return resp, err
}

func (in *interceptors) unaryInner(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, context.Context, error) {
	ctx, err := in.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, ctx, err
	}
	callCtx, cancel := in.withDefaultDeadline(ctx)
	defer cancel()
	if err := in.checkRateLimit(callCtx, info.FullMethod); err != nil {
		return nil, ctx, err
	}
	resp, err := handler(callCtx, req)
	return resp, ctx, err
}

// contextStream replaces a server stream's context.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }

func (in *interceptors) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	start := time.Now()
	ctx := withRequestID(ss.Context())
	defer in.recoverPanic(ctx, info.FullMethod, start, &err)

	logCtx, err := in.streamInner(ctx, srv, ss, info, handler)
	in.logCall(logCtx, info.FullMethod, start, err)
	return err
}

func (in *interceptors) streamInner(ctx context.Context, srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (context.Context, error) {
	ctx, err := in.authenticate(ctx, info.FullMethod)
	if err != nil {
		return ctx, err
	}
	if err := in.checkRateLimit(ctx, info.FullMethod); err != nil {
		return ctx, err
	}
	return ctx, handler(srv, &contextStream{ServerStream: ss, ctx: ctx})
}

// outgoing adds the request ID and the caller's bearer token to an
// outgoing call, unless the caller already set them.
func outgoing(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	var pairs []string
	if id := logging.RequestID(ctx); id != "" && len(md.Get(RequestIDHeader)) == 0 {
		pairs = append(pairs, RequestIDHeader, id)
	}
	if token, ok := auth.TokenFromContext(ctx); ok && len(md.Get(AuthorizationHeader)) == 0 {
		pairs = append(pairs, AuthorizationHeader, "Bearer "+token)
	}
	if len(pairs) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

func unaryClient(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoker(outgoing(ctx), method, req, reply, cc, opts...)
}

func streamClient(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return streamer(outgoing(ctx), desc, cc, method, opts...)
}

func toSet(methods []string) map[string]bool {
	set := make(map[string]bool, len(methods))
	for _, m := range methods {
		set[m] = true
	}
	return set
}
