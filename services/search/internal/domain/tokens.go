package domain

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Token is a word of text with its position in code points.
type Token struct {
	Text       string // lowercased (strings.ToLower)
	Start, End int    // code points in the original text
}

// Tokenize splits text into search tokens: maximal runs of Unicode
// letters, digits and marks, lowercased. It is the tokenizer the seed
// truth's relevance judgements use (docs/seed-data.md, "Text queries"), so
// a term matches a note exactly when the truth says it does. Lowercasing
// is per code point (unicode.ToLower), which keeps offsets aligned with the
// original text; it agrees with strings.ToLower for every letter the
// tokenizer keeps.
func Tokenize(text string) []Token {
	var out []Token
	var cur []rune
	start, pos := 0, 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, Token{Text: string(cur), Start: start, End: pos})
			cur = cur[:0]
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			if len(cur) == 0 {
				start = pos
			}
			cur = append(cur, unicode.ToLower(r))
		} else {
			flush()
		}
		pos++
	}
	flush()
	return out
}

// TokenizeCaseSensitive is Tokenize without lowercasing, for
// case-sensitive text search.
func TokenizeCaseSensitive(text string) []Token {
	runes := []rune(text)
	tokens := Tokenize(text)
	for i, t := range tokens {
		tokens[i].Text = string(runes[t.Start:t.End])
	}
	return tokens
}

// IsCJK reports whether r is Han, Hiragana or Katakana. Text in these
// scripts has no spaces between words, so a query term in them is matched
// as a substring rather than as a token.
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// Term is one term of a text query.
type Term struct {
	Text string // lowercased unless the search is case-sensitive
	// Substring marks a CJK term, matched anywhere in the text instead of
	// against whole tokens.
	Substring bool
}

// ParseTextQuery splits a text query into distinct terms, in order of
// first appearance: each white-space-separated field containing a CJK code
// point is one substring term; any other field contributes its tokens.
// "key-range" is the two terms key and range. This mirrors the seed truth.
func ParseTextQuery(query string, caseSensitive bool) []Term {
	var terms []Term
	seen := map[string]bool{}
	add := func(t Term) {
		if !seen[t.Text] {
			seen[t.Text] = true
			terms = append(terms, t)
		}
	}
	for _, field := range strings.Fields(query) {
		if strings.IndexFunc(field, IsCJK) >= 0 {
			if !caseSensitive {
				field = strings.ToLower(field)
			}
			add(Term{Text: field, Substring: true})
			continue
		}
		tokens := Tokenize(field)
		if caseSensitive {
			tokens = TokenizeCaseSensitive(field)
		}
		for _, t := range tokens {
			add(Term{Text: t.Text})
		}
	}
	return terms
}

// TypoBudget is how many edits a term of this many code points tolerates:
// none up to three (short words have too many neighbours), one up to
// seven, two beyond. Damerau distance counts a swapped pair as one edit,
// the most common typo.
func TypoBudget(term string) int {
	switch n := utf8.RuneCountInString(term); {
	case n < 4:
		return 0
	case n < 8:
		return 1
	default:
		return 2
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
