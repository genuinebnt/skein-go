package domain

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/libs/trigram"
)

// Readable is the set of notes a caller may read. Search never looks at a
// note outside it: candidates are intersected with it before anything is
// read, ranked or counted.
type Readable map[string]bool

// Has reports whether the caller may read note.
func (r Readable) Has(note string) bool { return r[note] }

// Filters restrict a search to some blocks.
type Filters struct {
	CaseSensitive bool
	WholeWord     bool
	CodeOnly      bool
	Languages     []string
}

func (f Filters) codeOnly() bool { return f.CodeOnly || len(f.Languages) > 0 }

func (f Filters) allows(b Block) bool {
	if !f.codeOnly() {
		return true
	}
	if !b.IsCode() {
		return false
	}
	if len(f.Languages) == 0 {
		return true
	}
	lang := trigram.NormalizeLanguage(b.Language)
	return slices.ContainsFunc(f.Languages, func(l string) bool { return trigram.NormalizeLanguage(l) == lang })
}

func (f Filters) trigramOptions(within *trigram.Bitmap) trigram.Options {
	return trigram.Options{CaseSensitive: f.CaseSensitive, WholeWord: f.WholeWord, CodeOnly: f.CodeOnly, Languages: f.Languages, Within: within}
}

// Ranking constants. BM25's usual k1 and b; field and proximity boosts
// multiply the BM25 sum, so they reorder notes of similar textual weight
// without letting a weak match outrank a strong one.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// titleBoost applies when every term occurs in the title: the note is
	// about the query, not merely mentioning it.
	titleBoost = 1.0
	// blockBoost applies when every term occurs in one block: the terms are
	// used together.
	blockBoost = 0.5
)

// typoWeight discounts a term matched through a misspelling: a near word
// is evidence, but weaker than the word itself.
func typoWeight(distance int) float64 { return math.Pow(0.5, float64(distance)) }

// termMatch is how one query term matched one note, through its best
// variant.
type termMatch struct {
	word     string // the vocabulary word, or the substring
	distance int
	title    int
	body     int
	blocks   map[string]int // block ID -> occurrences
	score    float64
}

// TextMatch is one ranked note of a text search, before rendering.
type TextMatch struct {
	Note  string
	Score float64
	// Best is the block shown for the hit: the one holding the most
	// distinct terms, then the most occurrences, then the first. Empty when
	// the note matched through its title only.
	Best  string
	terms []termMatch
}

// TextResults are the ranked notes of a text search.
type TextResults struct {
	Matches []TextMatch
	// Candidates counts the readable, filter-passing blocks the indexes
	// proposed for any term; Matched counts the blocks of matching notes
	// that hold at least one term.
	Candidates, Matched int
	terms               []Term
}

// SearchText runs a typo-tolerant text search: every term must occur in a
// note (in its title or any allowed block; AND semantics), exactly or
// within its typo budget, and notes are ranked by BM25 over the readable
// notes with the title weighted TitleWeight times.
//
// Candidates: each term is expanded to vocabulary words through the
// vocabulary's trigram index (WordIndex.Variants, confirmed by banded and
// then Damerau edit distance); CJK terms are found as substrings through
// the block trigram index. Statistics (document frequency, average length)
// are computed over the readable notes only, so scores reveal nothing
// about notes the caller cannot read.
//
// Whole-word mode changes nothing here: terms always match whole tokens.
// Case-sensitive mode turns typo tolerance off and keeps only occurrences
// with the exact case.
func (w *Workspace) SearchText(query string, readable Readable, f Filters) (TextResults, error) {
	terms := ParseTextQuery(query, f.CaseSensitive)
	if len(terms) == 0 {
		return TextResults{}, fmt.Errorf("%w: query has no words", ErrInvalidQuery)
	}
	notes := w.readableVisible(readable)
	allowedDocs := w.BlocksOf(notes)
	res := TextResults{terms: terms}
	if len(notes) == 0 {
		return res, nil
	}
	avgLen := 0.0
	for _, n := range notes {
		avgLen += float64(WeightedLength(w.words.Lengths(n)))
	}
	avgLen /= float64(len(notes))

	candidates := &trigram.Bitmap{}
	perTerm := make([]map[string]termMatch, len(terms))
	for i, term := range terms {
		perTerm[i] = w.matchTerm(term, readable, f, allowedDocs, len(notes), avgLen, candidates)
	}
	res.Candidates = candidates.Len()

	for _, note := range sortedKeys(perTerm[0]) {
		m := TextMatch{Note: note, terms: make([]termMatch, len(terms))}
		inTitle := true
		ok := true
		for i := range terms {
			tm, found := perTerm[i][note]
			if !found {
				ok = false
				break
			}
			m.terms[i] = tm
			m.Score += tm.score
			inTitle = inTitle && tm.title > 0
		}
		if !ok {
			continue
		}
		m.Best = w.bestBlock(note, m.terms)
		if inTitle {
			m.Score *= 1 + titleBoost
		}
		if m.Best != "" && allTermsIn(m.terms, m.Best) {
			m.Score *= 1 + blockBoost
		}
		res.Matched += blocksWithTerms(m.terms)
		res.Matches = append(res.Matches, m)
	}
	slices.SortStableFunc(res.Matches, func(a, b TextMatch) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return strings.Compare(a.Note, b.Note)
	})
	return res, nil
}

// readableVisible lists the visible notes the caller can read, ascending.
func (w *Workspace) readableVisible(readable Readable) []string {
	var out []string
	for _, id := range w.VisibleNotes() {
		if readable[id] {
			out = append(out, id)
		}
	}
	return out
}

// matchTerm finds every readable note matching term, keeping per note the
// variant that scores best, and adds the blocks it proposed to candidates.
func (w *Workspace) matchTerm(term Term, readable Readable, f Filters, allowedDocs *trigram.Bitmap,
	noteCount int, avgLen float64, candidates *trigram.Bitmap) map[string]termMatch {
	out := map[string]termMatch{}
	consider := func(note, word string, distance, df int, title int, blocks map[string]int) {
		body := 0
		for _, n := range blocks {
			body += n
		}
		tf := float64(TitleWeight*title + body)
		if tf == 0 {
			return
		}
		dl := float64(WeightedLength(w.words.Lengths(note)))
		idf := math.Log(1 + (float64(noteCount)-float64(df)+0.5)/(float64(df)+0.5))
		norm := tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*dl/max(avgLen, 1)))
		score := idf * norm * typoWeight(distance)
		if best, ok := out[note]; !ok || score > best.score {
			out[note] = termMatch{word: word, distance: distance, title: title, body: body, blocks: blocks, score: score}
		}
	}
	if term.Substring {
		hits := w.blocks.SearchLiteral(term.Text, trigram.Options{CaseSensitive: f.CaseSensitive, CodeOnly: f.CodeOnly,
			Languages: f.Languages, Within: allowedDocs})
		found := map[string]map[string]int{}
		for _, h := range hits.Hits {
			ref := w.blockAt[h.Doc]
			candidates.Add(h.Doc)
			if found[ref.Note] == nil {
				found[ref.Note] = map[string]int{}
			}
			found[ref.Note][ref.Block] = len(h.Matches)
		}
		titles := map[string]int{}
		if !f.codeOnly() {
			for _, note := range w.readableVisible(readable) {
				if n := countSubstring(w.notes[note].Title, term.Text, f.CaseSensitive); n > 0 {
					titles[note] = n
				}
			}
		}
		df := len(unionKeys(found, titles))
		for _, note := range unionKeys(found, titles) {
			consider(note, term.Text, 0, df, titles[note], found[note])
		}
		return out
	}
	budget := TypoBudget(term.Text)
	lookup := term.Text
	if f.CaseSensitive {
		budget, lookup = 0, strings.ToLower(term.Text)
	}
	for _, v := range w.words.Variants(lookup, budget) {
		postings := w.words.Postings(v.Word)
		type occurrence struct {
			title  int
			blocks map[string]int
		}
		occ := map[string]occurrence{}
		for _, note := range sortedKeys(postings) {
			if !readable[note] || !w.notes[note].Visible() {
				continue
			}
			p := postings[note]
			o := occurrence{blocks: map[string]int{}}
			if !f.codeOnly() {
				o.title = p.Title
			}
			for block, n := range p.Blocks {
				ref := BlockRef{note, block}
				if f.codeOnly() {
					if b, _ := w.Block(ref); !f.allows(b) {
						continue
					}
				}
				candidates.Add(w.blockDoc[ref])
				o.blocks[block] = n
			}
			if f.CaseSensitive {
				o = occurrence{title: 0, blocks: map[string]int{}}
				if !f.codeOnly() {
					o.title = countToken(w.notes[note].Title, term.Text)
				}
				for block := range p.Blocks {
					if b, _ := w.Block(BlockRef{note, block}); f.allows(b) {
						if n := countToken(b.Text, term.Text); n > 0 {
							o.blocks[block] = n
						}
					}
				}
			}
			if o.title > 0 || len(o.blocks) > 0 {
				occ[note] = o
			}
		}
		for _, note := range sortedKeys(occ) {
			consider(note, v.Word, v.Distance, len(occ), occ[note].title, occ[note].blocks)
		}
	}
	return out
}

func unionKeys(a map[string]map[string]int, b map[string]int) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	return sortedKeys(set)
}

func countToken(text, token string) int {
	n := 0
	for _, t := range TokenizeCaseSensitive(text) {
		if t.Text == token {
			n++
		}
	}
	return n
}

// countSubstring counts non-overlapping occurrences, lowercasing text
// unless caseSensitive (the term is already lowercased then).
func countSubstring(text, sub string, caseSensitive bool) int {
	if !caseSensitive {
		text = strings.ToLower(text)
	}
	return strings.Count(text, sub)
}

func (w *Workspace) bestBlock(note string, terms []termMatch) string {
	best, bestTerms, bestCount, bestOrdinal := "", 0, 0, 0
	for _, b := range w.notes[note].Blocks {
		distinct, count := 0, 0
		for _, tm := range terms {
			if n := tm.blocks[b.ID]; n > 0 {
				distinct++
				count += n
			}
		}
		if distinct == 0 {
			continue
		}
		if distinct > bestTerms || distinct == bestTerms && (count > bestCount || count == bestCount && b.Ordinal < bestOrdinal) {
			best, bestTerms, bestCount, bestOrdinal = b.ID, distinct, count, b.Ordinal
		}
	}
	return best
}

func allTermsIn(terms []termMatch, block string) bool {
	for _, tm := range terms {
		if tm.blocks[block] == 0 {
			return false
		}
	}
	return true
}

func blocksWithTerms(terms []termMatch) int {
	set := map[string]bool{}
	for _, tm := range terms {
		for b, n := range tm.blocks {
			if n > 0 {
				set[b] = true
			}
		}
	}
	return len(set)
}

// SnippetWindow is the number of code points a hit's snippet shows.
const SnippetWindow = 160

// Hit is a rendered search hit.
type Hit struct {
	Note       string
	NoteTitle  string
	Block      string
	Location   trigram.Span // in the block; zero means the whole block
	Snippet    string
	Highlights []trigram.Span // in the snippet
	Score      float64
	Language   string
}

// RenderText turns a ranked match into a hit with a snippet of its best
// block, highlighting every occurrence of the matched words.
func (w *Workspace) RenderText(res TextResults, m TextMatch) Hit {
	n := w.notes[m.Note]
	hit := Hit{Note: m.Note, NoteTitle: n.Title, Score: m.Score}
	block, ok := w.Block(BlockRef{m.Note, m.Best})
	if !ok {
		if len(n.Blocks) == 0 {
			return hit
		}
		block = n.Blocks[0]
		s, _ := trigram.MakeSnippet(block.Text, nil, SnippetWindow)
		hit.Block, hit.Snippet = block.ID, s.Text
		hit.Language = codeLanguage(block)
		return hit
	}
	var matches []dp.Match
	runes := []rune(block.Text)
	for i, term := range res.terms {
		tm := m.terms[i]
		if term.Substring {
			for _, s := range substringSpans(runes, []rune(term.Text), true) {
				matches = append(matches, dp.Match{Term: i, Start: s.Start, End: s.End})
			}
			continue
		}
		for _, t := range Tokenize(block.Text) {
			if t.Text == tm.word || term.Text == t.Text {
				matches = append(matches, dp.Match{Term: i, Start: t.Start, End: t.End})
			}
		}
	}
	s, _ := trigram.MakeSnippet(block.Text, matches, SnippetWindow)
	hit.Block, hit.Snippet, hit.Highlights, hit.Location = block.ID, s.Text, s.Highlights, s.Focus
	hit.Language = codeLanguage(block)
	return hit
}

func codeLanguage(b Block) string {
	if b.IsCode() {
		return b.Language
	}
	return ""
}

// substringSpans finds non-overlapping occurrences of sub in text,
// comparing lowercased code points when fold.
func substringSpans(text, sub []rune, fold bool) []trigram.Span {
	var out []trigram.Span
	eq := func(a, b rune) bool {
		if fold {
			return trigram.Fold(a) == trigram.Fold(b)
		}
		return a == b
	}
	for i := 0; i+len(sub) <= len(text) && len(sub) > 0; {
		match := true
		for j := range sub {
			if !eq(text[i+j], sub[j]) {
				match = false
				break
			}
		}
		if match {
			out = append(out, trigram.Span{Start: i, End: i + len(sub)})
			i += len(sub)
			continue
		}
		i++
	}
	return out
}
