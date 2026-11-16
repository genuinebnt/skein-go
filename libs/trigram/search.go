package trigram

import (
	"fmt"
	"slices"
	"unicode/utf8"
)

// Options restrict and shape a search.
type Options struct {
	// CaseSensitive compares code points exactly; otherwise folded (Fold).
	// For regular expressions it is applied when compiling (CompileRegexp).
	CaseSensitive bool
	// WholeWord keeps only matches that neither start nor end inside a word
	// (IsWordRune): the code point before the match and the match's first
	// code point are not both word code points, and likewise the match's
	// last code point and the one after it. For an empty match, the code
	// points on either side are not both word code points.
	WholeWord bool
	// CodeOnly keeps only code documents.
	CodeOnly bool
	// Languages keeps only code documents in one of these languages
	// (compared with A-Z lowercased). Empty means any document.
	Languages []string
	// Within, when not nil, restricts the search to these documents: the
	// caller's permission filter. It is intersected with the candidates
	// before any document is read, so documents outside it are never
	// examined or counted.
	Within *Bitmap
}

// Span is a half-open range [Start, End) of code points in a document's
// text.
type Span struct {
	Start, End int
}

// Hit is one document with matches.
type Hit struct {
	Doc DocID
	// Matches are leftmost-first, non-overlapping, in ascending order.
	Matches []Span
}

// Result is the outcome of a search.
type Result struct {
	// Candidates is the number of documents the trigram query and the
	// filters selected, all of which were confirmed by reading them.
	Candidates int
	// Hits are the documents that matched, by ascending ID. Matched is
	// len(Hits).
	Hits []Hit
}

// filterSet returns the documents the options allow, or nil when they allow
// everything.
func (ix *Index) filterSet(opts Options) *Bitmap {
	var allowed *Bitmap
	if opts.CodeOnly {
		allowed = &ix.code
	}
	if len(opts.Languages) > 0 {
		langs := &Bitmap{}
		seen := map[string]bool{}
		for _, l := range opts.Languages {
			l = asciiLower(l)
			if !seen[l] {
				seen[l] = true
				langs = Or(langs, ix.languages[l])
			}
		}
		allowed = langs // language sets hold only code documents
	}
	if opts.Within != nil {
		if allowed == nil {
			allowed = opts.Within
		} else {
			allowed = And(allowed, opts.Within)
		}
	}
	return allowed
}

// Candidates returns the documents q selects that the options allow.
func (ix *Index) Candidates(q Query, opts Options) *Bitmap {
	allowed := ix.filterSet(opts)
	if allowed != nil && allowed.IsEmpty() {
		return &Bitmap{}
	}
	out := q.Eval(ix)
	if allowed != nil {
		out = And(out, allowed)
	}
	return out
}

// SearchLiteral finds every occurrence of lit: leftmost-first and
// non-overlapping, folded unless opts.CaseSensitive. Candidates are the
// documents holding every trigram of lit; a literal shorter than three
// code points has none, so every allowed document is read. An empty
// literal matches nothing.
func (ix *Index) SearchLiteral(lit string, opts Options) Result {
	if lit == "" {
		return Result{}
	}
	candidates := ix.Candidates(TrigramQuery(lit), opts)
	needle := lit
	if !opts.CaseSensitive {
		needle = FoldString(lit)
	}
	res := Result{Candidates: candidates.Len()}
	candidates.ForEach(func(id DocID) bool {
		text := ix.docs[id].Text
		if !opts.CaseSensitive {
			text = FoldString(text)
		}
		if spans := literalMatches(text, needle, opts.WholeWord); len(spans) > 0 {
			res.Hits = append(res.Hits, Hit{Doc: id, Matches: spans})
		}
		return true
	})
	return res
}

// literalMatches returns the leftmost-first non-overlapping occurrences of
// needle in text as code point spans. Both are compared as decoded code
// points, so invalid bytes in either compare as U+FFFD.
func literalMatches(text, needle string, wholeWord bool) []Span {
	runes := []rune(text)
	pattern := []rune(needle)
	var out []Span
	for i := 0; i+len(pattern) <= len(runes); {
		if !slices.Equal(runes[i:i+len(pattern)], pattern) {
			i++
			continue
		}
		span := Span{i, i + len(pattern)}
		if !wholeWord || isWholeWord(runes, span) {
			out = append(out, span)
		}
		i += len(pattern)
	}
	return out
}

// isWholeWord applies the WholeWord rule (Options.WholeWord) to span.
func isWholeWord(runes []rune, span Span) bool {
	wordAt := func(i int) bool { return i >= 0 && i < len(runes) && IsWordRune(runes[i]) }
	if span.Start == span.End {
		return !(wordAt(span.Start-1) && wordAt(span.Start))
	}
	return !(wordAt(span.Start-1) && wordAt(span.Start)) && !(wordAt(span.End-1) && wordAt(span.End))
}

// SearchRegexp compiles pattern with CompileRegexp(pattern,
// opts.CaseSensitive) and searches with it.
func (ix *Index) SearchRegexp(pattern string, opts Options) (Result, error) {
	re, err := CompileRegexp(pattern, opts.CaseSensitive)
	if err != nil {
		return Result{}, err
	}
	return ix.SearchCompiled(re, opts), nil
}

// SearchCompiled runs a compiled pattern: the candidates its trigram query
// selects, confirmed with the regular expression. Matches are Go's
// FindAllStringIndex (leftmost-first, non-overlapping, an empty match
// abutting the previous match skipped) converted to code point offsets,
// then filtered by WholeWord. opts.CaseSensitive is ignored: case was
// decided at compile time.
func (ix *Index) SearchCompiled(re *Regexp, opts Options) Result {
	candidates := ix.Candidates(re.query, opts)
	res := Result{Candidates: candidates.Len()}
	candidates.ForEach(func(id DocID) bool {
		if spans := RegexpMatches(re, ix.docs[id].Text, opts.WholeWord); len(spans) > 0 {
			res.Hits = append(res.Hits, Hit{Doc: id, Matches: spans})
		}
		return true
	})
	return res
}

// RegexpMatches returns re's matches in text as code point spans, filtered
// by the WholeWord rule when wholeWord. It reads text directly, without the
// index; the brute-force oracle and the service use it.
func RegexpMatches(re *Regexp, text string, wholeWord bool) []Span {
	found := re.re.FindAllStringIndex(text, -1)
	if len(found) == 0 {
		return nil
	}
	var runes []rune
	if wholeWord {
		runes = []rune(text)
	}
	out := make([]Span, 0, len(found))
	// Byte offsets ascend, so one pass converts them all.
	pos, runePos := 0, 0
	toRunes := func(b int) int {
		runePos += utf8.RuneCountInString(text[pos:b])
		pos = b
		return runePos
	}
	for _, m := range found {
		span := Span{Start: toRunes(m[0])}
		span.End = toRunes(m[1])
		if !wholeWord || isWholeWord(runes, span) {
			out = append(out, span)
		}
	}
	return out
}

// FuzzyCandidates returns the documents that may contain a substring within
// maxEdits edits of term, under Damerau–Levenshtein distance (insertions,
// deletions, substitutions and adjacent transpositions), compared folded.
//
// It is the q-gram count filter (Ukkonen 1992; Jokinen and Ukkonen): an
// insertion, deletion or substitution destroys at most three of the term's
// trigram occurrences, and a transposition at most four, so a document
// containing such a substring holds at least |T(term)| - 4·maxEdits of the
// term's distinct trigrams T(term). Documents reaching that count are the
// candidates; when the bound is zero or less every allowed document is.
// Callers confirm candidates with an edit distance (libs/dp).
//
// A negative maxEdits returns ErrInvalidArgument.
func (ix *Index) FuzzyCandidates(term string, maxEdits int, opts Options) (*Bitmap, error) {
	if maxEdits < 0 {
		return nil, fmt.Errorf("%w: max edits %d", ErrInvalidArgument, maxEdits)
	}
	trigrams := Trigrams(term)
	need := len(trigrams) - 4*maxEdits
	if need <= 0 {
		return ix.Candidates(allQuery, opts), nil
	}
	allowed := ix.filterSet(opts)
	counts := map[DocID]int{}
	for _, t := range trigrams {
		ix.postings[t].ForEach(func(id DocID) bool {
			counts[id]++
			return true
		})
	}
	out := &Bitmap{}
	for id, n := range counts {
		if n >= need && (allowed == nil || allowed.Contains(id)) {
			out.Add(id)
		}
	}
	return out, nil
}

// MinSharedTrigrams is the count bound FuzzyCandidates uses: how many of
// term's distinct trigrams a text within maxEdits Damerau–Levenshtein edits
// must share with it. Zero or less means the filter cannot exclude
// anything.
func MinSharedTrigrams(term string, maxEdits int) int {
	return len(Trigrams(term)) - 4*maxEdits
}

// NormalizeLanguage returns the form languages are compared in: A-Z
// lowercased, nothing else changed.
func NormalizeLanguage(lang string) string { return asciiLower(lang) }
