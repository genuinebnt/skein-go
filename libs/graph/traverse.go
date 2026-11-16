package graph

import "fmt"

// Direction says which links a traversal follows.
//
// Rust: `enum Direction { CitedBy, Cites, Undirected }`, with CitedBy as the
// `Default`.
type Direction uint8

const (
	// CitedBy follows links backwards, from a note to the notes that link to
	// it. It is the zero value because it is the blast-radius question users
	// ask: "if this note changes, which notes that rely on it are affected?"
	CitedBy Direction = iota
	// Cites follows links as written, from a note to the notes it links to:
	// "what does this note rely on?"
	Cites
	// Undirected follows links both ways: "what is near this note by links?"
	Undirected
)

// String returns the snake-case name used in fixtures and logs.
func (d Direction) String() string {
	switch d {
	case CitedBy:
		return "cited_by"
	case Cites:
		return "cites"
	case Undirected:
		return "undirected"
	default:
		return fmt.Sprintf("Direction(%d)", uint8(d))
	}
}

// neighbours returns v's neighbour list for dir. The slice is shared with
// the graph and must not be modified.
func (g *Graph) neighbours(v int, dir Direction) ([]int, error) {
	switch dir {
	case CitedBy:
		return g.in[v], nil
	case Cites:
		return g.out[v], nil
	case Undirected:
		return g.adj[v], nil
	default:
		return nil, fmt.Errorf("%w: direction %d", ErrInvalidArgument, uint8(dir))
	}
}

// unreached marks a node a traversal did not reach.
const unreached = -1

// bfs returns the hop distance from source to every node following dir,
// unreached for nodes it did not reach, never going further than maxHops
// (no limit when maxHops is 0).
func (g *Graph) bfs(source int, dir Direction, maxHops int) ([]int, error) {
	if _, err := g.neighbours(source, dir); err != nil {
		return nil, err
	}
	dist := make([]int, len(g.ids))
	for i := range dist {
		dist[i] = unreached
	}
	dist[source] = 0
	queue := []int{source}
	for head := 0; head < len(queue); head++ {
		v := queue[head]
		if maxHops > 0 && dist[v] == maxHops {
			continue
		}
		next, _ := g.neighbours(v, dir)
		for _, w := range next {
			if dist[w] == unreached {
				dist[w] = dist[v] + 1
				queue = append(queue, w)
			}
		}
	}
	return dist, nil
}

// distanceMap converts a distance slice to a map over the reached nodes.
func (g *Graph) distanceMap(dist []int) map[string]int {
	out := make(map[string]int)
	for v, d := range dist {
		if d != unreached {
			out[g.ids[v]] = d
		}
	}
	return out
}

// Distances returns the hop distance from source to every node it can reach
// in the undirected view, including source itself at 0. Unreachable nodes
// are absent.
//
// Breadth-first search, O(V + E).
//
// It is not the blast radius: direction is ignored here, because "how far
// apart are these notes" does not care which one cites the other. Use
// BlastRadius to follow links one way.
func (g *Graph) Distances(source string) (map[string]int, error) {
	s, err := g.lookup(source)
	if err != nil {
		return nil, err
	}
	dist, err := g.bfs(s, Undirected, 0)
	if err != nil {
		return nil, err
	}
	return g.distanceMap(dist), nil
}

// Path is a shortest path between two nodes.
type Path struct {
	// Found is false when no path exists; Nodes is then empty and Cost 0.
	// An empty path and "no path" are different answers, so they are not
	// both encoded as an empty list.
	Found bool
	// Nodes lists the path from the first endpoint to the second, both
	// included. A path from a node to itself is that one node.
	Nodes []string
	// Hops is len(Nodes) - 1.
	Hops int
	// Cost is the sum of edge weights for a weighted path and Hops for an
	// unweighted one.
	Cost float64
}

// ShortestPath returns one shortest path from `from` to `to` in the
// undirected view, counting hops.
//
// Several paths often tie. The one returned is defined by walking back from
// `to`: at each step, move to the smallest-ID neighbour that is one hop
// closer to `from`. That rule is easy to state in any language, so the Rust
// port returns the same path.
//
// Breadth-first search, O(V + E).
//
// It is not a weighted path (see ShortestWeightedPath) and not a directed
// one: a reader following a citation backwards is still following it.
func (g *Graph) ShortestPath(from, to string) (Path, error) {
	s, err := g.lookup(from)
	if err != nil {
		return Path{}, err
	}
	t, err := g.lookup(to)
	if err != nil {
		return Path{}, err
	}
	dist, err := g.bfs(s, Undirected, 0)
	if err != nil {
		return Path{}, err
	}
	if dist[t] == unreached {
		return Path{Nodes: []string{}}, nil
	}
	reversed := []int{t}
	for v := t; v != s; {
		closer := dist[v] - 1
		v = g.closerNeighbour(v, func(u int, _ float64) bool { return dist[u] == closer })
		reversed = append(reversed, v)
	}
	return g.pathFromReversed(reversed, float64(dist[t])), nil
}

// closerNeighbour returns v's smallest-ID undirected neighbour u satisfying
// onPath(u, weight of the u-v edge). A caller only asks when one exists.
func (g *Graph) closerNeighbour(v int, onPath func(u int, weight float64) bool) int {
	for i, u := range g.adj[v] {
		if onPath(u, g.adjWeight[v][i]) {
			return u
		}
	}
	panic("graph: shortest-path walk found no predecessor")
}

// pathFromReversed builds a Path from positions listed target first.
func (g *Graph) pathFromReversed(reversed []int, cost float64) Path {
	nodes := make([]string, len(reversed))
	for i, v := range reversed {
		nodes[len(reversed)-1-i] = g.ids[v]
	}
	return Path{Found: true, Nodes: nodes, Hops: len(nodes) - 1, Cost: cost}
}

// Rings is the result of BlastRadius.
type Rings struct {
	// Hops maps every node reached to its hop distance from the source. The
	// source is included at 0.
	Hops map[string]int
	// Sizes[d] is the number of nodes exactly d hops away; Sizes[0] is 1, the
	// source. Its length is one more than the furthest distance reached.
	Sizes []int
}

// BlastRadius returns every node reachable from source by following links in
// dir, by hop distance, stopping at maxHops (0 means no limit).
//
// With CitedBy (the zero value) it answers "if this note changes, which
// notes are affected": the notes that cite it, the notes that cite those,
// and so on. With Cites it answers "what does this note rely on". Ring sizes
// show the shape: a radius whose rings stop growing is a neighbourhood that
// stops connecting.
//
// Breadth-first search, O(V + E). A negative maxHops or an unknown
// direction returns ErrInvalidArgument.
//
// It is not a measure of importance (see PageRank) and does not weight how
// strongly notes depend on each other: one link is one hop.
func (g *Graph) BlastRadius(source string, dir Direction, maxHops int) (Rings, error) {
	if maxHops < 0 {
		return Rings{}, fmt.Errorf("%w: max hops %d", ErrInvalidArgument, maxHops)
	}
	s, err := g.lookup(source)
	if err != nil {
		return Rings{}, err
	}
	dist, err := g.bfs(s, dir, maxHops)
	if err != nil {
		return Rings{}, err
	}
	var sizes []int
	for _, d := range dist {
		if d == unreached {
			continue
		}
		for len(sizes) <= d {
			sizes = append(sizes, 0)
		}
		sizes[d]++
	}
	return Rings{Hops: g.distanceMap(dist), Sizes: sizes}, nil
}
