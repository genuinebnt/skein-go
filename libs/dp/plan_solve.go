package dp

import (
	"fmt"
	"math/bits"
	"slices"
)

// PlanReading picks the notes to read within budget minutes so that their
// total value is as large as possible, where a note can only be picked
// together with all of its prerequisites. This is the precedence-constrained
// knapsack problem, NP-hard in general.
//
// The caller (the search service) passes the notes on the topic plus every
// note they transitively require, whether on the topic or not.
//
// # Which plan wins
//
// Among feasible plans the result is the one with, in order:
//
//  1. the largest total value;
//  2. then the fewest total minutes;
//  3. then, comparing the two plans' notes by canonical rank (see below),
//     the plan containing the lowest-ranked note that only one of them
//     contains.
//
// The canonical rank is a depth-first preorder: start from the notes without
// prerequisites in ID order, visit dependents in ID order, number each note
// at its first visit. Rule 3 prefers foundational notes and earlier trees,
// and it is the rule that lets the tree DP break ties in O(1). The whole
// order is part of the specification and pinned by conformance fixtures.
//
// # Methods
//
// Tree DP (MethodTreeDP), when every note has at most one prerequisite. The
// prerequisite trees, laid out in canonical preorder, put each subtree in a
// contiguous range [i, i+size(i)). Then, with best(i, b) the best plan using
// notes i.. within b minutes:
//
//	best(n, b) = empty
//	best(i, b) = better of  best(i+size(i), b)                       skip i and its subtree
//	                   and  {i} ∪ best(i+1, b-minutes(i))            take i (if it fits)
//
// Taking wins a tie in value and minutes, which is rule 3: i is the
// lowest-ranked note either option could differ on. Time and memory
// O(n·B), where B is the budget capped at the total minutes of all notes;
// problems over MaxPlanCells cells are rejected with ErrProblemTooLarge.
//
// Branch and bound (MethodBranchAndBound), otherwise. Notes are decided in a
// topological order (Kahn's algorithm, smallest rank first); a note can be
// taken only if all its prerequisites were taken and it fits the remaining
// time, otherwise it is excluded, and excluding a note blocks everything
// that needs it. Each node is bounded by the fractional knapsack over the
// undecided, unblocked notes that fit (precedence relaxed), rounded down,
// and pruned when the bound is below the best plan's value, or equal to it
// while already using more minutes. The take branch is explored first. The
// search starts from the better of the empty plan and a greedy plan
// (repeatedly add the note whose not-yet-chosen prerequisite closure has the
// best value per minute and fits). Time O(nodes · (n + edges)), memory
// O(n + edges).
//
// Fallback (MethodBestFound): when the search visits more than
// PlanOptions.NodeLimit nodes it stops and returns the best plan found so
// far, which is never worse than the greedy plan, with Optimal false and
// UpperBound set to the root bound. Callers can show "within X of optimal"
// or retry with a larger limit or a smaller topic.
//
// Errors: ErrInvalidInput, ErrUnknownPrerequisite, *CycleError (matches
// ErrCyclicPrerequisites) and ErrProblemTooLarge.
func PlanReading(notes []Note, budget int, opts PlanOptions) (Plan, error) {
	if opts.NodeLimit < 0 {
		return Plan{}, fmt.Errorf("%w: node limit %d is negative", ErrInvalidInput, opts.NodeLimit)
	}
	p, err := newPlanProblem(notes, budget)
	if err != nil {
		return Plan{}, err
	}
	if p.isForest() {
		return p.solveTreeDP()
	}
	limit := opts.NodeLimit
	if limit == 0 {
		limit = DefaultNodeLimit
	}
	return p.solveBranchAndBound(limit), nil
}

// treeCell is best(i, b): the value and minutes of the best plan, and
// whether it takes note i.
type treeCell struct {
	value   int64
	minutes int
	take    bool
}

func (p *planProblem) solveTreeDP() (Plan, error) {
	n := p.size()
	totalMinutes := 0
	for _, m := range p.minutes {
		totalMinutes += m
	}
	budget := min(p.budget, totalMinutes)
	width := budget + 1
	if cells := (n + 1) * width; cells > MaxPlanCells {
		return Plan{}, fmt.Errorf("%w: %d notes with a %d-minute budget need %d cells, limit %d", ErrProblemTooLarge, n, budget, cells, MaxPlanCells)
	}

	// In preorder every child comes after its parent, so walking backwards
	// adds each finished subtree to its parent.
	subtree := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		subtree[i]++
		if len(p.prerequisites[i]) == 1 {
			subtree[p.prerequisites[i][0]] += subtree[i]
		}
	}

	table := make([]treeCell, (n+1)*width)
	cell := func(i, b int) *treeCell { return &table[i*width+b] }
	for i := n - 1; i >= 0; i-- {
		for b := 0; b <= budget; b++ {
			best := *cell(i+subtree[i], b)
			best.take = false
			if p.minutes[i] <= b {
				rest := cell(i+1, b-p.minutes[i])
				value, minutes := rest.value+p.value[i], rest.minutes+p.minutes[i]
				if value > best.value || (value == best.value && minutes <= best.minutes) {
					best = treeCell{value, minutes, true}
				}
			}
			*cell(i, b) = best
		}
	}

	chosen := make([]bool, n)
	for i, b := 0, budget; i < n; {
		if cell(i, b).take {
			chosen[i] = true
			b -= p.minutes[i]
			i++
		} else {
			i += subtree[i]
		}
	}
	return p.plan(chosen, MethodTreeDP, 0, 0), nil
}

// noteSet is a set of notes by rank, as a bit set, so plans compare by rule
// 3 with one scan.
type noteSet []uint64

func newNoteSet(n int) noteSet { return make(noteSet, (n+63)/64) }

func (s noteSet) add(i int)           { s[i/64] |= 1 << (i % 64) }
func (s noteSet) contains(i int) bool { return s[i/64]&(1<<(i%64)) != 0 }

// hasLowestDifference reports whether s contains the lowest-ranked note in
// which s and other differ.
func (s noteSet) hasLowestDifference(other noteSet) bool {
	for w := range s {
		if diff := s[w] ^ other[w]; diff != 0 {
			return s[w]&(diff&-diff) != 0
		}
	}
	return false
}

// candidate is a complete plan found during the search.
type candidate struct {
	value   int64
	minutes int
	notes   noteSet
}

// beats reports whether c is strictly preferred to other by the order in
// PlanReading.
func (c *candidate) beats(other *candidate) bool {
	if c.value != other.value {
		return c.value > other.value
	}
	if c.minutes != other.minutes {
		return c.minutes < other.minutes
	}
	return c.notes.hasLowestDifference(other.notes)
}

type decision uint8

const (
	undecided decision = iota
	taken
	excluded
)

// branchAndBound holds the search state. Notes are indexed by rank.
type branchAndBound struct {
	p          *planProblem
	order      []int // topological decision order
	byDensity  []int // notes by value per minute, best first, for the bound
	state      []decision
	blocked    []bool // scratch for bound
	value      int64
	minutes    int
	best       candidate
	nodes      int
	nodeLimit  int
	stoppedAll bool
}

func (p *planProblem) solveBranchAndBound(nodeLimit int) Plan {
	n := p.size()
	all := make([]bool, n)
	for i := range all {
		all[i] = true
	}
	s := &branchAndBound{
		p:         p,
		order:     p.readingOrder(all),
		byDensity: p.densityOrder(),
		state:     make([]decision, n),
		blocked:   make([]bool, n),
		best:      candidate{notes: newNoteSet(n)},
		nodeLimit: nodeLimit,
	}
	if greedy := p.greedyPlan(); greedy.beats(&s.best) {
		s.best = greedy
	}
	rootBound := s.bound(0)
	s.search(0)

	chosen := make([]bool, n)
	for i := range chosen {
		chosen[i] = s.best.notes.contains(i)
	}
	method := MethodBranchAndBound
	if s.stoppedAll {
		method = MethodBestFound
	}
	return p.plan(chosen, method, rootBound, s.nodes)
}

func (s *branchAndBound) search(pos int) {
	s.nodes++
	if s.nodes > s.nodeLimit {
		s.stoppedAll = true
		return
	}
	// Notes that cannot be taken here are excluded without branching.
	forced := pos
	for forced < len(s.order) && !s.canTake(s.order[forced]) {
		s.state[s.order[forced]] = excluded
		forced++
	}
	defer func() {
		for k := pos; k < forced; k++ {
			s.state[s.order[k]] = undecided
		}
	}()
	if forced == len(s.order) {
		s.offerCurrent()
		return
	}
	bound := s.bound(forced)
	if bound < s.best.value || (bound == s.best.value && s.minutes > s.best.minutes) {
		return
	}
	next := s.order[forced]
	s.state[next] = taken
	s.value += s.p.value[next]
	s.minutes += s.p.minutes[next]
	s.search(forced + 1)
	s.value -= s.p.value[next]
	s.minutes -= s.p.minutes[next]
	if !s.stoppedAll {
		s.state[next] = excluded
		s.search(forced + 1)
	}
	s.state[next] = undecided
}

func (s *branchAndBound) canTake(i int) bool {
	if s.minutes+s.p.minutes[i] > s.p.budget {
		return false
	}
	for _, pre := range s.p.prerequisites[i] {
		if s.state[pre] != taken {
			return false
		}
	}
	return true
}

func (s *branchAndBound) offerCurrent() {
	c := candidate{value: s.value, minutes: s.minutes, notes: newNoteSet(s.p.size())}
	for i, d := range s.state {
		if d == taken {
			c.notes.add(i)
		}
	}
	if c.beats(&s.best) {
		s.best = c
	}
}

// bound returns the current value plus the fractional-knapsack optimum,
// rounded down, over the notes from order[pos:] that are not blocked by an
// excluded prerequisite and fit the remaining time on their own.
func (s *branchAndBound) bound(pos int) int64 {
	p := s.p
	remaining := p.budget - s.minutes
	for _, i := range s.order[pos:] {
		s.blocked[i] = false
		for _, pre := range p.prerequisites[i] {
			if s.state[pre] == excluded || (s.state[pre] == undecided && s.blocked[pre]) {
				s.blocked[i] = true
				break
			}
		}
	}
	bound := s.value
	for _, i := range s.byDensity {
		if s.state[i] != undecided || s.blocked[i] || p.minutes[i] > p.budget-s.minutes {
			continue
		}
		if p.minutes[i] <= remaining {
			bound += p.value[i]
			remaining -= p.minutes[i]
			continue
		}
		// The fractional item: value·remaining/minutes stays below 2^60 by
		// the input limits. Later items have no more value per minute and
		// zero-minute items are all ahead in the order, so stop here.
		bound += p.value[i] * int64(remaining) / int64(p.minutes[i])
		break
	}
	return bound
}

// densityOrder sorts notes by value per minute, best first: zero-minute
// notes first (they always fit), then by cross-multiplied ratio, ties by
// rank. Any order with these ratios gives the same bound.
func (p *planProblem) densityOrder() []int {
	order := make([]int, p.size())
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(x, y int) int {
		freeX, freeY := p.minutes[x] == 0, p.minutes[y] == 0
		switch {
		case freeX != freeY:
			if freeX {
				return -1
			}
			return 1
		case !freeX:
			left := p.value[x] * int64(p.minutes[y])
			right := p.value[y] * int64(p.minutes[x])
			if left != right {
				if left > right {
					return -1
				}
				return 1
			}
		}
		return x - y
	})
	return order
}

// greedyPlan repeatedly adds the note whose closure (itself plus its
// prerequisites not yet chosen) has the best value per minute and fits the
// remaining time, ties to the lowest rank, until no closure with positive
// value fits. It seeds branch and bound with a reasonable plan, so even a
// truncated search returns something sensible. O(n² + n·edges) per added
// closure.
func (p *planProblem) greedyPlan() candidate {
	n := p.size()
	chosen := make([]bool, n)
	c := candidate{notes: newNoteSet(n)}
	inClosure := make([]bool, n)
	for {
		bestNote := -1
		var bestValue int64
		bestMinutes := 0
		for i := 0; i < n; i++ {
			if chosen[i] {
				continue
			}
			value, minutes, members := p.closure(i, chosen, inClosure)
			for _, m := range members {
				inClosure[m] = false
			}
			if value == 0 || c.minutes+minutes > p.budget {
				continue
			}
			if bestNote < 0 || denser(value, minutes, bestValue, bestMinutes) {
				bestNote, bestValue, bestMinutes = i, value, minutes
			}
		}
		if bestNote < 0 {
			return c
		}
		_, _, members := p.closure(bestNote, chosen, inClosure)
		for _, m := range members {
			inClosure[m] = false
			chosen[m] = true
			c.notes.add(m)
			c.value += p.value[m]
			c.minutes += p.minutes[m]
		}
	}
}

// closure returns the value, minutes and members of note i together with all
// its transitive prerequisites that are not chosen yet. It marks members in
// mark; the caller clears them.
func (p *planProblem) closure(i int, chosen, mark []bool) (int64, int, []int) {
	stack := []int{i}
	mark[i] = true
	var members []int
	var value int64
	minutes := 0
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		members = append(members, x)
		value += p.value[x]
		minutes += p.minutes[x]
		for _, pre := range p.prerequisites[x] {
			if !chosen[pre] && !mark[pre] {
				mark[pre] = true
				stack = append(stack, pre)
			}
		}
	}
	return value, minutes, members
}

// denser reports whether value1/minutes1 > value2/minutes2, treating zero
// minutes as infinite density (and two zero-minute closures as equal). The
// products can exceed 64 bits for closures of many notes, so they are
// compared as 128-bit numbers.
func denser(value1 int64, minutes1 int, value2 int64, minutes2 int) bool {
	hi1, lo1 := bits.Mul64(uint64(value1), uint64(minutes2))
	hi2, lo2 := bits.Mul64(uint64(value2), uint64(minutes1))
	return hi1 > hi2 || (hi1 == hi2 && lo1 > lo2)
}
