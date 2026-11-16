# Platform module

`platform/` (`github.com/genuinebnt/skein-go/platform`) is the shared
infrastructure of the Go services: configuration, logging, telemetry, gRPC
and HTTP middleware, tokens, Postgres, Redis, rate limiting, events, the
transactional outbox and shutdown. It exists so that gateway, identity,
documents, search and notifications stay thin and behave the same way.

Unlike `libs/`, this module does I/O. Each package is small, puts its
behaviour behind an interface where a service would want a fake, and is
unit-tested without Docker or the network. Every package's `doc.go` (or
package comment) is the detailed reference: design, invariants and Rust
porting notes. This page is the map.

## Packages

| Package | What it gives a service | Fake for tests |
|---|---|---|
| `config` | Typed env reads with defaults, all errors reported at once; `Secret` that never prints | `config.FromMap` |
| `logging` | slog JSON logs with service, version, env, trace_id, span_id, request_id, user_id, agent_run_id; key-based redaction | `logging.Discard()` |
| `telemetry` | OTLP gRPC traces, metrics and logs from `OTEL_*` variables; W3C propagation; one `Shutdown` | `OTEL_SDK_DISABLED=true`, or in-memory exporters |
| `grpcx` | Server with the standard interceptor chain, health, dev-only reflection, graceful stop; clients with round_robin, retries, keepalive, request ID and token forwarding | bufconn |
| `httpx` | The same chain for the gateway, plus CORS, security headers, gRPC-code-to-HTTP JSON errors, rate limit headers | `httptest` |
| `auth` | Ed25519 JWT access tokens for users and delegated agent runs; JWKS; rotation; `TokenVerifier` | `auth.NewKeySet` with a generated key |
| `postgres` | Traced pgx pool, `WithTx`, embedded forward-only migrations | `postgres.Beginner` / `Execer` interfaces |
| `redisx` | Traced Redis client; typed `Cache[V]` with TTL, namespaces, singleflight, tag invalidation | `redisx.MemoryStore` |
| `ratelimit` | `Limiter` over GCRA in Redis and an in-process token bucket | `ratelimit.NewTokenBucket` with a fake clock |
| `events` | `Publisher` / `Subscriber` over event envelopes; Pub/Sub (and its emulator) | `events.Memory` |
| `outbox` | `Enqueue` in the caller's transaction, the relay, idempotent consumers | in-memory `Store` (see `outbox/fakes_test.go`) |
| `shutdown` | Run components, stop them in reverse order on SIGINT/SIGTERM within a timeout | a cancellable context |

The module does not import generated proto code (`gen/go`): events carry
payloads as bytes, and grpcx is generic over services.

## Wiring a service

```go
func main() {
	l := config.FromEnv()
	svc := config.LoadService(l, "documents")
	dbCfg := postgres.LoadConfig(l)
	psCfg := events.LoadPubSubConfig(l)
	if err := l.Err(); err != nil {
		log.Fatal(err) // every bad variable, in one message
	}

	ctx := context.Background()
	tel, _ := telemetry.Setup(ctx, telemetry.Config{Service: svc.Name, Version: svc.Version, Env: string(svc.Env)})
	logger := logging.New(logging.Options{Service: svc.Name, Version: svc.Version, Env: string(svc.Env),
		Extra: []slog.Handler{tel.LogHandler()}})

	pool, _ := postgres.Open(ctx, dbCfg)
	_ = postgres.Migrate(ctx, pool, migrations, "migrations", logger)
	bus, _ := events.NewPubSub(ctx, psCfg)
	relay, _ := outbox.NewRelay(outbox.NewPostgresStore(pool, outbox.PostgresStoreOptions{}), bus, outbox.RelayConfig{Logger: logger})

	keys := auth.NewRemoteKeySet(fetchJWKSFromIdentity, auth.RemoteKeySetOptions{})
	srv := grpcx.NewServer(grpcx.ServerConfig{Env: svc.Env, Logger: logger,
		Verifier: auth.NewVerifier(keys, auth.VerifierOptions{})})
	// documentsv1.RegisterDocumentsServiceServer(srv, handlers)
	lis, _ := net.Listen("tcp", ":9090")

	m := shutdown.New(shutdown.Options{Logger: logger})
	m.Add("telemetry", nil, tel.Shutdown)
	m.Add("postgres", nil, func(context.Context) error { pool.Close(); return nil })
	m.Add("pubsub", nil, func(context.Context) error { return bus.Close() })
	m.Add("outbox-relay", relay.Run, nil)
	m.Add("grpc", func(ctx context.Context) error { return srv.Serve(ctx, lis) }, nil)
	if err := m.Run(ctx); err != nil {
		logger.Error("exit", "error", err)
		os.Exit(1)
	}
}
```

Components are added in dependency order and stop in reverse: the server
drains first, then the relay, then the clients, and telemetry flushes last.

## Middleware order

gRPC (grpcx) and HTTP (httpx) use the same order, outermost first:

| # | gRPC | HTTP |
|---|---|---|
| 0 | otelgrpc stats handler (transport level, around everything) | otelhttp |
| 1 | request ID (`x-request-id`, echoed as a trailer) | request ID (`X-Request-ID`) |
| 2 | recovery (panic to `codes.Internal`, stack logged) | recovery (JSON 500) |
| 3 | logging (method, code, duration) | access log (route, status, bytes, duration) |
| 4 | auth (bearer token, public methods, fails closed) | security headers |
| 5 | default deadline (unary only) | CORS |
| 6 | rate-limit hook (fail open) | (auth and rate limit per route) |

Two deviations from the order first proposed for this module, both
deliberate. Tracing is position 0, not 3, because otelgrpc instruments
through a stats handler, which gRPC always runs outside the interceptors.
Request ID runs before recovery so that the panic log line carries the
request ID. The request ID goes back to the client in a **trailer**, not a
header: a call whose response headers have reached the client is committed,
and gRPC will not retry it (a test catches a regression).

## Environment variables

| Variable | Read by | Default |
|---|---|---|
| `SKEIN_ENV` | `config.LoadService` | `dev` (one of dev, test, staging, prod) |
| `SKEIN_VERSION` | `config.LoadService` | `dev` |
| `DATABASE_URL`, `DATABASE_MAX_CONNS` | `postgres.LoadConfig` | required, pgx default |
| `REDIS_URL` | `redisx.LoadConfig` | required |
| `PUBSUB_PROJECT` | `events.LoadPubSubConfig` | required |
| `PUBSUB_EMULATOR_HOST` | the Pub/Sub client library | unset (production) |
| `OTEL_SDK_DISABLED`, `OTEL_EXPORTER_OTLP_*`, `OTEL_{TRACES,METRICS,LOGS}_EXPORTER`, `OTEL_TRACES_SAMPLER`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | `telemetry` and the OTel SDK | SDK defaults |

## Metrics

No `docs/observability.md` existed on any branch when this module was
written, so this table is the reference until that document lands and links
here. Names follow the OpenTelemetry semantic conventions where one exists,
and a `skein.` prefix otherwise.

| Metric | Type | Attributes | Source |
|---|---|---|---|
| `rpc.server.duration`, `rpc.client.duration` | histogram, ms | `rpc.service`, `rpc.method`, `rpc.grpc.status_code` | otelgrpc (semconv 1.37) via grpcx |
| `rpc.server.request.size`, `rpc.server.response.size`, `rpc.server.requests_per_rpc`, `rpc.server.responses_per_rpc` (and client equivalents) | histogram | as above | otelgrpc |
| `http.server.request.duration` | histogram, s | `http.request.method`, `http.route`, `http.response.status_code` | otelhttp via httpx |
| `http.server.request.body.size`, `http.server.response.body.size` | histogram, By | as above | otelhttp |
| `db.client.*`, pgx pool statistics | various | | otelpgx via postgres |
| `db.client.connections.*` (Redis pool) | various | | redisotel via redisx |
| `skein.cache.hits`, `skein.cache.misses` | counter | `cache.name` | redisx |
| `skein.cache.invalidations` | counter (entries removed) | `cache.name` (`*` for tag invalidation) | redisx |
| `skein.cache.errors` | counter | `cache.name`, `operation` | redisx |
| `skein.outbox.published`, `skein.outbox.publish_failures` | counter | `messaging.destination.name` | outbox relay |
| `skein.outbox.publish_lag` | histogram, s | `messaging.destination.name` | outbox relay |
| `skein.events.duplicates` | counter | `consumer` | outbox consumer |

RED for a service: rate is the count of `rpc.server.duration`, errors are
the counts whose `rpc.grpc.status_code` is a server fault, and duration is
the histogram itself. The same holds for `http.server.request.duration` at
the gateway.

Spans: `<service>/<method>` (gRPC), the HTTP operation name, `<topic> send`
(producer), `<subscription> process` (consumer, child of the event's
`traceparent`), and one span per SQL query and Redis command.

## Decisions

These are recorded here rather than as ADRs because they are internal to
one module; promote any of them to an ADR if they become contested.

- **Tokens are Ed25519 JWTs, not PASETO v4.public.** Same cryptography; JWT
  gets PASETO's safety by pinning `alg` to EdDSA, requiring `typ: at+jwt`
  and strict base64, all tested, and brings the JWKS standard for key
  publication and rotation plus mature libraries in Go, Python (the agent)
  and Rust (the port). Key IDs are RFC 7638 thumbprints.
- **Migrations use a 150-line runner, not goose.** goose needs a second
  driver stack (database/sql) and brings down-migrations and Go migrations
  this project does not want. The runner has sqlx's semantics (forward-only,
  checksums), an advisory lock per schema, and detection of out-of-order
  versions.
- **One active outbox relay per service.** Every replica runs a relay, but
  claims take a transaction advisory lock, so per-key order holds across
  replicas. `FOR UPDATE SKIP LOCKED` keeps claims non-blocking, and
  `PostgresStoreOptions.Shared` allows concurrent relays where order across
  them does not matter.
- **Rate limits and caches fail open** when Redis fails: they protect and
  accelerate, and are not worth an outage. Auth fails closed.
- **Redis cache entries are hashes that record their own tags.** A
  randomised differential test against the in-memory store showed that
  plain tag sets over-invalidate an entry re-set without the tag.

## Tests

```
cd platform
go test -race ./...                         # unit tests, no Docker or network
go test -tags integration ./...             # adds Postgres, Redis, Pub/Sub via testcontainers
go test -run='^$' -fuzz=FuzzVerify ./auth   # one fuzz target; make fuzz runs them all
```

Unit tests use in-process fakes only: bufconn for gRPC, `httptest`,
miniredis (an in-process Redis with Lua) for the GCRA script and
RedisStore, and Google's `pstest` (an in-process Pub/Sub) for the Pub/Sub
adapter. The last two listen on a loopback port inside the test process;
nothing leaves the machine.

Property and randomised tests log their seed and replay with `SKEIN_SEED`:

- `ratelimit`: both limiters against an exact rational-arithmetic reference
  model on a fake clock; no window admits more than burst + rate x window;
  overload converges to the rate; `RetryAfter` is exactly enough.
- `outbox`: the relay under random publish failures, lost commits and
  failed marks, with restarts: never a loss, first publications in per-key
  order, duplicates only from crashes.
- `events`: per-key order under random nacks; dead letters after the
  maximum attempts.
- `redisx`: RedisStore against MemoryStore under random operations.

Fuzz targets: `auth.FuzzVerify`, `auth.FuzzParseJWKS`,
`logging.FuzzRequestIDOrNew`, `logging.FuzzIsSensitiveKey`,
`postgres.FuzzParseMigrationName`, `events.FuzzAttributesRoundTrip`.

Integration tests (build tag `integration`; they need Docker):

| Test | Checks |
|---|---|
| `postgres.TestMigrateAppliesOnceAndDetectsEditedFiles` | idempotent apply, no-transaction files, checksum mismatch |
| `postgres.TestFailedMigrationLeavesNoTrace` | a failing migration rolls back |
| `postgres.TestConcurrentMigrateRunsEachMigrationOnce` | the advisory lock |
| `postgres.TestWithTxRollsBackOnError` | real rollback |
| `outbox.TestEnqueueCommitsWithTheTransactionOnly` | the outbox row shares the caller's transaction |
| `outbox.TestRelayPublishesInOrderAndRespectsBackoff` | the claim SQL's per-key rule and backoff |
| `outbox.TestSecondRelayClaimsNothingWhileFirstHoldsTheLock` | the advisory lock, and SKIP LOCKED in shared mode |
| `outbox.TestProcessOnceRecordsEachEventOncePerConsumer` | idempotent consumer on real Postgres |
| `outbox.TestPruneDeletesOnlyOldPublishedRows` | retention |
| `redisx.TestRealRedisMatchesMemoryStore` | the Lua scripts on real Redis, not gopher-lua |
| `events.TestEmulatorDeadLettersAfterMaxAttempts` | dead-lettering and `DeliveryAttempt` on the Pub/Sub emulator |

## Known limits and follow-ups

- A cache load that started before a tag invalidation can write a stale
  value after it; the TTL bounds the staleness.
- The Redis scripts assume a non-clustered Redis (Memorystore standard
  tier).
- There is no outbox backlog gauge yet (a periodic `count(*)` of
  unpublished rows would give one).
- `RemoteKeySet` needs a fetch function; identity's JWKS RPC or endpoint
  comes with the contracts.
