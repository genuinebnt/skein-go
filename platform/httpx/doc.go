// Package httpx is the HTTP counterpart of grpcx, for the gateway: the same
// request IDs, recovery, tracing and access logs, plus CORS, security
// headers, JSON errors mapped from gRPC status codes, and rate limit
// headers.
//
// # Middleware order
//
// Chain wraps a handler in this order, outermost first, mirroring grpcx:
//
//  0. tracing and RED metrics (otelhttp): outermost, as the gRPC stats
//     handler is, so spans and http.server.request.duration cover
//     everything below, including panics turned into 500s.
//  1. request ID: X-Request-ID from the client when valid
//     (logging.ValidRequestID), otherwise new; set on the response and in
//     the context, from where grpcx clients forward it to the services.
//  2. recovery: a panic becomes a JSON 500 and one Error log line with the
//     stack; the span is marked as an error.
//  3. access log: method, route pattern (never the raw query, which can
//     hold share-link tokens), status, bytes and duration_ms, one line per
//     request.
//  4. security headers: nosniff, frame denial, no referrer, a CSP that
//     forbids everything (this is a JSON API), and HSTS when enabled.
//  5. CORS: an exact allow-list of origins, answered per request; preflight
//     requests are handled here and never reach the handler.
//
// Authentication and rate limiting are per route in the gateway, because
// public routes (sign-in, the JWKS) need neither: see Authenticate and
// RateLimit.
//
// # Errors
//
// WriteError writes {"error": {"code", "message", "request_id"}} with an
// HTTP status taken from the error's gRPC code, so a gateway handler can
// return whatever a backend returned. The mapping (StatusFromCode) follows
// google.rpc.Code's documented HTTP equivalents, as grpc-gateway does:
//
//	OK 200                    InvalidArgument 400     FailedPrecondition 400
//	OutOfRange 400            Unauthenticated 401     PermissionDenied 403
//	NotFound 404              AlreadyExists 409       Aborted 409
//	ResourceExhausted 429     Canceled 499            Unknown 500
//	Internal 500              DataLoss 500            Unimplemented 501
//	Unavailable 503           DeadlineExceeded 504
//
// 499 is nginx's "client closed request": the caller went away, which is
// neither a client mistake nor a server fault. For 5xx codes the message is
// replaced by a generic one so internal details never reach the browser;
// the original is in the log line. A RetryInfo detail (grpcx's rate limit
// error) becomes a Retry-After header.
//
// # Rate limit headers
//
// WriteRateLimitHeaders sets RateLimit-Limit, RateLimit-Remaining and
// RateLimit-Reset (seconds until the bucket is full), the three-header form
// of the IETF httpapi rate limit draft that browsers' fetch wrappers and
// most clients understand, and Retry-After (seconds, rounded up) on a
// denial.
//
// # Porting notes
//
// Rust: `tower-http` layers (`trace`, `request_id`, `catch_panic`, `cors`,
// `set_header`) in the same order around an `axum` router; errors as an
// `IntoResponse` enum with the same table.
package httpx
