# Porting `services/gateway` to Rust

The gateway is Skein's only public entry point (ADR-0002). It serves
REST/JSON for every annotated RPC, the three streaming RPCs as WebSockets,
the OpenAPI document and a docs page; it verifies tokens, spends
rate-limit budgets per IP and per user (GCRA in Redis), handles
idempotency keys, routes each note's sync stream to the documents instance
that owns the note (rendezvous hashing), and closes sockets when access is
revoked. It owns no data.

It is recommended last among the Go services (see
[docs/porting/README.md](../../docs/porting/README.md)): every e2e journey
goes through it, so with a trusted Go gateway in front, each ported backend
is tested on its own. Read [platform/PORTING.md](../../platform/PORTING.md)
(`httpx`, `grpcx`, `ratelimit`, `auth`) first. Every slice in
[docs/porting/slices](../../docs/porting/slices/README.md) starts at the
gateway.

## Contract

### HTTP surface

| Path | What | Code |
|---|---|---|
| every route in `docs/api/*.md` (one per annotated RPC; table in [gen/PORTING.md](../../gen/PORTING.md)) | REST/JSON translated to gRPC | `adapters/httpapi/rest.go`, `routes.go` |
| `GET /v1/notes/{noteId}/sync` | WebSocket bridged to `DocumentService.Sync` on the note's owner | `adapters/wsapi/handlers.go` (`serveSync`) |
| `GET /v1/chats/{chatId}/messages` | WebSocket; each client frame is one `AgentService.SendMessage` run | `serveAgent` |
| `GET /v1/notifications/live` | WebSocket bridged to `NotificationService.Subscribe` | `serveNotifications` |
| `/openapi.json`, `/docs` | the generated OpenAPI document and a server-rendered route index | `adapters/httpapi/openapi.go` |
| `/healthz`, `/readyz` | liveness, readiness (503 while draining) | `adapters/httpapi/server.go` |

Conventions it enforces (docs/api/README.md): errors as `google.rpc.Status`
JSON with server faults hidden; `X-Request-ID`; `RateLimit-*` and
`Retry-After` headers; `Idempotency-Key` on authenticated non-idempotent
POSTs; 30 s deadline and 4 MiB body limit; the five session routes public,
everything else authenticated; no client header ever becomes gRPC
metadata. WebSocket framing, subprotocols (`skein.v1.proto`,
`skein.v1.json`, `bearer.<token>`), keepalive and close codes are in
`docs/api/websocket.md`.

### Events

| Direction | Subscription | Messages |
|---|---|---|
| consumes | `gateway.identity-events` on `identity.events` | `AccessChanged` revocations, applied locally and broadcast to other replicas over Redis pub/sub |

### Calls

gRPC clients (grpcx: round_robin, retries for idempotent methods, request
ID and token forwarding) to identity, documents, search, ml, notifications
and agent. The documents sync router resolves `DOCUMENTS_SYNC_ADDR` (a
headless Service) to the set of instances and picks the owner per note.
JWKS from `IDENTITY_JWKS_URL`.

### State in Redis

Rate-limit TATs (GCRA), idempotency records (pending or complete, with the
stored response), the revocation channel. Namespace `GATEWAY_REDIS_NAMESPACE`.

### Environment

`GATEWAY_HTTP_ADDR`, `GATEWAY_REQUEST_TIMEOUT`, `GATEWAY_DRAIN_DELAY`,
`GATEWAY_HSTS`, `GATEWAY_REDIS_NAMESPACE`, `IDENTITY_GRPC_TARGET`,
`IDENTITY_JWKS_URL`, `DOCUMENTS_GRPC_TARGET`, `DOCUMENTS_SYNC_ADDR`,
`SEARCH_GRPC_TARGET`, `ML_GRPC_TARGET`, `NOTIFICATIONS_GRPC_TARGET`,
`AGENT_GRPC_TARGET`, `GRPC_INSECURE`, the rate limits and CORS origins
(`cmd/gateway/config.go`), plus the platform's.

## The big decision: what replaces grpc-gateway

Go gets REST translation for free from `protoc-gen-grpc-gateway`. Rust has
no equivalent generator in common use. Three options:

| Option | How | For | Against |
|---|---|---|---|
| A. Descriptor-driven transcoder | read the `google.api.http` rules from `skein-proto`'s descriptor set with `prost-reflect`; build one route table (the Go gateway already builds exactly this table in `routes.go` to look up policies); per request, bind path and query into a `DynamicMessage`, merge the JSON body, encode, call the backend with a generic tonic codec, decode the reply and write ProtoJSON | one generic handler for every route; adding an RPC needs no gateway code, as in Go; the route table and the policy table cannot drift | the most to build (about the size of `rest.go` plus a path-template matcher); dynamic messages instead of typed ones |
| B. Hand-written axum handlers | one handler per route, calling the typed tonic client, with `pbjson` for JSON | simplest to read and debug; typed end to end | 48 handlers (one per REST route) to write and keep in step with the protos; the route table is duplicated (contract tests in `gen/go/contracttest` then matter even more) |
| C. Keep a transcoding proxy | Envoy's gRPC-JSON transcoder in front of a Rust gateway that speaks gRPC and WebSocket only | no REST code at all | a second process; policy (auth, budgets, idempotency) has to move into gRPC interceptors or Envoy filters |

Recommendation: **A**, because it keeps the property the Go code relies on
(routes and policies come from the same descriptors, and an unknown route
fails closed), and it is a good learning exercise in protobuf reflection.
Start with B for the five session routes in [slice 1](../../docs/porting/slices/01-sign-up-and-sign-in.md)
if you want something running in a day, then replace it. Either way,
`TestEveryDocumentedRESTRouteReachesItsRPCWithTheCallersIdentity` and the
e2e suite decide when it is right.

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| grpc-gateway | [grpc-ecosystem/grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway) | what the Go gateway gets for free: path templates, custom verbs (`:rename`), body and query binding, error handler hooks |
| proto3 JSON | [ProtoJSON format](https://protobuf.dev/programming-guides/json/) | the exact JSON the transcoder must emit (int64 as strings, enum names, lowerCamelCase) |
| descriptors | [prost-reflect](https://docs.rs/prost-reflect/latest/prost_reflect/) | `DescriptorPool`, method options (the `google.api.http` extension), `DynamicMessage` with serde |
| axum | [axum](https://docs.rs/axum/latest/axum/), [axum WebSocket extractor](https://docs.rs/axum/latest/axum/extract/ws/index.html), [tokio-rs/axum examples](https://github.com/tokio-rs/axum) (`examples/websockets`, `examples/chat`) | routers, extractors, `WebSocketUpgrade` with `protocols` for subprotocol negotiation |
| tower | [tower](https://docs.rs/tower/latest/tower/), [tower-http](https://docs.rs/tower-http/latest/tower_http/) | the middleware chain, in the Go order |
| WebSocket protocol | [RFC 6455](https://datatracker.ietf.org/doc/html/rfc6455) | close codes and the 123-byte reason limit, ping and pong, the closing handshake |
| backpressure | [I'm not feeling the async pressure](https://lucumr.pocoo.org/2020/1/1/async-pressure/), [Tokio channels](https://tokio.rs/tokio/tutorial/channels) | why `Send` never blocks and a full queue closes with 1013; `mpsc::Sender::try_send` |
| GCRA | [Rate Limiting, Cells, and GCRA](https://brandur.org/rate-limiting), [governor](https://docs.rs/governor/latest/governor/) | the algorithm behind the Redis script; governor is GCRA in process (useful to compare, not to replace the shared Redis limit) |
| idempotency keys | [The Idempotency-Key HTTP header (IETF draft)](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/), [Stripe: designing robust APIs with idempotency](https://stripe.com/blog/idempotency), [Implementing Stripe-like idempotency keys (brandur)](https://brandur.org/idempotency-keys) | key scope, fingerprints, replay versus conflict, which responses are safe to store |
| rendezvous hashing | [Rendezvous hashing](https://en.wikipedia.org/wiki/Rendezvous_hashing) | highest random weight: why removing a member moves only its keys |
| client IP | [X-Forwarded-For (MDN)](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/X-Forwarded-For), [ipnet](https://docs.rs/ipnet/latest/ipnet/) | why only hops to the right of trusted proxies count; IPv6 keyed by /64 |
| JWT verification | [RFC 8725](https://datatracker.ietf.org/doc/html/rfc8725) | the checks `skein-auth` performs; expired versus invalid gives different close codes |

## Port order

Domain, app, ports and fakes go into `gateway-core`; adapters and `cmd/`
into `gateway`.

### 1. Domain (`internal/domain`, pure)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `policy.go` | `Budget` (api, search, sync, agent), `SubjectKind`, `RoutePolicy`, `IsPublicMethod`, `PolicyFor(rpc, verb)` | | `TestOnlyTheFiveSessionRoutesArePublic`, `TestSearchAndSyncRoutesSpendTheirOwnBudgets`, `TestIdempotencyKeysApplyToAuthenticatedNonIdempotentPostsOnly` |
| `limits.go` | `Limits` per budget and subject, `DefaultLimits`, `ParseLimit`, `LimiterKey`, `IPKey` (IPv6 by /64, IPv4-mapped unmapped) | policy | `TestDefaultLimitsCoverEveryBudgetAndSubject`, `TestParseLimit`, `TestIPKeyGroupsIPv6ByPrefixAndUnmapsIPv4` |
| `clientip.go` | `ClientIP` behind trusted proxies, `ParsePrefixes` | `std::net`, `ipnet` | `TestClientIP`, `TestParsePrefixesAcceptsAddressesAndBlocks`, `FuzzClientIP` |
| `closecode.go` | `CloseCode`, `CloseCodeForStatus` (the table in websocket.md), `CloseReason` (at most 123 bytes, never splitting a UTF-8 sequence) | | `TestCloseCodeForStatusFollowsTheDocumentedTable`, `TestCloseReasonTruncatesOnARuneBoundary`, `FuzzCloseReason` |
| `rendezvous.go` | `Ring`, `Owner`, `RendezvousScore` (FNV-1a 64 over note ID, a zero byte and the member, then the splitmix64 finaliser; ties to the smaller member) | | **`TestRendezvousMatchesGoldenValues`** (`internal/domain/testdata/rendezvous.json`), `TestMembershipChangesMoveOnlyTheAffectedKeys`, `TestOwnershipIsBalanced`, `TestOwnerIgnoresInputOrder` |
| `idempotency.go` | `ValidateIdempotencyKey`, `Fingerprint` (SHA-256 over length-prefixed method, path with query and body), `RecordState`, `Decide`, `Storable` (2xx, 400, 403, 404, 422 only) | `sha2` | `TestValidateIdempotencyKey`, `TestFingerprintSeparatesFieldsAndCoversEachOne`, `TestDecide`, `TestOnlyResponsesARetryWouldRepeatAreStorable` |
| `revocation.go` | `SocketKind`, `Revocation` and `Affects` | | the registry tests |

Port `rendezvous.go` with its golden file first: a Rust gateway replica and
a Go one must choose the same owner for every note.

### 2. Application and ports (`internal/app`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `ports.go` | `IdempotencyStore`, `RevocationBus`, `SyncRouter` and `SyncRoute`, `NoteAccess` | domain | the fakes implement it |
| `throttle.go` | `Throttler.Check`: IP budget first, then user; reports the tighter; fails open and counts | `skein-ratelimit` | `TestThrottlerChecksIPThenUserAndReportsTheTighterBudget`, `TestThrottlerStopsAtAnIPDenialWithoutSpendingTheUserBudget`, `TestThrottlerFailsOpenAndCountsTheFailure` |
| `idempotency.go` | `Idempotency.Begin`, `Finish`, `Abandon`, `ScopedKey` (per user); fails open | ports, domain | `TestIdempotencyReplaysMismatchesAndReportsInFlight`, `TestIdempotencyReleasesUnstorableResponsesSoTheRetryRuns`, `TestIdempotencyFailsOpenWhenTheStoreFails` |
| `sockets.go` | `Registry` of live sockets: `Add`, `Revoke`, `Reroute` (owner changed), `Drain` (closes all, refuses new, waits with a deadline); `CheckInvariants` | domain | `TestRevokeClosesOnlyTheAffectedSockets`, `TestRerouteClosesSyncSocketsWhoseOwnerChanged`, `TestDrainClosesEverySocketRefusesNewOnesAndWaits`, `TestDrainGivesUpAtTheDeadline` |
| `revocations.go` | apply locally, then broadcast; listen for other replicas' broadcasts | sockets, ports | `TestRevocationsCloseLocallyEvenWhenTheBroadcastFails`, `TestRevocationsReachOtherReplicasThroughTheBus` |

### 3. Fakes (`internal/fakes`)

`stores.go` (idempotency store with failure injection and
`CheckInvariants`, revocation bus) and `backends.go` (fake gRPC backends,
including several documents instances, on in-process transports; record
every call with its metadata). The REST and WebSocket tests depend on
these heavily; port them before the adapters.

### 4. Adapters

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `adapters/edge/errors.go` | `StatusOf` (context errors too), `WriteError`, `PublicStatus` (hide server faults, keep `RetryInfo` and `RequestInfo`), helpers, rate-limit headers | `skein-http` | `TestPublicStatusHidesServerFaultsButKeepsRetryInfo`, `TestPublicStatusKeepsAnExistingRequestInfo`, `TestStatusOfMapsContextErrors` |
| `adapters/edge/auth.go` | bearer extraction and verification (expired versus invalid), outgoing subject metadata, client IP | `skein-auth` | `TestVerifyBearerDistinguishesExpiredFromInvalid` |
| `adapters/edge/middleware.go` | route label, recovery (status 500), access log with the route template | | `TestRecoverTurnsAPanicIntoAStatus500AndLogsTheStack` |
| `adapters/jwks/jwks.go` | fetch identity's JWKS with a size bound | `reqwest` | `TestRotatedKeyIsFetchedOnFirstUse`, `TestFetchReportsErrorsAndBoundsTheDocument` |
| `adapters/redisstore/idempotency.go`, `revocations.go` | idempotency records and the revocation channel in Redis | `skein-redis` | `TestRedisStoreMatchesMemoryStoreUnderRandomOperations`, `TestStoresCompleteOnlyForTheOwnerAndKeepTheResponse`, `TestRevocationBusReachesEverySubscriber` |
| `adapters/identityevents/identityevents.go` | `identity.events` handler: only revocations are applied; undecodable payloads nack | `skein-events` | `TestOnlyRevocationsAreApplied`, `TestUndecodablePayloadsAreNackedTowardTheDeadLetterTopic`, `TestBroadcastFailureIsRedelivered` |
| `adapters/backends/backends.go` | the six tonic clients; `NoteAccess.CanRead` through identity | `skein-grpc` | the REST tests |
| `adapters/backends/syncrouter.go` | resolve the documents instances, keep the last members when DNS fails, reuse one channel per instance, close removed ones, tell the registry to reroute | domain rendezvous | `TestRouterIsUnavailableUntilAnInstanceIsKnown`, `TestRouterRoutesByRendezvousAndReusesConnections`, `TestRouterKeepsTheLastMembersWhenDNSFailsAndDropsRemovedInstances` |
| `adapters/httpapi/routes.go` | the route table from the proto descriptors | `skein-proto` descriptors | `TestUnknownPathsAndInternalRPCsAreNotFound` |
| `adapters/httpapi/rest.go` | REST translation (see the decision above) and the per-route policy: authenticate, rate-limit, deadline, body limit, idempotency, in that order; no header forwarding | routes, app, edge | `TestEveryDocumentedRESTRouteReachesItsRPCWithTheCallersIdentity`, `TestPathAndQueryParametersBindToTheRequest`, `TestClientsCannotForgeTheForwardedIdentity`, `TestBackendErrorsFollowTheDocumentedMapping`, `TestRateLimitHeadersOnEveryResponseAnd429OverBudget`, `TestIdempotencyKeyReplaysTheFirstResponse` and the other idempotency tests |
| `adapters/wsapi/socket.go` | one socket: reader, writer, pinger and token-expiry timer; bounded send queue; first close wins; closing handshake with code and reason | domain close codes | `TestParseOfferPicksTheFirstEncodingAndExtractsTheToken`, `FuzzParseOffer`, `TestSendClosesWith1013WhenTheQueueIsFull`, `TestCloseReasonsDoNotLeakServerFaults` |
| `adapters/wsapi/handlers.go` | upgrade (token from subprotocol, budget, read check, refusals as immediate closes), the three bridges, detached agent runs | socket, backends, registry | everything in `httpapi/websocket_test.go`: `TestSyncBridgesFramesInBothEncodingsToTheNotesOwner`, `TestUpgradeRefusalsCloseWithTheDocumentedCodes`, `TestFramesOverOneMebibyteClose1009`, `TestTokenExpiryClosesTheSocketWith4401`, `TestSlowClientIsDroppedWith1013`, `TestClientsThatStopAnsweringPingsAreClosedWith1001`, `TestSyncOpsSentJustBeforeTheClientClosesStillReachDocuments` |
| `adapters/httpapi/openapi.go` | serve the embedded OpenAPI document and the docs page | | `TestOpenAPIDocumentAndDocsPageAreServed` |
| `adapters/httpapi/server.go` | the middleware chain and router; health, readiness, drain (readiness 503, wait, close the listener, close sockets with 1012, let detached runs finish) | everything above | `TestHealthAndReadiness`, `TestCORSAllowsTheDocumentedHeaders`, `TestAccessLogShowsRouteTemplatesNotPaths`, `TestRequestIDIsKeptWhenValidAndGeneratedOtherwise` |

### 5. Main

| Go file | Job | Proven by |
|---|---|---|
| `cmd/gateway/config.go` | every variable, limits overriding one budget at a time, CORS default for the Vite dev server only in dev | `TestConfigOverridesOneLimitAndKeepsTheRest`, `TestConfigReportsEveryBadValue`, `TestConfigAllowsTheViteDevServerOnlyInDevWhenOriginsAreUnset` |
| `cmd/gateway/main.go`, `healthcheck.go` | wiring and shutdown order | every e2e journey |

`internal/adapters/httpapi/ratelimit_load_test.go`
(`TestRateLimiterLongRunRateMatchesTheBudget`) checks the limiter under
sustained load; port it as an ignored test you run by hand.

## Dependencies

- Libraries: none.
- Platform: `config`, `logging`, `telemetry`, `auth` (verify), `ratelimit`,
  `redisx`, `events`, `grpcx` (clients), `httpx`, `shutdown`.
- Generated code: `skein-proto`, including the descriptor set.
- Other services: all six backends, through their contracts only.

## Crates

`axum` (ws), `tower`, `tower-http`, `hyper`, `tonic`, `prost`,
`prost-reflect` (option A), `pbjson`, `serde_json`, `redis`, `sha2`,
`ipnet`, `reqwest` (JWKS), `tokio-util` (`CancellationToken`),
`thiserror`, `tracing`; tests: `tokio-tungstenite` as the WebSocket client,
`tower::ServiceExt::oneshot`, `proptest`.

## Pitfalls

- **Refusing an upgrade.** Browsers cannot read the HTTP status of a failed
  upgrade, so a refusal completes the handshake and closes at once with
  the documented code (4401, 4429, 4404, 1012). In axum, accept the upgrade
  and close in `on_upgrade`, rather than returning an error response.
- **Never echo the bearer subprotocol.** Select only `skein.v1.proto` or
  `skein.v1.json` in the response; with neither offered, select none and
  speak JSON.
- **Close never blocks.** The registry calls `Close` under its lock; in
  Rust, closing records the code and cancels a token, and the socket's own
  task does the handshake.
- **Sync half-close.** When the socket closes, the documents stream is
  half-closed, not cancelled, so frames sent just before closing still
  apply; it is cancelled when documents ends it or after `StreamDrain`.
  Dropping a tonic stream cancels it, so keep the request sender alive until
  then.
- **Agent runs are detached.** Closing the socket does not cancel a run;
  keep reading its events (and discarding them) until it ends, times out or
  the gateway stops. `tokio::spawn` the reader with its own deadline.
- **Header forwarding.** grpc-gateway is configured to forward no incoming
  headers; a hand-rolled transcoder that copies headers into metadata would
  let a client name its own user. `TestClientsCannotForgeTheForwardedIdentity`
  exists for this.
- **Idempotency outcome after disconnect.** The outcome must be recorded
  even if the client has gone, or the key stays reserved until the pending
  TTL; record it in a `Drop` guard or a spawned task, not only on the happy
  path.
- **Hashing must match bit for bit.** FNV-1a 64 and splitmix64 use wrapping
  multiplication: `u64::wrapping_mul`, or debug builds panic.

## Done when

- every Go test above passes in Rust;
- the rendezvous golden file passes;
- `make e2e` passes in full with the Rust gateway in front of the Go
  services (and later the Rust ones), including
  `TestRateLimitingAnswers429WithTheDocumentedHeaders`,
  `TestIdempotencyKeyReplaysTheFirstResponse`,
  `TestRemovingAMemberClosesTheirOpenSyncSocket` and
  `TestAnEditSentJustBeforeClosingTheSocketIsKept`.
