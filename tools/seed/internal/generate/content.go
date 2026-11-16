package generate

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

// genBlock is a planned block and the topic it was generated from (-1 for
// anchor and language-neutral blocks).
type genBlock struct {
	blocks.Block
	Topic int
	// Agent marks the block an agent run suggests in a later session.
	Agent bool
}

// content returns the planned blocks of a note. It is a pure function of
// (seed, plan, note index), so a near-duplicate can regenerate its
// original's content.
func (p *plan) content(n *notePlan) []genBlock {
	if n.Anchor != nil {
		out := make([]genBlock, len(n.Anchor.Blocks))
		for i, b := range n.Anchor.Blocks {
			out[i] = genBlock{Block: b, Topic: n.Topic}
		}
		return out
	}
	if n.Stress != "" {
		return p.stressContent(n)
	}
	if n.DupOf >= 0 {
		return p.duplicateContent(n)
	}
	r := p.src.Stream("note.content", uint64(n.Index))
	g := &writer{r: r, p: p, n: n}
	return g.note()
}

type writer struct {
	r *prng.Rand
	p *plan
	n *notePlan
}

func (g *writer) topicFor() int {
	if g.n.Topic2 >= 0 && g.r.Chance(g.n.Mix) {
		return g.n.Topic2
	}
	return g.n.Topic
}

func (g *writer) word(t int) string { return prng.Pick(g.r, corpus.Topics[t].Vocab) }

// phrase fills one sentence template.
func (g *writer) phrase(t int) string {
	tpl := prng.Pick(g.r, corpus.Topics[t].Phrases)
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		if tpl[i] == '{' && i+2 < len(tpl) && tpl[i+2] == '}' {
			switch tpl[i+1] {
			case 'w':
				b.WriteString(g.word(t))
			case 'W':
				b.WriteString(capitalize(g.word(t)))
			case 'n':
				b.WriteString(fmt.Sprint(2 + g.r.IntN(498)))
			case 'x':
				b.WriteString(g.word(g.r.IntN(len(corpus.Topics))))
			}
			i += 2
			continue
		}
		b.WriteByte(tpl[i])
	}
	s := b.String()
	if g.r.Chance(0.05) {
		for _, ty := range corpus.Typos {
			if strings.Contains(s, ty.Right) {
				s = strings.Replace(s, ty.Right, ty.Wrong, 1)
				break
			}
		}
	}
	if g.r.Chance(0.04) {
		s += " " + prng.Pick(g.r, corpus.Emoji)
	}
	return s
}

func (g *writer) sentence() string {
	if g.n.Lang != "" {
		var pool []corpus.Snippet
		for _, s := range corpus.Multilingual {
			if s.Language == g.n.Lang {
				pool = append(pool, s)
			}
		}
		return prng.Pick(g.r, pool).Text
	}
	s := g.phrase(g.topicFor())
	if g.r.Chance(0.12) {
		s = prng.Pick(g.r, corpus.Connectives) + " " + lowerFirst(s)
	}
	if g.r.Chance(0.05) {
		s += " The " + prng.Pick(g.r, corpus.GeneralWords) + " is still open."
	}
	return s
}

// lowerFirst lowercases the first letter, unless the text starts with an
// acronym (two capitals), which keeps its case.
func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if !unicode.IsUpper(r) {
		return s
	}
	if next, _ := utf8.DecodeRuneInString(s[size:]); unicode.IsUpper(next) {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}

func (g *writer) paragraph() string {
	k := 1 + g.r.IntN(g.p.prof.MaxSentences)
	parts := make([]string, k)
	for i := range parts {
		parts[i] = g.sentence()
	}
	if g.n.Lang == "" && g.r.Chance(0.03) {
		parts = append(parts, prng.Pick(g.r, corpus.Multilingual).Text)
	}
	return strings.Join(parts, " ")
}

func (g *writer) heading(t int) string {
	return capitalize(g.word(t)) + " " + []string{"and " + g.word(t), "notes", "in practice", "open questions", "plan", "results", "background"}[g.r.IntN(7)]
}

func identParts(w string) []string {
	return strings.FieldsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func pascal(words ...string) string {
	var b strings.Builder
	for _, w := range words {
		for _, part := range identParts(w) {
			b.WriteString(capitalize(part))
		}
	}
	return b.String()
}

func snake(words ...string) string {
	var parts []string
	for _, w := range words {
		parts = append(parts, identParts(strings.ToLower(w))...)
	}
	return strings.Join(parts, "_")
}

func (g *writer) code(t int, ident string) genBlock {
	tp := corpus.Topics[t]
	lang := tp.Languages[0]
	if len(tp.Languages) > 1 && g.r.Chance(0.4) {
		lang = tp.Languages[1+g.r.IntN(len(tp.Languages)-1)]
	}
	var pool []corpus.CodeTemplate
	for _, c := range corpus.CodeTemplates {
		if c.Language == lang {
			pool = append(pool, c)
		}
	}
	tpl := prng.Pick(g.r, pool)
	a, b := g.word(t), g.word(t)
	typeName := pascal(a, b)
	if ident != "" {
		typeName = ident
	}
	src := strings.NewReplacer(
		"{Type}", typeName,
		"{func}", lowerFirstASCII(pascal(b, a)),
		"{snake}", snake(b, a),
		"{table}", snake(a, b)+"s",
		"{kebab}", strings.ReplaceAll(snake(a, b), "_", "-"),
		"{n}", fmt.Sprint(2+g.r.IntN(30)),
	).Replace(tpl.Source)
	return genBlock{Block: blocks.Block{Type: blocks.Code, Language: lang, Text: src}, Topic: t}
}

func lowerFirstASCII(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (g *writer) table(t int) string {
	rows := []string{"| " + capitalize(g.word(t)) + " | Value | Note |", "| --- | --- | --- |"}
	for k := 0; k < 2+g.r.IntN(4); k++ {
		rows = append(rows, fmt.Sprintf("| %s | %d ms | %s |", g.word(t), 1+g.r.IntN(900), g.word(t)))
	}
	return strings.Join(rows, "\n")
}

var linkSentences = []string{"See [[%s]] for details.", "This builds on [[%s]].", "Related: [[%s]].", "Compare with [[%s]].", "Background in [[%s]]."}

// note writes a procedural note.
func (g *writer) note() []genBlock {
	n := g.n
	var out []genBlock
	add := func(b blocks.Block, t int) { out = append(out, genBlock{Block: b, Topic: t}) }
	add(blocks.Block{Type: blocks.Paragraph, Text: g.paragraph()}, n.Topic)
	identQueue := append([]string{}, n.CodeIdents...)
	target := n.Blocks
	if n.Agent {
		target--
	}
	for len(out) < target {
		t := g.topicFor()
		left := target - len(out)
		switch x := g.r.Float64(); {
		case len(out)%7 == 0 && left > 2:
			add(blocks.Block{Type: []blocks.Type{blocks.Heading2, blocks.Heading2, blocks.Heading3}[g.r.IntN(3)], Text: g.heading(t)}, t)
		case x < 0.42:
			add(blocks.Block{Type: blocks.Paragraph, Text: g.paragraph()}, t)
		case x < 0.62:
			for k := 0; k < min(left, 2+g.r.IntN(4)); k++ {
				indent := 0
				if k > 0 && g.r.Chance(0.25) {
					indent = 1 + g.r.IntN(2)
				}
				add(blocks.Block{Type: blocks.Bulleted, Indent: indent, Text: g.sentence()}, t)
			}
		case x < 0.68:
			for k := 0; k < min(left, 2+g.r.IntN(3)); k++ {
				add(blocks.Block{Type: blocks.Numbered, Text: g.sentence()}, t)
			}
		case x < 0.76:
			for k := 0; k < min(left, 2+g.r.IntN(3)); k++ {
				add(blocks.Block{Type: blocks.Todo, Checked: g.r.Chance(0.45), Text: capitalize(g.word(t)) + ": " + lowerFirst(g.sentence())}, t)
			}
		case x < 0.86 && n.Lang == "":
			ident := ""
			if len(identQueue) > 0 {
				ident, identQueue = identQueue[0], identQueue[1:]
			}
			out = append(out, g.code(t, ident))
		case x < 0.90:
			add(blocks.Block{Type: blocks.Quote, Text: g.sentence()}, t)
		case x < 0.94 && n.Lang == "":
			add(blocks.Block{Type: blocks.Table, Text: g.table(t)}, t)
		case x < 0.97:
			add(blocks.Block{Type: blocks.Divider}, -1)
		default:
			add(blocks.Block{Type: blocks.Paragraph, Text: g.paragraph()}, t)
		}
	}
	out = out[:target]
	// Planted identifiers that did not get a code block of their own replace
	// the last non-code blocks with code blocks.
	for k := len(out) - 1; len(identQueue) > 0 && k > 0; k-- {
		if out[k].Type != blocks.Code {
			out[k] = g.code(n.Topic, identQueue[0])
			identQueue = identQueue[1:]
		}
	}
	textual := func() []int {
		var idx []int
		for i, b := range out {
			if b.Type == blocks.Paragraph || b.Type == blocks.Bulleted || b.Type == blocks.Quote {
				idx = append(idx, i)
			}
		}
		return idx
	}
	appendTo := func(s string) {
		idx := textual()
		i := 0
		if len(idx) > 0 {
			i = idx[g.r.IntN(len(idx))]
		}
		if out[i].Text != "" {
			out[i].Text += " "
		}
		out[i].Text += s
	}
	for _, c := range n.Codenames {
		t := n.Topic
		appendTo(fmt.Sprintf("The %s %s %s.", capitalize(c), g.word(t), []string{"is behind schedule", "shipped last week", "needs an owner", "is the open question", "replaced the old " + g.word(t)}[g.r.IntN(5)]))
	}
	for k := 0; k < len(n.TwinTerms) && k < 1; k++ {
		for rep := 0; rep < 3; rep++ {
			appendTo(fmt.Sprintf("%s and %s both depend on the %s %s.", capitalize(n.TwinTerms[0]), n.TwinTerms[1], g.word(n.Topic), g.word(n.Topic)))
		}
	}
	for _, t := range n.Links {
		appendTo(fmt.Sprintf(prng.Pick(g.r, linkSentences), g.p.notes[t].Title))
	}
	for _, d := range n.Dangling {
		appendTo(fmt.Sprintf("Tracked in [[%s]].", d))
	}
	if n.Agent {
		out = append(out, genBlock{Block: blocks.Block{Type: blocks.Quote, Text: "Summary suggested by the assistant: " + lowerFirst(g.phrase(n.Topic))}, Topic: n.Topic, Agent: true})
	}
	return out
}

// duplicateContent copies the original's content with light edits: a
// dropped block, a few changed words and one added sentence.
func (p *plan) duplicateContent(n *notePlan) []genBlock {
	orig := p.content(p.notes[n.DupOf])
	r := p.src.Stream("note.dup", uint64(n.Index))
	out := make([]genBlock, 0, len(orig))
	for _, b := range orig {
		b.Agent = false
		out = append(out, b)
	}
	if len(out) > 3 {
		drop := 1 + r.IntN(len(out)-1)
		if !strings.Contains(out[drop].Text, "[[") {
			out = append(out[:drop], out[drop+1:]...)
		}
	}
	for k := 0; k < 3; k++ {
		i := r.IntN(len(out))
		if out[i].Type == blocks.Paragraph && out[i].Topic >= 0 {
			words := corpus.Topics[out[i].Topic].Vocab
			a, b := prng.Pick(r, words), prng.Pick(r, words)
			out[i].Text = strings.Replace(out[i].Text, " "+a+" ", " "+b+" ", 1)
		}
	}
	out = append(out, genBlock{Block: blocks.Block{Type: blocks.Paragraph, Text: "Copied here so the team can edit it separately."}, Topic: -1})
	return out
}
