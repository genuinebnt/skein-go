package graph

import (
	"fmt"
	"slices"
)

// Modularity returns Newman's modularity Q of a partition of the undirected
// view:
//
//	Q = sum over communities c of ( L_c/m - (D_c / 2m)^2 )
//
// where m is UndirectedEdgeCount, L_c the number of edges with both ends in
// c and D_c the sum of the degrees of c's nodes. Q is the fraction of edges
// inside communities minus the fraction expected if the same degrees were
// wired at random. Around 0 means the partition explains nothing the degrees
// do not; 0.3 to 0.7 is typical of real community structure. A graph with no
// edges has Q = 0 by convention.
//
// assignment must give every node a community label (any int; only equality
// matters) and name no unknown node; otherwise it returns ErrUnknownNode or
// ErrInvalidArgument. O(V + E).
//
// It is not a community detector (see Louvain); it scores any partition, for
// example topics declared by users against the communities links imply.
func (g *Graph) Modularity(assignment map[string]int) (float64, error) {
	labels, err := g.labelsFrom(assignment)
	if err != nil {
		return 0, err
	}
	return g.modularityOf(labels), nil
}

// labelsFrom converts an ID -> label map to a per-position label slice,
// checking it covers exactly the graph's nodes.
func (g *Graph) labelsFrom(assignment map[string]int) ([]int, error) {
	labels := make([]int, len(g.ids))
	for v, id := range g.ids {
		l, ok := assignment[id]
		if !ok {
			return nil, fmt.Errorf("%w: partition has no community for %q", ErrInvalidArgument, id)
		}
		labels[v] = l
	}
	if len(assignment) != len(g.ids) {
		for id := range assignment {
			if !g.Has(id) {
				return nil, fmt.Errorf("%w: partition names %q", ErrUnknownNode, id)
			}
		}
	}
	return labels, nil
}

// modularityOf computes Q for per-position labels. Communities are summed in
// ascending label order so the floating-point result is reproducible.
func (g *Graph) modularityOf(labels []int) float64 {
	m := float64(g.undirectedEdgeCount)
	if m == 0 {
		return 0
	}
	distinct := slices.Clone(labels)
	slices.Sort(distinct)
	distinct = slices.Compact(distinct)
	slot := func(label int) int {
		i, _ := slices.BinarySearch(distinct, label)
		return i
	}
	inside := make([]float64, len(distinct))
	degree := make([]float64, len(distinct))
	for v := range g.ids {
		c := slot(labels[v])
		degree[c] += float64(len(g.adj[v]))
		for _, w := range g.adj[v] {
			if w > v && labels[w] == labels[v] {
				inside[c]++
			}
		}
	}
	q := 0.0
	for c := range distinct {
		share := degree[c] / (2 * m)
		q += inside[c]/m - share*share
	}
	return q
}

// Communities is the result of Louvain.
type Communities struct {
	Partition Partition
	// Modularity is Q of Partition, as Modularity computes it.
	Modularity float64
}

// minGain is the smallest modularity gain (in units of edge weight) that
// counts as an improvement. Without it, rounding noise could move a node back
// and forth between two equally good communities forever.
const minGain = 1e-12

// Louvain finds communities in the undirected view by greedily maximising
// modularity.
//
// The Louvain method (Blondel, Guillaume, Lambiotte and Lefebvre, 2008)
// repeats two phases until a level makes no move:
//
//  1. Local moving. Every node starts in its own community. Visit nodes in
//     order, removing each from its community and adding it to the
//     neighbouring community with the largest gain
//     k_in(c) - tot(c) k / 2m (k_in(c): edge weight from the node to c;
//     tot(c): total degree of c; k: the node's degree), if that beats
//     staying by more than minGain. Each community is labelled by the
//     position of the node it started from, and keeps that label even
//     after its founder leaves; ties go to the smallest label. Repeat
//     passes until one moves nothing.
//  2. Aggregation. Each community becomes one node of a weighted graph
//     whose degree includes the edges inside it, and phase 1 runs again.
//
// Order is what makes it deterministic: nodes are visited by ascending ID at
// the first level, and aggregated nodes in the order of their smallest
// original member. Communities are numbered by smallest member.
//
// Each pass costs O(V + E); the number of passes and levels is small in
// practice (levels are logarithmic on typical graphs) but has no tight
// bound.
//
// It is not optimal (maximising modularity is NP-hard) and not Leiden: a
// community it returns can, rarely, be internally disconnected. It also
// cannot see communities smaller than about sqrt(2m) edges (the resolution
// limit of modularity).
func (g *Graph) Louvain() Communities {
	level := newCommunityGraph(g)
	// member[v] is the level node holding original node v.
	member := make([]int, len(g.ids))
	for v := range member {
		member[v] = v
	}
	for {
		community, moved := level.localMoving()
		if !moved {
			break
		}
		renumbered, count := canonicalLabels(community)
		for v := range member {
			member[v] = renumbered[member[v]]
		}
		level = level.aggregate(renumbered, count)
	}
	return Communities{Partition: g.partition(member), Modularity: g.modularityOf(member)}
}

// communityGraph is one level of the Louvain hierarchy: a weighted
// undirected graph whose nodes are communities of the level below.
type communityGraph struct {
	neighbours [][]int     // ascending, no self
	weights    [][]float64 // parallel to neighbours
	// degree counts edges inside the node twice (as a self-loop would) plus
	// the weights to its neighbours. The inside edges themselves are never
	// needed: a move's gain depends only on degrees and on weights between
	// distinct nodes.
	degree    []float64
	totalEdge float64 // m, the same at every level
}

func newCommunityGraph(g *Graph) *communityGraph {
	n := len(g.ids)
	level := &communityGraph{
		neighbours: g.adj,
		weights:    make([][]float64, n),
		degree:     make([]float64, n),
		totalEdge:  float64(g.undirectedEdgeCount),
	}
	for v := range g.ids {
		level.weights[v] = make([]float64, len(g.adj[v]))
		for i := range level.weights[v] {
			level.weights[v][i] = 1
		}
		level.degree[v] = float64(len(g.adj[v]))
	}
	return level
}

// localMoving runs phase 1 and returns each node's community label (the
// position of the node that started it) and whether any node moved.
func (cg *communityGraph) localMoving() ([]int, bool) {
	n := len(cg.degree)
	community := make([]int, n)
	total := slices.Clone(cg.degree) // total degree per community label
	for v := range community {
		community[v] = v
	}
	if cg.totalEdge == 0 {
		return community, false
	}
	twoM := 2 * cg.totalEdge
	weightTo := make([]float64, n) // scratch: edge weight from v to each label
	var touched []int              // labels with non-zero weightTo, to reset
	movedAny := false
	for moved := true; moved; {
		moved = false
		for v := range community {
			own := community[v]
			total[own] -= cg.degree[v]
			touched = touched[:0]
			for i, w := range cg.neighbours[v] {
				c := community[w]
				if weightTo[c] == 0 {
					touched = append(touched, c)
				}
				weightTo[c] += cg.weights[v][i]
			}
			gain := func(c int) float64 { return weightTo[c] - total[c]*cg.degree[v]/twoM }
			best, bestGain := own, gain(own)
			slices.Sort(touched)
			for _, c := range touched {
				if c != own && gain(c) > bestGain+minGain {
					best, bestGain = c, gain(c)
				}
			}
			for _, c := range touched {
				weightTo[c] = 0
			}
			total[best] += cg.degree[v]
			if best != own {
				community[v] = best
				moved, movedAny = true, true
			}
		}
	}
	return community, movedAny
}

// aggregate builds the next level, where node c is community c of this one
// (labels already numbered 0..count-1 by smallest member).
func (cg *communityGraph) aggregate(labels []int, count int) *communityGraph {
	next := &communityGraph{
		neighbours: make([][]int, count),
		weights:    make([][]float64, count),
		degree:     make([]float64, count),
		totalEdge:  cg.totalEdge,
	}
	between := make([]map[int]float64, count)
	for c := range between {
		between[c] = make(map[int]float64)
	}
	for v, c := range labels {
		next.degree[c] += cg.degree[v]
		for i, w := range cg.neighbours[v] {
			if d := labels[w]; d != c {
				between[c][d] += cg.weights[v][i]
			}
		}
	}
	for c, row := range between {
		for d := range row {
			next.neighbours[c] = append(next.neighbours[c], d)
		}
		slices.Sort(next.neighbours[c])
		for _, d := range next.neighbours[c] {
			next.weights[c] = append(next.weights[c], row[d])
		}
	}
	return next
}
