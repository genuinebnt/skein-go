package graph

// Partition assigns every node of a graph to exactly one numbered group.
//
// Every Partition this package returns is numbered the same way: group 0
// holds the smallest node ID, group 1 holds the smallest ID not in group 0,
// and so on. Algorithms discover groups in an order that depends on where
// they start; renumbering by smallest member makes the numbers stable, so a
// UI can colour by them and two runs can be compared.
type Partition struct {
	// Of maps every node ID to its group number.
	Of map[string]int
	// Members[c] lists group c's node IDs in ascending order.
	Members [][]string
}

// Sizes returns the number of nodes in each group, indexed by group number.
func (p Partition) Sizes() []int {
	sizes := make([]int, len(p.Members))
	for c, members := range p.Members {
		sizes[c] = len(members)
	}
	return sizes
}

// canonicalLabels renumbers labels (one per node position, any values, with
// unreached for nodes outside the partition) so groups are numbered by
// smallest member. It returns the new labels and the group count.
func canonicalLabels(labels []int) ([]int, int) {
	renumber := make(map[int]int)
	out := make([]int, len(labels))
	for v, l := range labels {
		if l == unreached {
			out[v] = unreached
			continue
		}
		c, seen := renumber[l]
		if !seen {
			// Positions ascend with ID, so the first node seen with a label
			// is that group's smallest member.
			c = len(renumber)
			renumber[l] = c
		}
		out[v] = c
	}
	return out, len(renumber)
}

// partition builds a Partition from labels (any values; unreached nodes are
// left out).
func (g *Graph) partition(labels []int) Partition {
	canonical, count := canonicalLabels(labels)
	p := Partition{Of: make(map[string]int), Members: make([][]string, count)}
	for v, c := range canonical {
		if c == unreached {
			continue
		}
		p.Of[g.ids[v]] = c
		p.Members[c] = append(p.Members[c], g.ids[v])
	}
	return p
}

// componentLabels flood-fills the undirected view from each unlabelled node
// in ascending order. The seed of each fill is the smallest member of its
// component (any smaller member would have been a seed first), so the labels
// are already numbered by smallest member.
func (g *Graph) componentLabels() ([]int, int) {
	labels := make([]int, len(g.ids))
	for i := range labels {
		labels[i] = unreached
	}
	count := 0
	var queue []int
	for seed := range g.ids {
		if labels[seed] != unreached {
			continue
		}
		labels[seed] = count
		queue = append(queue[:0], seed)
		for head := 0; head < len(queue); head++ {
			for _, w := range g.adj[queue[head]] {
				if labels[w] == unreached {
					labels[w] = count
					queue = append(queue, w)
				}
			}
		}
		count++
	}
	return labels, count
}

// ConnectedComponents partitions the nodes into connected components of the
// undirected view: two notes share a component when a chain of links joins
// them, whichever way the links point.
//
// Breadth-first flood fill, O(V + E).
//
// It is not strongly connected components: A -> B puts A and B in one
// component here but in two strongly connected components.
func (g *Graph) ConnectedComponents() Partition {
	labels, _ := g.componentLabels()
	return g.partition(labels)
}

// OrphanComponents returns, in ascending order, the numbers (as in
// ConnectedComponents) of the components that contain no root node.
//
// An orphan is a whole component nobody can navigate to, not a note with no
// inbound links: two notes that only link to each other each have an inbound
// link, yet neither is reachable from a root. A graph with no roots has
// every component orphaned.
//
// O(V + E).
func (g *Graph) OrphanComponents() []int {
	labels, count := g.componentLabels()
	hasRoot := make([]bool, count)
	for v, c := range labels {
		if g.root[v] {
			hasRoot[c] = true
		}
	}
	orphans := []int{}
	for c, rooted := range hasRoot {
		if !rooted {
			orphans = append(orphans, c)
		}
	}
	return orphans
}

// StronglyConnected partitions the nodes into strongly connected
// components: maximal sets in which every note can reach every other by
// following links as written. A component of more than one note is a set of
// notes citing each other in a loop; a component of one note is the normal
// case and means nothing on its own.
//
// Tarjan's algorithm with an explicit stack, O(V + E). Components are
// numbered by smallest member, like every Partition.
//
// It is not ConnectedComponents (which ignores direction) and it does not
// list cycles: FindCycle gives one cycle as a path.
func (g *Graph) StronglyConnected() Partition {
	starts := make([]int, len(g.ids))
	for i := range starts {
		starts[i] = i
	}
	return g.partition(g.tarjan(starts))
}

// tarjan labels the strongly connected components of every node reachable
// from starts (following out-links), leaving other nodes unreached. Labels
// are in completion order; callers renumber them.
//
// The recursion of the textbook algorithm is replaced by a stack of frames,
// each holding a node and how many of its out-links have been explored, so a
// 100,000-note chain needs no deep call stack (Rust test threads have 2 MiB).
func (g *Graph) tarjan(starts []int) []int {
	n := len(g.ids)
	order := make([]int, n) // discovery index + 1; 0 means not yet visited
	low := make([]int, n)
	onStack := make([]bool, n)
	labels := make([]int, n)
	for i := range labels {
		labels[i] = unreached
	}
	var (
		calls   []searchFrame
		stack   []int
		visited int
		count   int
	)
	visit := func(v int) {
		visited++
		order[v], low[v] = visited, visited
		stack = append(stack, v)
		onStack[v] = true
		calls = append(calls, searchFrame{node: v})
	}
	for _, s := range starts {
		if order[s] != 0 {
			continue
		}
		visit(s)
		for len(calls) > 0 {
			top := &calls[len(calls)-1]
			v := top.node
			if top.next < len(g.out[v]) {
				w := g.out[v][top.next]
				top.next++
				if order[w] == 0 {
					visit(w)
				} else if onStack[w] {
					// w's discovery index, not its low-link: using low here
					// is the classic mistake that merges components which
					// only touch.
					low[v] = min(low[v], order[w])
				}
				continue
			}
			calls = calls[:len(calls)-1]
			if len(calls) > 0 {
				parent := calls[len(calls)-1].node
				low[parent] = min(low[parent], low[v])
			}
			if low[v] == order[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					labels[w] = count
					if w == v {
						break
					}
				}
				count++
			}
		}
	}
	return labels
}
