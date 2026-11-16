package dp

import (
	"testing"
	"unicode/utf8"
)

// fuzzMaxRunes keeps each fuzz input small enough that the quadratic
// algorithms and the LCS cross-check stay fast.
const fuzzMaxRunes = 300

// FuzzDiff checks, for arbitrary pairs of strings at every granularity, that
// the diff is canonical, applies, inverts, and (for Myers) is minimal.
func FuzzDiff(f *testing.F) {
	seeds := [][2]string{
		{"", ""}, {"abc", "abc"}, {"abc", "axc"}, {"a\nb\nc\n", "a\nc\nb\n"},
		{"}\n}\n# H\n", "# H\n}\n}\n"}, {"🙂漢é", "漢🙂é"}, {"\xff\xfe", "\xfe\xff"},
		{"foo(bar, 2)", "foo(baz,2)"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if utf8.RuneCountInString(a) > fuzzMaxRunes || utf8.RuneCountInString(b) > fuzzMaxRunes {
			t.Skip()
		}
		for _, tokenise := range []func(string) []string{Lines, Words, Chars} {
			ta, tb := tokenise(a), tokenise(b)
			checkDiff(t, ta, tb, Diff(ta, tb), true)
			checkDiff(t, ta, tb, PatienceDiff(ta, tb), false)
		}
		for _, diff := range []func(string, string) []TextHunk{DiffLines, DiffWords, DiffChars, PatienceDiffLines} {
			got, err := ApplyTextPatch(a, diff(a, b))
			if err != nil || got != b {
				t.Fatalf("text patch of %q -> %q gives %q, %v", a, b, got, err)
			}
		}
	})
}

// FuzzEditDistance checks metric properties of both distances (symmetry,
// length bounds, Damerau <= Levenshtein, the triangle inequality) and that
// the banded distance agrees with the full one for every k.
func FuzzEditDistance(f *testing.F) {
	f.Add("kitten", "sitting", "mitten")
	f.Add("ca", "abc", "ac")
	f.Add("", "🙂", "漢字")
	f.Add("teh", "the", "eth")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		const limit = 40 // the full Damerau matrix and k loop are quadratic
		for _, s := range []string{a, b, c} {
			if utf8.RuneCountInString(s) > limit {
				t.Skip()
			}
		}
		checkEditDistanceProperties(t, a, b, c)
		if d := Levenshtein(a, a+"x"); d != 1 {
			t.Fatalf("appending one rune to %q costs %d", a, d)
		}
	})
}
