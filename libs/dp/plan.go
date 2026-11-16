package dp

import (
	"container/heap"
	"fmt"
	"slices"
)

// Note is a candidate for a reading plan.
type Note struct {
	// ID identifies the note. Non-empty and unique among the notes given.
	ID string
	// Minutes is the estimated reading time, 0 to MaxPlanMinutes.
	Minutes int
	// Value is how much reading the note is worth, 0 to MaxNoteValue. It is
	// an integer so that sums and ties are exact in every language; the
	// search service quantises its relevance scores (for example to
	// thousandths) before calling the planner.
	Value int64
	// Prerequisites are the IDs of notes that must be read before this one.
	// Duplicates are ignored.
	Prerequisites []string
}

// Limits on reading plan inputs. They keep every intermediate sum inside
// int64 (value times minutes in the bound) and the exact DP inside a
// predictable amount of memory.
const (
	MaxPlanMinutes = 1 << 20
	MaxNoteValue   = 1 << 40
	// MaxPlanCells caps (notes + 1) × (effective budget + 1) for the tree
	// DP, about 64 MiB of tables.
	MaxPlanCells = 1 << 22
	// DefaultNodeLimit is the branch-and-bound node limit used when
	// PlanOptions.NodeLimit is zero.
	DefaultNodeLimit = 200_000
)

// PlanOptions tunes PlanReading. The zero value is the default.
type PlanOptions struct {
	// NodeLimit caps the search nodes branch and bound may visit before it
	// stops and returns the best plan found so far. Zero means
	// DefaultNodeLimit.
	NodeLimit int
}

// PlanMethod says how a plan was found.
//
// Rust: `enum PlanMethod { TreeDp = 1, BranchAndBound = 2, BestFound = 3 }`.
type PlanMethod uint8

const (
	// MethodTreeDP: the prerequisites form a forest and the exact tree
	// knapsack DP solved the problem. The plan is optimal.
	MethodTreeDP PlanMethod = 1
	// MethodBranchAndBound: general prerequisites, and branch and bound
	// finished within its node limit. The plan is optimal.
	MethodBranchAndBound PlanMethod = 2
	// MethodBestFound: branch and bound hit its node limit. The plan is the
	// best one found, at least as good as the greedy plan the search starts
	// from, and Plan.UpperBound bounds how much better the optimum can be.
	MethodBestFound PlanMethod = 3
)

func (m PlanMethod) String() string {
	switch m {
	case MethodTreeDP:
		return "tree-dp"
	case MethodBranchAndBound:
		return "branch-and-bound"
	case MethodBestFound:
		return "best-found"
	}
	return fmt.Sprintf("PlanMethod(%d)", uint8(m))
}

// PlannedNote is one note of a plan.
type PlannedNote struct {
	ID      string
	Minutes int
	Value   int64
	// RequiredBy lists the chosen notes that name this one as a direct
	// prerequisite, in reading order. Empty when the note was chosen for its
	// own value; the service turns it into PlannedNote.reason.
	RequiredBy []string
}

// Plan is the result of PlanReading.
type Plan struct {
	// Notes in reading order: every note comes after its prerequisites.
	Notes        []PlannedNote
	TotalMinutes int
	TotalValue   int64
	Method       PlanMethod
	// Optimal is true for MethodTreeDP and MethodBranchAndBound.
	Optimal bool
	// UpperBound is a proven upper bound on the optimal total value: equal
	// to TotalValue when Optimal, otherwise the root bound of the search.
	UpperBound int64
	// NodesExplored counts branch-and-bound nodes; zero for the tree DP.
	NodesExplored int
}

// planProblem is a validated problem with notes renumbered by canonical
// rank (see rankNotes), so index order is rank order everywhere below.
type planProblem struct {
	id            []string
	minutes       []int
	value         []int64
	prerequisites [][]int // sorted by rank
	dependents    [][]int // sorted by rank
	budget        int
}

func (p *planProblem) size() int { return len(p.id) }

// isForest reports whether every note has at most one prerequisite, which
// makes each prerequisite tree a rooted tree and the tree DP applicable.
func (p *planProblem) isForest() bool {
	for _, pre := range p.prerequisites {
		if len(pre) > 1 {
			return false
		}
	}
	return true
}

// newPlanProblem validates notes and builds the ranked problem.
func newPlanProblem(notes []Note, budget int) (*planProblem, error) {
	if budget < 0 || budget > MaxPlanMinutes {
		return nil, fmt.Errorf("%w: budget %d minutes is outside 0..%d", ErrInvalidInput, budget, MaxPlanMinutes)
	}
	index := make(map[string]int, len(notes))
	for i, n := range notes {
		switch {
		case n.ID == "":
			return nil, fmt.Errorf("%w: note %d has an empty id", ErrInvalidInput, i)
		case n.Minutes < 0 || n.Minutes > MaxPlanMinutes:
			return nil, fmt.Errorf("%w: note %q takes %d minutes, outside 0..%d", ErrInvalidInput, n.ID, n.Minutes, MaxPlanMinutes)
		case n.Value < 0 || n.Value > MaxNoteValue:
			return nil, fmt.Errorf("%w: note %q has value %d, outside 0..%d", ErrInvalidInput, n.ID, n.Value, int64(MaxNoteValue))
		}
		if _, dup := index[n.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate note id %q", ErrInvalidInput, n.ID)
		}
		index[n.ID] = i
	}

	// byID is the input index of each note, sorted by ID; it fixes every
	// traversal order below independently of input order and map order.
	byID := make([]int, len(notes))
	for i := range byID {
		byID[i] = i
	}
	slices.SortFunc(byID, func(x, y int) int { return compareStrings(notes[x].ID, notes[y].ID) })
	idRank := make([]int, len(notes))
	for r, i := range byID {
		idRank[i] = r
	}

	prerequisites := make([][]int, len(notes))
	dependents := make([][]int, len(notes))
	for i, n := range notes {
		for _, pid := range n.Prerequisites {
			p, ok := index[pid]
			if !ok {
				return nil, fmt.Errorf("%w: note %q requires %q", ErrUnknownPrerequisite, n.ID, pid)
			}
			if !slices.Contains(prerequisites[i], p) {
				prerequisites[i] = append(prerequisites[i], p)
				dependents[p] = append(dependents[p], i)
			}
		}
	}
	byIDOrder := func(x, y int) int { return idRank[x] - idRank[y] }
	for i := range notes {
		slices.SortFunc(prerequisites[i], byIDOrder)
		slices.SortFunc(dependents[i], byIDOrder)
	}

	if cycle := findCycle(byID, dependents); cycle != nil {
		ids := make([]string, len(cycle))
		for k, i := range cycle {
			ids[k] = notes[i].ID
		}
		return nil, &CycleError{Cycle: ids}
	}

	rank := rankNotes(byID, prerequisites, dependents)
	p := &planProblem{
		id:            make([]string, len(notes)),
		minutes:       make([]int, len(notes)),
		value:         make([]int64, len(notes)),
		prerequisites: make([][]int, len(notes)),
		dependents:    make([][]int, len(notes)),
		budget:        budget,
	}
	relabel := func(list []int) []int {
		out := make([]int, len(list))
		for k, i := range list {
			out[k] = rank[i]
		}
		slices.Sort(out)
		return out
	}
	for i, n := range notes {
		r := rank[i]
		p.id[r], p.minutes[r], p.value[r] = n.ID, n.Minutes, n.Value
		p.prerequisites[r] = relabel(prerequisites[i])
		p.dependents[r] = relabel(dependents[i])
	}
	return p, nil
}

// compareStrings orders IDs by bytes, which for UTF-8 is code point order,
// the same as Rust's str ordering.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// findCycle returns one cycle in the dependents graph, or nil. The search is
// a depth-first search from each note in ID order, following dependents in
// ID order, so the cycle reported is deterministic; it is rotated to start
// at its smallest ID (byID position).
func findCycle(byID []int, dependents [][]int) []int {
	const (
		unvisited = iota
		onStack
		done
	)
	state := make([]uint8, len(dependents))
	idPos := make([]int, len(byID))
	for pos, i := range byID {
		idPos[i] = pos
	}
	type frame struct{ node, next int }
	for _, start := range byID {
		if state[start] != unvisited {
			continue
		}
		stack := []frame{{start, 0}}
		state[start] = onStack
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next == len(dependents[top.node]) {
				state[top.node] = done
				stack = stack[:len(stack)-1]
				continue
			}
			child := dependents[top.node][top.next]
			top.next++
			switch state[child] {
			case unvisited:
				state[child] = onStack
				stack = append(stack, frame{child, 0})
			case onStack:
				var cycle []int
				for k := len(stack) - 1; k >= 0; k-- {
					cycle = append(cycle, stack[k].node)
					if stack[k].node == child {
						break
					}
				}
				slices.Reverse(cycle)
				smallest := 0
				for k := range cycle {
					if idPos[cycle[k]] < idPos[cycle[smallest]] {
						smallest = k
					}
				}
				return append(cycle[smallest:], cycle[:smallest]...)
			}
		}
	}
	return nil
}

// rankNotes returns each note's canonical rank: its position in a
// depth-first preorder that starts from the notes without prerequisites in
// ID order and visits dependents in ID order, each note once, at its first
// visit.
//
// The rank breaks ties between equally good plans (see PlanReading). For a
// forest it is exactly the preorder of the prerequisite trees, so each
// tree's subtree is a contiguous range of ranks, which the tree DP relies
// on.
func rankNotes(byID []int, prerequisites, dependents [][]int) []int {
	rank := make([]int, len(byID))
	visited := make([]bool, len(byID))
	next := 0
	type frame struct{ node, child int }
	for _, root := range byID {
		if len(prerequisites[root]) > 0 || visited[root] {
			continue
		}
		visited[root] = true
		rank[root] = next
		next++
		stack := []frame{{root, 0}}
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.child == len(dependents[top.node]) {
				stack = stack[:len(stack)-1]
				continue
			}
			child := dependents[top.node][top.child]
			top.child++
			if !visited[child] {
				visited[child] = true
				rank[child] = next
				next++
				stack = append(stack, frame{child, 0})
			}
		}
	}
	return rank
}

// readingOrder sorts chosen notes topologically, reading the ready note of
// smallest rank first (Kahn's algorithm with a min-heap). For a forest that
// is the tree preorder: each prerequisite is followed straight away by what
// builds on it.
func (p *planProblem) readingOrder(chosen []bool) []int {
	waiting := make([]int, p.size())
	ready := &intHeap{}
	for i := range p.id {
		if !chosen[i] {
			continue
		}
		for _, pre := range p.prerequisites[i] {
			if chosen[pre] {
				waiting[i]++
			}
		}
		if waiting[i] == 0 {
			heap.Push(ready, i)
		}
	}
	var order []int
	for ready.Len() > 0 {
		i := heap.Pop(ready).(int)
		order = append(order, i)
		for _, d := range p.dependents[i] {
			if chosen[d] {
				waiting[d]--
				if waiting[d] == 0 {
					heap.Push(ready, d)
				}
			}
		}
	}
	return order
}

// plan turns a chosen set into a Plan with notes in reading order.
func (p *planProblem) plan(chosen []bool, method PlanMethod, upperBound int64, nodes int) Plan {
	order := p.readingOrder(chosen)
	position := make([]int, p.size())
	for k, i := range order {
		position[i] = k
	}
	out := Plan{Method: method, Optimal: method != MethodBestFound, NodesExplored: nodes}
	out.Notes = make([]PlannedNote, len(order))
	for k, i := range order {
		out.Notes[k] = PlannedNote{ID: p.id[i], Minutes: p.minutes[i], Value: p.value[i]}
		out.TotalMinutes += p.minutes[i]
		out.TotalValue += p.value[i]
	}
	for _, i := range order {
		for _, pre := range p.prerequisites[i] {
			planned := &out.Notes[position[pre]]
			planned.RequiredBy = append(planned.RequiredBy, p.id[i])
		}
	}
	// RequiredBy was filled in reading order of the dependents already,
	// because order is walked front to back.
	out.UpperBound = max(upperBound, out.TotalValue)
	if out.Optimal {
		out.UpperBound = out.TotalValue
	}
	return out
}

type intHeap []int

func (h intHeap) Len() int           { return len(h) }
func (h intHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h intHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *intHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *intHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// CheckPlan verifies that plan is a feasible reading plan for notes and
// budget: every planned note exists, appears once, matches the input's
// minutes and value, comes after all its prerequisites (which are all in
// the plan), has the right RequiredBy list, and the totals add up and fit
// the budget. It also checks that Method, Optimal and UpperBound agree. It
// cannot check optimality; the tests compare against exhaustive search for
// that. Errors wrap ErrInvariant.
func CheckPlan(notes []Note, budget int, plan Plan) error {
	byID := make(map[string]*Note, len(notes))
	for i := range notes {
		byID[notes[i].ID] = &notes[i]
	}
	position := make(map[string]int, len(plan.Notes))
	minutes, value := 0, int64(0)
	for k, pn := range plan.Notes {
		n, ok := byID[pn.ID]
		switch {
		case !ok:
			return fmt.Errorf("%w: planned note %q is not an input note", ErrInvariant, pn.ID)
		case pn.Minutes != n.Minutes || pn.Value != n.Value:
			return fmt.Errorf("%w: planned note %q has minutes %d and value %d, input has %d and %d", ErrInvariant, pn.ID, pn.Minutes, pn.Value, n.Minutes, n.Value)
		}
		if _, dup := position[pn.ID]; dup {
			return fmt.Errorf("%w: note %q is planned twice", ErrInvariant, pn.ID)
		}
		for _, pre := range n.Prerequisites {
			if _, before := position[pre]; !before {
				return fmt.Errorf("%w: note %q is planned before its prerequisite %q", ErrInvariant, pn.ID, pre)
			}
		}
		position[pn.ID] = k
		minutes += n.Minutes
		value += n.Value
	}
	for _, pn := range plan.Notes {
		var want []string
		for _, other := range plan.Notes {
			if slices.Contains(byID[other.ID].Prerequisites, pn.ID) {
				want = append(want, other.ID)
			}
		}
		if !slices.Equal(pn.RequiredBy, want) {
			return fmt.Errorf("%w: note %q is required by %v, plan says %v", ErrInvariant, pn.ID, want, pn.RequiredBy)
		}
	}
	switch {
	case minutes != plan.TotalMinutes || value != plan.TotalValue:
		return fmt.Errorf("%w: totals are %d minutes and value %d, plan says %d and %d", ErrInvariant, minutes, value, plan.TotalMinutes, plan.TotalValue)
	case minutes > budget:
		return fmt.Errorf("%w: plan takes %d minutes, budget is %d", ErrInvariant, minutes, budget)
	case plan.Method != MethodTreeDP && plan.Method != MethodBranchAndBound && plan.Method != MethodBestFound:
		return fmt.Errorf("%w: method %s", ErrInvariant, plan.Method)
	case plan.Optimal != (plan.Method != MethodBestFound):
		return fmt.Errorf("%w: method %s with optimal=%t", ErrInvariant, plan.Method, plan.Optimal)
	case plan.UpperBound < plan.TotalValue || (plan.Optimal && plan.UpperBound != plan.TotalValue):
		return fmt.Errorf("%w: upper bound %d with total value %d (optimal=%t)", ErrInvariant, plan.UpperBound, plan.TotalValue, plan.Optimal)
	}
	return nil
}
