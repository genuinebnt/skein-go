package crdt

import "sort"

// VersionVector records, for each replica, the counter of the last operation
// from that replica that has been applied. Because operations from one
// replica are applied strictly in counter order, the vector describes the
// applied set exactly: operation (r, c) has been applied if and only if
// c <= V[r].
//
// Replicas exchange version vectors when they connect, then send each other
// the operations the other side is missing (Sequence.OpsSince).
//
// A nil VersionVector is valid and means "nothing applied". Never iterate over
// the map directly when the order matters; use Replicas, which is sorted.
//
// Rust: `BTreeMap<ReplicaId, u64>` gives the sorted order for free.
type VersionVector map[ReplicaID]uint64

// Get returns the last applied counter for replica r, or 0 if none.
func (v VersionVector) Get(r ReplicaID) uint64 { return v[r] }

// Contains reports whether the operation with the given ID has been applied.
// The root is always contained.
func (v VersionVector) Contains(id ID) bool {
	return id.IsRoot() || (id.Counter != 0 && id.Counter <= v[id.Replica])
}

// Clone returns an independent copy. Sequence.Version returns clones so
// callers cannot change a sequence's state by accident.
func (v VersionVector) Clone() VersionVector {
	out := make(VersionVector, len(v))
	for r, c := range v {
		out[r] = c
	}
	return out
}

// Replicas returns the replicas with a non-zero counter, sorted ascending.
func (v VersionVector) Replicas() []ReplicaID {
	out := make([]ReplicaID, 0, len(v))
	for r, c := range v {
		if c > 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Equal reports whether two vectors describe the same applied set. Entries
// with counter 0 are treated as absent.
func (v VersionVector) Equal(other VersionVector) bool {
	for r, c := range v {
		if other[r] != c {
			return false
		}
	}
	for r, c := range other {
		if v[r] != c {
			return false
		}
	}
	return true
}

// Total returns the number of operations the vector describes.
func (v VersionVector) Total() uint64 {
	var n uint64
	for _, c := range v {
		n += c
	}
	return n
}
