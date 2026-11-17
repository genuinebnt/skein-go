# Porting `platform/` to Rust

`platform/` is the shared infrastructure of the Go services: configuration,
logging, telemetry, token signing and verification, gRPC and HTTP
middleware, Postgres, Redis, rate limiting, events, the transactional
outbox and shutdown. [docs/platform.md](../docs/platform.md) is the map;
each package's `doc.go` is the detailed reference. This page is the plan for
porting it. The overall order and the side-by-side setup are in
[docs/porting/README.md](../docs/porting/README.md).

Unlike `libs/`, this module does I/O, so most of the port is choosing the
right crate and keeping the same behaviour at the edges: the same middleware
order, the same error mapping, the same SQL, the same Redis scripts, the
same environment variables. Porting the tests is the way to keep the
behaviour. Every package has a fake for services' unit tests; port the fake
together with the package, because the services' tests depend on it.

## What each package does

| Package | Job | Used by |
|---|---|---|
| `config` | typed env reads with defaults; reports every bad variable at once; `Secret` that never prints | every service |
| `shutdown` | runs components, stops them in reverse order on SIGINT/SIGTERM within a timeout | every service |
| `logging` | JSON logs with service, version, env, trace_id, span_id, request_id, user_id, agent_run_id; key-based redaction; request ID validation | every service |
| `telemetry` | OTLP traces, metrics and logs from `OTEL_*`; W3C propagation; one `Shutdown` | every service |
| `auth` | Ed25519 JWT access tokens for users and agent runs; JWKS; rotation; `RemoteKeySet`; `TokenVerifier` | identity signs; every service verifies |
| `ratelimit` | `Limiter` with GCRA in Redis (a Lua script) and an in-process token bucket | gateway, identity, search |
| `postgres` | traced pool, `WithTx`, the forward-only migration runner | identity, documents, search, notifications |
| `redisx` | traced client; typed `Cache[V]` with TTL, namespaces, singleflight, tag invalidation; `MemoryStore` | every service |
| `events` | `Envelope`, `Publisher`, `Subscriber`; Pub/Sub and in-memory implementations | every service |
| `outbox` | `Enqueue` in the caller's transaction, the relay, `ProcessOnce` for idempotent consumers | identity, documents, search, notifications |
| `grpcx` | gRPC server with the standard interceptor chain, health, reflection in dev, graceful stop; clients with round_robin, retries, keepalive, request ID and token forwarding | every service |
| `httpx` | the same chain for HTTP, plus CORS, security headers, gRPC-code-to-HTTP JSON errors, rate-limit headers | gateway, admin endpoints |

The module does not import generated proto code: events carry payloads as
bytes and `grpcx` is generic over services. Keep that in Rust too: the
platform crates must not depend on `skein-proto`, so they can be built and
tested before it exists.

## Learn first

Check each link before relying on it; all were reachable on 2026-11-17.

| Topic | Read | What to take from it |
|---|---|---|
| gRPC concepts | [gRPC core concepts](https://grpc.io/docs/what-is-grpc/core-concepts/) | the four call types, metadata, deadlines and cancellation; the trailer-versus-header distinction the request ID relies on |
| gRPC deadlines | [Deadlines guide](https://grpc.io/docs/guides/deadlines/) | why the server sets a default deadline for unary calls only |
| gRPC retries | [Retry guide](https://grpc.io/docs/guides/retry/) | the retry policy grpcx declares in the service config, and what "transparent retry" means; tonic has neither, so you will build it with `tower::retry` |
| gRPC health | [Health checking](https://grpc.io/docs/guides/health-checking/) | the `grpc.health.v1` protocol that drain flips to NOT_SERVING |
| gRPC keepalive | [Keepalive guide](https://grpc.io/docs/guides/keepalive/) | the ping enforcement and `MaxConnectionAge` that grpcx configures |
| tonic | [tonic docs](https://docs.rs/tonic/latest/tonic/), [tonic-health](https://docs.rs/tonic-health/latest/tonic_health/), [tonic-reflection](https://docs.rs/tonic-reflection/latest/tonic_reflection/) | `Server::builder().layer(...)`, interceptors versus tower layers, `Status` and its details, the health reporter |
| tower | [tower](https://docs.rs/tower/latest/tower/), [tower-http](https://docs.rs/tower-http/latest/tower_http/) | `Layer` and `Service`; `ServiceBuilder` order is outermost first, the same order as the Go chain; `request_id`, `catch_panic`, `trace`, `cors`, `timeout` |
| shutdown | [Tokio graceful shutdown](https://tokio.rs/tokio/topics/shutdown), [CancellationToken](https://docs.rs/tokio-util/latest/tokio_util/sync/struct.CancellationToken.html) | the token plus `JoinSet` shape that replaces `shutdown.Manager` and `context.Context` |
| blocking in async | [Async: what is blocking?](https://ryhl.io/blog/async-what-is-blocking/) | why Argon2 and Lua-free CPU work go to `spawn_blocking`, and why a `std::sync::Mutex` is fine if never held across `.await` |
| tracing | [tracing](https://docs.rs/tracing/latest/tracing/), [tracing-subscriber](https://docs.rs/tracing-subscriber/latest/tracing_subscriber/) | spans with fields replace slog context attributes; the JSON formatter; writing a custom `Layer` for redaction |
| OpenTelemetry | [OpenTelemetry Rust](https://opentelemetry.io/docs/languages/rust/), [opentelemetry-otlp](https://docs.rs/opentelemetry-otlp/latest/opentelemetry_otlp/), [tracing-opentelemetry](https://docs.rs/tracing-opentelemetry/latest/tracing_opentelemetry/) | SDK setup from `OTEL_*` variables; bridging `tracing` spans to OTel |
| metric names | [RPC metrics semantic conventions](https://opentelemetry.io/docs/specs/semconv/rpc/rpc-metrics/), [messaging spans](https://opentelemetry.io/docs/specs/semconv/messaging/messaging-spans/) | the names in docs/observability.md (`rpc.server.duration`, `<topic> send`, `<subscription> process`); dashboards break if the Rust names differ |
| trace context | [W3C Trace Context](https://www.w3.org/TR/trace-context/) | the `traceparent` string that events carry across Pub/Sub |
| JWT with EdDSA | [RFC 8037](https://datatracker.ietf.org/doc/html/rfc8037), [RFC 9068](https://datatracker.ietf.org/doc/html/rfc9068), [RFC 8725](https://datatracker.ietf.org/doc/html/rfc8725) | Ed25519 in JOSE (`OKP`, `crv: Ed25519`); the `typ: at+jwt` access-token profile; the BCP rules auth enforces (pin `alg`, check `typ`, `iss`, `aud`) |
| JWKS | [RFC 7517](https://datatracker.ietf.org/doc/html/rfc7517), [RFC 7638](https://datatracker.ietf.org/doc/html/rfc7638) | the key-set document identity publishes, and the thumbprint that is every key ID |
| JWT crates | [jsonwebtoken](https://docs.rs/jsonwebtoken/latest/jsonwebtoken/), [ed25519-dalek](https://docs.rs/ed25519-dalek/latest/ed25519_dalek/) | `Algorithm::EdDSA`, `Validation` with one algorithm, `DecodingKey::from_ed_der` or `from_jwk` |
| GCRA | [Rate Limiting, Cells, and GCRA](https://brandur.org/rate-limiting), [GCRA on Wikipedia](https://en.wikipedia.org/wiki/Generic_cell_rate_algorithm) | the theoretical arrival time (TAT) that the Lua script stores; why GCRA needs one key and no timer |
| Redis scripting | [Redis Lua scripting](https://redis.io/docs/latest/develop/interact/programmability/eval-intro/) | `EVALSHA` with fallback to `EVAL`, atomicity, why scripts must name every key in `KEYS` |
| Redis crates | [redis-rs](https://docs.rs/redis/latest/redis/), [fred](https://docs.rs/fred/latest/fred/), [moka](https://docs.rs/moka/latest/moka/) | script support and connection managers; moka for the in-memory store |
| sqlx | [sqlx](https://docs.rs/sqlx/latest/sqlx/) | `PgPool`, `query_as` with `FromRow`, `pool.begin()` and explicit commit, `Executor` for "pool or transaction" |
| SKIP LOCKED | [SELECT ... FOR UPDATE SKIP LOCKED](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE), [advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) | the two locking tools the outbox relay and the migration runner use |
| outbox | [Transactional outbox](https://microservices.io/patterns/data/transactional-outbox.html), [Idempotent consumer](https://microservices.io/patterns/communication-style/idempotent-consumer.html) | the pattern behind `Enqueue`, the relay and `ProcessOnce` |
| Pub/Sub | [ordering](https://cloud.google.com/pubsub/docs/ordering), [handling failures and dead letters](https://cloud.google.com/pubsub/docs/handling-failures), [emulator](https://cloud.google.com/pubsub/docs/emulator) | ordering keys and how a nack holds back its key; dead-letter topics and `delivery_attempt`; `PUBSUB_EMULATOR_HOST` |
| Pub/Sub crate | [google-cloud-pubsub](https://docs.rs/google-cloud-pubsub/latest/google_cloud_pubsub/) | publisher with ordering keys, subscriber with `delivery_attempt`, emulator support |
| secrets | [secrecy](https://docs.rs/secrecy/latest/secrecy/) | `SecretString` with `ExposeSecret`, the Rust form of `config.Secret` |
| integration tests | [testcontainers](https://docs.rs/testcontainers/latest/testcontainers/), [testcontainers-modules](https://docs.rs/testcontainers-modules/latest/testcontainers_modules/) | the Rust form of the `integration` build tag tests (or run them against the native stack, as `make integration-native` does) |
| reference code | [tokio-rs/axum examples](https://github.com/tokio-rs/axum) (the `examples/` directory), [launchbadge/realworld-axum-sqlx](https://github.com/launchbadge/realworld-axum-sqlx) | idiomatic layering of tower middleware, `tracing` and sqlx in a real service |

## Port order inside `platform/`

The order follows the packages' non-test imports: nothing is ported before
what it uses. Each step names the Go files, the Rust crate, the job of each
file, what must exist first, and the tests that prove it. Port each
package's tests by name (snake case), and its fake, before moving on.

### 1. `config` -> `skein-config`

Needs nothing.

| Go file | Job | Rust |
|---|---|---|
| `config/config.go` | `Loader` over a lookup function; typed accessors (`String`, `Int`, `Bool`, `Duration`, `URL`, ...) that record errors instead of returning them; `Err()` joins them all | a `Loader` struct holding `Vec<ConfigError>`; `FromEnv` and `FromMap` constructors (the map is the test fake) |
| `config/secret.go` | `Secret` that prints as `[REDACTED]` in every format | `secrecy::SecretString`; implement `Debug` on config structs by hand or rely on secrecy's |
| `config/service.go` | `LoadService`: `SKEIN_ENV` (dev, test, staging, prod) and `SKEIN_VERSION` | `enum Env` with `FromStr` |

Tests: `TestDefaultsApplyWhenUnsetOrEmpty` (an empty variable is unset),
`TestErrReportsEveryProblemAtOnce`, `TestInvalidValueFallsBackToDefault`,
`TestSecretIsRedactedEverywhere`, `TestLoadServiceValidatesEnv`.

Do not reach for `envy` or `figment` unless they keep the "every problem at
once" behaviour; that behaviour is what makes a misconfigured Rust binary
fail loudly when it replaces a Go one.

### 2. `shutdown` -> `skein-shutdown`

Needs nothing.

| Go file | Job | Rust |
|---|---|---|
| `shutdown/shutdown.go` | `Manager.Add(name, run, stop)`; `Run` starts every `run`, waits for a signal, context cancel or the first component to exit, then calls `stop` in reverse order within a timeout | a `Manager` holding `Vec<Component>`; run futures in a `JoinSet`, a `CancellationToken` as the shared context, `tokio::signal::unix` for SIGTERM, `tokio::time::timeout` per stop |

Tests: `TestCancellingContextStopsComponentsInReverseOrder`,
`TestSIGTERMStartsShutdown`, `TestFailingComponentStopsTheRestAndIsReportedOnce`,
`TestComponentExitingEarlyIsAnError`, `TestStuckComponentTimesOutAndLaterOnesStillStop`.

### 3. `logging` -> `skein-logging`

Needs `config` (only for the env names).

| Go file | Job | Rust |
|---|---|---|
| `logging/logging.go` | a JSON slog handler with the fixed resource fields; `Discard()` | `tracing_subscriber::fmt().json()` with the service fields recorded on a root span, or a small custom JSON `Layer` if the field names must match exactly (they must: Loki queries use them) |
| `logging/context.go` | `WithRequestID`, `WithSubject`, `WithAgentRunID` put values in the context; the handler adds them to every record | fields on the request span; the JSON layer reads them from the current span's extensions. `RequestIDOrNew` validates an incoming ID (length, charset) or makes a UUIDv7 |
| `logging/redact.go` | `IsSensitiveKey` (whole-word match on password, token, secret, authorization, ...) and redaction at every level | a `Layer` that replaces values of sensitive field names, or a `Visit` wrapper in the formatter |

Tests: `TestRecordsCarryServiceAndContextFields`,
`TestSensitiveKeysAreRedactedAtEveryLevel`, `TestIsSensitiveKeyMatchesWholeWords`,
`TestRequestIDValidation`; fuzz targets `FuzzRequestIDOrNew` and
`FuzzIsSensitiveKey` (port the corpus in `logging/testdata/fuzz`).

### 4. `telemetry` -> `skein-telemetry`

Needs `logging`.

| Go file | Job | Rust |
|---|---|---|
| `telemetry/telemetry.go` | `Setup` builds tracer, meter and logger providers from `OTEL_*`, installs the W3C propagator, returns a `LogHandler` for the slog bridge and one `Shutdown` | `opentelemetry_sdk` providers with `opentelemetry-otlp` (gRPC/tonic exporter); `tracing-opentelemetry` layer; `opentelemetry-appender-tracing` for logs; `OTEL_SDK_DISABLED=true` installs no-op providers |

Tests: `TestDisabledSDKInstallsNoopProviders`,
`TestSpansCarryServiceResourceAndEnvOverrides`,
`TestPropagatorIsW3CTraceContextAndBaggage`,
`TestUnsupportedExporterIsRejected`. Use the SDK's in-memory exporters in
tests, as Go does.

### 5. `auth` -> `skein-auth`

Needs nothing from platform.

| Go file | Job | Rust |
|---|---|---|
| `auth/keys.go` | `KeyID` (RFC 7638 thumbprint), `GenerateKey`, `KeySet` (`With`, `Without`), `KeySource` | `ed25519-dalek` keys; thumbprint = base64url(SHA-256 of `{"crv":"Ed25519","kty":"OKP","x":"..."}` with members in that order) |
| `auth/token.go` | `Subject` (user ID, optional agent run ID), `Claims`, `Signer.Issue`, `Verifier.Verify` with leeway, `TokenVerifier` trait | `jsonwebtoken` with `Header { alg: EdDSA, typ: "at+jwt", kid }` and `Validation` pinned to EdDSA, `iss = skein-identity`, `aud` contains `skein`; check `typ` yourself (jsonwebtoken does not) |
| `auth/remote.go` | `RemoteKeySet`: fetch JWKS, cache, refetch at once (rate-limited) on an unknown `kid`, keep serving the last good set during an outage | a struct with `RwLock<KeySet>` and a `tokio::sync::Mutex` to single-flight refetches |
| `auth/context.go` | put verified claims and the raw token in the context | request extensions (`http::Extensions`) in tower; a typed `Claims` extension |

Tests (all security-relevant, port every one): `TestAlgorithmConfusionIsRejected`,
`TestTokenSignedByWrongKeyUnderTrustedKidIsRejected`, `TestTamperedPayloadIsRejected`,
`TestWrongTypeIssuerOrAudienceIsRejected`, `TestExpiredTokenIsRejectedAfterLeeway`,
`TestTokenFromTheFutureIsRejected`, `TestNonCanonicalBase64SpellingIsRejected`,
`TestRotationKeepsOldTokensValidUntilTheOldKeyIsRemoved`,
`TestJWKSRoundTripsAndKeyIDIsTheThumbprint`,
`TestRemoteKeySetCachesRefetchesOnUnknownKidAndSurvivesOutages`; fuzz
`FuzzVerify` and `FuzzParseJWKS`.

Cross-language check, the first one to do: start Go identity
(`make up-native`), sign up through the gateway, and verify the access
token with the Rust verifier against identity's
`/.well-known/jwks.json`. Then sign a token in Rust and verify it with Go
(`auth.Verifier` in a scratch test). Until both directions pass, no Rust
service can join the Go stack.

### 6. `ratelimit` -> `skein-ratelimit`

Needs nothing from platform.

| Go file | Job | Rust |
|---|---|---|
| `ratelimit/ratelimit.go` | `Limit` (rate, burst), `Decision` (allowed, remaining, retry after, reset), `Limiter` trait | the same types; `async fn allow(&self, key, cost) -> Result<Decision>` |
| `ratelimit/gcra.go` | the GCRA Lua script; `NewRedisGCRA(client, prefix)` | the same script, byte for byte, through `redis::Script` (EVALSHA with EVAL fallback) |
| `ratelimit/tokenbucket.go` | in-process token bucket with a fake clock and idle sweeping | `Mutex<HashMap<String, Bucket>>`; use `u128` products instead of the Go overflow checks |

Tests: `TestDecisionsMatchReferenceModel` (both limiters against an exact
rational-arithmetic model on a fake clock: port the model too),
`TestNoWindowAdmitsMoreThanBurstPlusRate`, `TestWaitingRetryAfterIsExactlyEnough`,
`TestRedisKeysExpireWhenBucketIsFull`. Go tests the script on miniredis; in
Rust run them against a real `redis-server` (it is installed natively), or
behind an `integration` feature.

The key format (`<prefix>:<key>`) and the stored TAT (microseconds since
the epoch) are shared state: a Go and a Rust gateway replica must read each
other's keys.

### 7. `postgres` -> `skein-postgres`

Needs `config`.

| Go file | Job | Rust |
|---|---|---|
| `postgres/postgres.go` | `LoadConfig` (`DATABASE_URL`, `DATABASE_MAX_CONNS`), `Open` a traced pool, `WithTx` (commit on success, roll back on error or panic, even when the context is cancelled), `Beginner` and `Execer` interfaces for fakes | `PgPoolOptions`; `WithTx` becomes `pool.begin()` plus an explicit `commit()`; a dropped `Transaction` rolls back, which covers errors and panics |
| `postgres/migrate.go` | the migration runner: embedded files, plan (pure), apply under a schema advisory lock, `schema_migrations(version, name, checksum)`, `-- skein:no-transaction` files, `ErrOutOfOrder` | port the runner (about 150 lines) and embed the same `.sql` files with `include_dir!` or `include_str!` |
| `postgres/pgtest/pgtest.go` | test helper: a throwaway database per test | a helper that creates a database on the native Postgres (`25432`) or a testcontainer |

Tests: `TestPlan` (pure; port it first), `TestParseMigrationName`,
`FuzzParseMigrationName`, `TestLoadMigrationsRejectsBadNamesAndDuplicates`,
then the integration tests `TestMigrateAppliesOnceAndDetectsEditedFiles`,
`TestFailedMigrationLeavesNoTrace`, `TestConcurrentMigrateRunsEachMigrationOnce`.

Port the runner rather than switching to `sqlx::migrate!`. The Go runner
stores SHA-256 checksums in `schema_migrations`; sqlx stores SHA-384
checksums in `_sqlx_migrations`. With the same runner, a Rust service starts
cleanly on a database the Go service migrated, and the other way round,
which is what side-by-side running needs. Switch to sqlx's migrator later,
if you want, with a one-off migration that copies the rows across.

### 8. `redisx` -> `skein-redis`

Needs `config`.

| Go file | Job | Rust |
|---|---|---|
| `redisx/client.go` | `LoadConfig` (`REDIS_URL`), a traced client | `redis::aio::ConnectionManager` (or `fred`), with a `tracing` span per command |
| `redisx/store.go` | `Store` trait (get, set with TTL and tags, delete, invalidate tag); `RedisStore` (hashes that record their own tags, Lua for set and invalidate); `MemoryStore` | the same Lua; `MemoryStore` over `moka` plus a tag index, or a plain `Mutex<HashMap>` with expiry (simpler, and it is a fake) |
| `redisx/cache.go` | `Cache[V]`: typed JSON values, namespaces, `GetOrLoad` with singleflight, fail open on store errors, metrics | `Cache<V: Serialize + DeserializeOwned>`; singleflight as a `Mutex<HashMap<String, Shared<BoxFuture<...>>>>`; a cancelled caller must not cancel the shared load (spawn the load) |

Tests: `TestRedisStoreMatchesMemoryStore` (randomised differential test;
port it, it found the over-invalidation bug the hash design fixes),
`TestGetOrLoadLoadsOnceUnderStampede`, `TestCancelledCallerDoesNotFailTheSharedLoad`,
`TestStoreOutageFallsBackToLoader`, `TestUndecodableEntryIsAMiss`,
`TestRealRedisMatchesMemoryStore` (integration).

### 9. `events` -> `skein-events`

Needs `config`.

| Go file | Job | Rust |
|---|---|---|
| `events/events.go` | `Envelope` (ID, source, ordering key, traceparent, attributes, payload bytes), `Validate` (reserved attribute names), `Publisher`, `Subscriber`, `Handler`, `Message.DeliveryAttempt` | the same struct; `#[async_trait]` traits or `async fn` in traits with `Arc<dyn ...>` via boxing |
| `events/memory.go` | in-memory broker with per-key ordering and dead letters, pass-based delivery | `Mutex<HashMap<String, Subscription>>`; keep the same pass-based delivery so consumer tests see production's ordering |
| `events/pubsub.go` | Pub/Sub publisher and subscriber; attributes `skein-event-id`, `skein-source`, `traceparent`; consumer span per delivery | `google-cloud-pubsub`; set `ordering_key`; read `delivery_attempt`; start a consumer span whose parent is the envelope's traceparent |

Tests: `TestEnvelopeValidation`, `TestMemoryKeepsPerKeyOrderUnderRandomFailures`,
`TestMemoryRedeliversNackedAndDeadLettersAfterMaxAttempts`,
`TestMemoryPanickingHandlerNacks` (a panicking handler must nack: wrap
handlers in `catch_unwind` via `FutureExt::catch_unwind`),
`TestDeliveryContinuesThePublishersTrace`, `FuzzAttributesRoundTrip`, then
`TestPubSubRoundTripsEnvelopes` and `TestEmulatorDeadLettersAfterMaxAttempts`
against the emulator on `28085`.

The local-first work adds `EVENTS_BACKEND=pubsub|redis|memory` (a Redis
Streams backend for machines without the emulator). Port whatever has
merged into `platform/events` when you get here; the contract is the same
`Publisher` and `Subscriber`.

### 10. `outbox` -> `skein-outbox`

Needs `events` and `postgres`.

| Go file | Job | Rust |
|---|---|---|
| `outbox/outbox.go` | `Enqueue(ctx, tx, topic, envelope)` captures the traceparent and inserts in the caller's transaction; `SchemaSQL` | `async fn enqueue(tx: &mut PgConnection, topic, envelope)`; the schema stays in each service's migration (a copy, as in Go) |
| `outbox/postgres.go` | the `Store` over Postgres: the claim query (advisory transaction lock, `FOR UPDATE SKIP LOCKED`, per-key "no earlier unpublished row backing off"), mark published, record failure, prune | the same SQL strings, copied exactly; they are the hard part and they are already right |
| `outbox/relay.go` | `Relay.RunOnce` and `Run`: claim a batch, publish per ordering key one at a time and different keys concurrently, backoff with jitter, wake on `Notify` | a Tokio task; `tokio::sync::Notify` for wakeups; a `JoinSet` per batch with one task per key; `Semaphore` for `Concurrency` |
| `outbox/consumer.go` | `ProcessOnce(ctx, tx, consumer, eventID, effect)`: insert into `processed_events` and run the effect in one transaction; a duplicate is skipped and counted | the same, taking `&mut Transaction` |

Tests: `TestCrashBetweenPublishAndCommitDuplicatesButNeverLoses`,
`TestFailedEventHoldsBackItsKeyButNotOthers`,
`TestRelayUnderRandomFaultsNeverLosesOrReorders` (randomised; port it with
its seed logging), `TestDuplicateDeliveryAppliesTheEffectOnce`,
`TestBackoffDoublesUpToTheMaximum`, `TestDefaultJitterStaysBetweenHalfAndAll`,
then the integration tests `TestRelayPublishesInOrderAndRespectsBackoff`,
`TestSecondRelayClaimsNothingWhileFirstHoldsTheLock`,
`TestProcessOnceRecordsEachEventOncePerConsumer`. The in-memory `Store`
used by the unit tests is in `outbox/fakes_test.go`; port it as a public
fake.

ADR-0016 turns the outbox table into a permanent event log with a
`seq` column and a `ReadEvents` RPC. It has not been built in Go yet; if it
lands before you port this package, port the new shape.

### 11. `grpcx` -> `skein-grpc`

Needs `auth`, `config`, `logging`, `ratelimit`.

| Go file | Job | Rust |
|---|---|---|
| `grpcx/interceptors.go` | request ID (into the context, echoed as a **trailer**), recovery, logging, auth (fails closed; public methods, health and reflection exempt), default deadline (unary only), rate-limit hook (fails open, `RetryInfo` detail) | tower layers on `tonic::transport::Server`, in the same order (outermost first): `tower_http::trace` or a custom OTel layer, request ID, `CatchPanicLayer` mapped to `Status::internal`, logging, auth, timeout, rate limit |
| `grpcx/server.go` | `NewServer(ServerConfig)`, `Serve(ctx, listener)`: health, reflection in dev and test, keepalive, `MaxConnectionAge`, drain (NOT_SERVING, graceful stop, forced stop after timeout) | `Server::builder()` with `http2_keepalive_interval`, `tcp_keepalive`; `tonic_health::server::health_reporter`; `serve_with_incoming_shutdown`; tonic has no `MaxConnectionAge`, so close connections with a per-connection timer (or accept the gap and note it) |
| `grpcx/client.go` | `NewClient(target)`: `dns:///` with round_robin, retries for listed idempotent methods on UNAVAILABLE, keepalive; interceptors forward the request ID and the caller's bearer token | `Channel::balance_channel` fed by a DNS resolver task (tonic has no `dns:///` resolver), or `Endpoint::connect_lazy` per address behind `tower::balance::p2c`; retries via `tower::retry::Policy` checking the method path against the idempotent list; an interceptor copying the request ID and token from a task-local or explicit argument |

Tests: `TestAuthFailsClosedWithoutVerifier`, `TestPublicMethodsAndHealthNeedNoToken`,
`TestPanicBecomesInternalAndIsLoggedWithStack`,
`TestClientDeadlineIsKeptAndMissingOneDefaulted`,
`TestRateLimitDenialCarriesRetryAfter`, `TestStreamsAreAuthenticatedAndCarryRequestID`,
`TestClientForwardsRequestIDAndCallerToken`,
`TestIdempotentMethodsAreRetriedOthersAreNot`,
`TestServeDrainsInFlightCallsAndStopsServingHealth`. Go uses bufconn; in
Rust use `tokio::io::duplex` with `Server::serve_with_incoming` and a
connector, or a loopback port.

### 12. `httpx` -> `skein-http`

Needs `auth`, `grpcx`, `logging`, `ratelimit`.

| Go file | Job | Rust |
|---|---|---|
| `httpx/middleware.go` | the chain: otelhttp, request ID, recovery (JSON 500), access log (route template, never the query), security headers, CORS | tower-http layers around an `axum::Router` in the same order; `MatchedPath` for the route template |
| `httpx/errors.go` | gRPC code to HTTP status, `google.rpc.Status` JSON body, server faults hidden, `RetryInfo` to `Retry-After` | an `ApiError` enum implementing `IntoResponse` with the same table; build the JSON by hand to avoid depending on `skein-proto` |
| `httpx/routes.go` | per-route helpers: authenticate a bearer token, rate-limit headers | extractors (`FromRequestParts`) for the verified claims; a layer for rate-limit headers |

Tests: `TestStatusFromCodeMapsEveryCode` (table test; the table is the
contract in docs/api/README.md "Errors"), `TestWriteErrorHidesServerFaultsAndKeepsClientErrors`,
`TestWriteErrorTurnsRetryInfoIntoRetryAfter`,
`TestChainSetsRequestIDLogsRouteAndNeverTheQuery`, `TestCORSAllowsOnlyListedOrigins`,
`TestRateLimitSetsHeadersAndDenies`. Use `tower::ServiceExt::oneshot` in
place of `httptest`.

## Crates

| Need | Crate |
|---|---|
| async runtime, signals | `tokio` (full), `tokio-util` (`CancellationToken`) |
| gRPC | `tonic`, `tonic-health`, `tonic-reflection`, `prost`, `tonic-types` (rich error details such as `RetryInfo`) |
| HTTP | `axum`, `tower`, `tower-http`, `http`, `hyper` |
| tracing and telemetry | `tracing`, `tracing-subscriber` (json, env-filter), `opentelemetry`, `opentelemetry_sdk`, `opentelemetry-otlp`, `tracing-opentelemetry`, `opentelemetry-appender-tracing` |
| tokens | `jsonwebtoken`, `ed25519-dalek`, `base64`, `sha2`, `uuid` (v7) |
| Postgres | `sqlx` (postgres, runtime-tokio, tls-rustls, uuid, chrono) |
| Redis | `redis` (tokio-comp, connection-manager, script) or `fred`; `moka` for memory caches |
| Pub/Sub | `google-cloud-pubsub` |
| errors | `thiserror` in every crate; `anyhow` only in `main` |
| config secrets | `secrecy` |
| time | `chrono` (or `time`); keep UTC and microsecond truncation where Go truncates |
| tests | `proptest`, `tokio::test`, `testcontainers` (optional), `cargo-fuzz` |

Pin every version once in the workspace `Cargo.toml`
(`[workspace.dependencies]`); OpenTelemetry crates in particular must all be
the same release.

## Pitfalls

- **Middleware order.** `ServiceBuilder::new().layer(a).layer(b)` makes `a`
  the outermost. Write the Go order down as a test (the Go tests check, for
  example, that a panic log carries the request ID, which needs request ID
  outside recovery).
- **Trailer, not header.** grpcx echoes the request ID in a trailer so that
  a call whose headers reached the client can still be retried. In tonic,
  set it on the response's trailers, not with `Response::metadata_mut` on
  the initial metadata, for unary calls.
- **Auth fails closed, limits and caches fail open.** Keep each default;
  tests pin both.
- **No `MaxConnectionAge` and no `dns:///` in tonic.** Without them clients
  stick to the replicas that existed when they connected. Port the
  behaviour (a resolver task and connection recycling) or record the gap.
- **`std::sync::Mutex` across `.await`** compiles inside `tokio::spawn` only
  if the guard is dropped first; the compiler will remind you, but the fix
  is to restructure, not to switch to `tokio::sync::Mutex` everywhere.
- **Lua scripts are shared state.** Copy them byte for byte; a reformatted
  script has a different SHA and is a different script to Redis.
- **Migration checksums.** Do not let sqlx run the Go migrations: its
  checksum algorithm and table differ (see step 7).
- **Microsecond times.** Postgres stores microseconds; Go code truncates
  times before comparing or encoding page tokens. `chrono` keeps
  nanoseconds unless you truncate.
- **Panics in handlers.** Go's recovery catches panics in every handler and
  consumer. In Rust a panicking task aborts only itself; catch it at the
  same boundaries (`CatchPanicLayer`, `catch_unwind` around event
  handlers), or a panicking consumer silently stops consuming.
