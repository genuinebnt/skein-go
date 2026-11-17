# Skein (Go)

A collaborative notes workspace: Notion-style blocks with Markdown inside,
real-time editing that works offline, typo-tolerant and regex code search,
semantic discovery, a knowledge graph with real algorithms, and an AI agent
that can do anything its user is allowed to do.

This is the Go and Python implementation, built as microservices from the
start. A Rust port of the Go parts follows it; see
[docs/porting-to-rust.md](docs/porting-to-rust.md).

## Status

| Part | State |
|---|---|
| Sequence CRDT (`libs/crdt`): Fugue, causal delivery, binary codec, invariants, conformance fixtures | done |
| Service contracts (`proto/skein/v1`) | first version |
| Local infrastructure (`deploy/compose`) | Postgres, Redis, Pub/Sub emulator, Mailpit |
| Graph and dynamic programming libraries | next |
| Services: identity, gateway, documents, search, ml, agent, notifications | planned, in that order |
| Kubernetes manifests and Terraform | planned |
| Frontend | being designed |

## Quick start

```
curl https://mise.run | sh     # once: mise pins every tool (mise.toml); activate it in your shell
mise trust && mise install just
just setup                     # pinned tools, web and Python dependencies, then just doctor
just dev                       # the whole stack as local processes, small seed loaded; no Docker needed
just e2e                       # end-to-end journeys through the gateway
just stop
```

`just` lists every recipe; [docs/dev-workflow.md](docs/dev-workflow.md)
explains them and the usual loops: `just restart <service>`, `just logs`,
`just status`, `just seed medium`, `just reset`, `just test`, `just lint`,
`just prod-local` (the same system with production settings, checked),
`just dev web` (plus the web app) and `just dev docker` (the compose stack;
`make up` adds Grafana and Jaeger). `just doctor` says what mise cannot install:
PostgreSQL 16, Redis 7 and the Pub/Sub emulator. The Makefile still works
(`make help`).

## Layout

```
libs/            pure Go libraries (crdt so far)
services/        one directory per service
proto/skein/v1/  gRPC and event contracts
deploy/compose/  local stack
testdata/        conformance fixtures shared with the Rust port
docs/            architecture, decisions, specs, guides
```

## Documentation

- [Roadmap](docs/roadmap.md): what is done and what comes next
- [Architecture](docs/architecture.md): services, contracts, events, flows,
  environments
- [Decision records](docs/adr/README.md)
- [Sequence CRDT spec](docs/spec/crdt-sequence.md)
- [Testing guide](docs/testing.md)
- [Developer workflow](docs/dev-workflow.md): `just setup`, `just dev`, every
  recipe, prod-local, swapping in a Rust binary, typical loops
- [Running natively](docs/local-native.md): the native stack's ports, the e2e
  suite, integration tests without Docker, troubleshooting
- [Porting guide](docs/porting-to-rust.md)
- [Porting to Rust, step by step](docs/porting/README.md): the dependency graph,
  the order, a `PORTING.md` per library, platform, generated code and service,
  vertical slices, and how to run Go and Rust side by side
- [Rust design doc](docs/design/index.html): the original plan for the Rust version
  (workspace layout, document engine, labs, phases); open it in a browser.
  [skein-rust-design.html](docs/design/skein-rust-design.html) is the later revision
  with the agent and Phase 3 sections
- [Performance log](docs/PERF.md)
- [Seed data](docs/seed-data.md): deterministic datasets with ground truth (`make seed PROFILE=medium`)
- [e-means research notes](docs/research/e-means.md)
- [Contributor and agent rules](AGENTS.md)
