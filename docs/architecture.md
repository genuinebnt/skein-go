# Skein architecture

Skein is a collaborative, offline-capable notes workspace with an AI agent
that can do anything its user can. It is built as microservices from the
start, runs on one laptop with `make up`, and deploys to Kubernetes on GCP
with Terraform. This repository is the Go and Python implementation; a Rust
port of the Go parts follows it (see `porting-to-rust.md`).

Decisions behind this document are in `adr/`. Each section links the record
that justifies it.

## 1. Services

```
                 browser (editor, graph, search, agent chat)
                   │  REST/JSON · WebSocket (sync, presence, agent stream)
                   ▼
            ┌──────────────┐  rate limits, auth, routing, WebSocket fan-in
            │   gateway    │──────────────────────────────┐
            └──────┬───────┘                              │ Redis
        gRPC       │                                      │ (limits, presence,
   ┌──────────┬────┴─────┬───────────┬───────────┐        │  socket fan-out)
   ▼          ▼          ▼           ▼           ▼
identity  documents   search       ml         agent      notifications
  (Go)      (Go)       (Go)     (Python)    (Python)        (Go)
   │          │          │           │           │              │
   └──────────┴──── Postgres (one schema per service) ──────────┘
              │ outbox            ▲ consume    ▲ consume       ▲ consume
              ▼                   │            │               │
        ───────────────── Pub/Sub topics (domain events) ─────────────
```

| Service | Language | Owns | Main responsibilities |
|---|---|---|---|
| `gateway` | Go | no data | REST and WebSocket edge, token verification, rate limiting (GCRA in Redis), request IDs and tracing, translation to gRPC, WebSocket fan-out across gateway replicas |
| `identity` | Go | `identity` schema | Users, Argon2id passwords, Google and GitHub OAuth, sessions and refresh tokens, workspaces, memberships, note roles, share links, the `Authorize` check, delegated tokens for the agent |
| `documents` | Go | `documents` schema, snapshot store | Notes, the CRDT op log and snapshots, live sync streams, version history, diff, blame, undo, comments; publishes document events through its outbox |
| `search` | Go | `search` schema | Trigram index and regex code search, the link graph projection, graph analysis (blast radius, strongly connected components, shortest paths, centrality, PageRank, reading paths), dynamic programming features (reading plans, fuzzy ranking, snippets) |
| `ml` | Python | `ml` schema, vector files | Embeddings, vector indexes (brute force, IVF, HNSW, with library baselines), clustering (K-means++, Elkan, mini-batch, e-means, Gaussian mixtures by EM), discovery signals, the benchmark harness |
| `agent` | Python | `agent` schema | Chat sessions, the LangGraph agent, tools that call the other services on the user's behalf, confirmation gates, run traces and evaluations |
| `notifications` | Go | `notifications` schema | Inbox, email, live push through the gateway, user notification preferences |

Rules that keep the services independent ([ADR-0001](adr/0001-microservices-from-day-one.md)):

- A service reads and writes only its own schema. Another service's data is
  reached through that service's API or through events it publishes, never
  through a shared table.
- Every service runs and passes its tests alone, with fakes for the services
  it calls ([ADR-0013](adr/0013-testing-and-isolation.md)).
- Shared algorithms live in `libs/` as pure libraries with no I/O. Services
  import them; libraries never import services.

## 2. Contracts ([ADR-0002](adr/0002-contracts-grpc-rest-websocket.md))

- **Between services:** gRPC with Protocol Buffers, defined in
  `proto/skein/v1/`. Breaking changes are checked in CI with `buf breaking`.
- **To the browser:** REST/JSON for requests, WebSockets for the live sync
  stream, presence and agent output. The gateway is the only public entry
  point.
- **Events:** Protocol Buffers messages wrapped in a common `Event` envelope
  (`events.proto`), published to Pub/Sub topics. Locally the Pub/Sub emulator
  runs the same client code as production.
- **CRDT updates:** the binary format in `spec/crdt-sequence.md`, carried as
  bytes inside gRPC and WebSocket messages.

## 3. Data and events ([ADR-0006](adr/0006-op-log-outbox-projections.md))

The document op log is the source of truth, in the style of event sourcing:
the current text is the result of replaying operations, and snapshots are
checkpoints. Search indexes, embeddings, clusters and the link graph are
**projections**: read models built from the event stream, each tracking the
last event it applied, each rebuildable from scratch.

Events leave a service through a **transactional outbox**:

1. In one Postgres transaction, the service writes its state change and an
   `outbox` row describing the event.
2. A relay in the same service reads unpublished outbox rows in order,
   publishes them to Pub/Sub with the note ID as ordering key, and marks them
   published. A crash between publish and mark causes a duplicate, never a
   loss.
3. Consumers are idempotent: each records processed event IDs in its own
   schema, in the same transaction as the effect.

Topics: `documents.events`, `identity.events`, `agent.events`,
`notifications.requests`. Each consumer has its own subscription and a dead
letter topic.

## 4. Key flows

**Editing.** The browser's WASM replica applies an edit locally, stores it in
IndexedDB and sends the encoded operations over the gateway WebSocket. The
gateway forwards them on the document's sync stream to `documents`, which
checks the sender's role with `identity` (cached, with revocation events),
validates and applies them, appends them to the op log and outbox in one
transaction, and fans them out to the other connected replicas.

**Indexing.** `search` and `ml` consume `documents.events`, update their
projections for the changed blocks, and record the event as processed.

**Agent.** The user chats through the gateway. `agent` runs a LangGraph
graph whose tools call the same gRPC APIs as the frontend, with a delegated
token that carries the user's identity, so every permission check is the
user's own ([ADR-0009](adr/0009-agent-acts-as-the-user.md)). Destructive
actions pause the graph and ask the user to confirm. Output streams back over
the WebSocket.

**Notifications.** Services publish requests (mention, comment, share, agent
run finished); `notifications` stores them in the inbox, pushes them live
through the gateway and sends email according to preferences.

## 5. Cross-cutting concerns

- **Rate limiting:** GCRA per user and per IP in Redis at the gateway, with
  separate budgets for sync traffic, search and agent runs. Services keep an
  in-process token bucket as a second line of defence.
- **Load balancing:** Kubernetes Services for HTTP. gRPC uses long-lived
  HTTP/2 connections, so clients resolve a headless Service and balance per
  request (`round_robin`). Sync streams for one note go to the instance that
  owns it, chosen by consistent hashing on the note ID, so one actor per note
  holds its CRDT state.
- **Auth:** short-lived access tokens signed by `identity` (verified by every
  service from its public keys), rotating refresh tokens, and revocation
  events.
- **Observability:** OpenTelemetry traces and metrics from every service, with
  a collector and Jaeger locally and Cloud Trace in GCP. Structured JSON logs
  with trace IDs.
- **Health and shutdown:** gRPC health checks, readiness that waits for
  database and Pub/Sub, graceful drain on SIGTERM.
- **Shared Go infrastructure:** the `platform` module implements all of the above for the Go services ([platform.md](platform.md)).

## 6. Environments ([ADR-0014](adr/0014-deployment.md))

| Environment | How | Infrastructure |
|---|---|---|
| Unit | `go test ./...` in one module, `pytest` in one service | none |
| Local stack | `make up` (Docker Compose) | Postgres, Redis, Pub/Sub emulator, Mailpit |
| Local Kubernetes | `make kind-up` | the same manifests as GKE, on kind |
| Cloud | Terraform in `deploy/terraform` | GKE, Cloud SQL, Memorystore, Pub/Sub, Secret Manager, Artifact Registry |

Nothing requires the cloud. Every adapter that touches a cloud service has a
local implementation selected by configuration.

## 7. Repository layout

```
libs/            pure Go libraries, one module each (crdt, graph, dp, ...)
services/        one directory per service; Go services are modules, Python ones are packages
proto/skein/v1/  gRPC and event contracts
deploy/          compose/, k8s/, terraform/
testdata/        conformance fixtures shared with the Rust port
docs/            architecture, decision records, specs, guides
```

## 8. Status

Phase 1 is being built one pull request per milestone. Done: the sequence
CRDT (`libs/crdt`) with spec, codec, invariants and conformance fixtures, the
contracts, and the local infrastructure stack. Next, in order: graph and
dynamic programming libraries, `identity` and `gateway`, `documents` with the
outbox, `search`, `ml`, `agent`, `notifications`, Kubernetes and Terraform,
then the frontend from the design.
