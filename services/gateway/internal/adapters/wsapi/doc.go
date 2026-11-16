// Package wsapi serves the three streaming RPCs as WebSockets
// (docs/api/websocket.md) and bridges each to its gRPC stream.
//
// # Connecting
//
// The upgrade request is handled like a REST request until the last step:
// the bearer token comes from the "bearer.<token>" subprotocol entry (never
// echoed), is verified, and failures are ordinary google.rpc.Status
// responses (401, 403, 404, 429, 503). The upgrade spends one unit of the
// api budget. For sync the gateway also asks identity whether the caller
// can read the note, so an unreadable note fails with 404 before the
// upgrade, as documented. The encoding is the first of skein.v1.proto and
// skein.v1.json that the client offered, JSON when it offered neither (and
// then no subprotocol is echoed).
//
// # One socket
//
// Each connection is a socket with these goroutines:
//
//   - reader: reads frames (1 MiB limit, 1009 beyond), decodes them (4400 if
//     they do not), spends the endpoint's budget per frame (4429 with the
//     wait in seconds as reason) and hands them to the bridge;
//   - writer: the only writer of data frames, fed by a bounded queue;
//   - pinger: pings every PingInterval and closes with 1001 when a pong
//     does not come within PongTimeout;
//   - a timer that closes with 4401 when the access token expires.
//
// Backpressure: Send never blocks. When the queue is full the client is
// not keeping up, and the socket closes with 1013 (Try Again Later) rather
// than stalling the gRPC stream, buffering without bound, or silently
// dropping frames (which, for sync, would leave a replica missing
// operations without knowing it). The client reconnects and catches up
// from its version vector.
//
// Closing: the first Close wins. Close never blocks (it only records the
// code and wakes the socket), so the registry may call it under its lock.
// The socket then stops its gRPC streams, flushes what is queued (except
// after 1013 and 1001, when the client is not reading), and performs the
// closing handshake with the recorded code and a reason of at most 123
// bytes. Server faults get a generic reason, like REST's generic 5xx
// messages; RESOURCE_EXHAUSTED's reason is the seconds to wait.
//
// # Bridges
//
//   - Sync: one bidirectional DocumentService.Sync stream to the instance
//     that owns the note (app.SyncRouter). The first client frame must be a
//     hello for the path's note. The stream's end closes the socket.
//   - Agent: each client frame is one SendMessage call, that is one run,
//     whose events are relayed as they come. Runs are detached from the
//     socket: "closing the socket does not cancel a run" (websocket.md),
//     so a run keeps being read, and its events discarded, after the socket
//     closes, until it finishes, RunTimeout passes, or the gateway stops.
//   - Notifications: one Subscribe stream; client frames are read (so
//     control frames are seen) and ignored.
//
// Every backend call carries the verified token, the user ID metadata, the
// request ID and the trace context. Agent runs get a fresh request ID per
// frame (docs/observability.md: one per WebSocket message).
//
// # Porting notes
//
// Rust: axum's WebSocketUpgrade with tokio tasks for reader, writer and
// pinger, a tokio::sync::mpsc channel with try_send for the queue, and a
// CancellationToken per socket; the bridges use tonic streaming clients.
package wsapi
