package graph

import (
	"container/heap"
	"slices"
)

// Order is the result of TopologicalOrder.
type Order struct {
	// Order is a reading order in which every note comes after every note it
	// links to. It holds every placeable note; it is partial when the graph
	// has cycles.
	Order []string
	// Unplaced holds, in ascending order, the notes that can never be
	// placed: every note on a cycle and every note that links, directly or
	// not, to a note on a cycle. Empty exactly when the graph is acyclic.
	Unplaced []string
	// Layers groups Order into dependency levels, each in ascending ID
	// order. Layer 0 holds the placed notes that link to nothing; layer k
	// holds the notes whose furthest link leads to layer k-1. Notes in one
	// layer can be read in any order, and the number of layers is the length
	// of the longest dependency chain plus one.
	Layers [][]string
}

// TopologicalOrder orders the graph so that no note comes before a note it
// links to: links point at prerequisites, so cited notes come first. This
// matches AnalyzeGraphResponse.topological_order and ReadingPath.
//
// Kahn's algorithm, O((V + E) log V): a note becomes ready once every note it
// links to is placed, and the smallest-ID ready note is placed next. That
// makes Order the lexicographically smallest valid order, a definition that
// does not depend on the implementation.
//
// On a cyclic graph it does not fail: it returns every note it could place
// and lists the rest in Unplaced, because "these 40 notes are orderable,
// these 6 are tangled" is actionable where an error is not.
//
// It is not a cycle report: FindCycle points at one loop and
// StronglyConnected names every loop, while Unplaced measures the damage
// they do, including notes downstream of a loop that are on none.
func (g *Graph) TopologicalOrder() Order {
	n := len(g.ids)
	pending := make([]int, n) // out-links whose target is not placed yet
	ready := &minHeap{}
	for v := range g.ids {
		pending[v] = len(g.out[v])
		if pending[v] == 0 {
			ready.items = append(ready.items, v) // ascending, so already a heap
		}
	}
	layer := make([]int, n)
	placed := make([]bool, n)
	var order []int
	depth := 0
	for ready.Len() > 0 {
		v := heap.Pop(ready).(int)
		order = append(order, v)
		placed[v] = true
		for _, t := range g.out[v] {
			layer[v] = max(layer[v], layer[t]+1)
		}
		depth = max(depth, layer[v]+1)
		for _, citer := range g.in[v] {
			pending[citer]--
			if pending[citer] == 0 {
				heap.Push(ready, citer)
			}
		}
	}

	result := Order{Order: g.names(order), Unplaced: []string{}, Layers: make([][]string, depth)}
	for v := range g.ids {
		if !placed[v] {
			result.Unplaced = append(result.Unplaced, g.ids[v])
		}
	}
	for _, v := range order {
		result.Layers[layer[v]] = append(result.Layers[layer[v]], g.ids[v])
	}
	for _, l := range result.Layers {
		slices.Sort(l)
	}
	return result
}

// minHeap is a binary min-heap of node positions, for "smallest ID first".
// Rust: `BinaryHeap<Reverse<usize>>`.
type minHeap struct{ items []int }

func (h *minHeap) Len() int           { return len(h.items) }
func (h *minHeap) Less(i, j int) bool { return h.items[i] < h.items[j] }
func (h *minHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *minHeap) Push(x any)         { h.items = append(h.items, x.(int)) }
func (h *minHeap) Pop() any {
	last := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	return last
}
