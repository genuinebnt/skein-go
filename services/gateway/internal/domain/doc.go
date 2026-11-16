// Package domain holds the gateway's rules as pure functions and values:
// which routes are public, which rate-limit budget a call spends, how the
// client's IP is found behind proxies, which WebSocket close code a gRPC
// status becomes, which documents instance owns a note, and how an
// idempotency key is judged. Nothing here does I/O, starts goroutines or
// keeps global mutable state; the app layer and the adapters do that.
//
// # Route policy
//
// Every REST route is one RPC (docs/api/README.md). PolicyFor maps an RPC's
// full name ("/skein.v1.DocumentService/GetNote") and HTTP verb to:
//
//   - Public: the five session routes (sign up, sign in, OAuth, refresh,
//     sign out) work without a token. A valid token on them is still
//     honoured; an invalid one is ignored, never forwarded.
//   - Budget: search for text, title and semantic search; sync for CRDT
//     operations over REST (WebSocket sync frames spend it too); agent for
//     agent runs, which only start from WebSocket frames; api otherwise.
//   - AcceptsIdempotencyKey: POSTs by an authenticated caller, except
//     ApplyOps and MarkRead, which are idempotent by themselves. Public
//     routes ignore the header on purpose: their responses carry access and
//     refresh tokens, which must not be stored for a day in Redis, and an
//     anonymous caller has no scope to keep one person's key from
//     replaying another's response.
//
// # Rate limits
//
// Each budget has one limit per user and one per client IP (Limits). An
// authenticated request spends both, IP first, so a user cannot escape the
// per-IP budget by holding many accounts and a NAT full of users is not
// limited as one user. IPv6 clients are keyed by their /64, the smallest
// block a home network is usually given; keying by full address would let
// one host rotate through 2^64 keys.
//
// # Close codes
//
// CloseCodeForStatus is the table in docs/api/websocket.md. Codes the table
// does not list map to the nearest documented behaviour: OUT_OF_RANGE and
// ALREADY_EXISTS are client mistakes (4400), ABORTED and DEADLINE_EXCEEDED
// mean try again (1011 with backoff), CANCELLED means the client went away
// (1001). A slow client is dropped with 1013 (Try Again Later, from the
// IANA registry): the server shed it, and a reconnect resyncs from the
// version vector.
//
// # Rendezvous hashing
//
// Sync streams for a note must reach the one documents instance that holds
// the note's CRDT actor (architecture section 5). Ring.Owner picks, among
// the current members, the one with the highest score
// h(note ID, member), where h is 64-bit FNV-1a over the note ID, a zero
// byte and the member, passed through the splitmix64 finaliser so that
// similar inputs spread over all 64 bits. Ties, which need a 64-bit
// collision, go to the smaller member string. This is highest random
// weight hashing (Thaler and Ravishankar, 1998): removing a member moves
// only the notes it owned, adding one moves only the notes it now wins,
// and no virtual nodes are needed for balance. The function is part of the
// contract between gateway replicas (and with the Rust port): every
// replica must compute the same owner, so it is pinned by golden values in
// testdata/rendezvous.json.
//
// # Idempotency
//
// A record is pending while the first request runs and complete once its
// response is stored. Decide compares a stored record with a retry's
// fingerprint (SHA-256 over the method, path with query, and body, each
// length-prefixed so that field boundaries cannot be shifted): a different
// fingerprint is a conflict whatever the state, the same one is replayed
// when complete and in flight while pending. Storable keeps only responses
// that a retry would get again: successes and the deterministic client
// errors 400, 403, 404 and 422. Everything else (401 before a refresh, 409
// conflicts, 429, 5xx) is released so the retry runs for real.
//
// # Invariants
//
//   - Ring members are sorted, unique and non-empty strings
//     (Ring.CheckInvariants); Owner is a member, and the same for the same
//     members in any input order.
//   - CloseReason never exceeds 123 bytes and never splits a UTF-8 sequence.
//   - ClientIP never returns an address taken from X-Forwarded-For unless
//     every hop to its right is a trusted proxy.
//
// # Porting notes
//
// Rust: Budget, SubjectKind, RecordState and Outcome become enums; Ring is
// a struct over a sorted Vec<String>; FNV-1a and splitmix64 are a few lines
// each and must match the golden file bit for bit; net/netip maps to
// std::net::IpAddr with ipnet for prefixes.
package domain
