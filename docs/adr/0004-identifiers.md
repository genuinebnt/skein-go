# 0004. Identifier scheme

Date: 2026-11-16. Status: accepted.

## Decision
| Thing | Identifier |
|---|---|
| Operation | `(replica u64, counter u64)`, counter from 1, plus a Lamport timestamp |
| Replica | random 64-bit, stored per device in IndexedDB; the server keeps a replica to user mapping |
| Character, block | the ID of the operation that created it |
| User, workspace, note, comment, chat, event | UUIDv7 |

- A replica's counter is saved in the same IndexedDB transaction as its
  operations. If that state is lost or inconsistent, the replica takes a new
  random ID rather than risk reusing a counter.
- The server accepts operations from a replica only on a connection
  authenticated as the user who registered that replica, so authorship (blame)
  cannot be forged.
- Integers in every format have fixed widths; nothing encodes Go's `int`.

## Consequences
- Version vectors grow with the number of devices that ever edited a note.
  Compaction (section 10 of the CRDT spec) will fold retired replicas into
  snapshots.
- UUIDv7 sorts by creation time, which keeps Postgres B-tree inserts local and
  is supported by Go (`google/uuid`), Python (`uuid6` or the standard library)
  and Rust (`uuid` with `v7`).
