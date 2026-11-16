// Package graph holds Skein's note link graph and the algorithms the search
// service runs over it (ADR-0011): traversal and blast radius, connected and
// strongly connected components, cycles, topological order, weighted
// (semantic) shortest paths, betweenness, PageRank, Louvain communities and
// modularity, reading paths, and diameter.
//
// # Model
//
// A Graph is a directed graph whose nodes are notes, identified by
// non-empty strings, and whose edges are links: an edge From -> To means
// the note From links to (cites) the note To. Every edge has a weight,
// which only the weighted shortest path reads; other algorithms ignore it,
// so graphs built for them leave it zero. Some nodes are marked as roots
// (notes a reader can reach without already knowing another note exists),
// which only OrphanComponents uses.
//
// Construction validates its input and rejects, with a sentinel error:
// empty or repeated node IDs, edges to unknown nodes, repeated edges,
// negative, NaN or infinite weights, and self-loops.
//
// Self-loops are rejected rather than kept or silently dropped. A note
// linking to itself relates it to nothing, and keeping it would force a
// convention onto every algorithm: is it a cycle, does it make a note
// unplaceable in a reading order, does it add one or two to a degree in
// modularity? Dropping it silently would hide data. Rejecting it makes the
// projection that builds the graph decide, once, in one place; the search
// service is expected to drop self-links when it projects them.
//
// A Graph is immutable after New and safe for concurrent reads.
//
// # Directions and views
//
// Algorithms state which view they use:
//
//   - Directed, following links as written: blast radius with Cites,
//     strongly connected components, FindCycle, TopologicalOrder, PageRank,
//     ReadingPath.
//   - Directed backwards: blast radius with CitedBy, the default, because
//     "if this note changes, which notes are affected" follows citations to
//     the citing notes.
//   - Undirected, where a link's existence matters but not its direction:
//     Distances, ShortestPath, ShortestWeightedPath, ConnectedComponents,
//     Betweenness, Louvain, Modularity and Diameter. A pair of notes
//     linking to each other is one undirected edge.
//
// Links point at prerequisites. TopologicalOrder and ReadingPath therefore
// put a cited note before the notes citing it, as the AnalyzeGraph contract
// states ("no note comes before one it links to").
//
// # Determinism
//
// Every result is a function of the graph alone, never of input order or of
// Go's randomised map iteration. Nodes are stored sorted by ID (byte order,
// which is also Rust's str order) and every loop visits nodes and
// neighbours in that order. Every tie has a stated rule, written on the
// function that breaks it and in docs/spec/graph.md: equal-length paths walk
// back to the smallest-ID neighbour, ready nodes are placed smallest ID
// first, every Partition is numbered by smallest member. Maps appear only in
// results, where order does not matter, or as accumulators whose keys are
// sorted before use.
//
// Floating-point results (Betweenness, PageRank, Modularity, path costs) add
// the same values in the same order in both languages; conformance fixtures
// still compare them with a small tolerance.
//
// # Invariants
//
// CheckInvariants verifies these; tests call it on every graph they build.
//
//   - G1 Node IDs are non-empty and strictly ascending, and the index maps
//     each ID to its position.
//   - G2 Each node's out-list is strictly ascending, in range, never
//     contains the node itself, and carries one finite non-negative weight
//     per target; the cached edge count matches.
//   - G3 In-lists are exactly the transpose of out-lists, strictly
//     ascending.
//   - G4 The undirected neighbour list of each node is the sorted union of
//     its in- and out-lists, with the smaller weight where a pair links both
//     ways.
//   - G5 The undirected edge count is half the sum of undirected degrees.
//
// # Iteration, not recursion
//
// Depth-first searches (Tarjan, FindCycle) keep an explicit stack of frames
// instead of recursing. A workspace can hold a 100,000-note chain; Go would
// grow its goroutine stack to survive that, but Rust threads have fixed
// stacks (2 MiB for test threads) and a recursive port would overflow.
// TestDeepChainNeedsNoDeepStack caps the Go stack to prove the point.
//
// # Credit
//
// The shape of this package follows the owner's earlier project Marginal
// (services/graphalgo): orphans as a components question rather than
// "backlinks == 0", SCCs renumbered by smallest member, Kahn's algorithm
// returning its partial order and the unplaced remainder, dependency
// layers, and three-colour cycle detection. This is a new implementation
// with different choices: an index-based immutable graph, validated
// construction, iterative searches, inbound blast radius by default,
// reading paths that follow outbound links, and betweenness normalised by
// unordered pairs (Marginal's scale was twice that).
//
// # Porting notes
//
// No dependencies beyond the standard library, no I/O, no goroutines, no
// reflection. Node positions are `usize` in Rust; the per-node slices become
// `Vec<Vec<usize>>`, and container/heap becomes
// `BinaryHeap<Reverse<...>>`. Results that are maps become `BTreeMap` or
// `HashMap`; nothing depends on their order. The panics mark unreachable
// states and port as `unreachable!`. Passing testdata/conformance/graph is
// the definition of done.
package graph
