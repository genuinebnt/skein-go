package graphs

import (
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"testing"
)

func testSeed(t *testing.T) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	return rand.Uint64()
}

// recursiveTarjan is the textbook algorithm, the oracle for the iterative one.
func recursiveTarjan(g *Graph) [][]int {
	n := g.N()
	index, low := make([]int, n), make([]int, n)
	on := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var out [][]int
	next := 0
	var visit func(int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range g.Out[v] {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				c = append(c, w)
				if w == v {
					break
				}
			}
			sort.Ints(c)
			out = append(out, c)
		}
	}
	for v := 0; v < n; v++ {
		if index[v] < 0 {
			visit(v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func randomGraph(r *rand.Rand) *Graph {
	n := 1 + r.IntN(40)
	g := New(n)
	for k := r.IntN(n * 3); k > 0; k-- {
		g.AddEdge(r.IntN(n), r.IntN(n))
	}
	g.Normalize()
	return g
}

func TestIterativeSCCMatchesRecursiveTarjan(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	r := rand.New(rand.NewPCG(seed, 7))
	for round := 0; round < 500; round++ {
		g := randomGraph(r)
		got, want := g.SCC(), recursiveTarjan(g)
		if len(got) != len(want) {
			t.Fatalf("round %d: %d components, oracle %d", round, len(got), len(want))
		}
		for i := range got {
			if len(got[i]) != len(want[i]) {
				t.Fatalf("round %d: component %d is %v, oracle %v", round, i, got[i], want[i])
			}
			for j := range got[i] {
				if got[i][j] != want[i][j] {
					t.Fatalf("round %d: component %d is %v, oracle %v", round, i, got[i], want[i])
				}
			}
		}
	}
}

func TestSCCHandlesAHundredThousandNodeChainWithoutRecursion(t *testing.T) {
	g := New(100000)
	for i := 0; i+1 < g.N(); i++ {
		g.AddEdge(i, i+1)
	}
	g.AddEdge(g.N()-1, 0)
	g.Normalize()
	if c := g.SCC(); len(c) != 1 || len(c[0]) != 100000 {
		t.Fatalf("ring of 100000 gave %d components", len(c))
	}
}

func TestShortestPathCountsEqualLengthPaths(t *testing.T) {
	// A diamond: two shortest paths from 0 to 3, ignoring direction.
	g := New(5)
	g.AddEdge(0, 1)
	g.AddEdge(2, 0)
	g.AddEdge(1, 3)
	g.AddEdge(3, 2)
	g.Normalize()
	path, count := g.ShortestPath(0, 3)
	if len(path) != 3 || count != 2 || path[0] != 0 || path[2] != 3 {
		t.Fatalf("path %v count %d, want length 2 with 2 paths", path, count)
	}
	if p, c := g.ShortestPath(0, 4); p != nil || c != 0 {
		t.Fatalf("unreachable node gave path %v", p)
	}
}

func TestRingsFollowDirection(t *testing.T) {
	g := New(4)
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(3, 1)
	g.Normalize()
	if _, rings := g.Rings(0, true, 0); len(rings) != 3 || rings[1] != 1 || rings[2] != 1 {
		t.Fatalf("outbound rings %v", rings)
	}
	if _, rings := g.Rings(1, false, 0); len(rings) != 2 || rings[1] != 2 {
		t.Fatalf("inbound rings %v", rings)
	}
	if _, rings := g.Rings(0, true, 1); len(rings) != 2 {
		t.Fatalf("max hops ignored: %v", rings)
	}
}
