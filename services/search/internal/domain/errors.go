package domain

import "errors"

// Sentinel errors of the domain. Callers wrap them with context and test
// with errors.Is; the gRPC adapter maps them to status codes.
var (
	// ErrInvalidMutation means a mutation is malformed (no note ID, an
	// unknown kind, a block without an ID).
	ErrInvalidMutation = errors.New("search: invalid mutation")
	// ErrInvalidQuery means a search request cannot be run (an empty query,
	// a regular expression that does not compile).
	ErrInvalidQuery = errors.New("search: invalid query")
	// ErrCorruptSnapshot means snapshot bytes do not decode or disagree with
	// themselves.
	ErrCorruptSnapshot = errors.New("search: corrupt snapshot")
	// ErrInvariant is wrapped by CheckInvariants; it always means a bug.
	ErrInvariant = errors.New("search: invariant violated")
)
