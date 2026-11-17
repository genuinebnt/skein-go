# Porting Skein to Rust

This is the entry point for porting Skein's Go code (and, if you choose,
its Python services) to Rust. It is written for one person doing the port
by hand, one piece at a time, while the Go stack keeps running and keeps
passing its tests.

Read in this order:

1. [porting-to-rust.md](../porting-to-rust.md): the language mapping (Go
   types, errors, maps, goroutines to Rust) and the per-package checklist.
   It applies to every crate below.
2. This page: what depends on what, the order to port in, and how to run
   Go and Rust side by side.
3. The `PORTING.md` next to the code you are about to port (table below):
   what that code does, what to learn first, the file-by-file order, the
   crates to use and the pitfalls.
4. A [vertical slice](slices/README.md) when you would rather port one
   feature end to end (route, handler, use case, domain, library, storage,
   tests) than one service at a time.

The original Rust design is in
[design/skein-rust-design.html](../design/skein-rust-design.html); its
"Porting from Go" section shows the target workspace and the crate
dependency rules. Where it and the Go code disagree, the Go code and the
ADRs are newer.

## What there is to port

| Part | Go or Python | Size (non-test lines, about) | Guide |
|---|---|---|---|
| `libs/crdt` | Go | 1,200 | [libs/crdt/PORTING.md](../../libs/crdt/PORTING.md) |
| `libs/blocks` | Go | 2,000 | [libs/blocks/PORTING.md](../../libs/blocks/PORTING.md) |
| `libs/dp` | Go | 2,300 | [libs/dp/PORTING.md](../../libs/dp/PORTING.md) |
| `libs/graph` | Go | 1,900 | [libs/graph/PORTING.md](../../libs/graph/PORTING.md) |
| `libs/trigram` | Go | 2,600 | [libs/trigram/PORTING.md](../../libs/trigram/PORTING.md) |
| library overview | | | [docs/porting/libs/](libs/) |
| `gen/` (generated code from `proto/`) | Go, Python | generated | [gen/PORTING.md](../../gen/PORTING.md) |
| `platform/` | Go | 6,000 | [platform/PORTING.md](../../platform/PORTING.md) |
| `services/gateway` | Go | 5,000 | [services/gateway/PORTING.md](../../services/gateway/PORTING.md) |
| `services/identity` | Go | 7,700 | [services/identity/PORTING.md](../../services/identity/PORTING.md) |
| `services/documents` | Go | 5,900 | [services/documents/PORTING.md](../../services/documents/PORTING.md) |
| `services/search` | Go | 5,700 | [services/search/PORTING.md](../../services/search/PORTING.md) |
| `services/notifications` | Go | 6,800 | [services/notifications/PORTING.md](../../services/notifications/PORTING.md) |
| `services/ml` | Python | 6,400 | [services/ml/PORTING.md](../../services/ml/PORTING.md) (recommendation: keep in Python) |
| `services/agent` | Python | 7,800 | [services/agent/PORTING.md](../../services/agent/PORTING.md) (recommendation: port last, optional) |

The library guides (`libs/*/PORTING.md` and `docs/porting/libs/`) are
written in a parallel change; if a link above is dead, that change has not
merged yet, and `docs/porting-to-rust.md` section 5 has the short version.

## Dependency graph

Arrows point from a part to what it needs. Solid arrows are build-time
dependencies (imports); dashed arrows are run-time calls over gRPC or
events. Port a box only after everything its solid arrows reach.

```mermaid
flowchart TB
  subgraph libs["libs/ (pure, no I/O)"]
    crdt
    dp
    graph
    blocks --> crdt
    trigram --> dp
  end

  subgraph gen["gen/ (from proto/skein/v1)"]
    proto["skein-proto<br/>messages, tonic stubs, protojson"]
  end

  subgraph platform["platform/"]
    config
    shutdown
    plauth["auth"]
    ratelimit
    logging
    telemetry
    redisx --> config
    postgres --> config
    events --> config
    outbox --> events
    outbox --> postgres
    grpcx --> plauth
    grpcx --> config
    grpcx --> logging
    grpcx --> ratelimit
    httpx --> grpcx
    httpx --> plauth
    httpx --> logging
    httpx --> ratelimit
  end

  subgraph services["services/"]
    identity
    documents
    search
    notifications
    gateway
    ml["ml (Python)"]
    agent["agent (Python)"]
  end

  identity --> proto
  identity --> outbox
  identity --> grpcx
  identity --> httpx
  identity --> redisx
  identity --> telemetry
  identity --> shutdown

  documents --> crdt
  documents --> blocks
  documents --> dp
  documents --> proto
  documents --> outbox
  documents --> grpcx
  documents --> redisx
  documents --> telemetry
  documents --> shutdown

  search --> trigram
  search --> graph
  search --> dp
  search --> proto
  search --> outbox
  search --> grpcx
  search --> redisx
  search --> telemetry
  search --> shutdown

  notifications --> proto
  notifications --> outbox
  notifications --> grpcx
  notifications --> redisx
  notifications --> telemetry
  notifications --> shutdown

  gateway --> proto
  gateway --> httpx
  gateway --> redisx
  gateway --> events
  gateway --> telemetry
  gateway --> shutdown

  agent -. "Python CRDT port" .-> crdt

  gateway -. gRPC .-> identity
  gateway -. gRPC .-> documents
  gateway -. gRPC .-> search
  gateway -. gRPC .-> notifications
  gateway -. gRPC .-> ml
  gateway -. gRPC .-> agent
  documents -. "Authorize, RegisterNote, RegisterReplica" .-> identity
  search -. "ListReadableNotes" .-> identity
  search -. "Discover, SemanticSearch" .-> ml
  notifications -. "Authorize, users" .-> identity
  agent -. "delegated token" .-> identity
  agent -. "GetNote, Sync, ApplyOps" .-> documents
  agent -. "Search, graph" .-> search
  agent -. "SemanticSearch" .-> ml

  documents -. "documents.events" .-> search
  documents -. "documents.events" .-> ml
  documents -. "documents.events" .-> notifications
  documents -. "documents.events" .-> identity
  identity -. "identity.events" .-> documents
  identity -. "identity.events" .-> search
  identity -. "identity.events" .-> gateway
  identity -. "identity.events" .-> notifications
  agent -. "agent.events" .-> notifications
```

The event edges come from `deploy/compose/pubsub-init.sh`, which is the
list of topics and subscriptions; ml also subscribes to `identity.events`.

Three facts shape the order:

- **Libraries depend on nothing outside `libs/`.** They can be ported and
  verified first, against the shared fixtures, with no services running.
- **Every Go service needs `platform/` and `gen/`.** Port the platform
  packages a service needs before the service, not all of platform at once.
- **Services only talk over gRPC and Pub/Sub**, with the same protos in both
  languages. A Rust service can therefore replace its Go twin while every
  other service stays in Go. That is what makes a gradual port possible.

## Recommended order

Each step ends with something you can run and test. The column "proven by"
is the test that tells you the step is done.

| # | Port | Why now | Proven by |
|---|---|---|---|
| 1 | `libs/crdt` | the heart of the system; no dependencies; has golden bytes | `testdata/conformance/crdt`, codec golden bytes, cross-language replay |
| 2 | `libs/dp`, `libs/graph` | no dependencies; small, self-contained algorithms | `testdata/conformance/dp`, `testdata/conformance/graph` |
| 3 | `libs/blocks`, `libs/trigram` | need crdt and dp | `testdata/conformance/blocks`, `testdata/conformance/trigram` |
| 4 | `gen/` as the `skein-proto` crate | every service needs the messages and stubs | a round trip of every message against the Go encoding ([gen/PORTING.md](../../gen/PORTING.md)) |
| 5 | platform, first half: `config`, `shutdown`, `logging`, `telemetry`, `auth` | what a gRPC server needs to start and verify a token | the unit tests ported from Go; a Rust verifier accepting tokens identity (Go) signed |
| 6 | platform, second half: `grpcx`, `postgres`, `redisx`, `ratelimit`, `events`, `outbox`, `httpx` | storage, events and the edge | ported unit tests; integration tests against the native Postgres, Redis and Pub/Sub emulator |
| 7 | `identity` | everything else asks it `Authorize`; its domain is pure and fully pinned by the seed's permission matrix | its unit tests, the seed loader matching every matrix row, then the e2e suite with Rust identity and Go everything else |
| 8 | `documents` | the CRDT engine as a service; needs identity's `Authorize` (Go or Rust) | sync convergence tests, the seed's CRDT truth, e2e `sync_test.go` |
| 9 | `search` | trigram, regex and graph over documents' events | the seed's search qrels and graph truth, e2e `search_test.go` and `graph_test.go` |
| 10 | `notifications` | consumes everyone's events; email outbox | `testdata/conformance/notifications`, e2e `notify_test.go` |
| 11 | `gateway` | last of the Go services: once every backend is Rust it is the only Go left, and porting it last means you can debug each backend through a gateway you trust | every e2e journey, which all go through the gateway |
| 12 | `agent` (optional) | the original design wanted a from-scratch Rust agent runtime; the contract and evals are already fixed | e2e `agent_test.go`, the eval harness |
| - | `ml` | recommended to stay in Python ([services/ml/PORTING.md](../../services/ml/PORTING.md)); its algorithms can be ported as crates for learning | the benchmark harness |

Why identity before documents and gateway last: identity is the dependency
everything else has at run time, its rules are a pure function checked
against the 6,256 rows of the small seed's permission matrix, and it teaches the whole service stack
(tonic, sqlx, outbox, Redis cache, JWT) on simple data. The gateway is
mostly plumbing whose bugs show up as other services' bugs; replacing it
while the backends are still changing makes failures hard to place.

If you would rather see a whole feature work in Rust early, follow the
[slices](slices/README.md) instead: slice 1 (sign up and sign in) needs only
identity and a thin gateway, and slice 2 (live editing) adds documents.

## Target layout

The design doc's "Porting from Go" section has the full tree and its
dependency rules. In short: one Cargo workspace in `rust/` at the root of
this repository, so the protos, fixtures, seed data and e2e suite are shared
by path, with one crate per Go module or package:

```
rust/
  Cargo.toml                  workspace, shared dependency versions, lints
  crates/
    libs/      skein-crdt  skein-blocks  skein-dp  skein-graph  skein-trigram
    proto/     skein-proto                     (from gen/, see gen/PORTING.md)
    platform/  skein-config  skein-shutdown  skein-logging  skein-telemetry
               skein-auth  skein-ratelimit  skein-postgres  skein-redis
               skein-events  skein-outbox  skein-grpc  skein-http
    services/
      identity/      identity-core (domain, app, ports, fakes)  identity (adapters, main)
      documents/     documents-core  documents
      search/        search-core  search
      notifications/ notifications-core  notifications
      gateway/       gateway-core  gateway
```

Each service is split into a `-core` crate (domain, app, ports, fakes; no
tonic, sqlx, redis or Pub/Sub in its `Cargo.toml`) and the service crate
(adapters and `main`). Go enforces the hexagonal layering only by
convention; in Rust, Cargo enforces it, because the core crate cannot import
what it does not depend on.

Keeping the Rust code in this repository is a recommendation, not a
decision: a separate repository would have to vendor `proto/`,
`testdata/` and `e2e/`, and the side-by-side runs below would need two
checkouts.

## Running Go and Rust side by side

The port is safe only if a Rust service is a drop-in replacement for its Go
twin. Five things make that true; keep all of them.

### 1. Same protos

Both languages generate from `proto/skein/v1/*.proto`. Never copy a
message into Rust by hand. `make proto-check` runs `buf breaking`, so a
contract change shows up in both languages at once. JSON at the edge is
the proto3 JSON mapping (protojson in Go, `pbjson` in Rust); the e2e suite
encodes its requests with protojson, so it catches any drift.

### 2. Same ports

A Rust service listens on exactly the address variables its Go twin reads:
gRPC on `<SERVICE>_GRPC_ADDR` (or `GRPC_ADDR` for search and
notifications), the admin HTTP port (health, readiness, JWKS for identity)
on `<SERVICE>_ADMIN_ADDR` (`ADMIN_ADDR`, `HTTP_ADDR`). In the native stack
the convention is gRPC on `291NN` and admin on `281NN`
(`deploy/local/ports.env`). Callers find a service through its target
variable (`IDENTITY_GRPC_TARGET=dns:///127.0.0.1:29101`), so swapping the
process behind a port is invisible to them.

### 3. Same environment variables

A Rust binary reads the same variables, with the same defaults, as the Go
binary it replaces: the platform's (`SKEIN_ENV`, `DATABASE_URL`,
`REDIS_URL`, `PUBSUB_PROJECT`, `PUBSUB_EMULATOR_HOST`, `OTEL_*`; table in
[platform.md](../platform.md)) and the service's own, listed in each
`cmd/<service>/main.go` doc comment and each `PORTING.md`. Port
`config`'s "report every bad variable at once" behaviour too; it is what
makes a misconfigured swap obvious. The same database schema and
migrations are used: a Rust service must start against a database the Go
service migrated, and the other way round (see the migration note in
[platform/PORTING.md](../../platform/PORTING.md)).

### 4. Same conformance fixtures

Anything both languages must agree on byte for byte or bit for bit is a
JSON fixture with a `why` field:

| Fixtures | Pins |
|---|---|
| `testdata/conformance/crdt`, `blocks`, `dp`, `graph`, `trigram` | library behaviour (the library guides) |
| `testdata/conformance/notifications` (`mentions.json`, `email_plans.json`) | the mention parser and the email digest planner |
| `services/gateway/internal/domain/testdata/rendezvous.json` | which documents instance owns a note; every gateway replica, Go or Rust, must agree |
| `testdata/seed/small/truth/` | ground truth for permissions, CRDT final texts, search, graph, clustering, discovery and the agent |

The seed truth is the strongest check a service has: each Go service has a
seed loader (`cmd/<service>-seed`) and tests that compare its answers with
the truth. Port the loader with the service and run the same comparisons.

### 5. The e2e suite is the acceptance test

`e2e/` drives user journeys through the gateway only (REST and WebSocket),
against a running stack, with real CRDT replicas as clients. It does not
know which language a service is written in. A ported service is accepted
when the whole suite passes with that service in Rust and the rest in Go.

```
make up-native            # the whole stack natively, small seed loaded (docs/local-native.md)
make e2e                  # every journey; RUN=TestSync narrows it
make integration-native   # every Go integration test against the same stack
```

To swap one service for its Rust build in a running native stack:

```
cd rust && cargo build --release -p identity
pid=$(cat deploy/local/.run/pids/identity.pid)
tr '\0' '\n' < /proc/$pid/environ > /tmp/identity.env   # the exact env up.sh gave Go
kill -TERM -- -$pid                                      # its process group
env -i $(cat /tmp/identity.env) rust/target/release/identity \
  > deploy/local/.run/logs/identity-rust.log 2>&1 &
curl -fsS http://127.0.0.1:28101/readyz && make e2e
```

`up.sh` starts each service with exactly the variables it needs
(`run_service` with `env -i`), so copying them from `/proc` gives the Rust
binary the same environment the Go one had. A cleaner hook, a per-service
binary override in `deploy/local/up.sh` (for example
`SKEIN_NATIVE_IDENTITY_BIN`), is a follow-up for the owners of
`deploy/local/`.

Run the suite against Go first and keep the result; a journey that fails
with Rust but passed with Go is the port's bug. Two journeys depend on
timing (`TestRateLimitingAnswers429WithTheDocumentedHeaders` and the
notification email poll); run them a second time before chasing a failure.

### Mixed fleets

Some state is shared between replicas of one service and must agree across
languages if Go and Rust replicas ever run together (a canary, a rolling
switch):

- gateway: rendezvous owner choice (golden file), idempotency records and
  the revocation channel in Redis (key and value formats in
  `redisstore/`), rate-limit state (the same GCRA Lua script and keys);
- every publisher: the outbox table and its advisory-lock key, so a Go
  relay and a Rust relay never publish one service's rows at once;
- documents: the op log's primary key (a second writer is a conflict),
  snapshots in the CRDT codec;
- search: the snapshot format in `internal/domain/snapshot.go`;
- the access-token format and JWKS (platform/auth).

If you only ever swap a whole service at once, only the database and
Redis formats matter, because they outlive the process.

## Before you start

- Install the Rust toolchain (stable, edition 2024), `cargo-nextest` and
  `cargo-fuzz`; `buf` is already in `~/go/bin` (see
  [gen/PORTING.md](../../gen/PORTING.md) for the Rust plugins).
- Read `AGENTS.md`: the same rules apply to the Rust code (pure libraries,
  invariants as code, fixtures for shared behaviour, ADRs for changes of
  direction).
- Record every benchmark next to the Go number in `docs/PERF.md`.
