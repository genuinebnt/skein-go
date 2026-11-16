package graph

import (
	"math"
	"reflect"
	"slices"
	"testing"
)

// Randomised comparisons of every algorithm against the oracle, and result
// validators that check the defining properties directly.

const graphsPerTest = 300

func TestDistancesMatchMatrixBreadthFirstSearch(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 12, func(t *testing.T, g *Graph, o *oracle) {
		for s, id := range o.ids {
			got, err := g.Distances(id)
			if err != nil {
				t.Fatal(err)
			}
			want := o.hops(s, Undirected)
			assertHops(t, o, got, want, "distances from "+id)
		}
	})
}

func assertHops(t *testing.T, o *oracle, got map[string]int, want []int, what string) {
	t.Helper()
	reached := 0
	for v, d := range want {
		if d < 0 {
			continue
		}
		reached++
		if got[o.ids[v]] != d {
			t.Errorf("%s: %s at %d, want %d", what, o.ids[v], got[o.ids[v]], d)
		}
	}
	if len(got) != reached {
		t.Errorf("%s: reached %d nodes, want %d", what, len(got), reached)
	}
}

func TestShortestPathIsShortestAndFollowsTheTieRule(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 12, func(t *testing.T, g *Graph, o *oracle) {
		for s, from := range o.ids {
			dist := o.hops(s, Undirected)
			for tt, to := range o.ids {
				p, err := g.ShortestPath(from, to)
				if err != nil {
					t.Fatal(err)
				}
				if p.Found != (dist[tt] >= 0) {
					t.Fatalf("%s -> %s: found %v, oracle distance %d", from, to, p.Found, dist[tt])
				}
				if !p.Found {
					continue
				}
				if p.Hops != dist[tt] || len(p.Nodes) != dist[tt]+1 || p.Cost != float64(dist[tt]) {
					t.Fatalf("%s -> %s: path %v, want %d hops", from, to, p.Nodes, dist[tt])
				}
				if p.Nodes[0] != from || p.Nodes[p.Hops] != to {
					t.Fatalf("%s -> %s: path %v has wrong endpoints", from, to, p.Nodes)
				}
				// Walking back, each step is the smallest-ID neighbour one
				// hop closer to from.
				for i := p.Hops; i > 0; i-- {
					here := o.pos[p.Nodes[i]]
					want := -1
					for u := range o.n {
						if o.linked[here][u] && dist[u] == dist[here]-1 {
							want = u
							break
						}
					}
					if o.pos[p.Nodes[i-1]] != want {
						t.Fatalf("%s -> %s: path %v breaks the tie rule at %d", from, to, p.Nodes, i)
					}
				}
			}
		}
	})
}

func TestBlastRadiusMatchesExhaustiveReachability(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 12, func(t *testing.T, g *Graph, o *oracle) {
		for s, id := range o.ids {
			for _, dir := range []Direction{CitedBy, Cites, Undirected} {
				full := o.hops(s, dir)
				for _, limit := range []int{0, 1, 2} {
					rings, err := g.BlastRadius(id, dir, limit)
					if err != nil {
						t.Fatal(err)
					}
					want := slices.Clone(full)
					for v, d := range want {
						if limit > 0 && d > limit {
							want[v] = -1
						}
					}
					assertHops(t, o, rings.Hops, want, "blast radius "+dir.String()+" from "+id)
					sizes := []int{}
					for _, d := range want {
						for d >= 0 && len(sizes) <= d {
							sizes = append(sizes, 0)
						}
						if d >= 0 {
							sizes[d]++
						}
					}
					if !slices.Equal(rings.Sizes, sizes) {
						t.Fatalf("ring sizes %v, want %v", rings.Sizes, sizes)
					}
				}
			}
		}
	})
}

// checkCanonical verifies a Partition covers every node once and is numbered
// by smallest member.
func checkCanonical(t *testing.T, o *oracle, p Partition) {
	t.Helper()
	if len(p.Of) != o.n {
		t.Fatalf("partition covers %d of %d nodes", len(p.Of), o.n)
	}
	total := 0
	for c, members := range p.Members {
		if len(members) == 0 || !slices.IsSorted(members) {
			t.Fatalf("group %d members %v are empty or unsorted", c, members)
		}
		if c > 0 && p.Members[c-1][0] >= members[0] {
			t.Fatalf("groups %d and %d are not numbered by smallest member", c-1, c)
		}
		for _, id := range members {
			if p.Of[id] != c {
				t.Fatalf("%s is listed in group %d but mapped to %d", id, c, p.Of[id])
			}
		}
		total += len(members)
	}
	if total != o.n {
		t.Fatalf("members list %d nodes, want %d", total, o.n)
	}
}

func TestConnectedComponentsMatchUnionFind(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		p := g.ConnectedComponents()
		checkCanonical(t, o, p)
		if !samePartition(o.labelsOf(p), o.components()) {
			t.Fatalf("components %v disagree with union-find", p.Members)
		}
		orphans := g.OrphanComponents()
		want := []int{}
		for c, members := range p.Members {
			rooted := false
			for _, id := range members {
				rooted = rooted || o.root[o.pos[id]]
			}
			if !rooted {
				want = append(want, c)
			}
		}
		if !slices.Equal(orphans, want) {
			t.Fatalf("orphans %v, want %v", orphans, want)
		}
	})
}

func TestStronglyConnectedMatchesKosarajuAndIsMaximal(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		p := g.StronglyConnected()
		checkCanonical(t, o, p)
		labels := o.labelsOf(p)
		if !samePartition(labels, o.kosaraju()) {
			t.Fatalf("SCCs %v disagree with Kosaraju", p.Members)
		}
		// Validator, independent of Kosaraju: same component exactly when
		// mutually reachable (strongly connected and maximal).
		for u := range o.n {
			for v := range o.n {
				mutual := o.reach[u][v] && o.reach[v][u]
				if mutual != (labels[u] == labels[v]) {
					t.Fatalf("%s and %s: mutual reachability %v, same SCC %v", o.ids[u], o.ids[v], mutual, !mutual)
				}
			}
		}
	})
}

func TestFindCycleReturnsARealCycleExactlyWhenOneExists(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		cycle := g.FindCycle()
		cyclic := false
		for u := range o.n {
			cyclic = cyclic || o.onCycle(u)
		}
		if !cyclic {
			if len(cycle) != 0 {
				t.Fatalf("acyclic graph gave cycle %v", cycle)
			}
			return
		}
		checkCycle(t, o, cycle)
	})
}

func checkCycle(t *testing.T, o *oracle, cycle []string) {
	t.Helper()
	if len(cycle) < 3 || cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("cycle %v is not closed with at least two nodes", cycle)
	}
	seen := map[string]bool{}
	for i, id := range cycle[:len(cycle)-1] {
		if seen[id] {
			t.Fatalf("cycle %v repeats %s", cycle, id)
		}
		seen[id] = true
		if !o.arc[o.pos[id]][o.pos[cycle[i+1]]] {
			t.Fatalf("cycle %v uses missing edge %s -> %s", cycle, id, cycle[i+1])
		}
	}
}

func TestTopologicalOrderIsTheSmallestValidOrder(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		got := g.TopologicalOrder()
		if want := o.smallestTopologicalOrder(); !slices.Equal(got.Order, want) {
			t.Fatalf("order %v, want %v", got.Order, want)
		}
		checkTopological(t, o, got)
	})
}

// checkTopological validates an Order from its definition alone.
func checkTopological(t *testing.T, o *oracle, got Order) {
	t.Helper()
	position := map[string]int{}
	for i, id := range got.Order {
		position[id] = i
	}
	var unplaced []string
	for u, id := range o.ids {
		if !o.placeable(u) {
			unplaced = append(unplaced, id)
		}
	}
	if !slices.Equal(got.Unplaced, unplaced) && !(len(got.Unplaced) == 0 && len(unplaced) == 0) {
		t.Fatalf("unplaced %v, want %v", got.Unplaced, unplaced)
	}
	if len(got.Order)+len(got.Unplaced) != o.n {
		t.Fatalf("placed %d and unplaced %d of %d", len(got.Order), len(got.Unplaced), o.n)
	}
	layerOf := map[string]int{}
	total := 0
	for l, members := range got.Layers {
		if len(members) == 0 || !slices.IsSorted(members) {
			t.Fatalf("layer %d %v is empty or unsorted", l, members)
		}
		for _, id := range members {
			layerOf[id] = l
		}
		total += len(members)
	}
	if total != len(got.Order) {
		t.Fatalf("layers hold %d notes, order %d", total, len(got.Order))
	}
	for _, id := range got.Order {
		u := o.pos[id]
		want := 0
		for v := range o.n {
			if !o.arc[u][v] {
				continue
			}
			pv, placed := position[o.ids[v]]
			if !placed || pv > position[id] {
				t.Fatalf("%s is placed before %s, which it links to", id, o.ids[v])
			}
			want = max(want, layerOf[o.ids[v]]+1)
		}
		if layerOf[id] != want {
			t.Fatalf("%s in layer %d, want %d", id, layerOf[id], want)
		}
	}
}

func TestWeightedDistancesMatchFloydWarshall(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 12, func(t *testing.T, g *Graph, o *oracle) {
		all := o.floydWarshall()
		for s, from := range o.ids {
			got, err := g.WeightedDistances(from)
			if err != nil {
				t.Fatal(err)
			}
			for v, want := range all[s] {
				c, ok := got[o.ids[v]]
				if ok == math.IsInf(want, 1) || (ok && math.Abs(c-want) > 1e-9) {
					t.Fatalf("%s -> %s: cost %v (reached %v), want %v", from, o.ids[v], c, ok, want)
				}
			}
			for v, to := range o.ids {
				p, err := g.ShortestWeightedPath(from, to)
				if err != nil {
					t.Fatal(err)
				}
				if p.Found == math.IsInf(all[s][v], 1) {
					t.Fatalf("%s -> %s: found %v, oracle cost %v", from, to, p.Found, all[s][v])
				}
				if p.Found {
					checkWeightedPath(t, o, p, from, to, all[s][v])
					if want := o.fewestHopsAtLeastCost(s, all[s])[v]; p.Hops != want {
						t.Fatalf("%s -> %s: path %v has %d hops, a cheapest path has %d", from, to, p.Nodes, p.Hops, want)
					}
				}
			}
		}
	})
}

func checkWeightedPath(t *testing.T, o *oracle, p Path, from, to string, want float64) {
	t.Helper()
	if p.Nodes[0] != from || p.Nodes[len(p.Nodes)-1] != to || p.Hops != len(p.Nodes)-1 {
		t.Fatalf("%s -> %s: malformed path %+v", from, to, p)
	}
	sum := 0.0
	for i := 1; i < len(p.Nodes); i++ {
		u, v := o.pos[p.Nodes[i-1]], o.pos[p.Nodes[i]]
		if !o.linked[u][v] {
			t.Fatalf("%s -> %s: path %v uses a missing link", from, to, p.Nodes)
		}
		sum += o.weight[u][v]
	}
	if math.Abs(sum-p.Cost) > 1e-9 || math.Abs(p.Cost-want) > 1e-9 {
		t.Fatalf("%s -> %s: path %v sums to %v, reports %v, optimum %v", from, to, p.Nodes, sum, p.Cost, want)
	}
}

func TestBetweennessMatchesPairCounting(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 12, func(t *testing.T, g *Graph, o *oracle) {
		got := g.Betweenness()
		want := o.betweenness()
		for v, id := range o.ids {
			if math.Abs(got[id]-want[v]) > 1e-9 {
				t.Fatalf("betweenness of %s is %v, want %v", id, got[id], want[v])
			}
			if got[id] < 0 || got[id] > 1+1e-12 {
				t.Fatalf("betweenness of %s is %v, outside [0, 1]", id, got[id])
			}
		}
	})
}

func TestPageRankMatchesHighPrecisionPowerIteration(t *testing.T) {
	forEachRandomGraph(t, 100, 12, func(t *testing.T, g *Graph, o *oracle) {
		opts := DefaultPageRankOptions()
		opts.Tolerance = 1e-14
		got, err := g.PageRank(opts)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Converged {
			t.Fatalf("did not converge in %d iterations", got.Iterations)
		}
		want := o.pageRank(opts.Damping)
		sum := 0.0
		for v, id := range o.ids {
			sum += got.Scores[id]
			if math.Abs(got.Scores[id]-want[v]) > 1e-12 || got.Scores[id] <= 0 {
				t.Fatalf("pagerank of %s is %v, want %v", id, got.Scores[id], want[v])
			}
		}
		if o.n > 0 && math.Abs(sum-1) > 1e-12 {
			t.Fatalf("ranks sum to %v", sum)
		}
	})
}

func TestModularityMatchesTheDoubleSum(t *testing.T) {
	seed := testSeed(t)
	t.Logf("label seed %d", seed)
	r := newRand(seed)
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		assignment := map[string]int{}
		labels := make([]int, o.n)
		for v, id := range o.ids {
			labels[v] = r.IntN(4) - 1 // negative labels are allowed
			assignment[id] = labels[v]
		}
		got, err := g.Modularity(assignment)
		if err != nil {
			t.Fatal(err)
		}
		if want := o.modularity(labels); math.Abs(got-want) > 1e-12 {
			t.Fatalf("modularity %v, want %v (labels %v)", got, want, labels)
		}
	})
}

func TestLouvainIsConsistentAndNeverBeatsTheOptimum(t *testing.T) {
	forEachRandomGraph(t, 150, 9, func(t *testing.T, g *Graph, o *oracle) {
		c := g.Louvain()
		checkCanonical(t, o, c.Partition)
		labels := o.labelsOf(c.Partition)
		if q := o.modularity(labels); math.Abs(q-c.Modularity) > 1e-12 {
			t.Fatalf("reported modularity %v, partition scores %v", c.Modularity, q)
		}
		if best := o.bestModularity(); c.Modularity > best+1e-12 {
			t.Fatalf("modularity %v beats the optimum %v", c.Modularity, best)
		}
		singletons := make([]int, o.n)
		for v := range singletons {
			singletons[v] = v
		}
		if c.Modularity < o.modularity(singletons)-1e-12 {
			t.Fatalf("modularity %v is worse than singletons", c.Modularity)
		}
	})
}

func TestReadingPathPutsPrerequisitesFirst(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		scc := o.kosaraju()
		for target, id := range o.ids {
			got, err := g.ReadingPath(id)
			if err != nil {
				t.Fatal(err)
			}
			checkReadingPath(t, o, scc, target, got)
		}
	})
}

func checkReadingPath(t *testing.T, o *oracle, scc []int, target int, got Reading) {
	t.Helper()
	position := map[int]int{}
	for i, s := range got.Steps {
		position[o.pos[s.Note]] = i
	}
	hops := o.hops(target, Cites)
	reached := 0
	for v, d := range hops {
		if d < 0 {
			continue
		}
		reached++
		i, ok := position[v]
		if !ok {
			t.Fatalf("prerequisite %s missing from %+v", o.ids[v], got.Steps)
		}
		s := got.Steps[i]
		if s.Hops != d {
			t.Fatalf("%s at %d hops, want %d", s.Note, s.Hops, d)
		}
		if d == 0 && s.Via != "" {
			t.Fatalf("target has via %q", s.Via)
		}
		if d > 0 {
			via := o.pos[s.Via]
			if !o.arc[via][v] || hops[via] != d-1 {
				t.Fatalf("%s via %s, which is not one hop closer and linking", s.Note, s.Via)
			}
			for u := 0; u < via; u++ {
				if o.arc[u][v] && hops[u] == d-1 {
					t.Fatalf("%s via %s, but %s is smaller", s.Note, s.Via, o.ids[u])
				}
			}
		}
		// Every link out of a step leads to an earlier step, unless both are
		// on one cycle; the layer of a step exceeds those of linked groups.
		for w := range o.n {
			if !o.arc[v][w] {
				continue
			}
			if scc[w] == scc[v] {
				if got.Steps[position[w]].Layer != s.Layer {
					t.Fatalf("%s and %s share a cycle but not a layer", s.Note, o.ids[w])
				}
				continue
			}
			if position[w] > i || got.Steps[position[w]].Layer >= s.Layer {
				t.Fatalf("%s comes before or level with %s, which it links to", s.Note, o.ids[w])
			}
		}
	}
	if len(got.Steps) != reached || got.Steps[len(got.Steps)-1].Note != o.ids[target] {
		t.Fatalf("steps %+v: want %d ending at %s", got.Steps, reached, o.ids[target])
	}
	var cycles [][]string
	for c := range o.n {
		var members []string
		for v := range o.n {
			if scc[v] == c && hops[v] >= 0 {
				members = append(members, o.ids[v])
			}
		}
		if len(members) > 1 {
			cycles = append(cycles, members)
		}
	}
	slices.SortFunc(cycles, func(a, b []string) int { return slices.Compare(a, b) })
	if !reflect.DeepEqual(got.Cycles, cycles) && !(len(got.Cycles) == 0 && len(cycles) == 0) {
		t.Fatalf("cycles %v, want %v", got.Cycles, cycles)
	}
}

func TestDiameterMatchesAllPairs(t *testing.T) {
	forEachRandomGraph(t, graphsPerTest, 14, func(t *testing.T, g *Graph, o *oracle) {
		want := Diameter{}
		for s := range o.n {
			dist := o.hops(s, Undirected)
			for v := s + 1; v < o.n; v++ {
				if dist[v] > want.Length {
					want = Diameter{Length: dist[v], From: o.ids[s], To: o.ids[v]}
				}
			}
		}
		if got := g.Diameter(); got != want {
			t.Fatalf("diameter %+v, want %+v", got, want)
		}
	})
}

// allResults runs every parameterless algorithm, plus per-node queries from
// every node, so two graphs can be compared wholesale.
func allResults(t *testing.T, g *Graph) []any {
	t.Helper()
	ranks, err := g.PageRank(DefaultPageRankOptions())
	if err != nil {
		t.Fatal(err)
	}
	out := []any{
		g.Nodes(), g.Edges(), g.Roots(), g.ConnectedComponents(), g.OrphanComponents(),
		g.StronglyConnected(), g.FindCycle(), g.TopologicalOrder(), g.Betweenness(),
		ranks, g.Louvain(), g.Diameter(),
	}
	for _, id := range g.Nodes() {
		d, _ := g.Distances(id)
		w, _ := g.WeightedDistances(id)
		b, _ := g.BlastRadius(id, CitedBy, 0)
		r, _ := g.ReadingPath(id)
		out = append(out, d, w, b, r)
		for _, to := range g.Nodes() {
			p, _ := g.ShortestPath(id, to)
			q, _ := g.ShortestWeightedPath(id, to)
			out = append(out, p, q)
		}
	}
	return out
}

func TestResultsDoNotDependOnInputOrder(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 100; i++ {
		nodes, edges := randomInput(r, 12)
		g1 := mustGraph(t, nodes, edges)
		r.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		r.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
		g2 := mustGraph(t, nodes, edges)
		if !reflect.DeepEqual(allResults(t, g1), allResults(t, g2)) {
			t.Fatalf("seed %d graph %d: results changed when the input was shuffled", seed, i)
		}
	}
}
