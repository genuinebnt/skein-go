package dp

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"
)

// oracleRank is an independent, recursive implementation of the canonical
// rank described on PlanReading.
func oracleRank(notes []Note) map[string]int {
	dependents := map[string][]string{}
	var roots []string
	for _, n := range notes {
		if len(n.Prerequisites) == 0 {
			roots = append(roots, n.ID)
		}
		for _, p := range n.Prerequisites {
			if !slices.Contains(dependents[p], n.ID) {
				dependents[p] = append(dependents[p], n.ID)
			}
		}
	}
	sort.Strings(roots)
	rank := map[string]int{}
	var visit func(id string)
	visit = func(id string) {
		if _, seen := rank[id]; seen {
			return
		}
		rank[id] = len(rank)
		children := slices.Clone(dependents[id])
		sort.Strings(children)
		for _, c := range children {
			visit(c)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	return rank
}

// oraclePlan tries every subset of notes and returns the IDs of the best
// feasible one in reading order, by the rules on PlanReading.
func oraclePlan(notes []Note, budget int) (ids []string, value int64) {
	rank := oracleRank(notes)
	byRank := slices.Clone(notes)
	slices.SortFunc(byRank, func(x, y Note) int { return rank[x.ID] - rank[y.ID] })
	pos := map[string]int{}
	for i, n := range byRank {
		pos[n.ID] = i
	}
	bestMask, bestValue, bestMinutes := 0, int64(0), 0
	for mask := 1; mask < 1<<len(byRank); mask++ {
		var value int64
		minutes := 0
		feasible := true
		for i, n := range byRank {
			if mask&(1<<i) == 0 {
				continue
			}
			value += n.Value
			minutes += n.Minutes
			for _, p := range n.Prerequisites {
				if mask&(1<<pos[p]) == 0 {
					feasible = false
				}
			}
		}
		if !feasible || minutes > budget {
			continue
		}
		better := value > bestValue ||
			(value == bestValue && minutes < bestMinutes) ||
			(value == bestValue && minutes == bestMinutes && lowestBitIsIn(mask, bestMask))
		if better {
			bestMask, bestValue, bestMinutes = mask, value, minutes
		}
	}
	// Reading order: repeatedly read the ready note of smallest rank.
	done := map[string]bool{}
	for len(ids) < bitsSet(bestMask) {
		for i, n := range byRank {
			if bestMask&(1<<i) == 0 || done[n.ID] {
				continue
			}
			ready := true
			for _, p := range n.Prerequisites {
				ready = ready && done[p]
			}
			if ready {
				done[n.ID] = true
				ids = append(ids, n.ID)
				break
			}
		}
	}
	return ids, bestValue
}

// lowestBitIsIn reports whether the lowest bit where a and b differ is set
// in a: rule 3 with bit i standing for the note of rank i.
func lowestBitIsIn(a, b int) bool {
	diff := a ^ b
	return diff != 0 && a&(diff&-diff) != 0
}

func bitsSet(x int) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// randomNotes builds a random acyclic problem. In forest mode each note has
// at most one prerequisite. IDs are shuffled so that ID order, input order
// and topological order all differ. Small values and minutes make ties
// common, which exercises the tie-breaking rules.
func randomNotes(r *rand.Rand, n int, forest bool) []Note {
	ids := make([]string, n)
	for i, p := range r.Perm(n) {
		ids[i] = fmt.Sprintf("n%02d", p)
	}
	notes := make([]Note, n)
	for i := range notes {
		notes[i] = Note{ID: ids[i], Minutes: r.IntN(8), Value: int64(r.IntN(6))}
		if i == 0 {
			continue
		}
		count := r.IntN(2)
		if !forest {
			count = r.IntN(4)
		}
		for range count {
			notes[i].Prerequisites = append(notes[i].Prerequisites, ids[r.IntN(i)])
		}
	}
	r.Shuffle(n, func(i, j int) { notes[i], notes[j] = notes[j], notes[i] })
	return notes
}

func planIDs(p Plan) []string {
	ids := make([]string, len(p.Notes))
	for i, n := range p.Notes {
		ids[i] = n.ID
	}
	return ids
}

func mustPlan(t *testing.T, notes []Note, budget int, opts PlanOptions) Plan {
	t.Helper()
	plan, err := PlanReading(notes, budget, opts)
	if err != nil {
		t.Fatalf("PlanReading(%v, %d): %v", notes, budget, err)
	}
	if err := CheckPlan(notes, budget, plan); err != nil {
		t.Fatalf("PlanReading(%v, %d) = %+v: %v", notes, budget, plan, err)
	}
	return plan
}

func TestPlanReadingMatchesExhaustiveSearch(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 400; trial++ {
		forest := trial%2 == 0
		notes := randomNotes(r, 1+r.IntN(15), forest)
		total := 0
		for _, n := range notes {
			total += n.Minutes
		}
		budget := r.IntN(total + 2)
		plan := mustPlan(t, notes, budget, PlanOptions{})
		wantIDs, wantValue := oraclePlan(notes, budget)
		if !plan.Optimal || plan.TotalValue != wantValue || !slices.Equal(planIDs(plan), wantIDs) {
			t.Fatalf("trial %d (forest %t): plan %v value %d via %s, oracle %v value %d\nnotes %+v budget %d",
				trial, forest, planIDs(plan), plan.TotalValue, plan.Method, wantIDs, wantValue, notes, budget)
		}
		// Random forest-mode notes may still have duplicate parents, and
		// DAG-mode notes may happen to form a forest; decide from the data.
		wantMethod := MethodBranchAndBound
		if p, _ := newPlanProblem(notes, budget); p.isForest() {
			wantMethod = MethodTreeDP
		}
		if plan.Method != wantMethod {
			t.Fatalf("method %s, want %s", plan.Method, wantMethod)
		}
	}
}

func TestBranchAndBoundAgreesWithTreeDPOnForests(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 300; trial++ {
		notes := randomNotes(r, 1+r.IntN(25), true)
		budget := r.IntN(60)
		p, err := newPlanProblem(notes, budget)
		if err != nil {
			t.Fatal(err)
		}
		dp, err := p.solveTreeDP()
		if err != nil {
			t.Fatal(err)
		}
		bb := p.solveBranchAndBound(1 << 30)
		if !slices.Equal(planIDs(dp), planIDs(bb)) || dp.TotalValue != bb.TotalValue {
			t.Fatalf("tree DP %v (value %d), branch and bound %v (value %d)\nnotes %+v budget %d",
				planIDs(dp), dp.TotalValue, planIDs(bb), bb.TotalValue, notes, budget)
		}
	}
}

func TestPlanReadingFallsBackToBestFoundAtNodeLimit(t *testing.T) {
	r := newRand(testSeed(t))
	sawFallback := false
	for trial := 0; trial < 300; trial++ {
		notes := randomNotes(r, 6+r.IntN(10), false)
		budget := 5 + r.IntN(25)
		plan := mustPlan(t, notes, budget, PlanOptions{NodeLimit: 1 + r.IntN(20)})
		_, optimum := oraclePlan(notes, budget)
		if plan.Method == MethodTreeDP {
			continue
		}
		p, _ := newPlanProblem(notes, budget)
		greedy := p.greedyPlan()
		switch {
		case plan.TotalValue > optimum:
			t.Fatalf("plan value %d beats the optimum %d", plan.TotalValue, optimum)
		case plan.UpperBound < optimum:
			t.Fatalf("upper bound %d is below the optimum %d", plan.UpperBound, optimum)
		case plan.TotalValue < greedy.value:
			t.Fatalf("plan value %d is worse than greedy %d", plan.TotalValue, greedy.value)
		case plan.Method == MethodBranchAndBound && plan.TotalValue != optimum:
			t.Fatalf("claimed optimal with %d, optimum %d", plan.TotalValue, optimum)
		}
		sawFallback = sawFallback || plan.Method == MethodBestFound
	}
	if !sawFallback {
		t.Fatal("no trial hit the node limit; the test is not exercising the fallback")
	}
}

func TestPlanReadingTakesPrerequisitesForAValuableNote(t *testing.T) {
	notes := []Note{
		{ID: "async", Minutes: 10, Value: 50, Prerequisites: []string{"futures"}},
		{ID: "futures", Minutes: 5, Value: 1, Prerequisites: []string{"traits"}},
		{ID: "traits", Minutes: 5, Value: 1},
		{ID: "macros", Minutes: 15, Value: 30},
	}
	plan := mustPlan(t, notes, 20, PlanOptions{})
	if got := planIDs(plan); !slices.Equal(got, []string{"traits", "futures", "async"}) {
		t.Fatalf("plan = %v", got)
	}
	if plan.TotalMinutes != 20 || plan.TotalValue != 52 || plan.Method != MethodTreeDP {
		t.Fatalf("plan = %+v", plan)
	}
	if !slices.Equal(plan.Notes[1].RequiredBy, []string{"async"}) || len(plan.Notes[2].RequiredBy) != 0 {
		t.Fatalf("RequiredBy = %v / %v", plan.Notes[1].RequiredBy, plan.Notes[2].RequiredBy)
	}
}

func TestPlanReadingReportsCycleStartingAtSmallestID(t *testing.T) {
	notes := []Note{
		{ID: "c", Prerequisites: []string{"b"}},
		{ID: "a", Prerequisites: []string{"c"}},
		{ID: "b", Prerequisites: []string{"a"}},
		{ID: "z"},
	}
	_, err := PlanReading(notes, 10, PlanOptions{})
	var cycle *CycleError
	if !errors.As(err, &cycle) || !errors.Is(err, ErrCyclicPrerequisites) {
		t.Fatalf("err = %v, want a CycleError", err)
	}
	// a is a prerequisite of b, b of c, c of a.
	if !slices.Equal(cycle.Cycle, []string{"a", "b", "c"}) {
		t.Fatalf("cycle = %v, want [a b c]", cycle.Cycle)
	}
	_, err = PlanReading([]Note{{ID: "self", Prerequisites: []string{"self"}}}, 1, PlanOptions{})
	if !errors.As(err, &cycle) || !slices.Equal(cycle.Cycle, []string{"self"}) {
		t.Fatalf("self-prerequisite: err = %v", err)
	}
}

func TestPlanReadingRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		notes  []Note
		budget int
		opts   PlanOptions
		want   error
	}{
		{"negative budget", nil, -1, PlanOptions{}, ErrInvalidInput},
		{"huge budget", nil, MaxPlanMinutes + 1, PlanOptions{}, ErrInvalidInput},
		{"empty id", []Note{{ID: ""}}, 1, PlanOptions{}, ErrInvalidInput},
		{"duplicate id", []Note{{ID: "a"}, {ID: "a"}}, 1, PlanOptions{}, ErrInvalidInput},
		{"negative minutes", []Note{{ID: "a", Minutes: -1}}, 1, PlanOptions{}, ErrInvalidInput},
		{"negative value", []Note{{ID: "a", Value: -1}}, 1, PlanOptions{}, ErrInvalidInput},
		{"negative node limit", nil, 1, PlanOptions{NodeLimit: -1}, ErrInvalidInput},
		{"unknown prerequisite", []Note{{ID: "a", Prerequisites: []string{"b"}}}, 1, PlanOptions{}, ErrUnknownPrerequisite},
		{"too many cells", []Note{{ID: "a", Minutes: MaxPlanMinutes}, {ID: "b", Minutes: MaxPlanMinutes}, {ID: "c", Minutes: MaxPlanMinutes}, {ID: "d", Minutes: MaxPlanMinutes}}, MaxPlanMinutes, PlanOptions{}, ErrProblemTooLarge},
	}
	for _, c := range cases {
		if _, err := PlanReading(c.notes, c.budget, c.opts); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestPlanReadingOfNothingIsEmpty(t *testing.T) {
	plan := mustPlan(t, nil, 30, PlanOptions{})
	if len(plan.Notes) != 0 || !plan.Optimal {
		t.Fatalf("plan = %+v", plan)
	}
	// Zero-value notes are never worth time; zero-minute ones are free.
	plan = mustPlan(t, []Note{{ID: "a", Minutes: 5}, {ID: "b", Value: 3}}, 30, PlanOptions{})
	if got := planIDs(plan); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("plan = %v, want [b]", got)
	}
}

func TestCheckPlanRejectsInfeasiblePlans(t *testing.T) {
	notes := []Note{{ID: "a", Minutes: 5, Value: 1}, {ID: "b", Minutes: 5, Value: 2, Prerequisites: []string{"a"}}}
	good := Plan{
		Notes:        []PlannedNote{{ID: "a", Minutes: 5, Value: 1, RequiredBy: []string{"b"}}, {ID: "b", Minutes: 5, Value: 2}},
		TotalMinutes: 10, TotalValue: 3, Method: MethodTreeDP, Optimal: true, UpperBound: 3,
	}
	if err := CheckPlan(notes, 10, good); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	mutations := map[string]func(p *Plan){
		"wrong order":        func(p *Plan) { p.Notes[0], p.Notes[1] = p.Notes[1], p.Notes[0] },
		"missing prereq":     func(p *Plan) { p.Notes = p.Notes[1:]; p.TotalMinutes, p.TotalValue, p.UpperBound = 5, 2, 2 },
		"wrong total":        func(p *Plan) { p.TotalValue = 4; p.UpperBound = 4 },
		"wrong required by":  func(p *Plan) { p.Notes[0].RequiredBy = nil },
		"optimal best-found": func(p *Plan) { p.Method = MethodBestFound },
		"low upper bound":    func(p *Plan) { p.Method, p.Optimal, p.UpperBound = MethodBestFound, false, 2 },
	}
	for name, mutate := range mutations {
		p := good
		p.Notes = slices.Clone(good.Notes)
		mutate(&p)
		if err := CheckPlan(notes, 10, p); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: err = %v, want ErrInvariant", name, err)
		}
	}
	if err := CheckPlan(notes, 9, good); !errors.Is(err, ErrInvariant) {
		t.Errorf("over budget: err = %v", err)
	}
}
