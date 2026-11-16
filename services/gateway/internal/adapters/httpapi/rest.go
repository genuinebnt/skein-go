package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/backends"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// RESTConfig configures the REST mux.
type RESTConfig struct {
	Clients     backends.Clients
	Routes      *RouteTable
	Verifier    auth.TokenVerifier
	Throttler   *app.Throttler
	Idempotency *app.Idempotency
	ClientIP    edge.ClientIPFunc
	Logger      *slog.Logger
	// RequestTimeout is every REST call's deadline; default 30 seconds.
	RequestTimeout time.Duration
	// MaxBodyBytes bounds request bodies; default 4 MiB.
	MaxBodyBytes int64
	// MaxStoredResponseBytes bounds responses kept for idempotent replay;
	// larger ones are released instead. Default 1 MiB.
	MaxStoredResponseBytes int
}

const (
	headerIdempotencyKey = "Idempotency-Key"
	headerReplayed       = "Idempotent-Replayed"
)

// NewREST returns the grpc-gateway mux serving every annotated RPC.
//
// Header forwarding is locked down: no incoming header becomes gRPC
// metadata (otherwise "Grpc-Metadata-X-Skein-User-Id" would let a client
// name its own user), and no backend header becomes an HTTP header. The
// backends get exactly the request ID, the verified token, the verified
// user ID and the trace context, all from the request context.
func NewREST(ctx context.Context, cfg RESTConfig) (*runtime.ServeMux, error) {
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 4 << 20
	}
	if cfg.MaxStoredResponseBytes == 0 {
		cfg.MaxStoredResponseBytes = 1 << 20
	}
	p := &policy{cfg: cfg}
	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithOutgoingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithMetadata(edge.SubjectMetadata),
		runtime.WithErrorHandler(func(_ context.Context, _ *runtime.ServeMux, _ runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			edge.WriteError(w, r, cfg.Logger, err)
		}),
		runtime.WithRoutingErrorHandler(routingError(cfg.Logger)),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions:   protojson.MarshalOptions{},
			UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
		}),
		runtime.WithMiddlewares(p.middleware),
	)
	c := cfg.Clients
	for _, err := range []error{
		skeinv1.RegisterIdentityServiceHandlerClient(ctx, mux, c.Identity),
		skeinv1.RegisterDocumentServiceHandlerClient(ctx, mux, c.Documents),
		skeinv1.RegisterSearchServiceHandlerClient(ctx, mux, c.Search),
		skeinv1.RegisterGraphServiceHandlerClient(ctx, mux, c.Graph),
		skeinv1.RegisterMLServiceHandlerClient(ctx, mux, c.ML),
		skeinv1.RegisterAgentServiceHandlerClient(ctx, mux, c.Agent),
		skeinv1.RegisterNotificationServiceHandlerClient(ctx, mux, c.Notifications),
	} {
		if err != nil {
			return nil, err
		}
	}
	return mux, nil
}

// routingError answers paths that are not routes, which includes every
// internal RPC (docs/api/README.md: the gateway refuses any path that is
// not in the OpenAPI document).
func routingError(log *slog.Logger) runtime.RoutingErrorHandlerFunc {
	return func(_ context.Context, _ *runtime.ServeMux, _ runtime.Marshaler, w http.ResponseWriter, r *http.Request, httpStatus int) {
		switch httpStatus {
		case http.StatusMethodNotAllowed:
			edge.WriteError(w, r, log, &runtime.HTTPStatusError{HTTPStatus: httpStatus,
				Err: status.Errorf(codes.Unimplemented, "%s is not allowed on this path", r.Method)})
		case http.StatusNotFound:
			edge.WriteError(w, r, log, status.Error(codes.NotFound, "no such route"))
		default:
			edge.WriteError(w, r, log, status.Error(codes.InvalidArgument, http.StatusText(httpStatus)))
		}
	}
}

// policy applies a route's policy around grpc-gateway's handler, once the
// route is known: authentication, rate limits, deadline, body limit and
// idempotency, in that order (a request over budget never reserves a key).
type policy struct{ cfg RESTConfig }

func (p *policy) middleware(next runtime.HandlerFunc) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, params map[string]string) {
		pattern, ok := runtime.HTTPPattern(r.Context())
		route, known := p.cfg.Routes.Lookup(r.Method, pattern.String())
		if !ok || !known {
			// A route grpc-gateway serves but the table does not know would
			// otherwise run with no policy at all.
			p.cfg.Logger.ErrorContext(r.Context(), "route has no policy; refusing", slog.String("pattern", pattern.String()))
			edge.WriteError(w, r, p.cfg.Logger, status.Error(codes.Internal, "route has no policy"))
			return
		}
		edge.LabelRoute(r.Context(), r.Method, route.Template)

		ctx, err := p.authenticate(r, route.Policy)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			edge.WriteError(w, r, p.cfg.Logger, err)
			return
		}
		userID := ""
		if s, ok := auth.SubjectFromContext(ctx); ok {
			userID = s.UserID
		}
		verdict := p.cfg.Throttler.Check(ctx, route.Policy.Budget, userID, p.cfg.ClientIP(r))
		if verdict.Checked {
			edge.WriteRateLimitHeaders(w, verdict.Decision)
		}
		if verdict.Limited() {
			edge.WriteError(w, r.WithContext(ctx), p.cfg.Logger, edge.RateLimited(verdict.Key, verdict.Decision))
			return
		}

		ctx, cancel := context.WithTimeout(ctx, p.cfg.RequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		// Backends get the token from the context, and only once verified.
		r.Header.Del("Authorization")
		r.Body = http.MaxBytesReader(w, r.Body, p.cfg.MaxBodyBytes)

		key := r.Header.Get(headerIdempotencyKey)
		if !route.Policy.AcceptsIdempotencyKey || key == "" {
			next(w, r, params)
			return
		}
		p.idempotent(w, r, params, next, userID, key)
	}
}

// authenticate verifies the bearer token. Public routes accept a missing
// or invalid token and proceed anonymously; the invalid token is dropped,
// not forwarded.
func (p *policy) authenticate(r *http.Request, rp domain.RoutePolicy) (context.Context, error) {
	token := edge.BearerToken(r)
	if rp.Public && token == "" {
		return r.Context(), nil
	}
	ctx, _, err := edge.VerifyBearer(r.Context(), p.cfg.Verifier, token)
	if err != nil && rp.Public {
		return r.Context(), nil
	}
	return ctx, err
}

func (p *policy) idempotent(w http.ResponseWriter, r *http.Request, params map[string]string, next runtime.HandlerFunc, userID, key string) {
	ctx := r.Context()
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		edge.WriteError(w, r, p.cfg.Logger, edge.InvalidField(headerIdempotencyKey, "must be 1 to 255 printable ASCII characters"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			edge.WriteError(w, r, p.cfg.Logger, edge.InvalidField("body", "is too large"))
			return
		}
		edge.WriteError(w, r, p.cfg.Logger, status.Error(codes.InvalidArgument, "request body could not be read"))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	ticket := p.cfg.Idempotency.Begin(ctx, userID, key, domain.Fingerprint(r.Method, r.URL.RequestURI(), body))
	switch ticket.Outcome {
	case domain.OutcomeReplay:
		writeStored(w, ticket.Replay)
		return
	case domain.OutcomeMismatch:
		edge.WriteError(w, r, p.cfg.Logger, edge.InvalidField(headerIdempotencyKey,
			"was already used with a different method, path or body"))
		return
	case domain.OutcomeInFlight:
		edge.WriteError(w, r, p.cfg.Logger, edge.Aborted(edge.ReasonIdempotencyKeyInUse,
			"a request with this idempotency key is still running; retry shortly"))
		return
	}

	// The outcome must be recorded even if the client has gone, or the key
	// would stay reserved until PendingTTL.
	finishCtx := context.WithoutCancel(ctx)
	captured := &capture{limit: p.cfg.MaxStoredResponseBytes}
	finished := false
	defer func() {
		if !finished {
			p.cfg.Idempotency.Abandon(finishCtx, ticket)
		}
	}()
	next(captured.wrap(w), r, params)
	finished = true
	if captured.overflow {
		p.cfg.Idempotency.Abandon(finishCtx, ticket)
		return
	}
	p.cfg.Idempotency.Finish(finishCtx, ticket, domain.StoredResponse{
		Status: captured.status(), ContentType: captured.contentType, Body: captured.body.Bytes(),
	})
}

// capture copies a response as it is written, up to limit bytes.
type capture struct {
	limit       int
	code        int
	contentType string
	body        bytes.Buffer
	overflow    bool
}

func (c *capture) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}

func (c *capture) wrap(w http.ResponseWriter) http.ResponseWriter {
	return httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(f httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if c.code == 0 {
					c.code = code
					c.contentType = w.Header().Get("Content-Type")
				}
				f(code)
			}
		},
		Write: func(f httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(b []byte) (int, error) {
				if c.code == 0 {
					c.code = http.StatusOK
					c.contentType = w.Header().Get("Content-Type")
				}
				if !c.overflow {
					if c.body.Len()+len(b) > c.limit {
						c.overflow = true
						c.body.Reset()
					} else {
						c.body.Write(b)
					}
				}
				return f(b)
			}
		},
	})
}

func writeStored(w http.ResponseWriter, resp domain.StoredResponse) {
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.Header().Set(headerReplayed, "true")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
