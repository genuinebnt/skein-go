# 0013. Testing strategy and isolation

Date: 2026-11-16. Status: accepted.

## Decision
- Every library and service builds and tests on its own: `go test` inside one
  module, `pytest` inside one Python service. No test needs the full stack.
- Layers inside services follow ports and adapters: domain (pure), app (use
  cases over port interfaces), adapters (gRPC, Postgres, Pub/Sub). Use cases
  are tested with in-memory fakes; adapters with Testcontainers.
- Invariants are explicit functions (`CheckInvariants`) called after every
  operation in tests, fuzzing and property tests.
- Randomised tests log their seed and replay with `SKEIN_SEED`.
- Contract tests: `buf breaking` against the main branch; consumer tests for
  each event type.
- End-to-end tests run against `make up` and are few: one per user journey.

The details and commands are in `docs/testing.md`.
