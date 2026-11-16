package graph

import (
	"fmt"
	"runtime/debug"
	"testing"
)

// chain returns n nodes linked n0 -> n1 -> ... -> n(n-1), optionally closed
// into a ring. IDs are zero-padded so the chain runs in ascending ID order
// and a depth-first search from the smallest ID goes the full length.
func chain(t testing.TB, n int, closed bool) *Graph {
	t.Helper()
	nodes := make([]Node, n)
	edges := make([]Edge, 0, n)
	for i := range nodes {
		nodes[i] = Node{ID: fmt.Sprintf("n%06d", i), Root: i == 0}
		if i > 0 {
			edges = append(edges, Edge{From: nodes[i-1].ID, To: nodes[i].ID})
		}
	}
	if closed {
		edges = append(edges, Edge{From: nodes[n-1].ID, To: nodes[0].ID})
	}
	return mustGraph(t, nodes, edges)
}

// TestDeepChainNeedsNoDeepStack runs every depth-first and layered algorithm
// on a 100,000-note chain and a 100,000-note ring with the goroutine stack
// capped at 4 MiB. A recursive search needs a frame per note (well over
// 4 MiB here) and would crash the test binary; Go would otherwise grow the
// stack silently, hiding a bug that overflows the fixed-size stacks of the
// Rust port.
func TestDeepChainNeedsNoDeepStack(t *testing.T) {
	if testing.Short() {
		t.Skip("deep chain is slow under -short")
	}
	const n = 100_000
	line := chain(t, n, false)
	ring := chain(t, n, true)

	previous := debug.SetMaxStack(4 << 20)
	defer debug.SetMaxStack(previous)

	if got := len(line.StronglyConnected().Members); got != n {
		t.Fatalf("chain has %d SCCs, want %d", got, n)
	}
	if got := len(ring.StronglyConnected().Members); got != 1 {
		t.Fatalf("ring has %d SCCs, want 1", got)
	}
	if got := len(line.FindCycle()); got != 0 {
		t.Fatalf("chain has a cycle of length %d", got)
	}
	if got := len(ring.FindCycle()); got != n+1 {
		t.Fatalf("ring cycle has %d entries, want %d", got, n+1)
	}
	order := line.TopologicalOrder()
	if len(order.Order) != n || order.Order[0] != "n099999" || len(order.Layers) != n {
		t.Fatalf("chain order starts %q with %d layers, want the last note and %d", order.Order[0], len(order.Layers), n)
	}
	if got := len(ring.TopologicalOrder().Unplaced); got != n {
		t.Fatalf("ring leaves %d unplaced, want %d", got, n)
	}
	reading, err := line.ReadingPath("n000000")
	if err != nil || len(reading.Steps) != n || reading.Steps[0].Note != "n099999" {
		t.Fatalf("chain reading path: %d steps, %v", len(reading.Steps), err)
	}
	cyclic, err := ring.ReadingPath("n000000")
	if err != nil || len(cyclic.Cycles) != 1 || len(cyclic.Cycles[0]) != n {
		t.Fatalf("ring reading path: %d cycles, %v", len(cyclic.Cycles), err)
	}
	rings, err := line.BlastRadius("n099999", CitedBy, 0)
	if err != nil || len(rings.Sizes) != n {
		t.Fatalf("blast radius of the chain's end: %d rings, %v", len(rings.Sizes), err)
	}
	if got := len(line.ConnectedComponents().Members); got != 1 {
		t.Fatalf("chain has %d components", got)
	}
	if got := len(line.Louvain().Partition.Members); got < 2 {
		t.Fatalf("Louvain left a 100,000-note chain in %d community", got)
	}
}
