# gateway

The only public entry point of Skein (ADR-0002). It serves REST/JSON for
every annotated RPC through grpc-gateway, the three streaming RPCs as
WebSockets, the OpenAPI document and a docs page, and translates everything
to gRPC calls on the services behind it. It owns no data.

The contract it implements is in [`docs/api/`](../../docs/api/README.md);
[`websocket.md`](../../docs/api/websocket.md) covers the streams.

## Layout

```
cmd/gateway/                 wiring, configuration, `gateway healthcheck`
internal/domain/             route policy, budgets, client IP, close codes, rendezvous hashing, idempotency rules
internal/app/                Throttler, Idempotency, socket Registry, Revocations; the ports
internal/adapters/edge/      google.rpc.Status errors, token verification, route labels, recovery, access log
internal/adapters/httpapi/   the server: middleware chain, grpc-gateway mux and per-route policy, OpenAPI, health, drain
internal/adapters/wsapi/     WebSocket sockets and the Sync, SendMessage and Subscribe bridges
internal/adapters/backends/  gRPC clients (grpcx, round_robin) and the documents sync router
internal/adapters/redisstore/ idempotency records and the revocation channel in Redis
internal/adapters/identityevents/ consumer of identity.events
internal/adapters/jwks/      fetches identity's key set
internal/fakes/              in-memory stores and fake backends on bufconn
scripts/smoke.sh             smoke test against a running stack
```

Every package's doc comment explains its design, invariants and Rust
porting notes; this page is the summary.

## Request path

```
otelhttp → request ID → route label → recovery → access log → security headers → CORS
  → /healthz /readyz /openapi.json /docs
  → GET /v1/notes/{noteId}/sync, /v1/chats/{chatId}/messages, /v1/notifications/live  (wsapi)
  → everything else: grpc-gateway → per-route policy → gRPC
        auth (public: sign up, sign in, OAuth, refresh, sign out)
        rate limit: IP budget, then user budget (GCRA in Redis)
        deadline (30 s) and body limit (4 MiB)
        Idempotency-Key for POSTs
```

Backends receive `authorization: Bearer <verified token>`,
`x-skein-user-id` (read by the agent service), `x-request-id` and the W3C
trace context, and nothing else from the client: incoming headers are never
turned into metadata, so `Grpc-Metadata-*` cannot forge an identity.

## Decisions

- **Errors** are `google.rpc.Status` JSON exactly as documented, with
  `RequestInfo` always, generic messages and no details but `RetryInfo` for
  5xx, and `RetryInfo` added to `UNAVAILABLE`. `platform/httpx.WriteError`
  writes another shape, so the gateway has its own writer
  (`edge.WriteError`) and recovery.
- **Rate limits.** Budgets `api`, `search` (text, title and semantic
  search), `sync` (REST `ApplyOps` and every sync frame) and `agent` (every
  agent frame, one run each), each per user and per client IP (IPv6 by /64).
  The IP budget is checked first and is twice the user's. Headers report
  the tighter of the two; a denial reports the one that denied. Defaults are
  in `domain.DefaultLimits`; override with `RATE_LIMIT_<BUDGET>_<USER|IP>`.
  The limiter fails open and counts it (`skein.ratelimit.decisions` with
  `error.type=limiter_error`).
- **Idempotency.** Only authenticated POSTs, except `ApplyOps` and
  `MarkRead`; the session routes ignore the key because their responses
  carry tokens. Records are per user and key for 24 hours. Responses kept
  for replay: 2xx, 400, 403, 404, 422 up to 1 MiB; anything else releases the
  key so the retry runs. Same key with another method, path or body:
  `INVALID_ARGUMENT`; while the first is running: `ABORTED`. The store
  fails open.
- **Sync fan-out across gateway replicas: consistent hashing, not Redis
  pub/sub.** Each sync socket opens its own `Sync` stream to the documents
  instance that owns the note, chosen by rendezvous hashing over the
  addresses of the documents headless Service (`DOCUMENTS_SYNC_ADDR`). Every
  gateway replica computes the same owner, so all of a note's sockets meet
  in one documents actor, which already does catch-up, presence, rejection
  and fan-out. A Redis fan-out would duplicate that per gateway and still
  leave two documents instances holding the same note. When the set of
  instances changes, sockets whose note moved are closed with `1012` and
  reconnect to the new owner. The hash is pinned by
  `internal/domain/testdata/rendezvous.json` for the Rust port.
- **Revocation.** `gateway.identity-events` delivers each event to one
  replica, which closes its own sockets and broadcasts on the Redis channel
  `gw:access-revoked` to the others. Note-level revocations close that
  note's sync sockets; workspace-level ones close the user's sync and agent
  sockets. Close code `4403`.
- **Backpressure.** 256 queued frames per socket; a full queue closes with
  `1013` (added to websocket.md). Frames are never silently dropped.
- **Agent runs outlive their socket**, as websocket.md says ("closing the
  socket does not cancel a run"): the gateway keeps reading a run after its
  socket closes, for up to 15 minutes or until shutdown.
- **Drain.** SIGTERM: `/readyz` turns 503, after `GATEWAY_DRAIN_DELAY`
  the listener closes and in-flight requests finish, sockets close with
  `1012` (the documented deploy code), then detached runs get what is left
  of the 25 s budget.
- **Readiness** reports Redis as a diagnostic but does not fail on it, since
  everything that uses Redis fails open.

## Configuration

Besides the platform variables (`SKEIN_ENV`, `SKEIN_VERSION`, `REDIS_URL`,
`EVENTS_BACKEND` and its variables (`PUBSUB_PROJECT`, `PUBSUB_EMULATOR_HOST`
or `EVENTS_REDIS_URL`; [docs/local-first.md](../../docs/local-first.md)), `OTEL_*`):

| Variable | Default | Meaning |
|---|---|---|
| `GATEWAY_HTTP_ADDR` | `:8080` | listen address |
| `IDENTITY_GRPC_TARGET` … `NOTIFICATIONS_GRPC_TARGET` | `dns:///<service>:9090` | gRPC targets (search also serves GraphService) |
| `DOCUMENTS_SYNC_ADDR` | `documents:9090` | headless Service host:port for sync routing |
| `GRPC_INSECURE` | `true` | plaintext gRPC inside the cluster |
| `IDENTITY_JWKS_URL` | `http://identity:8080/.well-known/jwks.json` | identity's public key set |
| `CORS_ALLOWED_ORIGINS` | none; `http://localhost:5173` (the web dev server) in dev and test | exact origins, comma separated; also the WebSocket origin allow-list. Cross-origin responses expose `X-Request-ID`, `RateLimit-*`, `Retry-After` and `Idempotent-Replayed` |
| `TRUSTED_PROXIES` | none | CIDRs whose `X-Forwarded-For` is believed |
| `GATEWAY_HSTS` | `false` | send Strict-Transport-Security |
| `GATEWAY_DRAIN_DELAY` | `5s` | readiness-off time before the listener closes |
| `GATEWAY_REQUEST_TIMEOUT` | `30s` | deadline of every REST call |
| `GATEWAY_REDIS_NAMESPACE` | `gw` | prefix of every Redis key |
| `RATE_LIMIT_<BUDGET>_<USER\|IP>` | see `domain.DefaultLimits` | `rate/period:burst`, e.g. `120/1m:30` |

## Metrics

Besides `http.server.*` (otelhttp, with `http.route` set to the template)
and `rpc.client.*` (grpcx):

| Metric | Attributes |
|---|---|
| `skein.ratelimit.decisions` | `skein.ratelimit.layer=gateway`, `.budget`, `.subject`, `.decision`, `error.type` on limiter failure |
| `skein.gateway.idempotency.outcomes` | `skein.idempotency.outcome`: reserved, replay, mismatch, in_flight, stored, released, expired, error |
| `skein.gateway.websocket.connections` | `skein.websocket.endpoint`: sync, agent, notifications |
| `skein.gateway.websocket.closes` | endpoint and `skein.websocket.close_code` (normal, rate_limited, try_again_later, …) |

## Tests

```
cd services/gateway
go test -race ./...
go test -run='^$' -fuzz=FuzzClientIP ./internal/domain    # also FuzzCloseReason, wsapi FuzzParseOffer
go generate ./internal/adapters/httpapi                     # after make proto: refresh the embedded OpenAPI copy
```

Nothing needs Docker or the network: the backends are fakes on bufconn,
Redis is miniredis, and HTTP and WebSocket tests use in-process servers on
loopback. `httpapi` tests drive the whole server: every route reaching its
RPC with the right metadata, auth and public routes, the error table,
rate-limit headers and 429s, idempotency, WebSocket framing and close codes,
token expiry, backpressure, keepalive, revocation, rerouting and drain, and
`TestRateLimiterLongRunRateMatchesTheBudget`, a load-shaped run through the
real GCRA script with simulated time.

## Smoke test

Against a running stack with the small seed loaded:

```
SMOKE_PASSWORD=<seed users' password> services/gateway/scripts/smoke.sh
```

It signs in as the seed workspace owner, lists notes, searches, reads the
inbox, and checks request IDs, rate-limit headers, the error shape and
idempotent replay.
