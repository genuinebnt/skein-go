package dp

// LCSLength returns the length of a longest common subsequence of a and b.
//
// Recurrence:
//
//	L(i, 0) = L(0, j) = 0
//	L(i, j) = L(i-1, j-1) + 1               if a[i-1] == b[j-1]
//	        = max(L(i-1, j), L(i, j-1))     otherwise
//
// Time O(m·n). Memory O(min(m, n)).
func LCSLength[T comparable](a, b []T) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	return lcsForwardRow(a, b)[len(b)]
}

// LCS returns one longest common subsequence of a and b, using Hirschberg's
// divide and conquer so that memory stays linear.
//
// Hirschberg splits a at mid = len(a)/2, computes the last LCS row of
// a[:mid] against every prefix of b (forward) and of a[mid:] against every
// suffix of b (backward), and splits b at the column j that maximises
// forward[j] + backward[j]. The two halves are solved recursively and
// concatenated.
//
// When several LCSs exist the result is fixed by two rules, which the Rust
// port must follow: b is split at the smallest maximising j, and a single
// remaining element of a matches its first occurrence in b.
//
// Time O(m·n) (about twice the plain DP). Memory O(m + n) besides the
// result, plus O(log m) recursion depth.
func LCS[T comparable](a, b []T) []T {
	out := make([]T, 0, min(len(a), len(b)))
	return hirschberg(a, b, out)
}

func hirschberg[T comparable](a, b, out []T) []T {
	switch {
	case len(a) == 0 || len(b) == 0:
		return out
	case len(a) == 1:
		for _, x := range b {
			if x == a[0] {
				return append(out, x)
			}
		}
		return out
	}
	mid := len(a) / 2
	forward := lcsForwardRow(a[:mid], b)
	backward := lcsBackwardRow(a[mid:], b)
	split, best := 0, -1
	for j := 0; j <= len(b); j++ {
		if total := forward[j] + backward[j]; total > best {
			split, best = j, total
		}
	}
	out = hirschberg(a[:mid], b[:split], out)
	return hirschberg(a[mid:], b[split:], out)
}

// lcsForwardRow returns row[j] = LCSLength(a, b[:j]) for every j.
func lcsForwardRow[T comparable](a, b []T) []int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := range a {
		for j := 1; j <= len(b); j++ {
			if a[i] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else {
				cur[j] = max(prev[j], cur[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return prev
}

// lcsBackwardRow returns row[j] = LCSLength(a, b[j:]) for every j, by
// running the forward recurrence over both inputs reversed.
func lcsBackwardRow[T comparable](a, b []T) []int {
	n := len(b)
	prev := make([]int, n+1)
	cur := make([]int, n+1)
	for i := len(a) - 1; i >= 0; i-- {
		cur[n] = 0
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				cur[j] = prev[j+1] + 1
			} else {
				cur[j] = max(prev[j], cur[j+1])
			}
		}
		prev, cur = cur, prev
	}
	return prev
}
