package domain

import (
	"slices"
	"strings"
	"unicode"
)

// MaxHandleLength bounds a handle; longer runs are not treated as mentions.
const MaxHandleLength = 64

// ExtractHandles returns the distinct handles @mentioned in a block's inline
// Markdown, lowercased, in ascending order.
//
// A mention is "@" followed by an ASCII letter and then ASCII letters,
// digits, ".", "_" or "-", with trailing ".", "_" and "-" dropped (so
// "@ada." at the end of a sentence is "ada"). It does not count when:
//
//   - the "@" follows a letter, a digit or one of @ . _ - + / = & : \
//     (emails such as privacy@lumenwright.example, "recall@10", SQL's @@,
//     URLs);
//   - the handle is directly followed by another "@" (ada@example);
//   - it is inside an inline code span (`@ada`), using CommonMark's rule
//     that a span opened by n backticks closes at the next run of exactly
//     n backticks, and an unclosed run is literal text;
//   - the handle is longer than MaxHandleLength.
//
// Code blocks are excluded by the caller (BlockKindCode). The grammar is
// pinned by testdata/conformance/notifications/mentions.json.
func ExtractHandles(text string) []string {
	runes := []rune(text)
	inCode := codeSpans(runes)
	var handles []string
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' || inCode[i] {
			continue
		}
		if i > 0 && blocksMention(runes[i-1]) {
			continue
		}
		end := i + 1
		if end >= len(runes) || !isASCIILetter(runes[end]) {
			continue
		}
		for end < len(runes) && isHandleRune(runes[end]) && !inCode[end] {
			end++
		}
		if end < len(runes) && runes[end] == '@' {
			i = end
			continue
		}
		handle := strings.TrimRight(string(runes[i+1:end]), "._-")
		if len(handle) <= MaxHandleLength {
			handles = append(handles, strings.ToLower(handle))
		}
		i = end - 1
	}
	slices.Sort(handles)
	return slices.Compact(handles)
}

// codeSpans marks the runes that belong to inline code spans, backticks
// included.
func codeSpans(runes []rune) []bool {
	in := make([]bool, len(runes))
	runLength := func(at int) int {
		n := 0
		for at+n < len(runes) && runes[at+n] == '`' {
			n++
		}
		return n
	}
	for i := 0; i < len(runes); {
		if runes[i] != '`' {
			i++
			continue
		}
		open := runLength(i)
		closeAt := -1
		for j := i + open; j < len(runes); {
			if runes[j] != '`' {
				j++
				continue
			}
			n := runLength(j)
			if n == open {
				closeAt = j
				break
			}
			j += n
		}
		if closeAt < 0 {
			i += open
			continue
		}
		for k := i; k < closeAt+open; k++ {
			in[k] = true
		}
		i = closeAt + open
	}
	return in
}

func blocksMention(prev rune) bool {
	if unicode.IsLetter(prev) || unicode.IsDigit(prev) {
		return true
	}
	return strings.ContainsRune(`@._-+/=&:\`, prev)
}

func isASCIILetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

func isHandleRune(r rune) bool {
	return isASCIILetter(r) || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

// ExcerptLength is the length of a mention excerpt, in code points.
const ExcerptLength = 160

// Excerpt returns up to ExcerptLength code points of text around the first
// "@handle", with an ellipsis where it cut, for a notification body.
func Excerpt(text, handle string) string {
	runes := []rune(text)
	if len(runes) <= ExcerptLength {
		return text
	}
	start := 0
	if at := indexMention(runes, strings.ToLower(handle)); at > 0 {
		start = max(0, at-ExcerptLength/4)
	}
	end := min(len(runes), start+ExcerptLength)
	start = max(0, end-ExcerptLength)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// indexMention returns the rune index of the first "@" followed by handle
// (ASCII, compared case-insensitively), or -1. It works on runes so that
// case mapping elsewhere in the text cannot shift positions.
func indexMention(runes []rune, handle string) int {
	for i := 0; i+len(handle) < len(runes); i++ {
		if runes[i] != '@' {
			continue
		}
		match := true
		for k := 0; k < len(handle) && match; k++ {
			match = unicode.ToLower(runes[i+1+k]) == rune(handle[k])
		}
		if match {
			return i
		}
	}
	return -1
}
