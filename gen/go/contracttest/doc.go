// Package contracttest checks that the REST mapping of the gRPC contracts,
// the generated OpenAPI document and the hand-written API docs agree.
//
// It has no code of its own, only tests, and lives beside the generated
// packages because it reads their descriptors: the google.api.http rules,
// request field JSON names and message types compiled into skeinv1.
//
// Invariants checked (ADR-0002: "CI checks that every REST route maps to an
// existing RPC"):
//   - Every RPC is exactly one of: internal (an explicit list, no route),
//     streaming (no route, documented in docs/api/websocket.md) or
//     browser-facing (exactly one route, no additional bindings).
//   - Routes are unique, and gen/openapi/skein.swagger.json has exactly one
//     operation per route, with a summary and a tag, and nothing else.
//   - Every route appears, as "`VERB /path`", on its service's page in
//     docs/api, with a response example and, when it has a body, a request
//     example.
//   - Every JSON example in docs/api tagged ```json skein.v1.Message parses
//     as that message, uses lowerCamelCase field names and writes 64-bit
//     integers as strings, which is what the gateway emits.
//
// Porting notes: the Rust port keeps the same .proto files and docs, so this
// test stays in Go; nothing in it is ported.
package contracttest
