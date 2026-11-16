package blocks

import (
	"fmt"
	"strings"
)

// Document is a parsed anchor note: front matter plus blocks.
type Document struct {
	// Meta holds the front matter's "key: value" lines, in file order.
	Meta []MetaEntry
	// Blocks are the note's blocks in order.
	Blocks []Block
}

// MetaEntry is one front matter line.
type MetaEntry struct {
	Key   string
	Value string
}

// Get returns the first value for key, or "".
func (d Document) Get(key string) string {
	for _, m := range d.Meta {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

// Parse reads the Markdown subset the anchor notes are written in:
//
//	front matter between "---" lines at the top (key: value)
//	# / ## / ###          headings
//	- text, * text        bulleted list items (two spaces of indent per level)
//	1. text               numbered list items
//	- [ ] / - [x]         to-dos
//	> text                quote (consecutive lines form one block)
//	| a | b |             table (consecutive rows form one block)
//	```lang ... ```       code block
//	---                   divider
//	anything else         paragraph (consecutive lines joined by a space)
//
// The subset is deliberately strict: anything it cannot classify is an error,
// so a typo in an anchor file fails the build instead of silently producing a
// different note.
func Parse(src string) (Document, error) {
	var doc Document
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	i := 0
	if len(lines) > 0 && lines[0] == "---" {
		i = 1
		for ; i < len(lines) && lines[i] != "---"; i++ {
			k, v, ok := strings.Cut(lines[i], ":")
			if !ok {
				return doc, fmt.Errorf("front matter line %d: want key: value", i+1)
			}
			doc.Meta = append(doc.Meta, MetaEntry{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
		}
		if i == len(lines) {
			return doc, fmt.Errorf("front matter is not closed")
		}
		i++
	}
	var para []string
	flush := func() {
		if len(para) > 0 {
			doc.Blocks = append(doc.Blocks, Block{Type: Paragraph, Text: strings.Join(para, " ")})
			para = nil
		}
	}
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimLeft(line, " ")
		indent := (len(line) - len(trimmed)) / 2
		switch {
		case strings.TrimSpace(line) == "":
			flush()
			i++
		case strings.HasPrefix(line, "```"):
			flush()
			lang := strings.TrimSpace(strings.TrimPrefix(line, "```"))
			var body []string
			i++
			for i < len(lines) && !strings.HasPrefix(lines[i], "```") {
				body = append(body, lines[i])
				i++
			}
			if i == len(lines) {
				return doc, fmt.Errorf("code block opened with %q is not closed", line)
			}
			doc.Blocks = append(doc.Blocks, Block{Type: Code, Language: lang, Text: strings.Join(body, "\n")})
			i++
		case line == "---":
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Divider})
			i++
		case strings.HasPrefix(line, "### "):
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Heading3, Text: line[4:]})
			i++
		case strings.HasPrefix(line, "## "):
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Heading2, Text: line[3:]})
			i++
		case strings.HasPrefix(line, "# "):
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Heading1, Text: line[2:]})
			i++
		case strings.HasPrefix(line, "> ") || line == ">":
			flush()
			var body []string
			for i < len(lines) && (strings.HasPrefix(lines[i], "> ") || lines[i] == ">") {
				body = append(body, strings.TrimPrefix(strings.TrimPrefix(lines[i], ">"), " "))
				i++
			}
			doc.Blocks = append(doc.Blocks, Block{Type: Quote, Text: strings.Join(body, "\n")})
		case strings.HasPrefix(line, "|"):
			flush()
			var rows []string
			for i < len(lines) && strings.HasPrefix(lines[i], "|") {
				rows = append(rows, lines[i])
				i++
			}
			doc.Blocks = append(doc.Blocks, Block{Type: Table, Text: strings.Join(rows, "\n")})
		case strings.HasPrefix(trimmed, "- [ ] ") || strings.HasPrefix(trimmed, "- [x] "):
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Todo, Indent: indent, Checked: trimmed[3] == 'x', Text: trimmed[6:]})
			i++
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			flush()
			doc.Blocks = append(doc.Blocks, Block{Type: Bulleted, Indent: indent, Text: trimmed[2:]})
			i++
		case isNumbered(trimmed):
			flush()
			_, rest, _ := strings.Cut(trimmed, ". ")
			doc.Blocks = append(doc.Blocks, Block{Type: Numbered, Indent: indent, Text: rest})
			i++
		default:
			if indent > 0 {
				return doc, fmt.Errorf("line %d: indented text outside a list: %q", i+1, line)
			}
			para = append(para, line)
			i++
		}
	}
	flush()
	for n, b := range doc.Blocks {
		if strings.ContainsRune(b.Text, Marker) {
			return doc, fmt.Errorf("%w: block %d", ErrMarkerInText, n)
		}
	}
	return doc, nil
}

// isNumbered reports whether s starts with "<digits>. ".
func isNumbered(s string) bool {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n > 0 && strings.HasPrefix(s[n:], ". ")
}
