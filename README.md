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

Requirements: Go 1.24+, Docker (only for the local stack), Python 3.12+ (for
the `ml` and `agent` services, when they land).

```
make test        # all Go tests, race detector on; no Docker needed
make test-lib LIB=crdt
make fuzz        # fuzz every target for 30s
make up          # start Postgres, Redis, the Pub/Sub emulator and Mailpit
make help        # everything else
```

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

- [Architecture](docs/architecture.md): services, contracts, events, flows,
  environments
- [Decision records](docs/adr/README.md)
- [Sequence CRDT spec](docs/spec/crdt-sequence.md)
- [Testing guide](docs/testing.md)
- [Porting guide](docs/porting-to-rust.md)
- [Performance log](docs/PERF.md)
- [e-means research notes](docs/research/e-means.md)
- [Contributor and agent rules](AGENTS.md)
