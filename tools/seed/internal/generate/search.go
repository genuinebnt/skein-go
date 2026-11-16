package generate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

// Search ground truth.
//
// Text queries are sets of terms with AND semantics. A term matches a note
// when it equals one of the note's search tokens (lowercased maximal runs of
// letters, digits and combining marks), except that a term containing a
// Han, Hiragana or Katakana character matches as a lowercase substring,
// since those scripts do not separate words with spaces. A note is
// relevant when every term matches it, with grade
//
//	3  every term matches the title
//	2  every term matches within one block
//	1  otherwise
//
// Trashed notes are never relevant. Queries with a subject only count notes
// that subject can read at the reference time.
//
// Regex queries use RE2 syntax (Go regexp). Matches are leftmost-first and
// non-overlapping within each block's text, reported as code point offsets
// into that text. CaseSensitive false means the pattern is run with (?i).

type textQuery struct {
	ID      string
	Text    string
	Terms   []string
	Kind    string
	Subject int // user index; -1 means the workspace owner
}

type regexQuery struct {
	ID            string
	Pattern       string
	CaseSensitive bool
	CodeOnly      bool
	Languages     []string
	re            *regexp.Regexp
}

func (q *regexQuery) compiled() *regexp.Regexp {
	if q.re == nil {
		pat := q.Pattern
		if !q.CaseSensitive {
			pat = "(?i)" + pat
		}
		q.re = regexp.MustCompile(pat)
	}
	return q.re
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// searchTokens returns the lowercased search tokens of text.
func searchTokens(text string) []string {
	var out []string
	start := -1
	lower := strings.ToLower(text)
	for i, r := range lower {
		word := unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
		if word && start < 0 {
			start = i
		}
		if !word && start >= 0 {
			out = append(out, lower[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, lower[start:])
	}
	return out
}

func newTextQuery(id, text, kind string, subject int) textQuery {
	q := textQuery{ID: id, Text: text, Kind: kind, Subject: subject}
	for _, f := range strings.Fields(strings.ToLower(text)) {
		if strings.IndexFunc(f, isCJK) >= 0 {
			q.Terms = append(q.Terms, f)
			continue
		}
		q.Terms = append(q.Terms, searchTokens(f)...)
	}
	q.Terms = uniqueStrings(q.Terms)
	return q
}

// termIndex answers "does this term match" for one piece of text.
type termIndex struct {
	tokens map[string]bool
	lower  string
}

func newTermIndex(text string) termIndex {
	ti := termIndex{tokens: map[string]bool{}, lower: strings.ToLower(text)}
	for _, t := range searchTokens(text) {
		ti.tokens[t] = true
	}
	return ti
}

func (ti termIndex) has(term string) bool {
	if strings.IndexFunc(term, isCJK) >= 0 {
		return strings.Contains(ti.lower, term)
	}
	return ti.tokens[term]
}

func (ti termIndex) all(terms []string) bool {
	for _, t := range terms {
		if !ti.has(t) {
			return false
		}
	}
	return true
}

// gradeNote returns the relevance grade of a note for a query, 0 if not
// relevant.
func gradeNote(q *textQuery, title termIndex, blockIdx []termIndex, whole termIndex) int {
	if !whole.all(q.Terms) {
		return 0
	}
	if title.all(q.Terms) {
		return 3
	}
	for _, b := range blockIdx {
		if b.all(q.Terms) {
			return 2
		}
	}
	return 1
}

type regexMatch struct {
	BlockID string `json:"block_id"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Text    string `json:"text"`
	NoteID  string `json:"note_id"`
}

// matchRegex runs a regex query over one block.
func matchRegex(q *regexQuery, b blocks.Block) [][2]int {
	if q.CodeOnly && b.Type != blocks.Code {
		return nil
	}
	if len(q.Languages) > 0 && (b.Type != blocks.Code || !containsString(q.Languages, b.Language)) {
		return nil
	}
	var out [][2]int
	for _, m := range q.compiled().FindAllStringIndex(b.Text, -1) {
		start := utf8.RuneCountInString(b.Text[:m[0]])
		out = append(out, [2]int{start, start + utf8.RuneCountInString(b.Text[m[0]:m[1]])})
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// anchorQueries are hand-picked queries over the anchor workspace.
func anchorQueries() []textQuery {
	var out []textQuery
	for i, q := range []string{
		"election timeout", "write stall", "bloom filter", "hnsw recall", "trigram index", "lumenwright",
		"burn rate", "tombstone", "joint consensus", "cardinality budget", "readindex", "compaction",
		"backpressure", "pinning", "postmortem", "オフライン", "खोज", "Replikationsverzögerung", "kenta", "snapshot",
	} {
		out = append(out, newTextQuery(fmt.Sprintf("q-anchor-%02d", i), q, "anchor", -1))
	}
	// The guest only sees the notes shared with her; the same query must
	// return fewer notes.
	out = append(out, newTextQuery("q-anchor-guest-00", "search", "permission", 6))
	out = append(out, newTextQuery("q-anchor-guest-01", "vector", "permission", 6))
	return out
}

func anchorRegexes() []regexQuery {
	return []regexQuery{
		{ID: "re-anchor-incident", Pattern: `\bINC-\d{4}\b`, CaseSensitive: true},
		{ID: "re-anchor-compactions", Pattern: `max_background_compactions\s*=\s*\d+`, CaseSensitive: true},
		{ID: "re-anchor-ef", Pattern: `ef_(search|construction)`, CaseSensitive: false},
		{ID: "re-anchor-tokio-builder", Pattern: `\.worker_threads\(\d+\)`, CaseSensitive: true, CodeOnly: true, Languages: []string{"rust"}},
		{ID: "re-anchor-iso-dates", Pattern: `\b2026-1[01]-\d{2}\b`, CaseSensitive: true},
	}
}

// genericRegexes target the code templates, so every workspace has matches.
func genericRegexes() []regexQuery {
	return []regexQuery{
		{ID: "re-go-close", Pattern: `func \(\w+ \*\w+\) Close\(\) error`, CaseSensitive: true, CodeOnly: true, Languages: []string{"go"}},
		{ID: "re-go-timeout", Pattern: `ctx, cancel := context\.WithTimeout`, CaseSensitive: true, CodeOnly: true, Languages: []string{"go"}},
		{ID: "re-rust-drop", Pattern: `impl\s+Drop\s+for\s+\w+`, CaseSensitive: true, CodeOnly: true, Languages: []string{"rust"}},
		{ID: "re-rust-test", Pattern: `fn \w+_survives_restart\(`, CaseSensitive: true, CodeOnly: true, Languages: []string{"rust"}},
		{ID: "re-sql-count", Pattern: `select\s+count\(\*\)`, CaseSensitive: false, CodeOnly: true, Languages: []string{"sql"}},
		{ID: "re-python-test", Pattern: `def test_\w+\(`, CaseSensitive: true, CodeOnly: true, Languages: []string{"python"}},
		{ID: "re-ts-export", Pattern: `export (?:async )?function \w+`, CaseSensitive: true, CodeOnly: true, Languages: []string{"typescript"}},
		{ID: "re-yaml-image", Pattern: `(?m)^\s*image: \S+:\d+\.\d+\.\d+$`, CaseSensitive: true, CodeOnly: true, Languages: []string{"yaml"}},
		{ID: "re-any-dangling-plan", Pattern: `\[\[[^\]]+ plan\]\]`, CaseSensitive: true},
	}
}

func codenameRegexes(w *wsPlan) []regexQuery {
	var out []regexQuery
	for i, c := range w.Codenames {
		if i%3 != 0 {
			continue
		}
		out = append(out,
			regexQuery{ID: fmt.Sprintf("re-%s-shard", c), Pattern: `\b` + capitalize(c) + `Shard\b`, CaseSensitive: true, CodeOnly: true},
			regexQuery{ID: fmt.Sprintf("re-%s-any", c), Pattern: c + `\w*`, CaseSensitive: false})
	}
	return out
}

// proceduralQueries plans the text queries of a generated workspace.
func proceduralQueries(r *prng.Rand, w *wsPlan, p *plan) []textQuery {
	var out []textQuery
	id := func(kind string) string { return fmt.Sprintf("q-%s-%s-%02d", w.Slug, kind, len(out)) }
	top := corpus.Topics[w.Topics[0]]
	for i, c := range w.Codenames {
		out = append(out, newTextQuery(id("codename"), c, "codename", -1))
		if i%2 == 1 {
			out = append(out, newTextQuery(id("codename-topic"), c+" "+prng.Pick(r, top.Vocab), "codename+topic", -1))
		}
	}
	for k := 0; k < 4; k++ {
		t := corpus.Topics[w.Topics[k%len(w.Topics)]]
		out = append(out, newTextQuery(id("topic"), prng.Pick(r, t.Vocab)+" "+prng.Pick(r, t.Vocab), "topic", -1))
	}
	for _, m := range w.Members {
		if m.Role == policy.Guest && len(w.Codenames) > 0 {
			out = append(out, newTextQuery(id("guest"), w.Codenames[0], "permission", m.User))
			out = append(out, newTextQuery(id("guest"), prng.Pick(r, top.Vocab), "permission", m.User))
			break
		}
	}
	return out
}

// typoOf returns a misspelling of word and its Levenshtein distance from it.
func typoOf(r *prng.Rand, word string) (string, int) {
	rs := []rune(word)
	if len(rs) < 4 {
		return word + string(rs[len(rs)-1]), 1
	}
	i := 1 + r.IntN(len(rs)-2)
	var out []rune
	switch r.IntN(4) {
	case 0: // transposition
		out = append(append([]rune{}, rs[:i]...), rs[i+1], rs[i])
		out = append(out, rs[i+2:]...)
	case 1: // deletion
		out = append(append([]rune{}, rs[:i]...), rs[i+1:]...)
	case 2: // insertion
		out = append(append(append([]rune{}, rs[:i]...), rs[i]), rs[i:]...)
	default: // substitution
		out = append([]rune{}, rs...)
		sub := 'a' + rune(r.IntN(26))
		if sub == out[i] {
			sub = 'z' - (out[i] - 'a')
		}
		out[i] = sub
	}
	s := string(out)
	return s, levenshtein(word, s)
}

// levenshtein is the edit distance in code points (insert, delete,
// substitute, each cost 1).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// sortedKeys returns a map's keys in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
