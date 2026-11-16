# 0012. Go written to port to Rust

Date: 2026-11-16. Status: accepted.

## Decision
Code in `libs/` and the domain packages of Go services follows rules that make
a line-by-line Rust port straightforward. The full guide is
`docs/porting-to-rust.md`. In short:

- Sum types are a kind tag plus fields, switched exhaustively (Rust enums).
- Errors are sentinel values wrapped with context (a `thiserror` enum).
- Interfaces are small and defined where they are used (traits).
- Core packages are single-threaded and deterministic; goroutines live only
  in service adapters (Tokio tasks or actors).
- No reliance on map iteration order, `int` width, nil maps as signals or
  reflection.
- Package names match the planned crate names, and language-neutral fixtures
  in `testdata/conformance` define behaviour for both.
