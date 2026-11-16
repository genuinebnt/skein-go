package crdt

import "errors"

// Errors returned by this package. Callers should test them with errors.Is,
// because most are wrapped with detail.
//
// Rust: one `#[derive(thiserror::Error)] enum Error` with a variant per
// sentinel below.
var (
	// ErrInvalidOp means the operation can never be applied: it is malformed,
	// or it contradicts the structure of the sequence. A replica that sends
	// one is buggy or hostile.
	ErrInvalidOp = errors.New("crdt: invalid operation")

	// ErrNotReady means the operation depends on another operation that has
	// not been applied yet. Nothing was changed; retry after the missing
	// operation arrives. CausalBuffer does this automatically.
	ErrNotReady = errors.New("crdt: operation is not causally ready")

	// ErrIDConflict means an operation arrived with the ID of an already
	// applied operation but different content. Two replicas used the same
	// replica ID, or a replica reused counters after losing its state.
	ErrIDConflict = errors.New("crdt: operation id reused with different content")

	// ErrIndexOutOfRange means a local edit referred to a position outside
	// the visible text.
	ErrIndexOutOfRange = errors.New("crdt: index out of range")

	// ErrInvalidText means a local insert was given text that is not valid
	// UTF-8.
	ErrInvalidText = errors.New("crdt: text is not valid UTF-8")

	// ErrInvalidReplica means a sequence was created with replica ID 0.
	ErrInvalidReplica = errors.New("crdt: replica id 0 is reserved")

	// ErrBufferFull means a CausalBuffer reached its limit of pending
	// operations. The peer is sending far ahead of what it has sent before,
	// which a correct peer never does; disconnect and resync.
	ErrBufferFull = errors.New("crdt: too many pending operations")

	// ErrCorrupt means encoded bytes could not be decoded.
	ErrCorrupt = errors.New("crdt: corrupt encoding")

	// ErrInvariant means CheckInvariants found the sequence in an impossible
	// state. This is always a bug in this package.
	ErrInvariant = errors.New("crdt: invariant violated")
)
