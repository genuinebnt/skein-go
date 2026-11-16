package dp

import (
	"slices"
	"testing"
)

// allAlignmentsCost enumerates every alignment of a and b (each a sequence
// of match/substitute, delete and insert steps, with no memoisation) and
// returns the cheapest. Exponential: the oracle for Levenshtein.
func allAlignmentsCost(a, b []rune) int {
	switch {
	case len(a) == 0:
		return len(b)
	case len(b) == 0:
		return len(a)
	}
	substitute := allAlignmentsCost(a[1:], b[1:])
	if a[0] != b[0] {
		substitute++
	}
	return min(substitute, allAlignmentsCost(a[1:], b)+1, allAlignmentsCost(a, b[1:])+1)
}

// editGraphDistance is the definition of the unrestricted
// Damerau–Levenshtein distance: a breadth-first search over strings where
// each step inserts, deletes or substitutes one code point or swaps two
// adjacent ones. Intermediate strings use the runes of a and b plus one
// spare and are at most one longer than the longer input, which is enough
// for these sizes.
func editGraphDistance(a, b string) int {
	alphabet := map[rune]bool{'#': true}
	for _, r := range a + b {
		alphabet[r] = true
	}
	var letters []rune
	for r := range alphabet {
		letters = append(letters, r)
	}
	maxLen := max(len([]rune(a)), len([]rune(b))) + 1
	dist := map[string]int{a: 0}
	queue := []string{a}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == b {
			return dist[cur]
		}
		rs := []rune(cur)
		var next []string
		for i := 0; i <= len(rs); i++ {
			if len(rs) < maxLen {
				for _, l := range letters {
					next = append(next, string(rs[:i])+string(l)+string(rs[i:]))
				}
			}
			if i < len(rs) {
				next = append(next, string(rs[:i])+string(rs[i+1:]))
				for _, l := range letters {
					next = append(next, string(rs[:i])+string(l)+string(rs[i+1:]))
				}
			}
			if i+1 < len(rs) {
				swapped := append([]rune(nil), rs...)
				swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
				next = append(next, string(swapped))
			}
		}
		for _, s := range next {
			if _, seen := dist[s]; !seen {
				dist[s] = dist[cur] + 1
				queue = append(queue, s)
			}
		}
	}
	panic("unreachable: b is always reachable")
}

func TestLevenshteinMatchesAllAlignmentsOracle(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 400; trial++ {
		a, b := randomString(r, smallAlphabet, 7), randomString(r, smallAlphabet, 7)
		want := allAlignmentsCost([]rune(a), []rune(b))
		if got := Levenshtein(a, b); got != want {
			t.Fatalf("Levenshtein(%q, %q) = %d, oracle %d", a, b, got, want)
		}
	}
}

func TestLevenshteinWithinEqualsFullDistanceOrReportsTooFar(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 3000; trial++ {
		a, b := randomString(r, smallAlphabet, 12), randomString(r, smallAlphabet, 12)
		k := r.IntN(8)
		full := Levenshtein(a, b)
		got, ok := LevenshteinWithin(a, b, k)
		if full <= k && (!ok || got != full) {
			t.Fatalf("LevenshteinWithin(%q, %q, %d) = %d, %t; full distance %d", a, b, k, got, ok, full)
		}
		if full > k && (ok || got != k+1) {
			t.Fatalf("LevenshteinWithin(%q, %q, %d) = %d, %t; full distance %d is past k", a, b, k, got, ok, full)
		}
	}
}

func TestLevenshteinWithinStopsEarlyOnDistantLongStrings(t *testing.T) {
	// The band rule means 10,000-rune inputs that differ in their first
	// three runes are rejected after three rows.
	a := "xyz" + string(make([]rune, 10_000))
	b := "abc" + string(make([]rune, 10_000))
	if d, ok := LevenshteinWithin(a, b, 2); ok || d != 3 {
		t.Fatalf("got %d, %t; want 3, false", d, ok)
	}
	if d, ok := LevenshteinWithin("short", "much longer text", 3); ok || d != 4 {
		t.Fatalf("length gap: got %d, %t; want 4, false", d, ok)
	}
}

func TestDamerauLevenshteinMatchesEditGraphOracle(t *testing.T) {
	r := newRand(testSeed(t))
	alphabet := []rune("ab漢")
	for trial := 0; trial < 150; trial++ {
		a, b := randomString(r, alphabet, 5), randomString(r, alphabet, 5)
		want := editGraphDistance(a, b)
		if got := DamerauLevenshtein(a, b); got != want {
			t.Fatalf("DamerauLevenshtein(%q, %q) = %d, oracle %d", a, b, got, want)
		}
	}
}

func TestDamerauLevenshteinIsUnrestrictedNotOptimalStringAlignment(t *testing.T) {
	// OSA gives 3 here; the unrestricted distance edits the transposed pair
	// again: ca -> ac -> abc.
	if got := DamerauLevenshtein("ca", "abc"); got != 2 {
		t.Fatalf("DamerauLevenshtein(ca, abc) = %d, want 2", got)
	}
	if got := DamerauLevenshtein("teh", "the"); got != 1 {
		t.Fatalf("adjacent swap costs %d, want 1", got)
	}
}

func TestEditDistancesCountCodePointsNotBytes(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"🙂", "😀", 1}, // four bytes each, one code point
		{"café", "cafe", 1},
		{"漢字", "字漢", 2},
	}
	for _, c := range cases {
		if got := Levenshtein(c.a, c.b); got != c.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := DamerauLevenshtein("漢字", "字漢"); got != 1 {
		t.Errorf("DamerauLevenshtein swaps code points: got %d, want 1", got)
	}
}

func TestEditDistanceProperties(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 2000; trial++ {
		a := randomString(r, smallAlphabet, 10)
		b := randomString(r, smallAlphabet, 10)
		c := randomString(r, smallAlphabet, 10)
		checkEditDistanceProperties(t, a, b, c)
	}
}

// checkEditDistanceProperties asserts metric properties shared by the tests
// and the fuzz target.
func checkEditDistanceProperties(t *testing.T, a, b, c string) {
	t.Helper()
	ab, ba := Levenshtein(a, b), Levenshtein(b, a)
	la, lb := len([]rune(a)), len([]rune(b))
	if ab != ba {
		t.Fatalf("Levenshtein not symmetric for %q, %q: %d vs %d", a, b, ab, ba)
	}
	if ab < max(la, lb)-min(la, lb) || ab > max(la, lb) {
		t.Fatalf("Levenshtein(%q, %q) = %d outside length bounds", a, b, ab)
	}
	// Invalid bytes all decode to U+FFFD, so compare decoded runes.
	if (ab == 0) != slices.Equal([]rune(a), []rune(b)) {
		t.Fatalf("Levenshtein(%q, %q) = %d", a, b, ab)
	}
	dab := DamerauLevenshtein(a, b)
	if dab > ab || dab != DamerauLevenshtein(b, a) {
		t.Fatalf("Damerau(%q, %q) = %d with Levenshtein %d (or not symmetric)", a, b, dab, ab)
	}
	if ab+Levenshtein(b, c) < Levenshtein(a, c) {
		t.Fatalf("Levenshtein triangle inequality fails for %q, %q, %q", a, b, c)
	}
	if dab+DamerauLevenshtein(b, c) < DamerauLevenshtein(a, c) {
		t.Fatalf("Damerau triangle inequality fails for %q, %q, %q", a, b, c)
	}
	for k := 0; k <= ab+1; k++ {
		got, ok := LevenshteinWithin(a, b, k)
		if ok != (ab <= k) || (ok && got != ab) || (!ok && got != k+1) {
			t.Fatalf("LevenshteinWithin(%q, %q, %d) = %d, %t; full %d", a, b, k, got, ok, ab)
		}
	}
}
