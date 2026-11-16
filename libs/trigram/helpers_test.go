package trigram

import (
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// testSeed returns the seed for randomised tests and logs it. Set
// SKEIN_SEED to replay a failure.
func testSeed(t testing.TB) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		seed = parsed
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	return seed
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

// textAlphabet is small so trigrams repeat and matches are common. It holds
// case pairs, the Kelvin sign and long s (which fold with ASCII letters),
// a two-byte and a four-byte code point, a word separator, white space and
// a newline.
var textAlphabet = []rune("abcAB_ é\nkKKſs🙂-")

func randomText(r *rand.Rand, maxLen int) string {
	out := make([]rune, r.IntN(maxLen+1))
	for i := range out {
		out[i] = textAlphabet[r.IntN(len(textAlphabet))]
	}
	return string(out)
}

var languages = []string{"go", "Go", "rust", "", "sql"}

func randomDoc(r *rand.Rand, maxLen int) Doc {
	d := Doc{Text: randomText(r, maxLen)}
	if r.IntN(3) == 0 {
		d.Code = true
		d.Language = languages[r.IntN(len(languages))]
	}
	return d
}

// randomRegexp returns a random RE2 pattern over textAlphabet: literals,
// classes, anchors, word boundaries, groups, alternation and repetition,
// nested up to depth.
func randomRegexp(r *rand.Rand, depth int) string {
	if depth == 0 || r.IntN(3) == 0 {
		leaves := []func() string{
			func() string {
				n := 1 + r.IntN(4)
				var b strings.Builder
				for range n {
					b.WriteString(regexp.QuoteMeta(string(textAlphabet[r.IntN(len(textAlphabet))])))
				}
				return b.String()
			},
			func() string { return "[ab]" },
			func() string { return "[a-cK]" },
			func() string { return "[^a]" },
			func() string { return "." },
			func() string { return `\b` },
			func() string { return "^" },
			func() string { return "$" },
			func() string { return `\w` },
			func() string { return `\s` },
			func() string { return "(?i:ab)" },
			func() string { return "(?s:.)" },
		}
		return leaves[r.IntN(len(leaves))]()
	}
	sub := func() string { return randomRegexp(r, depth-1) }
	switch r.IntN(7) {
	case 0, 1:
		return sub() + sub() + sub()
	case 2:
		return "(?:" + sub() + "|" + sub() + ")"
	case 3:
		return "(?:" + sub() + ")*"
	case 4:
		return "(?:" + sub() + ")+"
	case 5:
		return "(?:" + sub() + ")?"
	default:
		return "(?:" + sub() + "){1,3}"
	}
}

func mustIndex(t testing.TB, docs map[DocID]Doc) *Index {
	t.Helper()
	ix := NewIndex()
	for _, id := range sortedDocIDs(docs) {
		if err := ix.Add(id, docs[id]); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return ix
}
