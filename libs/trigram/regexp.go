package trigram

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits on the string sets of the regular expression analysis, as in Russ
// Cox's codesearch. Larger sets give more precise queries and slower
// analysis; these values keep any query to a few hundred trigrams.
const (
	// maxExact is the most strings an exact set may hold before it is
	// turned into prefix and suffix sets.
	maxExact = 7
	// maxSet is the most strings a prefix or suffix set may hold before its
	// strings are shortened.
	maxSet = 20
	// maxClass is the largest character class expanded into an exact set;
	// larger classes are treated as "any character".
	maxClass = 100
)

// Regexp is a compiled search pattern: the RE2 expression that confirms
// matches and the trigram query that selects candidates for it.
type Regexp struct {
	re    *regexp.Regexp
	query Query
}

// CompileRegexp compiles an RE2 pattern for searching. Unless
// caseSensitive, the pattern is prefixed with (?i), exactly as the search
// contract and the seed truth define case-insensitive regex search. A
// pattern that does not parse returns ErrInvalidPattern wrapping the
// parser's message.
func CompileRegexp(pattern string, caseSensitive bool) (*Regexp, error) {
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
	}
	return &Regexp{re: re, query: RegexpQuery(parsed)}, nil
}

// Query returns the trigram query selecting the pattern's candidates.
func (r *Regexp) Query() Query { return r.query }

// String returns the pattern as compiled, including any (?i) prefix.
func (r *Regexp) String() string { return r.re.String() }

// RegexpQuery computes the trigram query for a parsed regular expression:
// a boolean formula over trigrams that every text the expression matches
// (anywhere inside it) satisfies. This is Russ Cox's construction from
// "Regular Expression Matching with a Trigram Index" (2012), as in Google
// Code Search and zoekt.
//
// For every subexpression the analysis keeps:
//
//   - emptyable: whether it can match the empty string;
//   - exact: the complete set of strings it matches, when that set is small
//     (nil when unknown or too large);
//   - prefix and suffix: sets that every match starts or ends with;
//   - match: a query every match satisfies.
//
// Literals and small character classes have exact sets; concatenation takes
// cross products, alternation takes unions; repetition keeps only prefixes
// and suffixes. When a set grows past its limit, its trigrams are moved
// into match and its strings are shortened, which loses precision but never
// soundness. The final query ANDs in the trigrams of whatever is left.
//
// Unlike codesearch, which indexes bytes, every string here is a sequence
// of folded code points (Fold): the index stores folded trigrams, so a
// case-sensitive literal and a (?i) literal ask for the same trigrams and
// the final regexp check decides case. This also makes case-insensitive
// literals cost nothing extra, where codesearch expands each letter into a
// character class.
//
// The expression is simplified first (syntax.Regexp.Simplify), which turns
// counted repetitions into concatenations the analysis understands.
func RegexpQuery(re *syntax.Regexp) Query {
	info := analyze(re.Simplify())
	info.simplify(true)
	info.addExact()
	return info.match
}

// regexpInfo is the analysis of one subexpression.
type regexpInfo struct {
	emptyable bool
	exact     stringSet // nil: not known
	prefix    stringSet
	suffix    stringSet
	match     Query
}

func anyMatch() regexpInfo {
	return regexpInfo{emptyable: true, prefix: stringSet{""}, suffix: stringSet{""}, match: allQuery}
}

func anyChar() regexpInfo {
	return regexpInfo{prefix: stringSet{""}, suffix: stringSet{""}, match: allQuery}
}

func noMatch() regexpInfo { return regexpInfo{match: noneQuery} }

func emptyString() regexpInfo {
	return regexpInfo{emptyable: true, exact: stringSet{""}, match: allQuery}
}

func analyze(re *syntax.Regexp) regexpInfo {
	var info regexpInfo
	switch re.Op {
	case syntax.OpNoMatch:
		return noMatch()
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return emptyString()
	case syntax.OpLiteral:
		var b strings.Builder
		for _, r := range re.Rune {
			b.WriteRune(Fold(r))
		}
		info.exact = stringSet{b.String()}
		info.emptyable = len(re.Rune) == 0
		info.match = allQuery
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return anyChar()
	case syntax.OpCapture:
		return analyze(re.Sub[0])
	case syntax.OpConcat:
		info = emptyString()
		for _, sub := range re.Sub {
			info = concat(info, analyze(sub))
		}
		return info
	case syntax.OpAlternate:
		info = noMatch()
		for i, sub := range re.Sub {
			if i == 0 {
				info = analyze(sub)
				continue
			}
			info = alternate(info, analyze(sub))
		}
		return info
	case syntax.OpQuest:
		return alternate(analyze(re.Sub[0]), emptyString())
	case syntax.OpStar:
		return anyMatch()
	case syntax.OpRepeat:
		// Simplify removes counted repetitions; this is a safe fallback.
		if re.Min == 0 {
			return anyMatch()
		}
		info = repeatAtLeastOnce(analyze(re.Sub[0]))
	case syntax.OpPlus:
		info = repeatAtLeastOnce(analyze(re.Sub[0]))
	case syntax.OpCharClass:
		info = classInfo(re.Rune)
		if info.match.Kind == QueryAll && info.exact == nil {
			return info
		}
	default:
		// Unknown operators match anything as far as the index knows.
		return anyMatch()
	}
	info.simplify(false)
	return info
}

// repeatAtLeastOnce analyses x+: a match starts with a match of x and ends
// with one, so x's exact strings become prefixes and suffixes.
func repeatAtLeastOnce(x regexpInfo) regexpInfo {
	if x.exact != nil {
		x.prefix = x.exact.clone()
		x.suffix = x.exact.clone()
		x.exact = nil
	}
	return x
}

// classInfo analyses a character class given as [lo, hi] rune pairs. A
// class of at most maxClass code points becomes an exact set of its folded
// members; a larger one is "any character".
func classInfo(ranges []rune) regexpInfo {
	if len(ranges) == 0 {
		return noMatch()
	}
	var n int64
	for i := 0; i < len(ranges); i += 2 {
		n += int64(ranges[i+1]) - int64(ranges[i]) + 1
	}
	if n > maxClass {
		return anyChar()
	}
	info := regexpInfo{match: allQuery}
	for i := 0; i < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1]; r++ {
			info.exact = append(info.exact, string(Fold(r)))
		}
	}
	info.exact = info.exact.clean(false)
	return info
}

func concat(x, y regexpInfo) regexpInfo {
	var xy regexpInfo
	xy.match = AndQuery(x.match, y.match)
	if x.exact != nil && y.exact != nil {
		xy.exact = x.exact.cross(y.exact, false)
	} else {
		if x.exact != nil {
			xy.prefix = x.exact.cross(y.prefix, false)
		} else {
			xy.prefix = x.prefix
			if x.emptyable {
				xy.prefix = xy.prefix.union(y.prefix, false)
			}
		}
		if y.exact != nil {
			xy.suffix = x.suffix.cross(y.exact, true)
		} else {
			xy.suffix = y.suffix
			if y.emptyable {
				xy.suffix = xy.suffix.union(x.suffix, true)
			}
		}
	}
	// Where x's suffixes meet y's prefixes a match contains one of their
	// concatenations; if every such string is long enough, its trigrams are
	// required and appear in neither side's sets.
	if x.exact == nil && y.exact == nil && len(x.suffix) <= maxSet && len(y.prefix) <= maxSet &&
		x.suffix.minLen()+y.prefix.minLen() >= 3 {
		xy.match = andTrigrams(xy.match, x.suffix.cross(y.prefix, false))
	}
	xy.emptyable = x.emptyable && y.emptyable
	xy.simplify(false)
	return xy
}

func alternate(x, y regexpInfo) regexpInfo {
	var xy regexpInfo
	switch {
	case x.exact != nil && y.exact != nil:
		xy.exact = x.exact.union(y.exact, false)
	case x.exact != nil:
		xy.prefix = x.exact.union(y.prefix, false)
		xy.suffix = x.exact.union(y.suffix, true)
		x.addExact()
	case y.exact != nil:
		xy.prefix = x.prefix.union(y.exact, false)
		xy.suffix = x.suffix.union(y.exact, true)
		y.addExact()
	default:
		xy.prefix = x.prefix.union(y.prefix, false)
		xy.suffix = x.suffix.union(y.suffix, true)
	}
	xy.emptyable = x.emptyable || y.emptyable
	xy.match = OrQuery(x.match, y.match)
	xy.simplify(false)
	return xy
}

// addExact ANDs the trigrams of the exact set into match.
func (info *regexpInfo) addExact() {
	if info.exact != nil {
		info.match = andTrigrams(info.match, info.exact)
	}
}

// simplify keeps the sets small. An exact set that is too large, or whose
// strings are all long enough to have trigrams (always when force), is
// recorded in match and replaced by the prefixes and suffixes of its
// strings; then prefix and suffix sets are shortened.
func (info *regexpInfo) simplify(force bool) {
	info.exact = info.exact.clean(false)
	if info.exact != nil && (len(info.exact) > maxExact || info.exact.minLen() >= 4 || force && info.exact.minLen() >= 3) {
		info.addExact()
		for _, s := range info.exact {
			n := runeCount(s)
			if n < 3 {
				info.prefix = append(info.prefix, s)
				info.suffix = append(info.suffix, s)
			} else {
				info.prefix = append(info.prefix, runePrefix(s, 2))
				info.suffix = append(info.suffix, runeSuffix(s, 2))
			}
		}
		info.exact = nil
	}
	if info.exact == nil {
		info.prefix = info.simplifySet(info.prefix, false)
		info.suffix = info.simplifySet(info.suffix, true)
	}
}

// simplifySet records the set's trigrams in match, then shortens its
// strings to two code points (and further while the set is larger than
// maxSet), and drops strings made redundant by a shorter prefix (suffix).
func (info *regexpInfo) simplifySet(s stringSet, isSuffix bool) stringSet {
	t := s.clean(isSuffix)
	info.match = andTrigrams(info.match, t)
	for n := 3; n == 3 || len(t) > maxSet; n-- {
		for i, str := range t {
			if runeCount(str) >= n {
				if isSuffix {
					t[i] = runeSuffix(str, n-1)
				} else {
					t[i] = runePrefix(str, n-1)
				}
			}
		}
		t = t.clean(isSuffix)
	}
	has := strings.HasPrefix
	if isSuffix {
		has = strings.HasSuffix
	}
	w := 0
	for _, str := range t {
		if w == 0 || !has(str, t[w-1]) {
			t[w] = str
			w++
		}
	}
	return t[:w]
}

// andTrigrams ANDs into q the requirement that the text contains one of
// the strings of t: an OR over the strings of the AND of each string's
// trigrams. A string shorter than three code points requires nothing, so
// then q is returned unchanged.
func andTrigrams(q Query, t stringSet) Query {
	if t.minLen() < 3 {
		return q
	}
	or := noneQuery
	for _, s := range t {
		or = OrQuery(or, TrigramQuery(s))
	}
	return AndQuery(q, or)
}

// stringSet is a set of folded strings. Prefix sets are kept in ascending
// order; suffix sets in ascending order of their reversed code points, so
// strings sharing a suffix sit together.
type stringSet []string

func (s stringSet) clone() stringSet { return slices.Clone(s) }

func (s stringSet) minLen() int {
	if len(s) == 0 {
		return 0
	}
	m := runeCount(s[0])
	for _, str := range s[1:] {
		m = min(m, runeCount(str))
	}
	return m
}

// clean sorts and dedupes s. A nil set stays nil (unknown); an empty
// non-nil set stays empty.
func (s stringSet) clean(isSuffix bool) stringSet {
	if s == nil {
		return nil
	}
	if isSuffix {
		slices.SortFunc(s, compareReversed)
	} else {
		slices.Sort(s)
	}
	return slices.Compact(s)
}

func (s stringSet) union(t stringSet, isSuffix bool) stringSet {
	out := make(stringSet, 0, len(s)+len(t))
	out = append(append(out, s...), t...)
	return out.clean(isSuffix)
}

func (s stringSet) cross(t stringSet, isSuffix bool) stringSet {
	out := make(stringSet, 0, len(s)*len(t))
	for _, a := range s {
		for _, b := range t {
			out = append(out, a+b)
		}
	}
	return out.clean(isSuffix)
}

// compareReversed orders strings by their code points read from the end.
func compareReversed(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ra, na := utf8.DecodeLastRuneInString(a)
		rb, nb := utf8.DecodeLastRuneInString(b)
		if ra != rb {
			if ra < rb {
				return -1
			}
			return 1
		}
		a, b = a[:len(a)-na], b[:len(b)-nb]
	}
	return len(a) - len(b)
}

func runePrefix(s string, n int) string {
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func runeSuffix(s string, n int) string {
	i := len(s)
	for range n {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:]
}
