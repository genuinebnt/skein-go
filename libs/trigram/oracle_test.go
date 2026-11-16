package trigram

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/dp"
)

// The oracle reads every document, with no index, using code written
// independently of the search path: orbit walking for case-insensitive
// comparison, and its own byte-to-code-point conversion.

func sameOrbit(a, b rune) bool {
	if a == b {
		return true
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

func oracleAllowed(doc Doc, id DocID, opts Options) bool {
	if opts.Within != nil && !opts.Within.Contains(id) {
		return false
	}
	if (opts.CodeOnly || len(opts.Languages) > 0) && !doc.Code {
		return false
	}
	if len(opts.Languages) > 0 {
		return slices.ContainsFunc(opts.Languages, func(l string) bool { return strings.EqualFold(l, doc.Language) && isASCII(l) })
	}
	return true
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func oracleWholeWord(runes []rune, s, e int) bool {
	word := func(i int) bool {
		if i < 0 || i >= len(runes) {
			return false
		}
		r := runes[i]
		return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
	}
	startInside := word(s-1) && word(s)
	if s == e {
		return !startInside
	}
	return !startInside && !(word(e-1) && word(e))
}

func oracleLiteral(docs map[DocID]Doc, lit string, opts Options) []Hit {
	var out []Hit
	needle := []rune(lit)
	for _, id := range sortedDocIDs(docs) {
		doc := docs[id]
		if lit == "" || !oracleAllowed(doc, id, opts) {
			continue
		}
		text := []rune(doc.Text)
		var spans []Span
		for i := 0; i+len(needle) <= len(text); {
			ok := true
			for j, r := range needle {
				if opts.CaseSensitive && text[i+j] != r || !opts.CaseSensitive && !sameOrbit(text[i+j], r) {
					ok = false
					break
				}
			}
			if !ok {
				i++
				continue
			}
			if !opts.WholeWord || oracleWholeWord(text, i, i+len(needle)) {
				spans = append(spans, Span{i, i + len(needle)})
			}
			i += len(needle)
		}
		if len(spans) > 0 {
			out = append(out, Hit{Doc: id, Matches: spans})
		}
	}
	return out
}

func oracleRegexp(docs map[DocID]Doc, re *regexp.Regexp, opts Options) []Hit {
	var out []Hit
	for _, id := range sortedDocIDs(docs) {
		doc := docs[id]
		if !oracleAllowed(doc, id, opts) {
			continue
		}
		text := []rune(doc.Text)
		var spans []Span
		for _, m := range re.FindAllStringIndex(doc.Text, -1) {
			s := utf8.RuneCountInString(doc.Text[:m[0]])
			e := s + utf8.RuneCountInString(doc.Text[m[0]:m[1]])
			if !opts.WholeWord || oracleWholeWord(text, s, e) {
				spans = append(spans, Span{s, e})
			}
		}
		if len(spans) > 0 {
			out = append(out, Hit{Doc: id, Matches: spans})
		}
	}
	return out
}

func randomCorpus(r *rand.Rand) map[DocID]Doc {
	docs := map[DocID]Doc{}
	for range 1 + r.IntN(25) {
		docs[DocID(r.IntN(100000))] = randomDoc(r, 40)
	}
	return docs
}

func randomOptions(r *rand.Rand, docs map[DocID]Doc) Options {
	opts := Options{CaseSensitive: r.IntN(2) == 0, WholeWord: r.IntN(3) == 0, CodeOnly: r.IntN(5) == 0}
	if r.IntN(4) == 0 {
		opts.Languages = []string{languages[r.IntN(len(languages))]}
	}
	if r.IntN(3) == 0 {
		opts.Within = &Bitmap{}
		for id := range docs {
			if r.IntN(2) == 0 {
				opts.Within.Add(id)
			}
		}
	}
	return opts
}

func assertSameHits(t *testing.T, what string, got Result, want []Hit) {
	t.Helper()
	if len(got.Hits) != len(want) {
		t.Fatalf("%s: %d hits, oracle %d: %v vs %v", what, len(got.Hits), len(want), got.Hits, want)
	}
	for i := range want {
		if got.Hits[i].Doc != want[i].Doc || !slices.Equal(got.Hits[i].Matches, want[i].Matches) {
			t.Fatalf("%s: hit %d is %v, oracle %v", what, i, got.Hits[i], want[i])
		}
	}
	if got.Candidates < len(got.Hits) {
		t.Fatalf("%s: %d candidates for %d hits", what, got.Candidates, len(got.Hits))
	}
}

func TestLiteralSearchMatchesBruteForceScan(t *testing.T) {
	r := newRand(testSeed(t))
	for range 1500 {
		docs := randomCorpus(r)
		ix := mustIndex(t, docs)
		opts := randomOptions(r, docs)
		lit := randomText(r, 5)
		if r.IntN(2) == 0 {
			// A literal cut from a document is likely to match somewhere.
			for _, d := range docs {
				runes := []rune(d.Text)
				if len(runes) > 3 {
					i := r.IntN(len(runes) - 3)
					lit = string(runes[i : i+1+r.IntN(min(5, len(runes)-i))])
				}
				break
			}
		}
		assertSameHits(t, "literal "+lit, ix.SearchLiteral(lit, opts), oracleLiteral(docs, lit, opts))
	}
}

func TestRegexpSearchMatchesBruteForceScan(t *testing.T) {
	r := newRand(testSeed(t))
	for range 1500 {
		docs := randomCorpus(r)
		ix := mustIndex(t, docs)
		opts := randomOptions(r, docs)
		pattern := randomRegexp(r, 3)
		got, err := ix.SearchRegexp(pattern, opts)
		if err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		full := pattern
		if !opts.CaseSensitive {
			full = "(?i)" + pattern
		}
		assertSameHits(t, "regexp "+full, got, oracleRegexp(docs, regexp.MustCompile(full), opts))
	}
}

func TestSearchCountsOnlyDocumentsInsideTheFilter(t *testing.T) {
	ix := mustIndex(t, map[DocID]Doc{1: {Text: "secret plan"}, 2: {Text: "public plan"}, 3: {Text: "plan", Code: true, Language: "Go"}})
	res := ix.SearchLiteral("plan", Options{Within: BitmapOf(2, 3)})
	if res.Candidates != 2 || len(res.Hits) != 2 || res.Hits[0].Doc != 2 {
		t.Fatalf("filtered search %+v", res)
	}
	res = ix.SearchLiteral("plan", Options{Languages: []string{"GO"}})
	if res.Candidates != 1 || res.Hits[0].Doc != 3 {
		t.Fatalf("language filter %+v", res)
	}
}

func TestFuzzyCandidatesNeverMissADocumentWithANearSubstring(t *testing.T) {
	r := newRand(testSeed(t))
	for range 400 {
		docs := map[DocID]Doc{}
		for i := range DocID(1 + r.IntN(12)) {
			docs[i] = Doc{Text: randomText(r, 18)}
		}
		ix := mustIndex(t, docs)
		term := randomText(r, 8)
		k := r.IntN(3)
		got, err := ix.FuzzyCandidates(term, k, Options{})
		if err != nil {
			t.Fatal(err)
		}
		folded := FoldString(term)
		for id, doc := range docs {
			runes := []rune(FoldString(doc.Text))
			near := false
			for i := 0; i <= len(runes) && !near; i++ {
				for j := i; j <= len(runes) && !near; j++ {
					near = dp.DamerauLevenshtein(folded, string(runes[i:j])) <= k
				}
			}
			if near && !got.Contains(id) {
				t.Fatalf("doc %q holds a substring within %d edits of %q but is not a candidate", doc.Text, k, term)
			}
		}
	}
}

func TestFuzzyCandidatesExcludeDocumentsSharingTooFewTrigrams(t *testing.T) {
	ix := mustIndex(t, map[DocID]Doc{1: {Text: "compaction"}, 2: {Text: "copmaction"}, 3: {Text: "zebra crossing"}})
	got, _ := ix.FuzzyCandidates("compaction", 1, Options{})
	if !slices.Equal(got.ToArray(), []uint32{1, 2}) {
		t.Fatalf("candidates %v", got.ToArray())
	}
	if _, err := ix.FuzzyCandidates("x", -1, Options{}); err == nil {
		t.Fatal("negative edits accepted")
	}
}
