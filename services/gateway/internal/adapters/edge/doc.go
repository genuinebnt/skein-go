// Package edge holds what the REST and WebSocket adapters share at the
// HTTP edge: the error format, token verification, the client's IP, the
// route label used by logs and metrics, and the outer middleware.
//
// # Errors
//
// WriteError writes a google.rpc.Status as proto3 JSON, exactly the shape
// docs/api/README.md documents ({"code", "message", "details"}), with the
// HTTP status from httpx.StatusFromCode (the same table). Before writing:
//
//   - every error gets a google.rpc.RequestInfo detail with the request ID,
//     unless the backend already added one;
//   - server faults (5xx) keep only RetryInfo and get a generic message,
//     because a backend's INTERNAL or a dial error can name hosts, SQL or
//     stack frames; the real message is logged with the request ID;
//   - UNAVAILABLE without RetryInfo gets one second of RetryInfo, since the
//     documentation promises it;
//   - RetryInfo becomes Retry-After, and 401 gets WWW-Authenticate.
//
// platform/httpx.WriteError writes a different, flatter shape
// ({"error": {...}}), so the gateway does not use it, nor httpx.Chain's
// recovery, which writes that shape too (a platform follow-up).
//
// # Authentication
//
// VerifyBearer checks a token with the platform verifier (EdDSA JWTs whose
// keys come from identity's key set) and, on success, puts the claims and
// raw token in the context: platform/grpcx clients forward the token from
// there as "authorization: Bearer", and OutgoingSubject adds
// x-skein-user-id, which the agent service reads (services/agent). A
// failure is UNAUTHENTICATED with ErrorInfo reason TOKEN_EXPIRED or
// TOKEN_INVALID, so clients know whether to refresh.
//
// # Route labels
//
// The REST routes are served by grpc-gateway behind one catch-all, so the
// ServeMux pattern says nothing. WithRouteLabel puts a holder in the
// context at the top of the chain; LabelRoute fills it once the route is
// known, and also sets http.route on otelhttp's labeler (metrics) and on
// the span. AccessLog reads the holder, so log lines and dashboards show
// "/v1/notes/{note_id}", never the raw path.
//
// # Porting notes
//
// Rust: tonic_types::StatusExt for the details, serialized with the
// prost-reflect JSON mapping; the route label is an axum MatchedPath
// extension set by the REST layer.
package edge
