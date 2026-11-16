// Package graphs holds the few graph algorithms the seed generator needs to
// compute ground truth over the link graph it planted: strongly connected
// components, weakly connected components, breadth-first rings in either
// direction, and undirected shortest paths with a count of how many there
// are.
//
// These are deliberately plain: they compute the expected answers that
// libs/graph and the search service will be tested against, so they must be
// easy to trust. Tests check them against a separate recursive Tarjan.
//
// Nodes are dense ints 0..N-1. Adjacency lists are kept sorted and
// deduplicated, so every traversal visits neighbours in ascending order and
// every result is deterministic.
package graphs

import "sort"

// Graph is a directed graph over nodes 0..N-1.
type Graph struct {
	Out [][]int
	In  [][]int
}

// New returns a graph with n nodes and no edges.
func New(n int) *Graph {
	return &Graph{Out: make([][]int, n), In: make([][]int, n)}
}

// N returns the number of nodes.
func (g *Graph) N() int { return len(g.Out) }

// AddEdge adds u -> v. Call Normalize once all edges are added.
func (g *Graph) AddEdge(u, v int) {
	g.Out[u] = append(g.Out[u], v)
	g.In[v] = append(g.In[v], u)
}

// Normalize sorts and deduplicates every adjacency list.
func (g *Graph) Normalize() {
	for i := range g.Out {
		g.Out[i] = sortedUnique(g.Out[i])
		g.In[i] = sortedUnique(g.In[i])
	}
}

// Edges returns the number of distinct edges (after Normalize).
func (g *Graph) Edges() int {
	n := 0
	for _, o := range g.Out {
		n += len(o)
	}
	return n
}

// HasEdge reports whether u -> v exists (after Normalize).
func (g *Graph) HasEdge(u, v int) bool {
	o := g.Out[u]
	i := sort.SearchInts(o, v)
	return i < len(o) && o[i] == v
}

func sortedUnique(xs []int) []int {
	sort.Ints(xs)
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// SCC returns the strongly connected components, each sorted ascending, and
// the list of components sorted by their smallest member. It is Tarjan's
// algorithm with an explicit stack, so a 100,000-node chain does not recurse.
func (g *Graph) SCC() [][]int {
	n := g.N()
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var comps [][]int
	next := 0
	type frame struct{ v, edge int }
	for root := 0; root < n; root++ {
		if index[root] >= 0 {
			continue
		}
		call := []frame{{root, 0}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(call) > 0 {
			f := &call[len(call)-1]
			if f.edge < len(g.Out[f.v]) {
				w := g.Out[f.v][f.edge]
				f.edge++
				if index[w] < 0 {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					call = append(call, frame{w, 0})
				} else if onStack[w] && index[w] < low[f.v] {
					low[f.v] = index[w]
				}
				continue
			}
			v := f.v
			call = call[:len(call)-1]
			if len(call) > 0 {
				p := call[len(call)-1].v
				if low[v] < low[p] {
					low[p] = low[v]
				}
			}
			if low[v] == index[v] {
				var comp []int
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp = append(comp, w)
					if w == v {
						break
					}
				}
				sort.Ints(comp)
				comps = append(comps, comp)
			}
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i][0] < comps[j][0] })
	return comps
}

// WeakComponents returns the connected components ignoring direction, each
// sorted ascending, ordered by smallest member.
func (g *Graph) WeakComponents() [][]int {
	n := g.N()
	seen := make([]bool, n)
	var comps [][]int
	for s := 0; s < n; s++ {
		if seen[s] {
			continue
		}
		comp := []int{s}
		seen[s] = true
		for q := 0; q < len(comp); q++ {
			v := comp[q]
			for _, nb := range [2][]int{g.Out[v], g.In[v]} {
				for _, w := range nb {
					if !seen[w] {
						seen[w] = true
						comp = append(comp, w)
					}
				}
			}
		}
		sort.Ints(comp)
		comps = append(comps, comp)
	}
	return comps
}

// Rings returns, for a breadth-first search from src following Out edges
// (forward) or In edges (backward), the hop distance of every reached node
// (-1 if unreached) and the number of nodes at each distance. ring[0] is 1.
// maxHops <= 0 means no limit.
func (g *Graph) Rings(src int, forward bool, maxHops int) (dist []int, rings []int) {
	adj := g.Out
	if !forward {
		adj = g.In
	}
	dist = make([]int, g.N())
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	frontier := []int{src}
	rings = []int{1}
	for len(frontier) > 0 && (maxHops <= 0 || len(rings) <= maxHops) {
		var next []int
		for _, v := range frontier {
			for _, w := range adj[v] {
				if dist[w] < 0 {
					dist[w] = dist[v] + 1
					next = append(next, w)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		rings = append(rings, len(next))
		frontier = next
	}
	return dist, rings
}

// ShortestPath finds a shortest path from src to dst ignoring edge
// direction. It returns the path (src first; nil if unreachable) and the
// number of distinct shortest paths, capped at 1<<30. Among equal-length
// paths the one returned is found by visiting neighbours in ascending order
// and keeping each node's first discoverer.
func (g *Graph) ShortestPath(src, dst int) ([]int, int) {
	n := g.N()
	dist := make([]int, n)
	parent := make([]int, n)
	count := make([]int, n)
	for i := range dist {
		dist[i], parent[i] = -1, -1
	}
	dist[src], count[src] = 0, 1
	queue := []int{src}
	for q := 0; q < len(queue); q++ {
		v := queue[q]
		if v == dst {
			break
		}
		for _, w := range mergeSorted(g.Out[v], g.In[v]) {
			switch {
			case dist[w] < 0:
				dist[w], parent[w], count[w] = dist[v]+1, v, count[v]
				queue = append(queue, w)
			case dist[w] == dist[v]+1:
				count[w] = min(count[w]+count[v], 1<<30)
			}
		}
	}
	if dist[dst] < 0 {
		return nil, 0
	}
	var path []int
	for v := dst; v >= 0; v = parent[v] {
		path = append(path, v)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, count[dst]
}

// mergeSorted returns the sorted union of two sorted lists.
func mergeSorted(a, b []int) []int {
	out := make([]int, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || (i < len(a) && a[i] < b[j]):
			out = append(out, a[i])
			i++
		case i == len(a) || b[j] < a[i]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i, j = i+1, j+1
		}
	}
	return out
}
