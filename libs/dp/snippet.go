package dp

import (
	"fmt"
	"slices"
)

// Match is one occurrence of a query term in a block's text.
type Match struct {
	// Term identifies which query term matched, for example its index in the
	// query. Matches with the same Term count once.
	Term int
	// Start and End are code point offsets, End exclusive, End >= Start.
	Start, End int
}

// Span is a range of code points, End exclusive.
type Span struct {
	Start, End int
}

// Snippet is the result of BestSnippet.
type Snippet struct {
	// Span is tight: it starts at the start of a covered match and ends at
	// the end of one.
	Span Span
	// Terms is the number of distinct terms among the matches inside Span.
	Terms int
	// Matches are the matches inside Span, sorted by start, then end, then
	// term, for highlighting.
	Matches []Match
}

// BestSnippet chooses the text to show for a search hit. Among spans of at
// most window code points it returns the one whose matches (those lying
// entirely inside it) cover the most distinct terms; then the shortest such
// span; then the earliest. ok is false when no match fits in the window.
// The search service then widens the span to the window around it for
// display.
//
// Preferring distinct terms over raw match count means a snippet showing
// "rust" and "async" beats one showing "rust" five times; preferring the
// shortest span keeps the terms close together, which is what a reader
// scans for.
//
// # Algorithm
//
// An optimal span can be shrunk until it starts at some match's start and
// ends at some match's end. So for each distinct match start s (the left
// edge), scan the matches with start >= s in order of end, adding each
// one's term, until the ends pass s + window. The first end at which the
// running distinct count reaches its maximum gives the shortest span for
// that left edge. A two-pointer sweep over the end-sorted matches skips
// those ending before s.
//
// Time O(m log m + m·k), where m is the number of matches and k the most
// matches whose ends fall within one window. Memory O(m + distinct terms).
// window must be non-negative and every match must have End >= Start >= 0
// (ErrInvalidInput).
func BestSnippet(matches []Match, window int) (Snippet, bool, error) {
	if window < 0 {
		return Snippet{}, false, fmt.Errorf("%w: window %d is negative", ErrInvalidInput, window)
	}
	for i, m := range matches {
		if m.Start < 0 || m.End < m.Start {
			return Snippet{}, false, fmt.Errorf("%w: match %d spans %d..%d", ErrInvalidInput, i, m.Start, m.End)
		}
	}
	byEnd := slices.Clone(matches)
	slices.SortFunc(byEnd, compareMatchByEnd)
	starts := make([]int, len(matches))
	for i, m := range matches {
		starts[i] = m.Start
	}
	slices.Sort(starts)
	starts = slices.Compact(starts)

	best := Span{}
	bestTerms := 0
	firstRelevant := 0
	counts := make(map[int]int)
	for _, s := range starts {
		for firstRelevant < len(byEnd) && byEnd[firstRelevant].End < s {
			firstRelevant++
		}
		clear(counts)
		distinct := 0
		for _, m := range byEnd[firstRelevant:] {
			if m.End-s > window {
				break
			}
			if m.Start < s {
				continue
			}
			counts[m.Term]++
			if counts[m.Term] > 1 {
				continue
			}
			distinct++
			span := Span{s, m.End}
			length, bestLength := span.End-span.Start, best.End-best.Start
			if distinct > bestTerms || (distinct == bestTerms && length < bestLength) {
				best, bestTerms = span, distinct
			}
		}
	}
	if bestTerms == 0 {
		return Snippet{}, false, nil
	}
	var inside []Match
	for _, m := range matches {
		if m.Start >= best.Start && m.End <= best.End {
			inside = append(inside, m)
		}
	}
	slices.SortFunc(inside, compareMatchByStart)
	return Snippet{Span: best, Terms: bestTerms, Matches: inside}, true, nil
}

func compareMatchByEnd(x, y Match) int {
	if x.End != y.End {
		return x.End - y.End
	}
	if x.Start != y.Start {
		return x.Start - y.Start
	}
	return x.Term - y.Term
}

func compareMatchByStart(x, y Match) int {
	if x.Start != y.Start {
		return x.Start - y.Start
	}
	if x.End != y.End {
		return x.End - y.End
	}
	return x.Term - y.Term
}
