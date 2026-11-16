# Testing guide

The aim: any piece can be built and tested on its own, in seconds, without
Docker or the rest of the system. Full-stack tests exist but are few.

## Levels

| Level | What | Needs | Command |
|---|---|---|---|
| Library | pure algorithms in `libs/*` | Go only | `cd libs/crdt && go test ./...` or `make test-lib LIB=crdt` |
| Use case | a service's app layer with in-memory fakes for its ports | Go or Python only | `cd services/<name> && go test ./internal/app/...` |
| Adapter | Postgres, Pub/Sub and gRPC adapters | Docker (Testcontainers starts what it needs) | `go test -tags integration ./internal/adapters/...` |
| Contract | proto lint and breaking changes; event consumer tests | `buf` | `make proto-check` |
| Service | one service with fakes for every service it calls | Docker for its own database | `make test-service SVC=<name>` |
| End to end | user journeys through the gateway | `make up` | `make e2e` |

## Rules

- **Invariants are code.** Data structures expose `CheckInvariants` (Go) or
  `check_invariants` (Python, Rust). Tests call it after every operation, not
  only at the end.
- **Oracles beat examples.** Where a simple slow version exists (a slice of
  runes for the CRDT, brute force for search, exhaustive search for dynamic
  programming), randomised tests compare against it.
- **Seeds are logged.** Every randomised test logs its seed; set
  `SKEIN_SEED=<seed>` to replay a failure exactly.
- **Fuzz anything that parses input from the network or the user.** Fuzz
  targets also assert invariants on whatever they accept.
- **Fixtures are shared.** Behaviour that the Rust port must match lives in
  `testdata/conformance` as JSON, with the reasoning for each expected result
  in the fixture itself.
- **Test names state behaviour.** `TestApplyRejectsReusedID`, not
  `TestApply3`.
- **Prove the test can fail.** When adding an important test, break the code
  on purpose once and watch it fail. (For the CRDT, reversing the sibling
  order fails four fixtures and the invariant check.)

## Faking other services

Each service defines the interfaces it needs (for example `Authorizer`,
`EventPublisher`) in its own app layer, and ships an in-memory fake in
`internal/fakes`. Tests construct the use case with fakes; `main` constructs
it with real gRPC clients. Nothing in a unit test opens a socket.

## Network faults

`libs/crdt/convergence_test.go` shows the pattern: a simulated network that
delays, reorders and duplicates messages, driven by a seed. Services that
replicate or consume events get the same treatment, with injected crashes
between "publish" and "mark published" for the outbox.

## Current commands

```
make test         # every Go module, with the race detector
make fuzz         # each fuzz target for 30 seconds
make bench        # benchmarks
make wasm-check   # build the WASM-targeted libraries for js/wasm and wasip1
make proto-check  # buf lint and buf breaking
```
