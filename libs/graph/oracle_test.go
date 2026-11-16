package graph

import (
	"math"
	"math/big"
	"slices"
	"sort"
)

// oracle holds a small graph as adjacency matrices built straight from the
// constructor input, and answers every question by the slowest obvious
// method. It shares no code with the package beyond the definition of ID
// order (sorted strings), so agreement means something.
type oracle struct {
	n      int
	ids    []string
	pos    map[string]int
	root   []bool
	arc    [][]bool    // arc[u][v]: u links to v
	linked [][]bool    // undirected
	weight [][]float64 // undirected weight, +Inf if not linked
	reach  [][]bool    // reflexive-transitive closure of arc
}

func newOracle(nodes []Node, edges []Edge) *oracle {
	o := &oracle{n: len(nodes), pos: map[string]int{}}
	for _, nd := range nodes {
		o.ids = append(o.ids, nd.ID)
	}
	sort.Strings(o.ids)
	for i, id := range o.ids {
		o.pos[id] = i
	}
	o.root = make([]bool, o.n)
	for _, nd := range nodes {
		o.root[o.pos[nd.ID]] = nd.Root
	}
	o.arc = boolMatrix(o.n)
	o.linked = boolMatrix(o.n)
	o.weight = make([][]float64, o.n)
	for i := range o.weight {
		o.weight[i] = make([]float64, o.n)
		for j := range o.weight[i] {
			o.weight[i][j] = math.Inf(1)
		}
	}
	for _, e := range edges {
		u, v := o.pos[e.From], o.pos[e.To]
		o.arc[u][v] = true
		o.linked[u][v], o.linked[v][u] = true, true
		w := math.Min(o.weight[u][v], e.Weight)
		o.weight[u][v], o.weight[v][u] = w, w
	}
	// Warshall's transitive closure.
	o.reach = boolMatrix(o.n)
	for i := range o.n {
		o.reach[i][i] = true
		for j := range o.n {
			o.reach[i][j] = o.reach[i][j] || o.arc[i][j]
		}
	}
	for k := range o.n {
		for i := range o.n {
			for j := range o.n {
				o.reach[i][j] = o.reach[i][j] || (o.reach[i][k] && o.reach[k][j])
			}
		}
	}
	return o
}

func boolMatrix(n int) [][]bool {
	m := make([][]bool, n)
	for i := range m {
		m[i] = make([]bool, n)
	}
	return m
}

// step reports whether a traversal in dir may go from u to v.
func (o *oracle) step(u, v int, dir Direction) bool {
	switch dir {
	case Cites:
		return o.arc[u][v]
	case CitedBy:
		return o.arc[v][u]
	default:
		return o.linked[u][v]
	}
}

// hops is matrix breadth-first search: distance[v] is the first round in
// which v is adjacent to the previous round's frontier, -1 if never.
func (o *oracle) hops(source int, dir Direction) []int {
	dist := make([]int, o.n)
	for i := range dist {
		dist[i] = -1
	}
	dist[source] = 0
	for round := 0; ; round++ {
		grew := false
		for v := range o.n {
			if dist[v] != -1 {
				continue
			}
			for u := range o.n {
				if dist[u] == round && o.step(u, v, dir) {
					dist[v] = round + 1
					grew = true
					break
				}
			}
		}
		if !grew {
			return dist
		}
	}
}

// floydWarshall returns all-pairs cheapest undirected costs.
func (o *oracle) floydWarshall() [][]float64 {
	d := make([][]float64, o.n)
	for i := range d {
		d[i] = slices.Clone(o.weight[i])
		d[i][i] = 0
	}
	for k := range o.n {
		for i := range o.n {
			for j := range o.n {
				if d[i][k]+d[k][j] < d[i][j] {
					d[i][j] = d[i][k] + d[k][j]
				}
			}
		}
	}
	return d
}

// fewestHopsAtLeastCost returns, for each node, the fewest hops among the
// cheapest paths from source, given the cheapest costs: a breadth-first
// search over "tight" links, those on which the cost rises by exactly the
// link's weight (within rounding, since Floyd-Warshall adds in a different
// order).
func (o *oracle) fewestHopsAtLeastCost(source int, cost []float64) []int {
	dist := make([]int, o.n)
	for i := range dist {
		dist[i] = -1
	}
	dist[source] = 0
	queue := []int{source}
	for head := 0; head < len(queue); head++ {
		u := queue[head]
		for v := range o.n {
			tight := o.linked[u][v] && math.Abs(cost[u]+o.weight[u][v]-cost[v]) <= 1e-12
			if tight && dist[v] == -1 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

// components labels undirected components by union-find.
func (o *oracle) components() []int {
	parent := make([]int, o.n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for u := range o.n {
		for v := range o.n {
			if o.linked[u][v] {
				parent[find(u)] = find(v)
			}
		}
	}
	labels := make([]int, o.n)
	for i := range labels {
		labels[i] = find(i)
	}
	return labels
}

// kosaraju labels strongly connected components: one DFS for finishing
// order, then a DFS of the transpose in reverse finishing order.
func (o *oracle) kosaraju() []int {
	visited := make([]bool, o.n)
	var finish []int
	var forward func(int)
	forward = func(u int) {
		visited[u] = true
		for v := range o.n {
			if o.arc[u][v] && !visited[v] {
				forward(v)
			}
		}
		finish = append(finish, u)
	}
	for u := range o.n {
		if !visited[u] {
			forward(u)
		}
	}
	labels := make([]int, o.n)
	for i := range labels {
		labels[i] = -1
	}
	var backward func(int, int)
	backward = func(u, c int) {
		labels[u] = c
		for v := range o.n {
			if o.arc[v][u] && labels[v] == -1 {
				backward(v, c)
			}
		}
	}
	c := 0
	for i := len(finish) - 1; i >= 0; i-- {
		if labels[finish[i]] == -1 {
			backward(finish[i], c)
			c++
		}
	}
	return labels
}

// onCycle reports whether u lies on a directed cycle.
func (o *oracle) onCycle(u int) bool {
	for v := range o.n {
		if o.arc[u][v] && o.reach[v][u] {
			return true
		}
	}
	return false
}

// placeable reports whether u reaches no cycle by following links.
func (o *oracle) placeable(u int) bool {
	for v := range o.n {
		if o.reach[u][v] && o.onCycle(v) {
			return false
		}
	}
	return true
}

// smallestTopologicalOrder places, one at a time, the smallest placeable
// node whose every link target is already placed: the definition of the
// lexicographically smallest order, without a heap.
func (o *oracle) smallestTopologicalOrder() []string {
	placed := make([]bool, o.n)
	var order []string
	for {
		next := -1
		for u := range o.n {
			if placed[u] || !o.placeable(u) {
				continue
			}
			ready := true
			for v := range o.n {
				if o.arc[u][v] && !placed[v] {
					ready = false
				}
			}
			if ready {
				next = u
				break
			}
		}
		if next == -1 {
			return order
		}
		placed[next] = true
		order = append(order, o.ids[next])
	}
}

// betweenness counts, for every pair s < t and every other v, the share of
// shortest s-t paths through v as sigma(s,v)*sigma(v,t)/sigma(s,t),
// with path counts from matrix BFS. No dependency accumulation.
func (o *oracle) betweenness() []float64 {
	dist := make([][]int, o.n)
	for s := range o.n {
		dist[s] = o.hops(s, Undirected)
	}
	sigma := make([][]float64, o.n)
	for s := range o.n {
		sigma[s] = make([]float64, o.n)
		sigma[s][s] = 1
		for d := 1; d < o.n; d++ {
			for v := range o.n {
				if dist[s][v] != d {
					continue
				}
				for u := range o.n {
					if o.linked[u][v] && dist[s][u] == d-1 {
						sigma[s][v] += sigma[s][u]
					}
				}
			}
		}
	}
	score := make([]float64, o.n)
	for s := range o.n {
		for t := s + 1; t < o.n; t++ {
			if dist[s][t] <= 0 {
				continue
			}
			for v := range o.n {
				if v == s || v == t || dist[s][v] < 0 || dist[v][t] < 0 {
					continue
				}
				if dist[s][v]+dist[v][t] == dist[s][t] {
					score[v] += sigma[s][v] * sigma[v][t] / sigma[s][t]
				}
			}
		}
	}
	if o.n > 2 {
		pairs := float64((o.n - 1) * (o.n - 2) / 2)
		for v := range score {
			score[v] /= pairs
		}
	} else {
		clear(score)
	}
	return score
}

// pageRank runs power iteration in 256-bit floating point until the L1
// change is below 1e-40, far beyond float64 precision.
func (o *oracle) pageRank(damping float64) []float64 {
	if o.n == 0 {
		return nil
	}
	const prec = 256
	newF := func(x float64) *big.Float { return new(big.Float).SetPrec(prec).SetFloat64(x) }
	n := newF(float64(o.n))
	d := newF(damping)
	oneMinusD := new(big.Float).SetPrec(prec).Sub(newF(1), d)
	outdeg := make([]int, o.n)
	for u := range o.n {
		for v := range o.n {
			if o.arc[u][v] {
				outdeg[u]++
			}
		}
	}
	rank := make([]*big.Float, o.n)
	for i := range rank {
		rank[i] = new(big.Float).SetPrec(prec).Quo(newF(1), n)
	}
	limit := newF(1e-40)
	for iter := 0; iter < 5000; iter++ {
		dangling := newF(0)
		for u := range o.n {
			if outdeg[u] == 0 {
				dangling.Add(dangling, rank[u])
			}
		}
		base := new(big.Float).SetPrec(prec).Quo(oneMinusD, n)
		spread := new(big.Float).SetPrec(prec).Mul(d, dangling)
		spread.Quo(spread, n)
		base.Add(base, spread)
		next := make([]*big.Float, o.n)
		change := newF(0)
		for v := range o.n {
			inflow := newF(0)
			for u := range o.n {
				if o.arc[u][v] {
					share := new(big.Float).SetPrec(prec).Quo(rank[u], newF(float64(outdeg[u])))
					inflow.Add(inflow, share)
				}
			}
			inflow.Mul(inflow, d)
			next[v] = inflow.Add(inflow, base)
			diff := new(big.Float).SetPrec(prec).Sub(next[v], rank[v])
			change.Add(change, diff.Abs(diff))
		}
		rank = next
		if change.Cmp(limit) < 0 {
			break
		}
	}
	out := make([]float64, o.n)
	for i, r := range rank {
		out[i], _ = r.Float64()
	}
	return out
}

// modularity is the textbook double sum over ordered pairs:
// Q = 1/2m * sum_ij (A_ij - k_i k_j / 2m) [c_i == c_j].
func (o *oracle) modularity(labels []int) float64 {
	degree := make([]float64, o.n)
	m := 0.0
	for u := range o.n {
		for v := range o.n {
			if o.linked[u][v] {
				degree[u]++
				m += 0.5
			}
		}
	}
	if m == 0 {
		return 0
	}
	q := 0.0
	for i := range o.n {
		for j := range o.n {
			if labels[i] != labels[j] {
				continue
			}
			a := 0.0
			if o.linked[i][j] {
				a = 1
			}
			q += a - degree[i]*degree[j]/(2*m)
		}
	}
	return q / (2 * m)
}

// bestModularity enumerates every set partition (restricted growth
// strings) and returns the highest Q. Bell(9) = 21147, so keep n <= 9.
func (o *oracle) bestModularity() float64 {
	labels := make([]int, o.n)
	best := math.Inf(-1)
	var assign func(i, used int)
	assign = func(i, used int) {
		if i == o.n {
			best = math.Max(best, o.modularity(labels))
			return
		}
		for c := 0; c <= used; c++ {
			labels[i] = c
			assign(i+1, max(used, c+1))
		}
	}
	assign(0, 0)
	return best
}

// labelsOf converts a Partition to oracle positions.
func (o *oracle) labelsOf(p Partition) []int {
	labels := make([]int, o.n)
	for id, c := range p.Of {
		labels[o.pos[id]] = c
	}
	return labels
}

// samePartition reports whether two label vectors group nodes identically.
func samePartition(a, b []int) bool {
	for i := range a {
		for j := range a {
			if (a[i] == a[j]) != (b[i] == b[j]) {
				return false
			}
		}
	}
	return true
}
