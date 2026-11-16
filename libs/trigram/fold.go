package trigram

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Fold returns the case-folded form of r: the smallest code point in r's
// simple case-folding orbit (the cycle unicode.SimpleFold walks). Every
// member of an orbit folds to the same code point, so "k", "K" and the
// Kelvin sign U+212A all fold to "K", and "ſ" (long s) folds with "s" and
// "S" to "S". This is exactly the equivalence Go's and Rust's regex engines
// use for (?i), which is what makes the index sound for case-insensitive
// regular expressions.
//
// Folding maps one code point to one code point, so offsets in folded text
// are offsets in the original text.
//
// The smallest member is used rather than "the lowercase form" because it is
// defined for every orbit (some have no lowercase member, some have two) and
// needs nothing beyond the orbit itself. Code points outside the Unicode
// range, including utf8.RuneError for invalid bytes, fold to themselves.
func Fold(r rune) rune {
	if r < utf8.RuneSelf {
		// ASCII fast path: every ASCII letter's orbit has its uppercase form as
		// the smallest member (k, K and U+212A; s, S and U+017F included).
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}
	smallest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < smallest {
			smallest = f
		}
	}
	return smallest
}

// FoldString folds every code point of s with Fold. Invalid UTF-8 bytes
// become U+FFFD, one per byte, as Go's range over a string decodes them.
func FoldString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(Fold(r))
	}
	return b.String()
}

// Trigram is three consecutive folded code points packed into an integer:
// the first in bits 42..62, the second in bits 21..41, the third in bits
// 0..20. Every code point fits in 21 bits. Ordering trigrams as integers is
// ordering them by their code points, first to last.
//
// Rust: `pub struct Trigram(pub u64)`.
type Trigram uint64

// MakeTrigram packs three code points, folding them first.
func MakeTrigram(a, b, c rune) Trigram {
	return Trigram(uint64(Fold(a)&0x1FFFFF)<<42 | uint64(Fold(b)&0x1FFFFF)<<21 | uint64(Fold(c)&0x1FFFFF))
}

// Runes returns the trigram's three folded code points.
func (t Trigram) Runes() [3]rune {
	return [3]rune{rune(t >> 42 & 0x1FFFFF), rune(t >> 21 & 0x1FFFFF), rune(t & 0x1FFFFF)}
}

// String returns the three code points as text, for debugging and fixtures.
func (t Trigram) String() string {
	r := t.Runes()
	return string(r[:])
}

// Trigrams returns the distinct trigrams of s, ascending: one for every run
// of three consecutive code points, after folding. Text shorter than three
// code points has none. Trigrams run across line breaks; a document's text
// is one string.
func Trigrams(s string) []Trigram {
	var out []Trigram
	var a, b rune
	n := 0
	for _, r := range s {
		r = Fold(r)
		if n >= 2 {
			out = append(out, Trigram(uint64(a)<<42|uint64(b)<<21|uint64(r)))
		}
		a, b = b, r
		n++
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// IsWordRune reports whether r is part of a word for whole-word matching:
// a Unicode letter, digit or mark, or '_'. It is the word class of libs/dp
// (Words) and, apart from being Unicode-aware, of RE2's \w, so an
// identifier like snake_case is one word.
func IsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// asciiLower lowercases A-Z only. Language names are compared through it so
// the rule is identical in every language runtime.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
