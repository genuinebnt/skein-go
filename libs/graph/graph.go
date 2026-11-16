package graph

import (
	"fmt"
	"math"
	"slices"
)

// Node is one note in the link graph.
type Node struct {
	// ID is the note's identifier. Any non-empty string; IDs are compared as
	// bytes (Go string order, Rust str order).
	ID string
	// Root marks a note a reader can reach without already knowing another
	// note exists (a top-level page). Only OrphanComponents uses it.
	Root bool
}

// Edge is a directed link: the note From links to (cites) the note To.
type Edge struct {
	From string
	To   string
	// Weight is the edge's length for the weighted shortest path, typically
	// SemanticDistance of the two notes' embeddings. It must be finite and
	// non-negative. Every other algorithm ignores it, so a graph built only
	// for them can leave it zero.
	Weight float64
}

// Graph is an immutable directed graph over string node IDs.
//
// Nodes are stored in ascending ID order and referred to internally by their
// position in that order, so every loop over nodes or neighbours visits them
// by ascending ID. That is what makes every result independent of input
// order and of Go map iteration order.
//
// A Graph is safe for concurrent use by multiple goroutines because nothing
// mutates it after New returns.
type Graph struct {
	ids   []string       // ascending
	index map[string]int // ids[index[id]] == id
	root  []bool

	out       [][]int     // out[v]: targets of v's links, ascending
	outWeight [][]float64 // parallel to out
	in        [][]int     // in[v]: notes linking to v, ascending

	// The undirected view: a link's existence, not its direction. adj[v] is
	// the union of out[v] and in[v], ascending and without repeats. When both
	// u->v and v->u exist, adjWeight holds the smaller of the two weights,
	// because a shortest path that ignores direction would take the cheaper
	// one anyway.
	adj       [][]int
	adjWeight [][]float64

	edgeCount           int // directed edges
	undirectedEdgeCount int
}

// New validates nodes and edges and builds a Graph.
//
// It rejects, wrapping the sentinel with the offending IDs: an empty ID
// (ErrEmptyID), a repeated node ID (ErrDuplicateNode), an edge whose endpoint
// is not a node (ErrUnknownNode), an edge from a node to itself
// (ErrSelfLoop), the same directed edge twice (ErrDuplicateEdge), and a
// negative, NaN or infinite weight (ErrInvalidWeight). Nodes are checked
// before edges and edges in input order, so the first offending element is
// the one reported; duplicate edges are found last, in (From, To) order.
//
// The order of nodes and edges in the input does not affect any result.
//
// Complexity: O((V + E) log(V + E)).
func New(nodes []Node, edges []Edge) (*Graph, error) {
	ids := make([]string, 0, len(nodes))
	rootByID := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.ID == "" {
			return nil, ErrEmptyID
		}
		if _, dup := rootByID[n.ID]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateNode, n.ID)
		}
		rootByID[n.ID] = n.Root
		ids = append(ids, n.ID)
	}
	slices.Sort(ids)

	g := &Graph{
		ids:       ids,
		index:     make(map[string]int, len(ids)),
		root:      make([]bool, len(ids)),
		out:       make([][]int, len(ids)),
		outWeight: make([][]float64, len(ids)),
		in:        make([][]int, len(ids)),
		adj:       make([][]int, len(ids)),
		adjWeight: make([][]float64, len(ids)),
		edgeCount: len(edges),
	}
	for i, id := range ids {
		g.index[id] = i
		g.root[i] = rootByID[id]
	}

	type arc struct {
		to     int
		weight float64
	}
	arcs := make([][]arc, len(ids))
	for _, e := range edges {
		from, ok := g.index[e.From]
		if !ok {
			return nil, fmt.Errorf("%w: edge %q -> %q: %q", ErrUnknownNode, e.From, e.To, e.From)
		}
		to, ok := g.index[e.To]
		if !ok {
			return nil, fmt.Errorf("%w: edge %q -> %q: %q", ErrUnknownNode, e.From, e.To, e.To)
		}
		if from == to {
			return nil, fmt.Errorf("%w: %q", ErrSelfLoop, e.From)
		}
		if !validWeight(e.Weight) {
			return nil, fmt.Errorf("%w: edge %q -> %q has weight %v", ErrInvalidWeight, e.From, e.To, e.Weight)
		}
		arcs[from] = append(arcs[from], arc{to: to, weight: e.Weight})
	}

	for v, list := range arcs {
		slices.SortStableFunc(list, func(a, b arc) int { return a.to - b.to })
		for i, a := range list {
			if i > 0 && list[i-1].to == a.to {
				return nil, fmt.Errorf("%w: %q -> %q", ErrDuplicateEdge, ids[v], ids[a.to])
			}
			g.out[v] = append(g.out[v], a.to)
			g.outWeight[v] = append(g.outWeight[v], a.weight)
		}
	}
	// Filling in[] by ascending source keeps every in[] list sorted for free.
	for v := range ids {
		for _, t := range g.out[v] {
			g.in[t] = append(g.in[t], v)
		}
	}
	for v := range ids {
		g.adj[v], g.adjWeight[v] = g.mergeUndirected(v)
		g.undirectedEdgeCount += len(g.adj[v])
	}
	g.undirectedEdgeCount /= 2
	return g, nil
}

// mergeUndirected merges v's sorted out- and in-lists into its undirected
// neighbour list.
func (g *Graph) mergeUndirected(v int) ([]int, []float64) {
	out, in := g.out[v], g.in[v]
	adj := make([]int, 0, len(out)+len(in))
	weights := make([]float64, 0, len(out)+len(in))
	i, j := 0, 0
	for i < len(out) || j < len(in) {
		switch {
		case j == len(in) || (i < len(out) && out[i] < in[j]):
			adj = append(adj, out[i])
			weights = append(weights, g.outWeight[v][i])
			i++
		case i == len(out) || in[j] < out[i]:
			adj = append(adj, in[j])
			weights = append(weights, g.weightOf(in[j], v))
			j++
		default: // links both ways
			adj = append(adj, out[i])
			weights = append(weights, math.Min(g.outWeight[v][i], g.weightOf(in[j], v)))
			i++
			j++
		}
	}
	return adj, weights
}

// weightOf returns the weight of the edge from -> to, which must exist.
func (g *Graph) weightOf(from, to int) float64 {
	pos, found := slices.BinarySearch(g.out[from], to)
	if !found {
		panic("graph: weightOf called for a missing edge")
	}
	return g.outWeight[from][pos]
}

func validWeight(w float64) bool {
	return w >= 0 && !math.IsInf(w, 1) // NaN fails w >= 0
}

// Len returns the number of nodes.
func (g *Graph) Len() int { return len(g.ids) }

// EdgeCount returns the number of directed edges.
func (g *Graph) EdgeCount() int { return g.edgeCount }

// UndirectedEdgeCount returns the number of edges in the undirected view: a
// pair of notes linking to each other counts once. This is the m of the
// modularity formula.
func (g *Graph) UndirectedEdgeCount() int { return g.undirectedEdgeCount }

// Has reports whether id is a node of the graph.
func (g *Graph) Has(id string) bool {
	_, ok := g.index[id]
	return ok
}

// Nodes returns every node ID in ascending order. The slice is a copy.
func (g *Graph) Nodes() []string { return slices.Clone(g.ids) }

// Roots returns the IDs of the root nodes in ascending order.
func (g *Graph) Roots() []string {
	var roots []string
	for v, isRoot := range g.root {
		if isRoot {
			roots = append(roots, g.ids[v])
		}
	}
	return roots
}

// Edges returns every directed edge, sorted by From and then To.
func (g *Graph) Edges() []Edge {
	edges := make([]Edge, 0, g.edgeCount)
	for v, targets := range g.out {
		for i, t := range targets {
			edges = append(edges, Edge{From: g.ids[v], To: g.ids[t], Weight: g.outWeight[v][i]})
		}
	}
	return edges
}

// Neighbours returns the IDs adjacent to id in the given direction, in
// ascending order: the notes linking to it (CitedBy), the notes it links to
// (Cites), or both (Undirected).
func (g *Graph) Neighbours(id string, dir Direction) ([]string, error) {
	v, err := g.lookup(id)
	if err != nil {
		return nil, err
	}
	list, err := g.neighbours(v, dir)
	if err != nil {
		return nil, err
	}
	return g.names(list), nil
}

// lookup converts an ID to its node position.
func (g *Graph) lookup(id string) (int, error) {
	v, ok := g.index[id]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownNode, id)
	}
	return v, nil
}

// names converts node positions to IDs.
func (g *Graph) names(nodes []int) []string {
	out := make([]string, len(nodes))
	for i, v := range nodes {
		out[i] = g.ids[v]
	}
	return out
}

// String summarises the graph's size, for logs. It never prints IDs.
func (g *Graph) String() string {
	return fmt.Sprintf("graph(%d nodes, %d edges)", len(g.ids), g.edgeCount)
}
