// Package httpapi is the gateway's HTTP server: the REST routes through
// grpc-gateway, the WebSocket endpoints (package wsapi), the OpenAPI
// document and docs page, health and readiness, and graceful drain.
//
// # Middleware order
//
// Outermost first, mirroring platform/httpx.Chain (docs/platform.md):
//
//  0. otelhttp: server span and http.server.request.duration, continuing a
//     W3C traceparent from the client.
//  1. request ID (httpx.RequestID): a valid X-Request-ID is kept, otherwise
//     a UUIDv7 is made; echoed on the response, forwarded to backends.
//  2. route label holder (edge.WithRouteLabel).
//  3. recovery (edge.Recover): a panic becomes a google.rpc.Status 500.
//  4. access log (edge.AccessLog), with the route template.
//  5. security headers (httpx.SecurityHeaders).
//  6. CORS (httpx.CORS), allowing Idempotency-Key and the trace headers,
//     and exposing Idempotent-Replayed besides httpx's list.
//  7. the router: health, readiness, /openapi.json, /docs, the three
//     WebSocket endpoints, and everything else to grpc-gateway, whose
//     per-route middleware (rest.go) authenticates, rate-limits, sets the
//     deadline and handles idempotency keys.
//
// # Drain
//
// Server.Drain, on SIGTERM: readiness turns 503 so the load balancer stops
// sending; after DrainDelay (the time endpoints take to notice) the
// listener closes and in-flight REST requests finish (http.Server.Shutdown);
// at the same time every WebSocket is closed with 1012 and the registry
// waits for them, since Shutdown does not track hijacked connections; last,
// detached agent runs get what is left of the deadline.
//
// # OpenAPI
//
// /openapi.json serves gen/openapi/skein.swagger.json, embedded from a copy
// in openapi/ (go:embed cannot reach outside the module). `go generate`
// refreshes the copy and a test fails when it is stale. /docs is a small
// server-rendered index of the routes with a link to the document, so it
// needs no scripts and keeps a strict Content-Security-Policy.
//
// # Porting notes
//
// Rust: an axum Router with tower-http layers in the same order; the REST
// translation comes from a tonic client per service; the OpenAPI file via
// include_str!.
package httpapi

//go:generate cp ../../../../../gen/openapi/skein.swagger.json openapi/skein.swagger.json
