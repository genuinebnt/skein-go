package graph

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
)

func TestNewRejectsInvalidInput(t *testing.T) {
	ab := []Node{{ID: "a"}, {ID: "b"}}
	cases := []struct {
		name  string
		nodes []Node
		edges []Edge
		want  error
	}{
		{"empty id", []Node{{ID: ""}}, nil, ErrEmptyID},
		{"duplicate node", []Node{{ID: "a"}, {ID: "a"}}, nil, ErrDuplicateNode},
		{"unknown source", ab, []Edge{{From: "x", To: "a"}}, ErrUnknownNode},
		{"unknown target", ab, []Edge{{From: "a", To: "x"}}, ErrUnknownNode},
		{"self-loop", ab, []Edge{{From: "a", To: "a"}}, ErrSelfLoop},
		{"duplicate edge", ab, []Edge{{From: "a", To: "b"}, {From: "a", To: "b", Weight: 1}}, ErrDuplicateEdge},
		{"negative weight", ab, []Edge{{From: "a", To: "b", Weight: -0.1}}, ErrInvalidWeight},
		{"NaN weight", ab, []Edge{{From: "a", To: "b", Weight: math.NaN()}}, ErrInvalidWeight},
		{"infinite weight", ab, []Edge{{From: "a", To: "b", Weight: math.Inf(1)}}, ErrInvalidWeight},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.nodes, c.edges); !errors.Is(err, c.want) {
				t.Fatalf("New: %v, want %v", err, c.want)
			}
		})
	}
}

func TestNewAcceptsLinksBothWaysAsTwoEdges(t *testing.T) {
	g := mustGraph(t, []Node{{ID: "a"}, {ID: "b"}}, []Edge{
		{From: "b", To: "a", Weight: 0.75},
		{From: "a", To: "b", Weight: 0.25},
	})
	if g.EdgeCount() != 2 || g.UndirectedEdgeCount() != 1 {
		t.Fatalf("edges %d undirected %d, want 2 and 1", g.EdgeCount(), g.UndirectedEdgeCount())
	}
	want := []Edge{{From: "a", To: "b", Weight: 0.25}, {From: "b", To: "a", Weight: 0.75}}
	if !slices.Equal(g.Edges(), want) {
		t.Fatalf("edges %v, want %v", g.Edges(), want)
	}
	p, err := g.ShortestWeightedPath("b", "a")
	if err != nil || p.Cost != 0.25 {
		t.Fatalf("undirected weight should be the cheaper link: %+v, %v", p, err)
	}
}

func TestEmptyGraphAnswersEverythingWithEmptyResults(t *testing.T) {
	g := mustGraph(t, nil, nil)
	if len(g.ConnectedComponents().Members) != 0 || len(g.OrphanComponents()) != 0 ||
		len(g.FindCycle()) != 0 || len(g.TopologicalOrder().Order) != 0 ||
		len(g.Betweenness()) != 0 || len(g.Louvain().Partition.Members) != 0 ||
		g.Diameter() != (Diameter{}) {
		t.Fatal("empty graph produced a non-empty result")
	}
	ranks, err := g.PageRank(DefaultPageRankOptions())
	if err != nil || len(ranks.Scores) != 0 || !ranks.Converged {
		t.Fatalf("pagerank of empty graph: %+v, %v", ranks, err)
	}
	if q, err := g.Modularity(map[string]int{}); err != nil || q != 0 {
		t.Fatalf("modularity of empty graph: %v, %v", q, err)
	}
}

func TestNeighboursFollowTheRequestedDirection(t *testing.T) {
	g := build(t, "a>b", "c>b", "b>d")
	for dir, want := range map[Direction][]string{
		CitedBy:    {"a", "c"},
		Cites:      {"d"},
		Undirected: {"a", "c", "d"},
	} {
		got, err := g.Neighbours("b", dir)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: %v, %v; want %v", dir, got, err, want)
		}
	}
}

func TestQueriesRejectUnknownNodesAndBadArguments(t *testing.T) {
	g := build(t, "a>b")
	checks := map[string]error{}
	_, checks["distances"] = g.Distances("x")
	_, checks["shortest path from"] = g.ShortestPath("x", "a")
	_, checks["shortest path to"] = g.ShortestPath("a", "x")
	_, checks["weighted path"] = g.ShortestWeightedPath("a", "x")
	_, checks["weighted distances"] = g.WeightedDistances("x")
	_, checks["blast radius"] = g.BlastRadius("x", CitedBy, 0)
	_, checks["reading path"] = g.ReadingPath("x")
	_, checks["neighbours"] = g.Neighbours("x", Cites)
	_, checks["modularity extra node"] = g.Modularity(map[string]int{"a": 0, "b": 0, "x": 1})
	for name, err := range checks {
		if !errors.Is(err, ErrUnknownNode) {
			t.Errorf("%s: %v, want ErrUnknownNode", name, err)
		}
	}
	invalid := map[string]error{}
	_, invalid["negative hops"] = g.BlastRadius("a", Cites, -1)
	_, invalid["unknown direction"] = g.BlastRadius("a", Direction(9), 0)
	_, invalid["neighbours direction"] = g.Neighbours("a", Direction(9))
	_, invalid["modularity missing node"] = g.Modularity(map[string]int{"a": 0})
	for _, opts := range []PageRankOptions{
		{Damping: 1, Tolerance: 1e-9, MaxIterations: 10},
		{Damping: -0.1, Tolerance: 1e-9, MaxIterations: 10},
		{Damping: math.NaN(), Tolerance: 1e-9, MaxIterations: 10},
		{Damping: 0.85, Tolerance: 0, MaxIterations: 10},
		{Damping: 0.85, Tolerance: 1e-9, MaxIterations: 0},
	} {
		_, invalid[fmt.Sprintf("pagerank %+v", opts)] = g.PageRank(opts)
	}
	for name, err := range invalid {
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: %v, want ErrInvalidArgument", name, err)
		}
	}
}

func TestSemanticDistanceClampsRoundingAndRejectsNonsense(t *testing.T) {
	for cosine, want := range map[float64]float64{1: 0, 0: 1, -1: 2, 1 + 1e-12: 0, 0.25: 0.75} {
		got, err := SemanticDistance(cosine)
		if err != nil || got != want {
			t.Errorf("SemanticDistance(%v) = %v, %v; want %v", cosine, got, err, want)
		}
	}
	for _, cosine := range []float64{1.1, -1.1, math.NaN(), math.Inf(1)} {
		if _, err := SemanticDistance(cosine); !errors.Is(err, ErrInvalidWeight) {
			t.Errorf("SemanticDistance(%v): %v, want ErrInvalidWeight", cosine, err)
		}
	}
}

func TestCheckInvariantsDetectsCorruption(t *testing.T) {
	corruptions := map[string]func(g *Graph){
		"unsorted ids":         func(g *Graph) { g.ids[0], g.ids[1] = g.ids[1], g.ids[0] },
		"self in out-list":     func(g *Graph) { g.out[0] = append(g.out[0], 0); g.outWeight[0] = append(g.outWeight[0], 0) },
		"missing in-list":      func(g *Graph) { g.in[1] = nil },
		"wrong undirected":     func(g *Graph) { g.adj[2] = g.adj[2][:0] },
		"wrong weight":         func(g *Graph) { g.adjWeight[0][0] = 9 },
		"negative weight":      func(g *Graph) { g.outWeight[0][0] = -1 },
		"stale edge count":     func(g *Graph) { g.edgeCount++ },
		"stale undirected sum": func(g *Graph) { g.undirectedEdgeCount++ },
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			g := build(t, "a>b", "b>c", "c>a")
			corrupt(g)
			if err := g.CheckInvariants(); !errors.Is(err, ErrInvariant) {
				t.Fatalf("CheckInvariants: %v, want ErrInvariant", err)
			}
		})
	}
}
