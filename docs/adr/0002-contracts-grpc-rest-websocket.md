# 0002. gRPC inside, REST and WebSocket at the edge, Protobuf events

Date: 2026-11-16. Status: accepted.

## Context
Services are written in Go and Python, and the Go ones will be ported to
Rust. Contracts must be language-neutral, typed and checkable for breaking
changes. Browsers cannot speak native gRPC and need a push channel for live
editing.

## Decision
- Service-to-service calls use gRPC; contracts live in `proto/skein/v1` and
  are linted and checked for breaking changes with `buf`.
- The gateway exposes REST/JSON and WebSockets to the browser and translates
  to gRPC. No backend service is reachable from outside.
- Events are Protobuf messages inside a common `Event` envelope, published to
  Pub/Sub. JSON appears only at the browser edge.
- Packages are versioned (`skein.v1`). A breaking change means a new version
  package served alongside the old one until clients move.

## Consequences
- Go (`google.golang.org/grpc`), Python (`grpcio`) and Rust (`tonic`,
  `prost`) all generate code from the same files.
- The gateway carries a translation layer that must be kept in sync; CI
  checks that every REST route maps to an existing RPC.
