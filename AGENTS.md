# Working in this repository

Guidance for people and coding agents. Read `docs/architecture.md` first.

## Rules

- **Pure libraries, thin services.** Algorithms live in `libs/` with no I/O,
  no goroutines and no global state. Services wire libraries to gRPC,
  Postgres and Pub/Sub through port interfaces.
- **Every library and service tests alone.** No unit test may need Docker,
  the network or another service. Use the in-memory fakes in each service's
  `internal/fakes`.
- **Invariants are code.** Data structures expose `CheckInvariants`; tests
  call it after every operation. Randomised tests log their seed and replay
  with `SKEIN_SEED`.
- **Comments explain why.** Package doc comments state the algorithm, the
  invariants and porting notes. Exported identifiers have doc comments.
  Comments do not restate code.
- **Write for the Rust port.** Follow `docs/porting-to-rust.md` and ADR-0012:
  kind tags for sum types, sentinel errors, small interfaces, no reliance on
  map order, fixed-width integers in formats.
- **Shared behaviour goes in fixtures.** Anything the Rust port must match
  gets a JSON fixture under `testdata/conformance` with a `why` field.
- **Contracts first.** Change `proto/` before the code that uses it;
  `make proto-check` must pass.
- **Decisions are recorded.** A change of direction gets a new ADR in
  `docs/adr/`, superseding the old one.

## Before a pull request

```
make ci          # gofmt, vet, tests with the race detector, wasm builds
make fuzz        # when a parser or data structure changed
make proto-check # when proto/ changed
```

Update `docs/architecture.md` when boundaries change, `docs/PERF.md` when
performance changes, and the spec in `docs/spec/` when a format or algorithm
changes.
