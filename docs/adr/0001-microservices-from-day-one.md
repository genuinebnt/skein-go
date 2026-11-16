# 0001. Microservices from the start, runnable on one machine

Date: 2026-11-16. Status: accepted.

## Context
The project is for learning distributed systems as much as for the product.
Splitting a monolith later is a common and painful refactor, and the goal
includes gRPC, Pub/Sub, an outbox, a gateway, rate limiting and Kubernetes.
The cost of microservices is operational: more moving parts locally and more
failure modes.

## Decision
Build seven services from the start: `gateway`, `identity`, `documents`,
`search`, `ml`, `agent` and `notifications`. Each owns its Postgres schema and
talks to the others only through gRPC or events. One command (`make up`)
starts the whole stack locally; each service also runs alone against fakes.

## Consequences
- More boilerplate per feature (proto, handler, client). Mitigated by keeping
  domain logic in pure libraries that services only wrap.
- Distributed failure modes (timeouts, partial failure, duplicate events) are
  present from day one. That is intended: they are part of what is learned.
- One Postgres instance with a schema per service keeps local setup light
  while keeping ownership boundaries enforceable (separate roles per schema).
