package dp

import (
	"slices"
	"testing"
)

// bruteLCSLength tries every subsequence of a. The oracle for LCS.
func bruteLCSLength(a, b []int) int {
	best := 0
	for mask := 0; mask < 1<<len(a); mask++ {
		var sub []int
		for i := range a {
			if mask&(1<<i) != 0 {
				sub = append(sub, a[i])
			}
		}
		if len(sub) > best && isSubsequence(sub, b) {
			best = len(sub)
		}
	}
	return best
}

func isSubsequence[T comparable](sub, of []T) bool {
	i := 0
	for _, x := range of {
		if i < len(sub) && sub[i] == x {
			i++
		}
	}
	return i == len(sub)
}

func TestLCSMatchesBruteForceOracle(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 500; trial++ {
		a, b := randomInts(r, 3, 10), randomInts(r, 3, 10)
		want := bruteLCSLength(a, b)
		if got := LCSLength(a, b); got != want {
			t.Fatalf("LCSLength(%v, %v) = %d, oracle %d", a, b, got, want)
		}
		lcs := LCS(a, b)
		if len(lcs) != want || !isSubsequence(lcs, a) || !isSubsequence(lcs, b) {
			t.Fatalf("LCS(%v, %v) = %v, not a common subsequence of length %d", a, b, lcs, want)
		}
	}
}

func TestLCSOnLongInputsAgreesWithLength(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 30; trial++ {
		a, b := randomInts(r, 5, 400), randomInts(r, 5, 400)
		lcs := LCS(a, b)
		if len(lcs) != LCSLength(a, b) || !isSubsequence(lcs, a) || !isSubsequence(lcs, b) {
			t.Fatalf("trial %d: LCS of length %d, LCSLength %d", trial, len(lcs), LCSLength(a, b))
		}
	}
}

func TestLCSBreaksTiesBySmallestSplit(t *testing.T) {
	// "ab" and "ba" have two LCSs of length 1. a splits into "a" | "b"; the
	// smallest column maximising forward+backward is 0, so the left half
	// gets nothing and "b" matches in "ba".
	got := string(LCS([]rune("ab"), []rune("ba")))
	if got != "b" {
		t.Fatalf("LCS(ab, ba) = %q, want %q", got, "b")
	}
	if got := LCS([]rune("abc"), []rune("")); len(got) != 0 {
		t.Fatalf("LCS with empty input = %q", string(got))
	}
	if got := string(LCS([]rune("🙂x漢"), []rune("漢🙂x"))); got != "🙂x" {
		t.Fatalf("LCS over runes = %q", got)
	}
	if !slices.Equal(LCS([]int{1, 2, 3}, []int{1, 2, 3}), []int{1, 2, 3}) {
		t.Fatal("LCS of equal inputs is not the input")
	}
}
