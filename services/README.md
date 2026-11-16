# Services

Each service lives in its own directory and builds, runs and tests on its own.
They are added one milestone at a time; see `docs/architecture.md` section 8.

| Service | Language | Status |
|---|---|---|
| `gateway` | Go | planned |
| `identity` | Go | planned |
| `documents` | Go | planned |
| `search` | Go | planned |
| `ml` | Python | phase 1: algorithms, benchmark, gRPC service (`services/ml/README.md`) |
| `agent` | Python (LangGraph) | planned |
| `notifications` | Go | planned |

## Layout of a Go service

```
services/<name>/
  go.mod
  cmd/<name>/main.go     wiring only: config, adapters, server, shutdown
  internal/domain/       entities and rules, pure
  internal/app/          use cases; defines the port interfaces it needs
  internal/adapters/     grpc/, postgres/, pubsub/, implementing the ports
  internal/fakes/        in-memory port implementations for tests
  migrations/            versioned SQL for the service's own schema
  Dockerfile
```

## Layout of a Python service

```
services/<name>/
  pyproject.toml         managed with uv
  src/skein_<name>/
    domain/  app/  adapters/
  tests/
  Dockerfile
```
