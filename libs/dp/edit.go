package dp

// Levenshtein returns the edit distance between a and b: the fewest
// single-code-point insertions, deletions and substitutions that turn a into
// b.
//
// Recurrence, over prefixes a[:i] and b[:j]:
//
//	D(i, 0) = i, D(0, j) = j
//	D(i, j) = min(D(i-1, j) + 1, D(i, j-1) + 1, D(i-1, j-1) + [a[i-1] != b[j-1]])
//
// Time O(m·n). Memory O(min(m, n)): only the previous and current rows are
// kept, and the row runs over the shorter string.
//
// Invalid UTF-8 bytes each decode to U+FFFD, so two different invalid bytes
// compare equal.
func Levenshtein(a, b string) int {
	return levenshtein([]rune(a), []rune(b))
}

func levenshtein[T comparable](a, b []T) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			substitute := prev[j-1]
			if a[i-1] != b[j-1] {
				substitute++
			}
			cur[j] = min(substitute, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// LevenshteinWithin reports whether the Levenshtein distance between a and b
// is at most maxDistance. If it is, it returns the exact distance and true;
// otherwise it returns maxDistance+1 and false, often without reading most
// of the input. Search uses it for typo-tolerant matching, where only small
// distances matter.
//
// This is Ukkonen's cut-off: a path through cell (i, j) costs at least
// |i-j|, so only the band |i-j| <= k of the matrix can lead to a distance of
// at most k. Cells outside the band are treated as k+1, values are clamped
// to k+1, and the scan stops as soon as a whole band row exceeds k, because
// row minima never decrease.
//
// Time O(k·min(m, n)) after an O(m+n) rune decode; it returns immediately
// when the lengths differ by more than k. Memory O(n). A negative
// maxDistance is treated as zero.
func LevenshteinWithin(a, b string, maxDistance int) (int, bool) {
	return levenshteinWithin([]rune(a), []rune(b), max(maxDistance, 0))
}

func levenshteinWithin[T comparable](a, b []T, k int) (int, bool) {
	// Rows run over the longer string so the outer loop (and the band) is
	// over the shorter one.
	if len(a) > len(b) {
		a, b = b, a
	}
	tooFar := k + 1
	if len(b)-len(a) > k {
		return tooFar, false
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = min(j, tooFar)
	}
	for i := 1; i <= len(a); i++ {
		lo, hi := max(1, i-k), min(len(b), i+k)
		// The cell just left of the band is either the real D(i, 0) or
		// outside the band.
		if lo == 1 {
			cur[0] = min(i, tooFar)
		} else {
			cur[lo-1] = tooFar
		}
		rowMin := cur[lo-1]
		for j := lo; j <= hi; j++ {
			substitute := prev[j-1]
			if a[i-1] != b[j-1] {
				substitute++
			}
			v := min(substitute, prev[j]+1, cur[j-1]+1, tooFar)
			cur[j] = v
			rowMin = min(rowMin, v)
		}
		// The next row reads prev[hi+1]; it is outside this row's band.
		if hi < len(b) {
			cur[hi+1] = tooFar
		}
		if rowMin > k {
			return tooFar, false
		}
		prev, cur = cur, prev
	}
	if d := prev[len(b)]; d <= k {
		return d, true
	}
	return tooFar, false
}

// DamerauLevenshtein returns the unrestricted Damerau–Levenshtein distance
// between a and b: the fewest insertions, deletions, substitutions and
// transpositions of two adjacent code points that turn a into b, where
// later edits may touch characters that were already transposed.
//
// # Which variant, and why
//
// This is the unrestricted distance (Lowrance and Wagner, 1975), not the
// more common "optimal string alignment" (OSA) distance. OSA forbids editing
// a substring more than once, which makes it violate the triangle
// inequality: OSA("ca", "abc") = 3, yet "ca" -> "ac" -> "abc" costs 2. The
// unrestricted distance is a true metric, so search can index titles in a
// metric tree (a BK-tree) and prune by the triangle inequality without
// losing matches. The price is a full matrix instead of two rows, which is
// irrelevant for titles and query words.
//
// Recurrence (Lowrance–Wagner), with da[c] the last row of a holding c and
// db the last column of b in this row whose code point matched a[i-1]:
//
//	D(i, j) = min(D(i-1, j-1) + [a[i-1] != b[j-1]], D(i-1, j) + 1, D(i, j-1) + 1,
//	              D(k-1, l-1) + (i-k-1) + 1 + (j-l-1))   with k = da[b[j-1]], l = db
//
// The last term transposes a[k-1] and a[i-1] (which equal b[j-1] and b[l-1])
// and deletes or inserts everything in between.
//
// Time O(m·n). Memory O(m·n) plus O(distinct code points of a).
func DamerauLevenshtein(a, b string) int {
	return damerauLevenshtein([]rune(a), []rune(b))
}

func damerauLevenshtein[T comparable](a, b []T) int {
	m, n := len(a), len(b)
	// The matrix has an extra leading row and column holding "infinity" so
	// that the transposition term can read D(k-1, l-1) for k or l of zero.
	width := n + 2
	d := make([]int, (m+2)*width)
	at := func(i, j int) *int { return &d[i*width+j] }
	infinity := m + n
	*at(0, 0) = infinity
	for i := 0; i <= m; i++ {
		*at(i+1, 0) = infinity
		*at(i+1, 1) = i
	}
	for j := 0; j <= n; j++ {
		*at(0, j+1) = infinity
		*at(1, j+1) = j
	}
	lastRow := make(map[T]int)
	for i := 1; i <= m; i++ {
		lastMatchColumn := 0
		for j := 1; j <= n; j++ {
			k := lastRow[b[j-1]]
			l := lastMatchColumn
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
				lastMatchColumn = j
			}
			*at(i+1, j+1) = min(
				*at(i, j)+cost,
				*at(i+1, j)+1,
				*at(i, j+1)+1,
				*at(k, l)+(i-k-1)+1+(j-l-1),
			)
		}
		lastRow[a[i-1]] = i
	}
	return *at(m+1, n+1)
}
