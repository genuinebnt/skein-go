package graph

import (
	"container/heap"
	"fmt"
	"math"
)

// SemanticDistance converts the cosine similarity of two notes' embeddings
// into an edge weight for ShortestWeightedPath: 1 - cosine, in [0, 2].
// Similar notes are close; unrelated ones are about 1 apart.
//
// Cosines computed in floating point can stray just outside [-1, 1]; values
// within 1e-9 of the range are clamped. Anything further out, or NaN, is a
// bug upstream and returns ErrInvalidWeight.
func SemanticDistance(cosine float64) (float64, error) {
	const slack = 1e-9
	if math.IsNaN(cosine) || cosine > 1+slack || cosine < -1-slack {
		return 0, fmt.Errorf("%w: cosine similarity %v", ErrInvalidWeight, cosine)
	}
	return 1 - math.Max(-1, math.Min(1, cosine)), nil
}

// weightedDistances runs Dijkstra from source over the undirected view and
// returns each node's cost and hop count (unreached nodes have cost +Inf).
//
// Paths are compared by cost and then by hops. Both are non-negative and
// add along a path, so the pair is a valid Dijkstra key; the hop count
// breaks ties between equal-cost paths (common with zero weights) in favour
// of the shorter one.
func (g *Graph) weightedDistances(source int) ([]float64, []int) {
	n := len(g.ids)
	cost := make([]float64, n)
	hops := make([]int, n)
	settled := make([]bool, n)
	for i := range cost {
		cost[i] = math.Inf(1)
		hops[i] = unreached
	}
	cost[source], hops[source] = 0, 0
	queue := &costHeap{{cost: 0, hops: 0, node: source}}
	for queue.Len() > 0 {
		top := heap.Pop(queue).(costItem)
		v := top.node
		if settled[v] {
			continue // a stale entry; v was settled through a cheaper one
		}
		settled[v] = true
		for i, w := range g.adj[v] {
			c, h := cost[v]+g.adjWeight[v][i], hops[v]+1
			if c < cost[w] || (c == cost[w] && h < hops[w]) {
				cost[w], hops[w] = c, h
				heap.Push(queue, costItem{cost: c, hops: h, node: w})
			}
		}
	}
	return cost, hops
}

// WeightedDistances returns the cheapest path cost from source to every node
// it can reach in the undirected view, using edge weights. Unreachable nodes
// are absent.
//
// Dijkstra's algorithm with a binary heap, O((V + E) log V). Negative
// weights, which break Dijkstra's greedy settling, cannot reach here: New
// rejects them.
func (g *Graph) WeightedDistances(source string) (map[string]float64, error) {
	s, err := g.lookup(source)
	if err != nil {
		return nil, err
	}
	cost, _ := g.weightedDistances(s)
	out := make(map[string]float64)
	for v, c := range cost {
		if !math.IsInf(c, 1) {
			out[g.ids[v]] = c
		}
	}
	return out, nil
}

// ShortestWeightedPath returns the cheapest path from `from` to `to` in the
// undirected view, where an edge costs its weight (for semantic paths,
// SemanticDistance of its endpoints). A pair linked both ways costs the
// smaller of the two weights.
//
// Among equally cheap paths it prefers fewer hops, and among those it walks
// back from `to`, at each step to the smallest-ID neighbour on such a path,
// the same rule as ShortestPath. Costs are compared exactly, which is
// reproducible because both languages add the same float64 values in the
// same order.
//
// Dijkstra's algorithm, O((V + E) log V).
//
// It is not a hop count (see ShortestPath): one long jump between two
// unrelated notes can cost more than several short ones.
func (g *Graph) ShortestWeightedPath(from, to string) (Path, error) {
	s, err := g.lookup(from)
	if err != nil {
		return Path{}, err
	}
	t, err := g.lookup(to)
	if err != nil {
		return Path{}, err
	}
	cost, hops := g.weightedDistances(s)
	if math.IsInf(cost[t], 1) {
		return Path{Nodes: []string{}}, nil
	}
	reversed := []int{t}
	for v := t; v != s; {
		here := v
		v = g.closerNeighbour(v, func(u int, weight float64) bool {
			return hops[u] == hops[here]-1 && cost[u]+weight == cost[here]
		})
		reversed = append(reversed, v)
	}
	return g.pathFromReversed(reversed, cost[t]), nil
}

// costItem is a Dijkstra queue entry, ordered by cost, then hops, then node
// position (so equal entries pop in ID order and the run is deterministic).
type costItem struct {
	cost float64
	hops int
	node int
}

type costHeap []costItem

func (h costHeap) Len() int { return len(h) }
func (h costHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	if a.hops != b.hops {
		return a.hops < b.hops
	}
	return a.node < b.node
}
func (h costHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *costHeap) Push(x any)   { *h = append(*h, x.(costItem)) }
func (h *costHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
