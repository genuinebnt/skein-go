package trigram

import "errors"

// Sentinel errors. Functions wrap them with context using fmt.Errorf("%w"),
// so callers test with errors.Is. In Rust they become one thiserror enum.
var (
	// ErrDuplicateDoc means Add was given an ID that is already indexed.
	ErrDuplicateDoc = errors.New("trigram: duplicate document id")
	// ErrUnknownDoc means Update or Remove was given an ID that is not
	// indexed.
	ErrUnknownDoc = errors.New("trigram: unknown document id")
	// ErrInvalidPattern means a regular expression did not parse.
	ErrInvalidPattern = errors.New("trigram: invalid pattern")
	// ErrInvalidArgument means an argument is out of range (a negative edit
	// budget or window).
	ErrInvalidArgument = errors.New("trigram: invalid argument")
	// ErrCorrupt means encoded bytes (a bitmap or an index snapshot) are
	// malformed. Decoders never return partial results.
	ErrCorrupt = errors.New("trigram: corrupt encoding")
	// ErrInvariant is wrapped by CheckInvariants when a structure is
	// inconsistent. It always means a bug.
	ErrInvariant = errors.New("trigram: invariant violated")
)
