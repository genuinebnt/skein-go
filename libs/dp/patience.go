package dp

import "sort"

// PatienceDiff returns a diff from a to b using Bram Cohen's patience diff.
// It is meant for lines: it anchors on lines that occur exactly once in both
// inputs, which are usually meaningful (a function header, a heading) rather
// than noise (a blank line, a closing brace), so moved or rewritten blocks
// read better than with a plain shortest diff.
//
// # Algorithm
//
//  1. Strip the common prefix and suffix.
//  2. Find the elements that occur exactly once in a and exactly once in b.
//  3. Take the longest sequence of those pairs that is increasing in both a
//     and b (patience sorting, O(u log u) for u unique pairs).
//  4. Recurse between consecutive anchors. When a region has no unique common
//     element, diff it with Myers (Diff).
//
// The result is a valid diff in the same canonical form as Diff (CheckHunks
// passes), but it is not always shortest: anchoring on a unique element can
// cost edits that a shortest script would avoid.
//
// Time O((N+M) log(N+M)) per recursion level plus the Myers fallbacks.
// Memory O(N+M).
func PatienceDiff[T comparable](a, b []T) []Hunk[T] {
	var s script[T]
	patience(&s, a, b)
	return s.hunks()
}

func patience[T comparable](s *script[T], a, b []T) {
	prefix := commonPrefix(a, b)
	s.emit(HunkEqual, a[:prefix])
	a, b = a[prefix:], b[prefix:]
	suffix := commonSuffix(a, b)
	tail := a[len(a)-suffix:]
	a, b = a[:len(a)-suffix], b[:len(b)-suffix]

	anchors := uniqueCommonLIS(a, b)
	if len(anchors) == 0 {
		s.diff(a, b)
	} else {
		x, y := 0, 0
		for _, anchor := range anchors {
			patience(s, a[x:anchor.inA], b[y:anchor.inB])
			s.emit(HunkEqual, a[anchor.inA:anchor.inA+1])
			x, y = anchor.inA+1, anchor.inB+1
		}
		patience(s, a[x:], b[y:])
	}
	s.emit(HunkEqual, tail)
}

type anchorPair struct{ inA, inB int }

// uniqueCommonLIS returns the longest chain of elements unique in both a and
// b whose positions increase in both, in order.
func uniqueCommonLIS[T comparable](a, b []T) []anchorPair {
	type occurrence struct{ countA, countB, inA, inB int }
	seen := make(map[T]*occurrence)
	for i, x := range a {
		o := seen[x]
		if o == nil {
			o = &occurrence{}
			seen[x] = o
		}
		o.countA++
		o.inA = i
	}
	for j, x := range b {
		if o := seen[x]; o != nil {
			o.countB++
			o.inB = j
		}
	}
	// Walking a (not the map) keeps the pairs sorted by position in a and
	// independent of map order.
	var pairs []anchorPair
	for _, x := range a {
		if o := seen[x]; o.countA == 1 && o.countB == 1 {
			pairs = append(pairs, anchorPair{o.inA, o.inB})
		}
	}
	return longestIncreasingInB(pairs)
}

// longestIncreasingInB returns the longest subsequence of pairs whose inB
// strictly increases. Pairs arrive sorted by inA. Patience sorting: piles
// hold the smallest possible tail for each chain length; each pair records
// its predecessor for reconstruction. Among equally long chains the one
// found by this procedure is returned, which is deterministic.
func longestIncreasingInB(pairs []anchorPair) []anchorPair {
	if len(pairs) == 0 {
		return nil
	}
	pileTops := make([]int, 0, len(pairs)) // index into pairs of each pile's top
	predecessor := make([]int, len(pairs))
	for i, p := range pairs {
		pile := sort.Search(len(pileTops), func(t int) bool { return pairs[pileTops[t]].inB > p.inB })
		if pile > 0 {
			predecessor[i] = pileTops[pile-1]
		} else {
			predecessor[i] = -1
		}
		if pile == len(pileTops) {
			pileTops = append(pileTops, i)
		} else {
			pileTops[pile] = i
		}
	}
	chain := make([]anchorPair, len(pileTops))
	for i, at := len(chain)-1, pileTops[len(pileTops)-1]; i >= 0; i, at = i-1, predecessor[at] {
		chain[i] = pairs[at]
	}
	return chain
}
