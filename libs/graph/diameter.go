package graph

// Diameter is the result of Graph.Diameter.
type Diameter struct {
	// Length is the largest hop distance between two connected nodes, 0
	// when no two distinct nodes are connected.
	Length int
	// From and To are the endpoints of one such pair, From < To: the
	// lexicographically smallest pair at that distance. Both are empty when
	// Length is 0.
	From, To string
}

// Diameter returns the longest shortest path between two nodes of the same
// connected component, in the undirected view. Pairs in different components
// are skipped rather than counted as infinitely far apart: the diameter of a
// workspace with several components is that of its widest component.
//
// One breadth-first search per node, O(V (V + E)). Exact, unlike the
// double-sweep heuristics, which only bound it.
//
// It is not the size of the largest component, and not a directed measure:
// direction is ignored, as in ShortestPath.
func (g *Graph) Diameter() Diameter {
	best := Diameter{}
	for s := range g.ids {
		dist, _ := g.bfs(s, Undirected, 0)
		for t := s + 1; t < len(dist); t++ {
			if dist[t] > best.Length {
				best = Diameter{Length: dist[t], From: g.ids[s], To: g.ids[t]}
			}
		}
	}
	return best
}
