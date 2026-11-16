// Package fakes holds in-memory implementations of the gateway's ports and
// fake backend services, for tests:
//
//   - IdempotencyStore and RevocationBus implement the app ports in memory,
//     with the same semantics as the Redis adapters (a shared contract test
//     in redisstore runs against both).
//   - Backends starts in-process gRPC servers on bufconn that implement
//     every generated service interface, record which RPC each call
//     reached with its metadata, and can be told to fail, block or stream.
//   - StaticSyncRouter routes every note over a fixed set of fake
//     documents instances with the real rendezvous hash.
//
// Nothing here opens a network socket.
package fakes
