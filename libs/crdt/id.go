package crdt

import "strconv"

// ReplicaID identifies one replica: one device or process editing a document.
//
// Replica IDs are random 64-bit values chosen by the replica itself. Zero is
// reserved for the root sentinel and is never a valid replica. A replica must
// never reuse its ID after losing its counter, or two different operations
// would share an ID; see docs/adr/0004-identifiers.md.
//
// Rust: `pub struct ReplicaId(pub u64)`.
type ReplicaID uint64

// ID uniquely identifies an operation, and for inserts also the character the
// operation created. Counter starts at 1 and increases by one for every
// operation the replica generates.
//
// Rust: `#[derive(Copy, Clone, Eq, PartialEq, Ord, PartialOrd, Hash)] pub struct
// Id { pub replica: ReplicaId, pub counter: u64 }`. The derived Ord compares
// fields in declaration order, which matches Compare below.
type ID struct {
	Replica ReplicaID
	Counter uint64
}

// Root is the ID of the sentinel node every sequence starts with. It is the
// parent of the first characters ever inserted and never holds a character.
var Root = ID{}

// IsRoot reports whether id is the root sentinel.
func (id ID) IsRoot() bool { return id == Root }

// Compare orders IDs by replica, then by counter. It returns -1, 0 or +1.
//
// This order breaks ties between sibling nodes and is part of the CRDT's
// specification: changing it changes the merged text.
func (id ID) Compare(other ID) int {
	switch {
	case id.Replica < other.Replica:
		return -1
	case id.Replica > other.Replica:
		return 1
	case id.Counter < other.Counter:
		return -1
	case id.Counter > other.Counter:
		return 1
	default:
		return 0
	}
}

// Less reports whether id sorts before other.
func (id ID) Less(other ID) bool { return id.Compare(other) < 0 }

// String formats the ID as "replica:counter", for logs and test failures.
func (id ID) String() string {
	return strconv.FormatUint(uint64(id.Replica), 10) + ":" + strconv.FormatUint(id.Counter, 10)
}
