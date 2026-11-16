package graph

import (
	"container/heap"
	"slices"
	"strings"
)

// ReadingStep is one note on a reading path.
type ReadingStep struct {
	Note string
	// Layer is the step's dependency level within the path: 0 for notes
	// that need nothing else on the path, otherwise one more than the
	// deepest group of notes it links to. Notes on one cycle share a layer.
	// The target has the largest layer.
	Layer int
	// Hops is the number of links from the target to this note, following
	// links as written; 0 for the target.
	Hops int
	// Via is the note that brings this one into the path: the smallest-ID
	// note one hop closer to the target that links to it. Empty for the
	// target. A UI shows it as "linked from Via".
	Via string
}

// Reading is the result of ReadingPath.
type Reading struct {
	// Steps lists the target's prerequisites, prerequisites first, and ends
	// with the target.
	Steps []ReadingStep
	// Cycles lists each set of steps that cite each other in a loop (a
	// strongly connected component of more than one note), members in
	// ascending order, sets ordered by smallest member. No order can put
	// every note of a cycle after the notes it links to, so cycle members
	// are kept together and read in ID order.
	Cycles [][]string
}

// ReadingPath answers "what should I read before this note, and in what
// order".
//
// The prerequisites of a note are every note it reaches by following links
// as written: a note that links to another relies on it. The steps are
// those notes plus the target, ordered so that every note comes after the
// notes it links to:
//
//  1. Collapse each cycle among the steps into one group (strongly connected
//     components, iterative Tarjan).
//  2. Order the groups by Kahn's algorithm: a group is ready once every
//     group it links to is placed; the ready group with the smallest member
//     ID goes next.
//  3. Emit each group's members in ascending ID order, except that the
//     target's group always comes last (it reaches every other group, so
//     it can only be ready last) with the target as its final member.
//
// A note that links to nothing has a path of one step: itself.
//
// O((V + E) log V) over the notes reached, plus O(V) for per-node scratch
// space.
//
// It is not the shortest path between two notes, and not "notes on the same
// topic": shared topics give neighbours, while a reading order needs the
// direction only links carry.
func (g *Graph) ReadingPath(note string) (Reading, error) {
	target, err := g.lookup(note)
	if err != nil {
		return Reading{}, err
	}
	hops, err := g.bfs(target, Cites, 0)
	if err != nil {
		return Reading{}, err
	}
	group, groupCount := canonicalLabels(g.tarjan([]int{target}))

	members := make([][]int, groupCount)
	for v, c := range group {
		if c != unreached {
			members[c] = append(members[c], v)
		}
	}
	// Condensation: pending[c] counts c's distinct out-links to other groups.
	pending := make([]int, groupCount)
	citedBy := make([][]int, groupCount)
	for c, list := range members {
		var targets []int
		for _, v := range list {
			for _, t := range g.out[v] {
				if group[t] != c {
					targets = append(targets, group[t])
				}
			}
		}
		slices.Sort(targets)
		targets = slices.Compact(targets)
		pending[c] = len(targets)
		for _, d := range targets {
			citedBy[d] = append(citedBy[d], c)
		}
	}

	// Groups are numbered by smallest member, so "smallest group number"
	// is "smallest member ID".
	ready := &minHeap{}
	for c := range members {
		if pending[c] == 0 {
			heap.Push(ready, c)
		}
	}
	layer := make([]int, groupCount)
	result := Reading{Steps: make([]ReadingStep, 0, len(group)), Cycles: [][]string{}}
	for ready.Len() > 0 {
		c := heap.Pop(ready).(int)
		for _, v := range members[c] {
			for _, t := range g.out[v] {
				if group[t] != c {
					layer[c] = max(layer[c], layer[group[t]]+1)
				}
			}
		}
		for _, v := range g.targetLast(members[c], target) {
			result.Steps = append(result.Steps, ReadingStep{
				Note:  g.ids[v],
				Layer: layer[c],
				Hops:  hops[v],
				Via:   g.via(v, hops),
			})
		}
		if len(members[c]) > 1 {
			result.Cycles = append(result.Cycles, g.names(members[c]))
		}
		for _, d := range citedBy[c] {
			pending[d]--
			if pending[d] == 0 {
				heap.Push(ready, d)
			}
		}
	}
	slices.SortFunc(result.Cycles, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return result, nil
}

// targetLast returns members (ascending) with target moved to the end if
// present.
func (g *Graph) targetLast(members []int, target int) []int {
	i := slices.Index(members, target)
	if i < 0 {
		return members
	}
	out := slices.Delete(slices.Clone(members), i, i+1)
	return append(out, target)
}

// via returns the smallest-ID note one hop closer to the target that links
// to v, or "" for the target itself.
func (g *Graph) via(v int, hops []int) string {
	if hops[v] == 0 {
		return ""
	}
	for _, u := range g.in[v] {
		if hops[u] == hops[v]-1 {
			return g.ids[u]
		}
	}
	panic("graph: reached note has no predecessor")
}
