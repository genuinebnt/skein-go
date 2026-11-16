---
title: Offline sync design
author: owner
editors: storage, admin
tags: crdt, sync, design
created: 185
---
The browser holds a WebAssembly build of the CRDT and an IndexedDB copy of every open note. Edits apply locally first; the sync stream sends them when a connection exists.

## Handshake

1. Client sends its version vector.
2. Server replies with every operation the client is missing.
3. Client sends its own missing operations.
4. Both sides stream live operations from then on.

## Revoked access while offline

If someone loses access while offline, the server refuses their batch when they reconnect. The client keeps the refused text as a local draft so nothing they typed disappears silently.

> The server is the only place a viewer's edit can be refused: a CRDT merges whatever it is given.

- [x] Version vector handshake
- [x] Rejected batch handling in the client
- [ ] Resumable uploads for very large offline sessions

Depends on [[Version vectors in practice]] and [[CRDT primer]].
