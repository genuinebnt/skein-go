package graph

import (
	"fmt"
	"slices"
)

// CheckInvariants verifies every structural invariant listed in the package
// documentation (G1 to G5) and returns an error wrapping ErrInvariant for the
// first one that fails.
//
// A Graph never changes after New, so tests call this once per constructed
// graph. It costs O((V + E) log V) and is meant for tests and fuzzing. The
// Rust port exposes the same check as `fn check_invariants(&self) ->
// Result<(), Error>`.
func (g *Graph) CheckInvariants() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
	}
	n := len(g.ids)
	if len(g.index) != n || len(g.root) != n || len(g.out) != n || len(g.outWeight) != n ||
		len(g.in) != n || len(g.adj) != n || len(g.adjWeight) != n {
		return fail("G1: per-node slices disagree on the node count %d", n)
	}
	// G1: IDs are non-empty, strictly ascending, and indexed.
	for v, id := range g.ids {
		if id == "" {
			return fail("G1: empty id at position %d", v)
		}
		if v > 0 && g.ids[v-1] >= id {
			return fail("G1: ids not strictly ascending at position %d", v)
		}
		if g.index[id] != v {
			return fail("G1: index maps %q to %d, want %d", id, g.index[id], v)
		}
	}

	// G2: out-lists are strictly ascending, in range, self-free, and carry
	// valid weights; G3: in-lists are exactly their transpose.
	edges := 0
	inCount := make([]int, n)
	for v := range g.ids {
		if len(g.outWeight[v]) != len(g.out[v]) {
			return fail("G2: node %q has %d targets and %d weights", g.ids[v], len(g.out[v]), len(g.outWeight[v]))
		}
		for i, t := range g.out[v] {
			if t < 0 || t >= n || t == v || (i > 0 && g.out[v][i-1] >= t) {
				return fail("G2: out-list of %q is invalid at %d", g.ids[v], i)
			}
			if !validWeight(g.outWeight[v][i]) {
				return fail("G2: edge %q -> %q has weight %v", g.ids[v], g.ids[t], g.outWeight[v][i])
			}
			if _, found := slices.BinarySearch(g.in[t], v); !found {
				return fail("G3: edge %q -> %q missing from the in-list", g.ids[v], g.ids[t])
			}
			inCount[t]++
			edges++
		}
	}
	for v := range g.ids {
		if len(g.in[v]) != inCount[v] {
			return fail("G3: in-list of %q has %d entries, %d edges point at it", g.ids[v], len(g.in[v]), inCount[v])
		}
		for i := 1; i < len(g.in[v]); i++ {
			if g.in[v][i-1] >= g.in[v][i] {
				return fail("G3: in-list of %q not strictly ascending", g.ids[v])
			}
		}
	}
	if edges != g.edgeCount {
		return fail("G2: counted %d edges, cached %d", edges, g.edgeCount)
	}

	// G4: the undirected view is the union of both lists, with the smaller
	// weight where a pair links both ways.
	undirected := 0
	for v := range g.ids {
		want, wantWeight := g.mergeUndirected(v)
		if !slices.Equal(g.adj[v], want) {
			return fail("G4: undirected neighbours of %q are not the union of in and out", g.ids[v])
		}
		for i := range want {
			if g.adjWeight[v][i] != wantWeight[i] {
				return fail("G4: undirected weight %q - %q is %v, want %v",
					g.ids[v], g.ids[want[i]], g.adjWeight[v][i], wantWeight[i])
			}
		}
		undirected += len(want)
	}
	// G5: the undirected edge count is half the sum of undirected degrees.
	if undirected%2 != 0 || undirected/2 != g.undirectedEdgeCount {
		return fail("G5: undirected degree sum %d does not match %d edges", undirected, g.undirectedEdgeCount)
	}
	return nil
}
