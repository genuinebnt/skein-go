package blocks

import "errors"

// Errors returned by this package. Callers should test them with errors.Is,
// because most are wrapped with detail.
//
// Rust: one `#[derive(thiserror::Error)] enum Error` with a variant per
// sentinel below.
var (
	// ErrInvalidAttr means an attribute write can never be applied: an
	// unknown key or block type, a reserved replica, a value out of range,
	// or fields set that do not belong to its key.
	ErrInvalidAttr = errors.New("blocks: invalid attribute write")

	// ErrCorrupt means encoded attribute bytes could not be decoded.
	ErrCorrupt = errors.New("blocks: corrupt encoding")

	// ErrTooManyElements means a note has more inserted elements than
	// Elements can order in one pass (see MaxElements).
	ErrTooManyElements = errors.New("blocks: too many elements to order")

	// ErrMarkerInText means a block's text contained Marker, which would
	// make the note's layout ambiguous.
	ErrMarkerInText = errors.New("blocks: block text contains the block marker")

	// ErrInvariant means CheckInvariants found an impossible state. This is
	// always a bug in this package.
	ErrInvariant = errors.New("blocks: invariant violated")
)
