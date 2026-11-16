package graph

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// Hand-checked examples. Most named behaviours also live in the conformance
// fixtures; these cover what a fixture cannot express well (seeded planted
// structure, analytic values derived in the test).

func TestFindCycleIgnoresADiamond(t *testing.T) {
	g := build(t, "a>b", "a>c", "b>d", "c>d")
	if c := g.FindCycle(); len(c) != 0 {
		t.Fatalf("diamond reported cycle %v", c)
	}
}

func TestFindCycleStartsAtTheTargetOfTheFirstBackEdge(t *testing.T) {
	// The search goes a, b, c and tries c's links in ID order: c -> b is a
	// back edge before c -> z is followed, so the cycle b, c, b is reported
	// and the longer a, b, c, z, a never is.
	g := build(t, "a>b", "b>c", "c>b", "c>z", "z>a")
	if got, want := g.FindCycle(), []string{"b", "c", "b"}; !slices.Equal(got, want) {
		t.Fatalf("cycle %v, want %v", got, want)
	}
}

func TestBetweennessOfAStarCentreIsOne(t *testing.T) {
	g := build(t, "hub>a", "b>hub", "hub>c", "d>hub")
	scores := g.Betweenness()
	if scores["hub"] != 1 {
		t.Fatalf("hub scores %v, want 1", scores["hub"])
	}
	for _, leaf := range []string{"a", "b", "c", "d"} {
		if scores[leaf] != 0 {
			t.Fatalf("leaf %s scores %v, want 0", leaf, scores[leaf])
		}
	}
}

func TestPageRankSolvesTheDanglingStarExactly(t *testing.T) {
	// Three leaves cite a centre that cites nothing. With d = 0.85, n = 4 and
	// the centre's rank spread over all four notes:
	//   leaf   = (1-d)/4 + d*centre/4
	//   centre = (1-d)/4 + d*(3*leaf + centre/4)
	// and 3*leaf + centre = 1 give centre = 0.8875/1.6375.
	g := build(t, "a>hub", "b>hub", "c>hub")
	opts := DefaultPageRankOptions()
	opts.Tolerance = 1e-15
	got, err := g.PageRank(opts)
	if err != nil {
		t.Fatal(err)
	}
	centre := 0.8875 / 1.6375
	if math.Abs(got.Scores["hub"]-centre) > 1e-12 || math.Abs(got.Scores["a"]-(1-centre)/3) > 1e-12 {
		t.Fatalf("scores %v, want hub %v", got.Scores, centre)
	}
}

func TestPageRankReportsWhenItRunsOutOfIterations(t *testing.T) {
	g := build(t, "a>b", "b>c", "c>a", "c>d")
	got, err := g.PageRank(PageRankOptions{Damping: 0.85, Tolerance: 1e-15, MaxIterations: 3})
	if err != nil || got.Converged || got.Iterations != 3 {
		t.Fatalf("%+v, %v: want 3 iterations without convergence", got, err)
	}
}

func TestLouvainRecoversPlantedCommunities(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	const groups, size = 4, 12
	var nodes []Node
	var edges []Edge
	id := func(group, i int) string { return fmt.Sprintf("g%d-%02d", group, i) }
	for group := range groups {
		for i := range size {
			nodes = append(nodes, Node{ID: id(group, i)})
		}
	}
	for a := range groups * size {
		for b := a + 1; b < groups*size; b++ {
			same := a/size == b/size
			if (same && r.Float64() < 0.7) || (!same && r.Float64() < 0.02) {
				edges = append(edges, Edge{From: id(a/size, a%size), To: id(b/size, b%size)})
			}
		}
	}
	g := mustGraph(t, nodes, edges)
	got := g.Louvain()
	planted := map[string]int{}
	for _, n := range nodes {
		planted[n.ID] = int(n.ID[1] - '0')
	}
	q, err := g.Modularity(planted)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Partition.Members) != groups || got.Modularity < q-1e-9 {
		t.Fatalf("found %d communities with Q %v; planted %d with Q %v", len(got.Partition.Members), got.Modularity, groups, q)
	}
	for _, members := range got.Partition.Members {
		for _, m := range members {
			if m[:2] != members[0][:2] {
				t.Fatalf("community %v mixes planted groups", members)
			}
		}
	}
}

func TestLouvainLeavesIsolatedNotesAlone(t *testing.T) {
	g := build(t, "a>b", "b>c", "c>a", "lonely")
	got := g.Louvain()
	if !slices.Equal(got.Partition.Members[1], []string{"lonely"}) {
		t.Fatalf("communities %v", got.Partition.Members)
	}
}

func TestShortestWeightedPathPrefersFewerHopsWhenCostsTie(t *testing.T) {
	// a-b-c-d costs 0 through zero-weight links, a-d costs 0 directly.
	g := mustGraph(t, []Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, []Edge{
		{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "d"}, {From: "a", To: "d"},
	})
	p, err := g.ShortestWeightedPath("a", "d")
	if err != nil || !slices.Equal(p.Nodes, []string{"a", "d"}) || p.Cost != 0 {
		t.Fatalf("%+v, %v: want the direct zero-cost link", p, err)
	}
}

func TestReadingPathOfANoteWithoutLinksIsItself(t *testing.T) {
	g := build(t, "a>b", "c")
	got, err := g.ReadingPath("c")
	if err != nil || len(got.Steps) != 1 || got.Steps[0] != (ReadingStep{Note: "c"}) || len(got.Cycles) != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
}
