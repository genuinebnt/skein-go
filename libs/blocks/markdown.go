package blocks

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// emptyParagraph is how Export writes a paragraph with no text. Blank lines
// separate blocks, so an empty paragraph needs a visible stand-in; this is
// the one Markdown renderers already show as an empty line.
const emptyParagraph = "&nbsp;"

// Source says where one code point of exported Markdown came from: Offset is
// the code point's position in Blocks[Block].Text, or -1 for markup that
// Export added (list markers, fences, separators), which belongs to the
// block it introduces.
type Source struct {
	Block  int
	Offset int
}

// Export renders blocks as Markdown (docs/spec/blocks.md section 6). It
// never fails; blocks outside the lossless subset (CheckLossless) lose what
// Markdown cannot carry.
func Export(bs []Block) string {
	w := mdWriter{}
	w.blocks(bs)
	return w.b.String()
}

// ExportMapped is Export that also returns, for each code point of the
// Markdown, where it came from. Blame uses it to attribute Markdown
// positions to the elements that produced them.
func ExportMapped(bs []Block) (string, []Source) {
	w := mdWriter{mapped: true}
	w.blocks(bs)
	return w.b.String(), w.sources
}

type mdWriter struct {
	b       strings.Builder
	mapped  bool
	sources []Source
}

func (w *mdWriter) markup(s string, block int) {
	w.b.WriteString(s)
	if w.mapped {
		for range utf8.RuneCountInString(s) {
			w.sources = append(w.sources, Source{Block: block, Offset: -1})
		}
	}
}

func (w *mdWriter) text(s string, block, offset int) {
	w.b.WriteString(s)
	if w.mapped {
		for i := range utf8.RuneCountInString(s) {
			w.sources = append(w.sources, Source{Block: block, Offset: offset + i})
		}
	}
}

func (w *mdWriter) blocks(bs []Block) {
	var numbers []int // the next number at each indent of the current list
	for i, b := range bs {
		if i > 0 {
			if bs[i-1].Type.IsList() && b.Type.IsList() {
				w.markup("\n", i)
			} else {
				w.markup("\n\n", i)
			}
		}
		indent := int(min(b.Indent, MaxIndent))
		if !b.Type.IsList() {
			numbers = numbers[:0]
		} else {
			for len(numbers) <= indent {
				numbers = append(numbers, 0)
			}
			numbers = numbers[:indent+1]
		}
		pad := strings.Repeat("  ", indent)
		switch b.Type {
		case Heading1, Heading2, Heading3:
			w.markup(strings.Repeat("#", int(b.Type-Heading1)+1)+" ", i)
			w.text(b.Text, i, 0)
		case Bulleted:
			numbers[indent] = 0
			w.markup(pad+"- ", i)
			w.text(b.Text, i, 0)
		case Todo:
			numbers[indent] = 0
			box := "- [ ] "
			if b.Checked {
				box = "- [x] "
			}
			w.markup(pad+box, i)
			w.text(b.Text, i, 0)
		case Numbered:
			numbers[indent]++
			w.markup(pad+strconv.Itoa(numbers[indent])+". ", i)
			w.text(b.Text, i, 0)
		case Quote:
			offset := 0
			for j, line := range strings.Split(b.Text, "\n") {
				if j > 0 {
					w.text("\n", i, offset-1)
				}
				if line == "" {
					w.markup(">", i)
				} else {
					w.markup("> ", i)
				}
				w.text(line, i, offset)
				offset += utf8.RuneCountInString(line) + 1
			}
		case Code:
			fence := strings.Repeat("`", codeFenceLength(b.Text))
			w.markup(fence+b.Language+"\n", i)
			if b.Text != "" {
				w.text(b.Text, i, 0)
				w.markup("\n", i)
			}
			w.markup(fence, i)
		case Divider:
			w.markup("---", i)
		case Table:
			w.text(b.Text, i, 0)
		default: // Paragraph, and any unknown type
			switch {
			case b.Text == "":
				w.markup(emptyParagraph, i)
			case needsEscape(b.Text):
				w.markup(`\`, i)
				w.text(b.Text, i, 0)
			default:
				w.text(b.Text, i, 0)
			}
		}
	}
}

// codeFenceLength returns a fence longer than any line of the code that
// consists only of backticks, and at least three, so no line closes it.
func codeFenceLength(code string) int {
	n := 3
	for _, line := range strings.Split(code, "\n") {
		if k := backtickOnlyLine(line); k >= n {
			n = k + 1
		}
	}
	return n
}

// backtickOnlyLine returns the number of backticks if line is one or more
// backticks followed only by spaces, and 0 otherwise.
func backtickOnlyLine(line string) int {
	t := strings.TrimRight(line, " ")
	if t == "" || strings.Trim(t, "`") != "" {
		return 0
	}
	return len(t)
}

// lineKind is how Import reads a line on its own.
type lineKind uint8

const (
	lineParagraph lineKind = iota
	lineBlank
	lineEscaped // a paragraph line written with a leading backslash
	lineEmptyParagraph
	lineFence
	lineDivider
	lineHeading
	lineQuote
	lineTable
	lineList
)

// classify returns how Import reads line when it starts a block.
func classify(line string) lineKind {
	// A line of backslashes then r is escaped exactly when r, read on its
	// own, is not a plain paragraph: unrolling the recursive rule of
	// needsEscape, since no construct but an escape starts with a backslash.
	if strings.HasPrefix(line, `\`) && classifyUnescaped(strings.TrimLeft(line, `\`)) != lineParagraph {
		return lineEscaped
	}
	return classifyUnescaped(line)
}

// classifyUnescaped is classify without the escape rule.
func classifyUnescaped(line string) lineKind {
	trimmed := strings.TrimLeft(line, " ")
	switch {
	case strings.TrimSpace(line) == "":
		return lineBlank
	case line == emptyParagraph:
		return lineEmptyParagraph
	case isFence(line):
		return lineFence
	case isDivider(line):
		return lineDivider
	case headingLevel(line) > 0:
		return lineHeading
	case strings.HasPrefix(line, ">"):
		return lineQuote
	case strings.HasPrefix(line, "|"):
		return lineTable
	case listItem(trimmed) != nil:
		return lineList
	}
	return lineParagraph
}

// needsEscape reports whether a paragraph's text, written as a line, would be
// read back as something else. Such text is written after a backslash, which
// Import removes; text that itself starts with a backslash is escaped when
// the rest would need it, so the rule applies recursively and round trips.
func needsEscape(text string) bool { return classify(text) != lineParagraph }

func isFence(line string) bool {
	if !strings.HasPrefix(line, "```") {
		return false
	}
	info := strings.TrimLeft(line, "`")
	return !strings.Contains(info, "`")
}

func isDivider(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return false
	}
	return strings.Trim(t, "-") == "" || strings.Trim(t, "*") == "" || strings.Trim(t, "_") == ""
}

// headingLevel returns 1 to 6 for "#" to "######" followed by a space or the
// end of the line, and 0 otherwise.
func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(line) && line[n] != ' ') {
		return 0
	}
	return n
}

type listMatch struct {
	kind    Type
	checked bool
	rest    string
}

// listItem reads a list marker at the start of s (indent already removed).
func listItem(s string) *listMatch {
	marker := func(prefix string) (string, bool) {
		if s == prefix {
			return "", true
		}
		if strings.HasPrefix(s, prefix+" ") {
			return s[len(prefix)+1:], true
		}
		return "", false
	}
	for _, bullet := range []string{"-", "*", "+"} {
		for _, box := range []string{"[ ]", "[x]", "[X]"} {
			if rest, ok := marker(bullet + " " + box); ok {
				return &listMatch{kind: Todo, checked: box != "[ ]", rest: rest}
			}
		}
		if rest, ok := marker(bullet); ok {
			return &listMatch{kind: Bulleted, rest: rest}
		}
	}
	digits := 0
	for digits < len(s) && digits < 9 && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(s) && (s[digits] == '.' || s[digits] == ')') {
		if rest, ok := marker(s[:digits+1]); ok {
			return &listMatch{kind: Numbered, rest: rest}
		}
	}
	return nil
}

// Import reads Markdown into blocks (docs/spec/blocks.md section 6). It never
// fails: it reads the subset Export writes plus common variants (other
// bullets, heading levels 4 to 6, unclosed fences, lazy paragraph lines), and
// anything else is a paragraph. Marker and invalid UTF-8 become U+FFFD and
// a lone carriage return ends a line, so every block is valid.
func Import(markdown string) []Block {
	markdown = strings.ToValidUTF8(markdown, "�")
	markdown = strings.ReplaceAll(markdown, string(Marker), "�")
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")
	if markdown == "" {
		return nil
	}
	lines := strings.Split(markdown, "\n")
	var out []Block
	add := func(t Type, text string) *Block {
		out = append(out, Block{Attrs: Attrs{Type: t}, Text: text})
		return &out[len(out)-1]
	}
	for i := 0; i < len(lines); {
		line := lines[i]
		switch classify(line) {
		case lineBlank:
			i++
		case lineEmptyParagraph:
			add(Paragraph, "")
			i++
		case lineFence:
			n := len(line) - len(strings.TrimLeft(line, "`"))
			info := strings.Fields(line[n:])
			b := add(Code, "")
			if len(info) > 0 && validateLanguage(info[0]) == nil {
				b.Language = info[0]
			}
			var body []string
			for i++; i < len(lines) && backtickOnlyLine(lines[i]) < n; i++ {
				body = append(body, lines[i])
			}
			b.Text = strings.Join(body, "\n")
			i++ // the closing fence, or past the end
		case lineDivider:
			add(Divider, "")
			i++
		case lineHeading:
			level := headingLevel(line)
			text := strings.TrimPrefix(line[level:], " ")
			add(Heading1+Type(min(level, 3)-1), text)
			i++
		case lineQuote:
			var body []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], ">"); i++ {
				body = append(body, strings.TrimPrefix(lines[i][1:], " "))
			}
			add(Quote, strings.Join(body, "\n"))
		case lineTable:
			var rows []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
				rows = append(rows, lines[i])
			}
			add(Table, strings.Join(rows, "\n"))
		case lineList:
			trimmed := strings.TrimLeft(line, " ")
			m := listItem(trimmed)
			b := add(m.kind, m.rest)
			b.Checked = m.checked
			b.Indent = uint32(min((len(line)-len(trimmed))/2, MaxIndent))
			i++
		default: // lineParagraph, lineEscaped: a paragraph with lazy continuation lines
			parts := []string{paragraphText(line)}
			for i++; i < len(lines); i++ {
				k := classify(lines[i])
				if k != lineParagraph && k != lineEscaped {
					break
				}
				parts = append(parts, paragraphText(lines[i]))
			}
			add(Paragraph, strings.Join(parts, " "))
		}
	}
	return out
}

func paragraphText(line string) string {
	if classify(line) == lineEscaped {
		return line[1:]
	}
	return line
}

// CheckLossless reports why Export would lose information about bs, or nil
// when Import(Export(bs)) gives back exactly the blocks' content. The
// conditions are the lossless subset of docs/spec/blocks.md section 6.
func CheckLossless(bs []Block) error {
	for i, b := range bs {
		fail := func(why string) error { return fmt.Errorf("block %d (%s): %s", i, b.Type, why) }
		switch {
		case !b.Type.Valid():
			return fail("unknown type")
		case strings.ContainsRune(b.Text, Marker):
			return fail("text contains the marker")
		case !utf8.ValidString(b.Text) || strings.ContainsRune(b.Text, '\r'):
			return fail("text is not valid UTF-8 or contains a carriage return")
		case b.Indent != 0 && !b.Type.IsList():
			return fail("indent outside a list")
		case b.Indent > MaxIndent:
			return fail("indent too deep")
		case b.Checked && b.Type != Todo:
			return fail("checked outside a to-do")
		case b.Language != "" && (b.Type != Code || validateLanguage(b.Language) != nil):
			return fail("language outside a code block, or not a fence word")
		case b.Type != Code && b.Type != Table && b.Type != Quote && strings.Contains(b.Text, "\n"):
			return fail("newline in a single-line block")
		case b.Type == Divider && b.Text != "":
			return fail("divider with text")
		case b.Type == Table && !tableText(b.Text):
			return fail("table rows must each start with |")
		case b.Type == Bulleted && listItem("- "+b.Text).kind != Bulleted:
			return fail("bulleted text that starts with a check box reads back as a to-do")
		}
	}
	return nil
}

func tableText(text string) bool {
	for _, row := range strings.Split(text, "\n") {
		if !strings.HasPrefix(row, "|") {
			return false
		}
	}
	return true
}
