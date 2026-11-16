// Package hashembed is a deterministic stand-in for a text embedding model:
// feature hashing of word tokens into a small dense vector. The seed data
// uses it to export block embeddings with real topic signal, so clustering
// and vector-index code can be tested before the ml service produces real
// embeddings. It is not a language model and makes no attempt to capture
// meaning beyond shared words.
//
// # Algorithm (normative: other languages must reproduce it exactly)
//
//  1. Tokenize: lowercase (Unicode simple folding via strings.ToLower);
//     a token is a maximal run of letters, digits or combining marks (so
//     Devanagari vowel signs stay inside their word), except that each Han,
//     Hiragana or Katakana character is its own token. Drop tokens of one
//     ASCII character and tokens in the stopword list below.
//  2. Count term frequency tf per token.
//  3. For each distinct token t, h = FNV-1a 32-bit of t's UTF-8 bytes;
//     index = h mod Dim; sign = +1 if bit 31 of h is 0, else -1;
//     v[index] += sign * (1 + ln tf). Tokens are visited in ascending byte
//     order so floating-point sums are reproducible.
//  4. L2-normalize (a zero vector stays zero) and round each component to
//     4 decimal places (round half away from zero).
//
// # Porting notes
//
// Products are wrapped in explicit float64 conversions so the Go compiler
// cannot fuse them into FMA instructions on arm64, which would change the
// last bits of the result between machines.
package hashembed

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Dim is the embedding dimension.
const Dim = 64

// Name identifies this embedder in the manifest and in exported files.
const Name = "skein-hash-v1"

// stopwords are common English function words; dropping them keeps topic
// words dominant.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for that with this from are was were but not you your our its it's
	into than then them they their there these those have has had will would can could should what when where which
	who how why all any each some more most other such only also very just about over after before between both
	off out per via onto upon one two use used using is be by an as at of on or to in we if so do does did it`) {
		stopwords[w] = true
	}
}

// Stopwords returns the stopword list, sorted, for documentation and tests.
func Stopwords() []string {
	out := make([]string, 0, len(stopwords))
	for w := range stopwords {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// Tokens splits text into embedder tokens (step 1).
func Tokens(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tok := string(cur)
			if !(len(cur) == 1 && cur[0] < 0x80) && !stopwords[tok] {
				out = append(out, tok)
			}
			cur = cur[:0]
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case isCJK(r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Embed returns the embedding of text.
func Embed(text string) [Dim]float64 {
	tf := map[string]int{}
	for _, t := range Tokens(text) {
		tf[t]++
	}
	keys := make([]string, 0, len(tf))
	for k := range tf {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var v [Dim]float64
	for _, k := range keys {
		h := fnv.New32a()
		_, _ = h.Write([]byte(k))
		sum := h.Sum32()
		w := 1 + math.Log(float64(tf[k]))
		if sum&(1<<31) != 0 {
			w = -w
		}
		v[sum%Dim] += w
	}
	norm := 0.0
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm == 0 {
		return v
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = Round4(v[i] / norm)
	}
	return v
}

// Round4 rounds to 4 decimal places, half away from zero.
func Round4(x float64) float64 { return math.Round(float64(x*10000)) / 10000 }

// Cosine returns the cosine similarity of two vectors (0 if either is zero).
func Cosine(a, b [Dim]float64) float64 {
	var dot, na, nb float64
	for i := 0; i < Dim; i++ {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(float64(na*nb))
}
