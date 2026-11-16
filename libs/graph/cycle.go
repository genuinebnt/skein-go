package graph

// Three-colour depth-first search states.
const (
	white uint8 = iota // not visited
	grey               // on the current search path
	black              // finished: everything reachable from it is explored
)

// searchFrame replaces one level of recursion in a depth-first search: the
// node, and how many of its out-links have been explored.
type searchFrame struct{ node, next int }

// FindCycle returns one directed cycle as a closed list of IDs (the first
// and last entries are the same node), or an empty list if the graph is
// acyclic. Self-loops cannot exist, so a cycle has at least two distinct
// nodes.
//
// Three-colour depth-first search, O(V + E). The cycle returned is the one
// closed by the first back edge met by a search that starts from nodes in
// ascending ID order and follows each node's links in ascending ID order; it
// starts at the node that edge points to.
//
// A plain "visited" set is not enough: in the diamond A -> B, A -> C,
// B -> D, C -> D the search reaches D twice without any cycle. Only an edge
// to a node still on the current path (grey) closes a cycle.
//
// It is not every cycle and not the shortest one. StronglyConnected splits
// all of them into sets; TopologicalOrder measures how much they block.
func (g *Graph) FindCycle() []string {
	colour := make([]uint8, len(g.ids))
	var path []searchFrame // the current search path, root first
	for s := range g.ids {
		if colour[s] != white {
			continue
		}
		colour[s] = grey
		path = append(path[:0], searchFrame{node: s})
		for len(path) > 0 {
			top := &path[len(path)-1]
			if top.next == len(g.out[top.node]) {
				colour[top.node] = black
				path = path[:len(path)-1]
				continue
			}
			w := g.out[top.node][top.next]
			top.next++
			switch colour[w] {
			case white:
				colour[w] = grey
				path = append(path, searchFrame{node: w})
			case grey:
				return g.closeCycle(path, w)
			case black:
				// Finished on an earlier branch: reachable, but not an
				// ancestor, so no cycle through this edge.
			}
		}
	}
	return []string{}
}

// closeCycle returns the path suffix starting at target, then target again.
func (g *Graph) closeCycle(path []searchFrame, target int) []string {
	for i, f := range path {
		if f.node == target {
			cycle := make([]string, 0, len(path)-i+1)
			for _, step := range path[i:] {
				cycle = append(cycle, g.ids[step.node])
			}
			return append(cycle, g.ids[target])
		}
	}
	panic("graph: grey node missing from the search path")
}
