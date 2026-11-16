package graph

import (
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// mustGraph builds a graph and checks every invariant.
func mustGraph(t testing.TB, nodes []Node, edges []Edge) *Graph {
	t.Helper()
	g, err := New(nodes, edges)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := g.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return g
}

// build is a terse constructor for hand-written tests: "a>b" is an edge,
// a bare ID is a node, and every endpoint becomes a node.
func build(t testing.TB, spec ...string) *Graph {
	t.Helper()
	nodes, edges := parseSpec(spec)
	return mustGraph(t, nodes, edges)
}

func parseSpec(spec []string) ([]Node, []Edge) {
	seen := map[string]bool{}
	var nodes []Node
	var edges []Edge
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			nodes = append(nodes, Node{ID: id})
		}
	}
	for _, s := range spec {
		from, to, isEdge := strings.Cut(s, ">")
		add(from)
		if isEdge {
			add(to)
			edges = append(edges, Edge{From: from, To: to})
		}
	}
	return nodes, edges
}

// testSeed returns the seed for randomised tests. Set SKEIN_SEED to replay a
// failure; every randomised test logs the seed it used.
func testSeed(t testing.TB) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		return seed
	}
	return rand.Uint64()
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

// idPool mixes lengths and digits so byte order differs from numeric order
// ("n10" < "n9") and from insertion order.
var idPool = []string{
	"a", "b", "c", "d", "e", "f", "g", "h", "n1", "n10", "n2", "n9",
	"x", "xa", "xb", "y", "z", "Z", "é", "漢",
}

// randomInput returns a random graph as constructor input: up to maxNodes
// nodes with shuffled IDs, each possible edge present with a random density,
// weights drawn from a small set (so equal-cost ties are common) or
// uniformly, and random roots. The slices come out in random order.
func randomInput(r *rand.Rand, maxNodes int) ([]Node, []Edge) {
	n := r.IntN(maxNodes + 1)
	ids := make([]string, len(idPool))
	copy(ids, idPool)
	r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	ids = ids[:min(n, len(ids))]
	nodes := make([]Node, len(ids))
	for i, id := range ids {
		nodes[i] = Node{ID: id, Root: r.IntN(4) == 0}
	}
	density := r.Float64() * 0.5
	weights := []float64{0, 0.25, 0.5, 1}
	var edges []Edge
	for _, from := range ids {
		for _, to := range ids {
			if from == to || r.Float64() >= density {
				continue
			}
			w := weights[r.IntN(len(weights))]
			if r.IntN(3) == 0 {
				w = r.Float64() * 2
			}
			edges = append(edges, Edge{From: from, To: to, Weight: w})
		}
	}
	r.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
	return nodes, edges
}

// forEachRandomGraph runs check on count random graphs, logging the seed.
func forEachRandomGraph(t *testing.T, count, maxNodes int, check func(t *testing.T, g *Graph, o *oracle)) {
	t.Helper()
	seed := testSeed(t)
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	r := newRand(seed)
	for i := 0; i < count; i++ {
		nodes, edges := randomInput(r, maxNodes)
		g := mustGraph(t, nodes, edges)
		o := newOracle(nodes, edges)
		check(t, g, o)
		if t.Failed() {
			t.Fatalf("graph %d of seed %d: nodes %v edges %v", i, seed, nodes, edges)
		}
	}
}
