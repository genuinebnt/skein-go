package graph

import (
	"fmt"
	"math"
)

// Betweenness returns each node's betweenness centrality in the undirected
// view, normalised to [0, 1]: the fraction of pairs of other nodes whose
// shortest paths pass through it, where a pair with k shortest paths, j of
// them through the node, contributes j/k.
//
//	B(v) = sum over pairs {s, t}, s != v != t, of sigma_st(v) / sigma_st
//	       divided by (n-1)(n-2)/2
//
// A star's centre scores 1 and its leaves 0. With fewer than three nodes
// every score is 0.
//
// Brandes' algorithm, O(V E) time and O(V + E) memory: one breadth-first
// search per source counts shortest paths forwards, then a pass in reverse
// discovery order accumulates each node's dependency in one sweep.
//
// It is computed over the undirected view on purpose. A link claims two notes
// are related, and relatedness is not one-way; following direction would give
// every note that is cited but cites nothing a score of 0, which says more
// about link direction than about the note's role as a bridge.
//
// It is not degree: a note with two links can be the only bridge between
// two halves of a workspace and score higher than a note with fifty.
func (g *Graph) Betweenness() map[string]float64 {
	n := len(g.ids)
	score := make([]float64, n)
	var (
		dist    = make([]int, n)
		paths   = make([]float64, n) // float64: path counts overflow integers on large graphs
		depends = make([]float64, n)
		order   = make([]int, 0, n) // nodes in discovery order
	)
	for s := range g.ids {
		for v := range dist {
			dist[v], paths[v], depends[v] = unreached, 0, 0
		}
		dist[s], paths[s] = 0, 1
		order = append(order[:0], s)
		for head := 0; head < len(order); head++ {
			v := order[head]
			for _, w := range g.adj[v] {
				if dist[w] == unreached {
					dist[w] = dist[v] + 1
					order = append(order, w)
				}
				if dist[w] == dist[v]+1 {
					paths[w] += paths[v]
				}
			}
		}
		// Reverse discovery order visits every node after all nodes further
		// from s, so each node's dependency is complete when it is read.
		// Predecessors are recomputed from dist instead of stored as lists.
		for i := len(order) - 1; i > 0; i-- {
			w := order[i]
			for _, v := range g.adj[w] {
				if dist[v] == dist[w]-1 {
					depends[v] += paths[v] / paths[w] * (1 + depends[w])
				}
			}
			score[w] += depends[w]
		}
	}

	// Each unordered pair was counted once from each end.
	scale := 0.0
	if n > 2 {
		scale = 1 / float64((n-1)*(n-2))
	}
	out := make(map[string]float64, n)
	for v, b := range score {
		out[g.ids[v]] = b * scale
	}
	return out
}

// PageRankOptions configures PageRank.
type PageRankOptions struct {
	// Damping is the probability of following a link rather than jumping to
	// a random note, in [0, 1).
	Damping float64
	// Tolerance stops the iteration once the L1 distance between successive
	// rank vectors is below it. Must be positive.
	Tolerance float64
	// MaxIterations caps the number of iterations. Must be at least 1.
	MaxIterations int
}

// DefaultPageRankOptions returns the usual settings: damping 0.85, tolerance
// 1e-10 and at most 200 iterations, enough to converge on any graph since the
// error shrinks by a factor of 0.85 per iteration.
func DefaultPageRankOptions() PageRankOptions {
	return PageRankOptions{Damping: 0.85, Tolerance: 1e-10, MaxIterations: 200}
}

// Ranks is the result of PageRank.
type Ranks struct {
	// Scores maps every node to its rank. Ranks are positive and sum to 1.
	Scores map[string]float64
	// Iterations is how many iterations ran.
	Iterations int
	// Converged reports whether Tolerance was reached before MaxIterations.
	Converged bool
}

// PageRank returns each node's PageRank following links as written: rank
// flows from a note to the notes it cites, so a note cited by well-cited
// notes ranks highly.
//
// Power iteration from the uniform vector:
//
//	r'(v) = (1-d)/n + d * ( sum over u -> v of r(u)/outdeg(u) + D/n )
//
// where D is the total rank of dangling notes (notes that link to nothing).
// Dangling rank is spread over every note, which is the same as giving each
// dangling note a link to every note; without it rank would leak away and
// the scores would not sum to 1. Each iteration costs O(V + E).
//
// It is not betweenness: PageRank rewards being cited, betweenness rewards
// being a bridge.
func (g *Graph) PageRank(opts PageRankOptions) (Ranks, error) {
	if !(opts.Damping >= 0 && opts.Damping < 1) || !(opts.Tolerance > 0) || opts.MaxIterations < 1 {
		return Ranks{}, fmt.Errorf("%w: pagerank options %+v", ErrInvalidArgument, opts)
	}
	n := len(g.ids)
	result := Ranks{Scores: make(map[string]float64, n), Converged: n == 0}
	if n == 0 {
		return result, nil
	}
	rank := make([]float64, n)
	next := make([]float64, n)
	for v := range rank {
		rank[v] = 1 / float64(n)
	}
	for result.Iterations < opts.MaxIterations {
		result.Iterations++
		dangling := 0.0
		for v := range g.ids {
			if len(g.out[v]) == 0 {
				dangling += rank[v]
			}
		}
		base := (1-opts.Damping)/float64(n) + opts.Damping*dangling/float64(n)
		change := 0.0
		for v := range g.ids {
			// Pulling from in-links (rather than pushing along out-links)
			// fixes the order of the additions, so the Rust port reproduces
			// the same floating-point sums.
			inflow := 0.0
			for _, u := range g.in[v] {
				inflow += rank[u] / float64(len(g.out[u]))
			}
			next[v] = base + opts.Damping*inflow
			change += math.Abs(next[v] - rank[v])
		}
		rank, next = next, rank
		if change < opts.Tolerance {
			result.Converged = true
			break
		}
	}
	for v, r := range rank {
		result.Scores[g.ids[v]] = r
	}
	return result, nil
}
