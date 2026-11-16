package dp

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tokenisers split text into the units a diff compares. Each is lossless:
// concatenating the tokens gives back the input exactly, including invalid
// UTF-8 (an invalid byte becomes a one-byte token of its own). That is what
// lets a text diff be applied to the original string.

// Lines splits s into lines, each keeping its trailing "\n". A final line
// without a newline is a token of its own, so "a\nb" and "a\nb\n" differ
// in their last token. "\r\n" stays inside the line, so a change of line
// ending shows up as a changed line.
func Lines(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// Words splits s into runs of word characters (letters, digits, marks and
// underscore), runs of white space, and single other code points
// (punctuation and symbols). "foo(bar, 2)" gives "foo", "(", "bar", ",",
// " ", "2", ")". Marks join words so accented text written with combining
// characters stays whole.
func Words(s string) []string {
	var out []string
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		class := wordClass(r, size)
		end := size
		if class != classOther {
			for end < len(s) {
				next, nextSize := utf8.DecodeRuneInString(s[end:])
				if wordClass(next, nextSize) != class {
					break
				}
				end += nextSize
			}
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}

type tokenClass uint8

const (
	classOther tokenClass = iota
	classWord
	classSpace
)

func wordClass(r rune, size int) tokenClass {
	switch {
	case r == utf8.RuneError && size == 1:
		return classOther
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
		return classWord
	case unicode.IsSpace(r):
		return classSpace
	}
	return classOther
}

// Chars splits s into code points, one token each. Positions in a character
// diff therefore count code points, as everywhere in Skein (ADR-0003).
func Chars(s string) []string {
	out := make([]string, 0, len(s))
	for len(s) > 0 {
		_, size := utf8.DecodeRuneInString(s)
		out = append(out, s[:size])
		s = s[size:]
	}
	return out
}

// TextHunk is a hunk of a text diff, with its tokens joined. It matches
// DiffHunk in proto/skein/v1/documents.proto.
type TextHunk struct {
	Kind HunkKind
	Text string
}

// DiffLines is Diff over Lines.
func DiffLines(a, b string) []TextHunk { return joinHunks(Diff(Lines(a), Lines(b))) }

// DiffWords is Diff over Words.
func DiffWords(a, b string) []TextHunk { return joinHunks(Diff(Words(a), Words(b))) }

// DiffChars is Diff over Chars.
func DiffChars(a, b string) []TextHunk { return joinHunks(Diff(Chars(a), Chars(b))) }

// PatienceDiffLines is PatienceDiff over Lines, the usual choice for showing
// a note's history to a person.
func PatienceDiffLines(a, b string) []TextHunk {
	return joinHunks(PatienceDiff(Lines(a), Lines(b)))
}

func joinHunks(hunks []Hunk[string]) []TextHunk {
	out := make([]TextHunk, len(hunks))
	for i, h := range hunks {
		out[i] = TextHunk{h.Kind, strings.Join(h.Elems, "")}
	}
	return out
}

// ApplyTextPatch replays text hunks against a, like ApplyPatch: equal and
// delete hunks must match a at their position byte for byte, and together
// they must consume all of a. It works for hunks from any tokeniser, because
// the tokenisers are lossless.
func ApplyTextPatch(a string, hunks []TextHunk) (string, error) {
	var out strings.Builder
	rest := a
	for i, h := range hunks {
		switch h.Kind {
		case HunkInsert:
			out.WriteString(h.Text)
		case HunkEqual, HunkDelete:
			if !strings.HasPrefix(rest, h.Text) {
				return "", fmt.Errorf("%w: hunk %d (%s) does not match the input at byte %d", ErrPatchMismatch, i, h.Kind, len(a)-len(rest))
			}
			if h.Kind == HunkEqual {
				out.WriteString(h.Text)
			}
			rest = rest[len(h.Text):]
		default:
			return "", fmt.Errorf("%w: hunk %d has kind %s", ErrPatchMismatch, i, h.Kind)
		}
	}
	if rest != "" {
		return "", fmt.Errorf("%w: %d bytes of input left after the last hunk", ErrPatchMismatch, len(rest))
	}
	return out.String(), nil
}
