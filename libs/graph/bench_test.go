package graph

import (
	"fmt"
	"testing"
)

// Benchmarks on a 10,000-note graph with about five random links per note,
// close to a large workspace. Record results in docs/PERF.md with the machine
// and Go version; the Rust port runs the same workloads with criterion.

const benchNodes = 10_000

// benchInput builds the workload deterministically (seed 1).
func benchInput() ([]Node, []Edge) {
	r := newRand(1)
	nodes := make([]Node, benchNodes)
	for i := range nodes {
		nodes[i] = Node{ID: fmt.Sprintf("note-%05d", i), Root: i%100 == 0}
	}
	seen := map[[2]int]bool{}
	var edges []Edge
	for from := range benchNodes {
		for range 5 {
			to := r.IntN(benchNodes)
			if to == from || seen[[2]int{from, to}] {
				continue
			}
			seen[[2]int{from, to}] = true
			edges = append(edges, Edge{From: nodes[from].ID, To: nodes[to].ID, Weight: r.Float64() * 2})
		}
	}
	return nodes, edges
}

var benchGraph = func() func(b *testing.B) *Graph {
	var g *Graph
	return func(b *testing.B) *Graph {
		if g == nil {
			nodes, edges := benchInput()
			g = mustGraph(b, nodes, edges)
		}
		b.ResetTimer()
		return g
	}
}()

func BenchmarkNew(b *testing.B) {
	nodes, edges := benchInput()
	b.ResetTimer()
	for range b.N {
		if _, err := New(nodes, edges); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDistances(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		if _, err := g.Distances("note-00000"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlastRadius(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		if _, err := g.BlastRadius("note-00000", CitedBy, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConnectedComponents(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.ConnectedComponents()
	}
}

func BenchmarkStronglyConnected(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.StronglyConnected()
	}
}

func BenchmarkFindCycle(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.FindCycle()
	}
}

func BenchmarkTopologicalOrder(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.TopologicalOrder()
	}
}

func BenchmarkShortestWeightedPath(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		if _, err := g.ShortestWeightedPath("note-00000", "note-09999"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBetweenness(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.Betweenness()
	}
}

func BenchmarkPageRank(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		if _, err := g.PageRank(DefaultPageRankOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLouvain(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.Louvain()
	}
}

func BenchmarkReadingPath(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		if _, err := g.ReadingPath("note-00000"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiameter(b *testing.B) {
	g := benchGraph(b)
	for range b.N {
		g.Diameter()
	}
}
