# 0007. Offline client: the CRDT compiled to WebAssembly

Date: 2026-11-16. Status: accepted.

## Decision
- The browser runs the same CRDT as the server, compiled to WebAssembly from
  `libs/crdt` (and later the block and Markdown libraries).
- Those libraries use only the standard library, no reflection and no
  `encoding/json`, so they build with both standard Go and TinyGo. TinyGo
  output is much smaller and is the target for production.
- The JavaScript side sees a small, stable API: apply a local edit, apply
  remote bytes, get updates since a version vector, render a block. A Rust
  WASM build can replace the Go one behind the same API.
- IndexedDB stores the op log, snapshots and the replica's counter. Offline,
  search uses a local trigram index; semantic search needs the server.

## Consequences
- CI builds `libs/crdt` for `GOOS=js GOARCH=wasm` and `wasip1` to keep it
  WASM-clean. A TinyGo build check is added with the frontend milestone.
- Edits made offline by a user whose access was revoked meanwhile are
  rejected on sync. The client drops them, reloads from the server and keeps
  the rejected text as a recoverable draft.
