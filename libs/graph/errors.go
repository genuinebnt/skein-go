package graph

import "errors"

// Errors returned by this package. Callers should test them with errors.Is,
// because most are wrapped with the offending IDs.
//
// Rust: one `#[derive(thiserror::Error)] enum Error` with a variant per
// sentinel below. The conformance fixtures name them in snake case
// (ErrSelfLoop is "self_loop").
var (
	// ErrEmptyID means a node was given the empty string as its ID. The empty
	// string is reserved so that results can use it for "no node" (for
	// example ReadingStep.Via of the target).
	ErrEmptyID = errors.New("graph: empty node id")

	// ErrDuplicateNode means two nodes were given the same ID.
	ErrDuplicateNode = errors.New("graph: duplicate node id")

	// ErrUnknownNode means an edge endpoint or a query argument names a node
	// that is not in the graph.
	ErrUnknownNode = errors.New("graph: unknown node")

	// ErrSelfLoop means an edge links a node to itself. See the package
	// documentation for why these are rejected rather than kept or dropped.
	ErrSelfLoop = errors.New("graph: self-loop")

	// ErrDuplicateEdge means the same directed edge was given twice. The link
	// graph is a set of links; the projection that builds it must collapse
	// repeated links (a note citing another in two blocks) first.
	ErrDuplicateEdge = errors.New("graph: duplicate edge")

	// ErrInvalidWeight means an edge weight is negative, NaN or infinite, or
	// a cosine similarity passed to SemanticDistance is outside [-1, 1].
	ErrInvalidWeight = errors.New("graph: invalid weight")

	// ErrInvalidArgument means a query parameter is out of range: a negative
	// hop limit, an unknown Direction, or PageRank options outside their
	// domain.
	ErrInvalidArgument = errors.New("graph: invalid argument")

	// ErrInvariant means CheckInvariants found the graph in an impossible
	// state. This is always a bug in this package.
	ErrInvariant = errors.New("graph: invariant violated")
)
