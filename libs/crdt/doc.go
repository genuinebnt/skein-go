// Package crdt implements Skein's replicated text sequence: a list CRDT that
// lets any number of replicas edit the same text concurrently, offline or
// online, and guarantees that every replica that has applied the same set of
// operations shows exactly the same text.
//
// # Algorithm
//
// The sequence is Fugue, from "The Art of the Fugue: Minimizing Interleaving
// in Collaborative Text Editing" (Matthew Weidner and Martin Kleppmann). Fugue
// was chosen over RGA and YATA because it provably avoids interleaving: when
// two people type at the same place at the same time, each person's text stays
// in one contiguous run instead of being shuffled together character by
// character.
//
// Every character is a node in a tree. A node is either a LEFT child or a
// RIGHT child of its parent. The text is the in-order traversal of the tree:
//
//	visit(n) = visit each left child (ascending id), then n, then
//	           visit each right child (ascending id)
//
// The tree's root is a sentinel that holds no character. To insert a
// character immediately after element L (the root when inserting at the
// start):
//
//  1. If L has no right children, the new node becomes L's right child.
//  2. Otherwise let R be the element that follows L in the traversal
//     (counting deleted elements). R is the first node of L's first right
//     subtree, so R has no left children. The new node becomes R's left
//     child.
//
// Either way the new node lands exactly between L and R in every replica's
// traversal, whatever else has been inserted concurrently. Deleting a
// character only marks its node as deleted (a tombstone); the node stays in
// the tree because concurrent inserts may use it as a parent.
//
// Siblings on the same side of the same parent are ordered by ID, ascending
// (replica first, then counter). This tie-break is part of the specification:
// the Rust port must use exactly the same order or replicas written in the two
// languages will diverge. See docs/spec/crdt-sequence.md.
//
// # Representation
//
// This is the reference implementation. It keeps two views of the same data:
//
//   - the tree (parent, side, sorted children), which defines the semantics;
//   - a doubly linked list of all nodes in traversal order, which makes
//     reading the text and finding a neighbour cheap.
//
// Remote inserts cost O(depth + siblings); local edits also pay an O(n) walk
// to turn a visible index into a node. That is deliberate for now: the
// optimised version (runs of characters and an order-statistic B-tree) will
// be tested against this one, operation for operation, so this one has to be
// easy to trust. Complexity is noted on each method.
//
// # Invariants
//
// CheckInvariants verifies all of these. Tests call it after every operation.
//
//   - I1 The root has ID Root, no parent, no left children, and is first in
//     the list.
//   - I2 Every node except the root has a parent; each child's parent and
//     side fields match the slice that holds it.
//   - I3 Children on each side are sorted strictly ascending by ID.
//   - I4 The linked list visits exactly the nodes of the tree, in in-order
//     traversal order, with prev and next pointers consistent.
//   - I5 The visible length equals the number of non-root nodes that are not
//     deleted.
//   - I6 For each replica, the log holds counters 1..V[replica] exactly once,
//     in increasing order, where V is the version vector.
//   - I7 Every insert in the log has a node; the Lamport clock is at least
//     the largest Lamport timestamp in the log.
//
// # Causality
//
// Sequence.Apply accepts an operation only when it is causally ready: the
// replica's previous operation and the element the operation refers to have
// already been applied. Otherwise it returns ErrNotReady and changes nothing.
// CausalBuffer holds operations that arrive early and applies them once they
// become ready, so a network that reorders or duplicates messages is safe.
// Applying an operation that was already applied is a no-op.
//
// # Text positions
//
// Indexes in this API count Unicode code points (Go runes, Rust chars).
// Operations never carry indexes, only IDs, so the wire format is the same
// whatever unit an editor uses. Conversion to UTF-16 for browsers happens at
// the WASM boundary, not here.
//
// # Porting notes
//
// The package has no dependencies outside the standard library, does no I/O,
// starts no goroutines and uses no reflection. That keeps it portable to Rust
// file for file and buildable with TinyGo for WebAssembly. Go map iteration
// order is random, so nothing here depends on it: anything encoded or
// compared is sorted first.
package crdt
