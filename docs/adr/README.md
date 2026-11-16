# Architecture decision records

One file per decision: the context, what was decided, and what it costs. A
decision that changes gets a new record that supersedes the old one; old
records are kept and marked superseded, never rewritten.

| # | Decision | Status |
|---|---|---|
| [0001](0001-microservices-from-day-one.md) | Microservices from the start, runnable on one machine | Accepted |
| [0002](0002-contracts-grpc-rest-websocket.md) | gRPC inside, REST and WebSocket at the edge, Protobuf events | Accepted |
| [0003](0003-text-positions.md) | Code points inside, UTF-16 at the browser boundary | Accepted |
| [0004](0004-identifiers.md) | Identifier scheme | Accepted |
| [0005](0005-sequence-crdt.md) | Fugue sequence with block markers | Accepted |
| [0006](0006-op-log-outbox-projections.md) | Op log as event store, transactional outbox, projections | Accepted |
| [0007](0007-offline-wasm-client.md) | Offline client: the CRDT compiled to WebAssembly | Accepted |
| [0008](0008-identity-and-roles.md) | Users, workspace and note roles, server-side checks | Accepted |
| [0009](0009-agent-acts-as-the-user.md) | The agent acts as the user, through the same APIs | Accepted |
| [0010](0010-search-and-discovery.md) | Trigram and regex code search; unblended discovery signals | Accepted |
| [0011](0011-graph-and-dp-libraries.md) | Graph algorithms and dynamic programming as pure libraries | Accepted |
| [0012](0012-rust-portable-go.md) | Go written to port to Rust | Accepted |
| [0013](0013-testing-and-isolation.md) | Testing strategy and isolation | Accepted |
| [0014](0014-deployment.md) | Compose, kind, GKE with Terraform; no Ansible | Accepted |
| [0015](0015-ml-and-agent-in-python.md) | ML and the agent in Python services | Accepted |
