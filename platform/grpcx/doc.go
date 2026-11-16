// Package grpcx builds gRPC servers and clients with Skein's standard
// middleware, health checking and graceful shutdown, so services register
// their handlers and nothing else.
//
// # Server middleware order
//
// The order is fixed and the same for every service. Outermost first:
//
//  0. tracing and RED metrics: the otelgrpc stats handler. It is not an
//     interceptor: gRPC calls it at the transport level, around all of the
//     interceptors below, so the span and the duration metric cover
//     everything, panics and auth failures included.
//  1. request ID: takes x-request-id from metadata when valid, otherwise
//     makes one, puts it in the context (logging.WithRequestID) and echoes
//     it in the response trailer. Not a header: a call whose headers have
//     reached the client is committed and gRPC will not retry it.
//  2. recovery: turns a panic in anything below into codes.Internal, logs
//     the stack with the request ID and marks the span as an error. The
//     client sees only "internal error".
//  3. logging: one line per call with rpc.method, code and duration_ms, at
//     Error for server faults (Internal, Unknown, DataLoss, Unavailable,
//     DeadlineExceeded) and Info otherwise. Health checks log at Debug.
//  4. auth: reads "authorization: Bearer <token>", verifies it with a
//     auth.TokenVerifier and puts the claims and subject in the context
//     (auth.WithClaims, logging.WithSubject). Methods in PublicMethods, the
//     health service and reflection skip the requirement, but still get
//     claims when a valid token is sent. With no verifier configured,
//     every non-public method is refused: auth fails closed.
//  5. default deadline: unary calls without a deadline get DefaultTimeout,
//     so a missing client deadline cannot pin a goroutine forever. Streams
//     are exempt, because sync streams live as long as an editor is open.
//  6. rate limit: an optional hook; a denial becomes ResourceExhausted with
//     a google.rpc.RetryInfo detail, which httpx turns into Retry-After.
//     If the limiter itself fails the call proceeds (fail open), because
//     the gateway's limit is the primary defence.
//
// Request ID comes before recovery, not after it, so the panic log line
// still carries the request ID; the request ID interceptor is a few lines
// that cannot realistically panic. A panicking call is logged once, by
// recovery, not again by logging.
//
// # Server lifecycle
//
// Server.Serve(ctx, listener) blocks until ctx is cancelled, then marks
// every service NOT_SERVING in grpc.health.v1 (so load balancers stop
// sending), drains with GracefulStop and, after ShutdownTimeout, forces
// Stop. Reflection is registered only when the environment is dev or test.
// Keepalive enforcement accepts client pings every 10 seconds or more, and
// MaxConnectionAge recycles connections so clients re-resolve DNS and spread
// over new replicas after a scale-up.
//
// # Clients
//
// NewClient dials a dns:/// target (a headless Kubernetes Service) with
// round_robin, so each RPC picks the next backend rather than one HTTP/2
// connection carrying everything (architecture section 5). Retries are
// declared in the service config, only for the methods listed as
// idempotent, only on UNAVAILABLE, with exponential backoff; gRPC also
// retries transparently when a request never left the client. Client
// interceptors forward the request ID and the caller's bearer token, so a
// chain of calls made for a user is authorised as that user (ADR-0009).
//
// # Porting notes
//
// Rust: `tonic` with `tower` layers in the same order
// (`tower_http::request_id`, a catch-panic layer, `tracing` spans, an auth
// layer, `tower::timeout`); `tonic-health` and `tonic-reflection`. tonic's
// client has no service-config retries: use `tower::retry` with a policy
// that checks the method against the same idempotent list.
package grpcx
