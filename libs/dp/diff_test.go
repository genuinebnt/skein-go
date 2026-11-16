package dp

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// checkDiff asserts everything a diff from a to b must satisfy: canonical
// hunks that apply, an inverse that applies back, and (when minimal is set)
// the fewest possible edits, len(a) + len(b) - 2·LCS.
func checkDiff[T comparable](t testing.TB, a, b []T, hunks []Hunk[T], minimal bool) {
	t.Helper()
	if err := CheckHunks(a, b, hunks); err != nil {
		t.Fatalf("diff of %v and %v: %v\nhunks %v", a, b, err, hunks)
	}
	back := InvertPatch(hunks)
	if err := CheckHunks(b, a, back); err != nil {
		t.Fatalf("inverted diff of %v and %v: %v", a, b, err)
	}
	shortest := len(a) + len(b) - 2*LCSLength(a, b)
	edits := EditCount(hunks)
	if minimal && edits != shortest {
		t.Fatalf("diff of %v and %v makes %d edits, shortest is %d\nhunks %v", a, b, edits, shortest, hunks)
	}
	if edits < shortest {
		t.Fatalf("diff of %v and %v makes %d edits, fewer than possible (%d)", a, b, edits, shortest)
	}
}

func TestDiffIsMinimalAndAppliesOnRandomSequences(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 3000; trial++ {
		alphabet := 2 + r.IntN(4)
		a, b := randomInts(r, alphabet, 30), randomInts(r, alphabet, 30)
		if r.IntN(2) == 0 {
			b = mutate(r, a, alphabet)
		}
		checkDiff(t, a, b, Diff(a, b), true)
		checkDiff(t, a, b, PatienceDiff(a, b), false)
	}
}

// mutate returns a copy of a with a few random edits, the common case for
// note history (small changes to a long text).
func mutate(r *rand.Rand, a []int, alphabet int) []int {
	b := slices.Clone(a)
	for range 1 + r.IntN(4) {
		switch i := r.IntN(len(b) + 1); r.IntN(3) {
		case 0:
			b = slices.Insert(b, i, r.IntN(alphabet))
		case 1:
			if i < len(b) {
				b = slices.Delete(b, i, i+1)
			}
		default:
			if i < len(b) {
				b[i] = r.IntN(alphabet)
			}
		}
	}
	return b
}

func TestDiffOfLongSimilarSequencesIsMinimal(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 20; trial++ {
		a := randomInts(r, 8, 2000)
		b := mutate(r, mutate(r, a, 8), 8)
		checkDiff(t, a, b, Diff(a, b), true)
	}
}

func TestDiffPutsDeletesBeforeInsertsInEachChange(t *testing.T) {
	got := DiffChars("abc", "axc")
	want := []TextHunk{{HunkEqual, "a"}, {HunkDelete, "b"}, {HunkInsert, "x"}, {HunkEqual, "c"}}
	if !slices.Equal(got, want) {
		t.Fatalf("DiffChars(abc, axc) = %v, want %v", got, want)
	}
}

func TestDiffOfEqualAndEmptyInputs(t *testing.T) {
	if got := DiffChars("", ""); len(got) != 0 {
		t.Fatalf("diff of empty strings = %v", got)
	}
	if got := DiffChars("same", "same"); !slices.Equal(got, []TextHunk{{HunkEqual, "same"}}) {
		t.Fatalf("diff of equal strings = %v", got)
	}
	if got := DiffChars("", "new"); !slices.Equal(got, []TextHunk{{HunkInsert, "new"}}) {
		t.Fatalf("diff from empty = %v", got)
	}
	if got := DiffChars("old", ""); !slices.Equal(got, []TextHunk{{HunkDelete, "old"}}) {
		t.Fatalf("diff to empty = %v", got)
	}
}

func TestTokenisersAreLossless(t *testing.T) {
	r := newRand(testSeed(t))
	alphabet := []rune("ab \n\t,_é🙂́\r")
	inputs := []string{"", "\n", "a\nb", "a\nb\n", "foo(bar, 2)", "\xff\xfe broken", "x\r\ny"}
	for range 500 {
		inputs = append(inputs, randomString(r, alphabet, 20))
	}
	for _, s := range inputs {
		for name, tokenise := range map[string]func(string) []string{"Lines": Lines, "Words": Words, "Chars": Chars} {
			tokens := tokenise(s)
			if strings.Join(tokens, "") != s {
				t.Fatalf("%s(%q) = %q does not join back", name, s, tokens)
			}
			for _, tok := range tokens {
				if tok == "" {
					t.Fatalf("%s(%q) has an empty token", name, s)
				}
			}
		}
	}
}

func TestWordsSplitsWordsSpacesAndPunctuation(t *testing.T) {
	got := Words("foo(bar,  2) café_x!")
	want := []string{"foo", "(", "bar", ",", "  ", "2", ")", " ", "café_x", "!"}
	if !slices.Equal(got, want) {
		t.Fatalf("Words = %q, want %q", got, want)
	}
	if got := Lines("a\nb"); !slices.Equal(got, []string{"a\n", "b"}) {
		t.Fatalf("Lines keeps a final line without newline: %q", got)
	}
}

func TestTextDiffsRoundTripAtEveryGranularity(t *testing.T) {
	r := newRand(testSeed(t))
	alphabet := []rune("ab c\n.é🙂")
	for trial := 0; trial < 1000; trial++ {
		a, b := randomString(r, alphabet, 40), randomString(r, alphabet, 40)
		for name, diff := range map[string]func(string, string) []TextHunk{
			"lines": DiffLines, "words": DiffWords, "chars": DiffChars, "patience": PatienceDiffLines,
		} {
			hunks := diff(a, b)
			got, err := ApplyTextPatch(a, hunks)
			if err != nil || got != b {
				t.Fatalf("%s diff of %q -> %q applies to %q, %v", name, a, b, got, err)
			}
		}
	}
}

func TestDiffCharsCountsCodePoints(t *testing.T) {
	hunks := Diff(Chars("a🙂b"), Chars("a😀b"))
	if EditCount(hunks) != 2 {
		t.Fatalf("replacing one emoji takes %d edits, want 2 (one delete, one insert)", EditCount(hunks))
	}
}

func TestPatienceDiffAnchorsOnUniqueLinesRatherThanRepeatedOnes(t *testing.T) {
	// The shortest diff keeps the two repeated "}" lines and moves the
	// heading (2 edits). Patience anchors on the heading, the only unique
	// line, and moves the braces instead (4 edits): longer, but it keeps the
	// meaningful line in place, which is the point of patience diff.
	a := "}\n}\n# Heading\n"
	b := "# Heading\n}\n}\n"
	myers := DiffLines(a, b)
	wantMyers := []TextHunk{{HunkInsert, "# Heading\n"}, {HunkEqual, "}\n}\n"}, {HunkDelete, "# Heading\n"}}
	if !slices.Equal(myers, wantMyers) {
		t.Fatalf("DiffLines = %q\nwant %q", myers, wantMyers)
	}
	patience := PatienceDiffLines(a, b)
	wantPatience := []TextHunk{{HunkDelete, "}\n}\n"}, {HunkEqual, "# Heading\n"}, {HunkInsert, "}\n}\n"}}
	if !slices.Equal(patience, wantPatience) {
		t.Fatalf("PatienceDiffLines = %q\nwant %q", patience, wantPatience)
	}
}

func TestApplyPatchRejectsMismatchedHunks(t *testing.T) {
	a := []rune("abc")
	cases := map[string][]Hunk[rune]{
		"equal differs":      {{HunkEqual, []rune("abx")}},
		"delete differs":     {{HunkDelete, []rune("x")}, {HunkEqual, []rune("bc")}},
		"runs past the end":  {{HunkEqual, []rune("abcd")}},
		"input left over":    {{HunkEqual, []rune("ab")}},
		"unknown hunk kind":  {{HunkKind(9), []rune("abc")}},
		"insert only, input": {{HunkInsert, []rune("abc")}},
	}
	for name, hunks := range cases {
		if _, err := ApplyPatch(a, hunks); !errors.Is(err, ErrPatchMismatch) {
			t.Errorf("%s: err = %v, want ErrPatchMismatch", name, err)
		}
	}
	if _, err := ApplyTextPatch("abc", []TextHunk{{HunkEqual, "ab"}, {HunkDelete, "x"}}); !errors.Is(err, ErrPatchMismatch) {
		t.Errorf("text patch: err = %v, want ErrPatchMismatch", err)
	}
}

func TestCheckHunksRejectsNonCanonicalDiffs(t *testing.T) {
	a, b := []rune("ab"), []rune("xb")
	cases := map[string][]Hunk[rune]{
		"insert before delete": {{HunkInsert, []rune("x")}, {HunkDelete, []rune("a")}, {HunkEqual, []rune("b")}},
		"empty hunk":           {{HunkDelete, []rune("a")}, {HunkInsert, []rune("x")}, {HunkEqual, nil}, {HunkEqual, []rune("b")}},
		"split equal":          {{HunkDelete, []rune("a")}, {HunkInsert, []rune("x")}, {HunkEqual, []rune("b")}, {HunkEqual, nil}},
		"wrong result":         {{HunkDelete, []rune("a")}, {HunkInsert, []rune("y")}, {HunkEqual, []rune("b")}},
	}
	for name, hunks := range cases {
		if err := CheckHunks(a, b, hunks); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: err = %v, want ErrInvariant", name, err)
		}
	}
}
