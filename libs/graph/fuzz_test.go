package graph

import (
	"errors"
	"testing"
)

// FuzzAlgorithmsOnArbitraryGraphs decodes bytes into a small graph (each
// pair of bytes is an edge between up to 16 nodes, the low bit of the first
// sets a weight) and checks that construction either fails with a sentinel
// or yields a graph on which every algorithm agrees with the oracle and the
// validators. Link graphs come from user-typed links, so shapes no
// hand-written test imagines are worth trying.
func FuzzAlgorithmsOnArbitraryGraphs(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01, 0x12, 0x23, 0x30})       // a 4-cycle
	f.Add([]byte{0x01, 0x10, 0x02, 0x20, 0x33}) // a self-loop
	f.Add([]byte{0x01, 0x01})                   // a duplicate edge
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			return
		}
		var nodes []Node
		for i := range 9 {
			nodes = append(nodes, Node{ID: idPool[i], Root: i%3 == 0})
		}
		var edges []Edge
		for _, b := range data {
			from, to := int(b>>4)%9, int(b&0x0f)%9
			edges = append(edges, Edge{From: idPool[from], To: idPool[to], Weight: float64(b%4) / 4})
		}
		g, err := New(nodes, edges)
		if err != nil {
			if !errors.Is(err, ErrSelfLoop) && !errors.Is(err, ErrDuplicateEdge) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if err := g.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		o := newOracle(nodes, edges)
		p := g.StronglyConnected()
		checkCanonical(t, o, p)
		if !samePartition(o.labelsOf(p), o.kosaraju()) {
			t.Fatal("SCCs disagree with Kosaraju")
		}
		if c := g.FindCycle(); len(c) > 0 {
			checkCycle(t, o, c)
		}
		order := g.TopologicalOrder()
		checkTopological(t, o, order)
		scc := o.kosaraju()
		for target := range o.n {
			r, err := g.ReadingPath(o.ids[target])
			if err != nil {
				t.Fatal(err)
			}
			checkReadingPath(t, o, scc, target, r)
		}
		c := g.Louvain()
		checkCanonical(t, o, c.Partition)
	})
}
