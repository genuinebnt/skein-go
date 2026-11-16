package generate

import (
	"fmt"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus"
	"github.com/genuinebnt/skein-go/tools/seed/internal/history"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
)

// The stress profile replaces the generated workspaces with pathological
// shapes, each one an edge case some component must survive:
//
//	stress-chain   100,000 notes linked in one chain (deep traversals, long paths)
//	stress-hub     a hub with 49,700 spokes pointing at it, plus a 300-note
//	               near-clique core (one big SCC, very high in-degree)
//	stress-shapes  one note of 200,000 characters, three huge code blocks,
//	               indentation 64 levels deep, every Unicode plane, 500
//	               concurrent replicas on one note, and 400 tiny batches

const (
	stressChainLen = 100000
	stressHubTotal = 50000
	stressCoreSize = 299
	stressReplicas = 500
)

var stressShapes = []struct{ Kind, Title string }{
	{"huge-text", "Huge note (200k characters)"},
	{"huge-code", "Huge code blocks"},
	{"deep-indent", "Deep indentation"},
	{"unicode-planes", "Every Unicode plane"},
	{"many-replicas", "Five hundred replicas"},
	{"many-batches", "Four hundred small edits"},
}

func (p *plan) stressWorkspace(name, kind string, owner, firstMember, lastMember int) *wsPlan {
	at := DataStart.Add(24 * time.Hour)
	w := p.addWorkspace(name, kind, corpus.Company{Name: name, Domain: "stress.example"}, owner, at)
	for u := firstMember; u <= lastMember && u < len(p.users); u++ {
		role := policy.Member
		if u == owner {
			role = policy.Owner
		}
		p.addMember(w, memberPlan{User: u, Role: role, JoinedAt: at.Add(time.Duration(u-firstMember) * time.Minute), InvitedBy: owner})
	}
	w.Topics = []int{0, 1, 3}
	return w
}

func (p *plan) stressNote(w *wsPlan, title, kind string, at time.Time, creator int, editors []int) *notePlan {
	n := &notePlan{Title: title, Kind: "stress", Stress: kind, Topic: 0, Topic2: -1, DupOf: -1, CreatedAt: at, CreatedBy: creator,
		Editors: editors, Blocks: 1, RejectRevoked: -1, RejectViewer: -1}
	p.addNote(w, n)
	return n
}

func (p *plan) planStress() error {
	base := len(corpus.AnchorCast)
	span := ReferenceTime.Sub(DataStart.Add(48*time.Hour)) - 24*time.Hour
	// The chain.
	chain := p.stressWorkspace("Stress chain", "stress-chain", base, base, base+7)
	editors := []int{base, base + 1, base + 2}
	first := len(p.notes)
	for i := 0; i < stressChainLen; i++ {
		at := DataStart.Add(48*time.Hour + time.Duration(int64(span)*int64(i)/stressChainLen))
		n := p.stressNote(chain, fmt.Sprintf("Chain %06d", i+1), "chain", at, editors[i%3], editors)
		n.Root = i == 0
	}
	for i := first; i < len(p.notes)-1; i++ {
		p.notes[i].Links = []int{i + 1}
		chain.Chain = append(chain.Chain, i)
	}
	chain.Chain = append(chain.Chain, len(p.notes)-1)
	chain.Queries = []textQuery{newTextQuery("q-stress-chain-00", "chain 050000", "stress", -1)}
	// The hub.
	hubWS := p.stressWorkspace("Stress hub", "stress-hub", base+8, base+8, base+15)
	editors = []int{base + 8, base + 9}
	at := DataStart.Add(72 * time.Hour)
	hub := p.stressNote(hubWS, "Hub", "hub", at, base+8, editors)
	hub.Root = true
	core := []int{hub.Index}
	for i := 0; i < stressCoreSize; i++ {
		n := p.stressNote(hubWS, fmt.Sprintf("Core %03d", i+1), "core", at.Add(time.Duration(i+1)*time.Minute), editors[i%2], editors)
		core = append(core, n.Index)
	}
	r := p.src.Stream("plan.stress", 0)
	for _, c := range core[1:] {
		links := []int{hub.Index}
		for _, d := range core[1:] {
			if d != c && r.Chance(0.5) {
				links = append(links, d)
			}
		}
		p.notes[c].Links = links
	}
	hub.Links = append([]int{}, core[1:]...)
	for i := 0; i < stressHubTotal-1-stressCoreSize; i++ {
		n := p.stressNote(hubWS, fmt.Sprintf("Spoke %05d", i+1), "spoke",
			at.Add(time.Hour+time.Duration(int64(span-72*time.Hour)*int64(i)/stressHubTotal)), editors[i%2], editors)
		n.Links = []int{hub.Index}
	}
	hubWS.SCCs = []plantedSCC{{Label: "hub-core", Members: sortedCopy(core)}}
	hubWS.Hubs = []int{hub.Index}
	hubWS.Queries = []textQuery{newTextQuery("q-stress-hub-00", "core 017", "stress", -1)}
	// The shapes.
	shapes := p.stressWorkspace("Stress shapes", "stress-shapes", base+16, base+16, len(p.users)-1)
	var members []int
	for _, m := range shapes.Members {
		members = append(members, m.User)
	}
	for i, s := range stressShapes {
		n := p.stressNote(shapes, s.Title, s.Kind, DataStart.Add(time.Duration(100+i)*time.Hour), base+16, members[:8])
		if s.Kind == "many-replicas" {
			n.Editors = members
		}
		n.Root = i == 0
	}
	shapes.Queries = []textQuery{newTextQuery("q-stress-shapes-00", "compaction", "stress", -1)}
	shapes.Regexes = genericRegexes()
	return nil
}

// unicodePlanes returns one sample block per Unicode plane plus the
// awkward cases: combining marks, right-to-left text, ZWJ sequences,
// noncharacters, the byte order mark and C0 controls other than the block
// marker.
func unicodePlanes() []blocks.Block {
	samples := []string{
		"Plane 0 (BMP): A é ß Ω Ж ש ع अ ข 中 한 ア ﬁ \uFFFD \uFFFE \uFFFF",
		"Plane 1 (SMP): 𐍈 𝔘𝔫𝔦𝔠𝔬𝔡𝔢 𝄞 🀄 😀 🧵 🦀",
		"Plane 2 (SIP): 𠀀 𠜎 𡃁 𪚥",
		"Plane 3 (TIP): \U00030000 \U0003134A",
	}
	for plane := 4; plane <= 13; plane++ {
		samples = append(samples, fmt.Sprintf("Plane %d (unassigned): %c %c", plane, rune(plane<<16), rune(plane<<16|0xFFFD)))
	}
	samples = append(samples,
		"Plane 14 (SSP): \U000E0041\U000E0042 tag characters, \U000E0100 variation selector",
		"Plane 15 (PUA-A): \U000F0000 \U000FFFFD",
		"Plane 16 (PUA-B): \U00100000 \U0010FFFD \U0010FFFF",
		"Combining: e\u0301 a\u0308\u0304 Z\u0335\u0321 क्षि",
		"Right to left: العربية עברית mixed with English and 123",
		"ZWJ and modifiers: 👩\u200D💻 👨\u200D👩\u200D👧\u200D👦 👍🏽 🇯🇵 🏳\uFE0F\u200D🌈",
		"Controls and invisibles: tab\there, BOM \uFEFF, zero width\u200Bspace, NEL\u0085, line sep\u2028para sep\u2029",
	)
	out := make([]blocks.Block, len(samples))
	for i, s := range samples {
		out[i] = blocks.Block{Type: blocks.Paragraph, Text: s}
	}
	return out
}

func (p *plan) stressContent(n *notePlan) []genBlock {
	r := p.src.Stream("note.content", uint64(n.Index))
	g := &writer{r: r, p: p, n: n}
	one := func(t blocks.Type, text string) []genBlock {
		return []genBlock{{Block: blocks.Block{Type: t, Text: text}, Topic: -1}}
	}
	linkList := func(prefix string, links []int) string {
		parts := make([]string, len(links))
		for i, t := range links {
			parts[i] = "[[" + p.notes[t].Title + "]]"
		}
		return prefix + strings.Join(parts, ", ") + "."
	}
	switch n.Stress {
	case "chain":
		if len(n.Links) == 0 {
			return one(blocks.Paragraph, "End of the chain.")
		}
		return one(blocks.Paragraph, linkList("Next: ", n.Links))
	case "hub":
		return one(blocks.Paragraph, linkList("Hub of ", n.Links))
	case "core":
		return one(blocks.Paragraph, linkList("Core links: ", n.Links))
	case "spoke":
		return one(blocks.Paragraph, linkList("Points at ", n.Links))
	case "huge-text":
		var out []genBlock
		for len(out) < 400 {
			var b strings.Builder
			for b.Len() < 500 {
				b.WriteString(g.phrase(len(out) % len(corpus.Topics)))
				b.WriteString(" ")
			}
			out = append(out, genBlock{Block: blocks.Block{Type: blocks.Paragraph, Text: strings.TrimSpace(b.String())}, Topic: len(out) % len(corpus.Topics)})
		}
		return out
	case "huge-code":
		var out []genBlock
		// One block per language, each about 2,500 lines: the topic's
		// templates repeated with fresh identifiers.
		for _, t := range []int{2, 1, 12} {
			var b strings.Builder
			lang := ""
			// newlines counts the separators written so far; the trailing
			// blank line is trimmed, so the block has newlines-1 lines.
			for newlines := 0; newlines-1 < 2500; {
				c := g.code(t, fmt.Sprintf("Generated%04d", newlines))
				if lang == "" {
					lang = c.Language
				}
				if c.Language != lang {
					continue
				}
				b.WriteString(c.Text)
				b.WriteString("\n\n")
				newlines += strings.Count(c.Text, "\n") + 2
			}
			out = append(out, genBlock{Block: blocks.Block{Type: blocks.Code, Language: lang, Text: strings.TrimRight(b.String(), "\n")}, Topic: t})
		}
		return out
	case "deep-indent":
		var out []genBlock
		for depth := 0; depth <= 64; depth++ {
			out = append(out, genBlock{Block: blocks.Block{Type: blocks.Bulleted, Indent: depth, Text: fmt.Sprintf("Level %d: %s", depth, g.sentence())}, Topic: 0})
		}
		for depth := 63; depth >= 0; depth -= 3 {
			out = append(out, genBlock{Block: blocks.Block{Type: blocks.Todo, Indent: depth, Checked: depth%2 == 0, Text: fmt.Sprintf("Back out to %d", depth)}, Topic: -1})
		}
		return out
	case "unicode-planes":
		var out []genBlock
		for _, b := range unicodePlanes() {
			out = append(out, genBlock{Block: b, Topic: -1})
		}
		return out
	case "many-replicas":
		var out []genBlock
		for i := 0; i < 20; i++ {
			out = append(out, genBlock{Block: blocks.Block{Type: blocks.Bulleted, Text: fmt.Sprintf("Item %d:", i+1)}, Topic: -1})
		}
		return out
	case "many-batches":
		return one(blocks.Paragraph, "Counter:")
	}
	return one(blocks.Paragraph, n.Title)
}

// stressHistory builds the history of a stress note.
func (p *plan) stressHistory(sc *script) error {
	n := sc.n
	sc.t = n.CreatedAt
	typeAll := func(user int, from, to int, at time.Time) error {
		s, _, err := sc.open(user, history.KindType, at)
		if err != nil {
			return err
		}
		for b := from; b < to; b++ {
			id, err := s.AppendBlock(sc.gb[b].Block, sc.gb[b].Text, nil)
			if err != nil {
				return err
			}
			sc.markers[b] = id
		}
		return sc.deliver(s, at.Add(time.Minute))
	}
	sc.markers = make([]crdt.ID, len(sc.gb))
	switch n.Stress {
	case "huge-text":
		for b := 0; b < len(sc.gb); b += 20 {
			if err := typeAll(n.Editors[(b/20)%len(n.Editors)], b, min(b+20, len(sc.gb)), sc.advance()); err != nil {
				return err
			}
		}
		// Edits deep inside the text, where the engine's local edits are
		// slowest.
		for k := 0; k < 30; k++ {
			s, _, err := sc.open(n.Editors[k%len(n.Editors)], history.KindRevise, sc.advance())
			if err != nil {
				return err
			}
			i := sc.r.IntN(s.Blocks())
			if l := s.BlockLen(i); l > 20 {
				if err := s.DeleteRange(i, l/2, 5); err != nil {
					return err
				}
			}
			if err := s.InsertText(i, 0, "Edited: "); err != nil {
				return err
			}
			if err := sc.deliver(s, sc.t.Add(time.Minute)); err != nil {
				return err
			}
		}
		return nil
	case "many-replicas":
		if err := typeAll(n.CreatedBy, 0, len(sc.gb), sc.advance()); err != nil {
			return err
		}
		return p.manyReplicas(sc)
	case "many-batches":
		if err := typeAll(n.CreatedBy, 0, len(sc.gb), sc.advance()); err != nil {
			return err
		}
		for k := 0; k < 400; k++ {
			s, _, err := sc.open(n.Editors[k%len(n.Editors)], history.KindType, sc.t.Add(time.Duration(k+1)*time.Minute))
			if err != nil {
				return err
			}
			if k%4 == 3 && s.BlockLen(0) > 10 {
				if err := s.DeleteRange(0, s.BlockLen(0)-2, 2); err != nil {
					return err
				}
			} else if _, err := s.AppendText(0, fmt.Sprintf(" %d", k), nil); err != nil {
				return err
			}
			if err := sc.deliver(s, sc.t.Add(time.Duration(k+1)*time.Minute+time.Second)); err != nil {
				return err
			}
		}
		return nil
	default:
		return typeAll(n.CreatedBy, 0, len(sc.gb), sc.t)
	}
}

// manyReplicas opens 500 sessions on 500 distinct replicas from the same
// base, has each append to one of the note's items (many choose the same
// item, so their runs land at the same spot), and delivers them all. It then
// replays the concurrent batches in reverse order on a fresh replica and
// checks the text converges to the same result.
func (p *plan) manyReplicas(sc *script) error {
	r := sc.p.src.Stream("note.replicas", uint64(sc.n.Index))
	editors := sc.n.Editors
	at := sc.advance()
	var sessions []*history.Session
	for k := 0; k < stressReplicas; k++ {
		user := editors[k%len(editors)]
		var id crdt.ReplicaID
		for id < history.MinReplicaID {
			id = crdt.ReplicaID(r.Uint64())
		}
		if err := sc.hn.AddReplica(history.Replica{ID: id, UserID: p.users[user].ID}); err != nil {
			return err
		}
		sc.extraReplicas = append(sc.extraReplicas, ReplicaRecord{ReplicaID: replicaString(id), UserID: p.users[user].ID, Kind: "device",
			Device: strPtr(fmt.Sprintf("stress-%03d", k)), RegisteredAt: ts(at)})
		s, err := sc.hn.Open(id, history.KindConcurrent, at)
		if err != nil {
			return err
		}
		sessions = append(sessions, s)
	}
	base := sc.hn.Delivered()
	for k, s := range sessions {
		item := r.IntN(4) // a few items take most of the edits
		if k%3 == 0 {
			item = r.IntN(s.Blocks())
		}
		if _, err := s.AppendText(item, fmt.Sprintf(" r%d", k), nil); err != nil {
			return err
		}
	}
	for k, s := range sessions {
		if err := sc.deliver(s, at.Add(time.Duration(k)*time.Second)); err != nil {
			return err
		}
	}
	// Convergence: the same batches, the concurrent ones reversed.
	seq, err := crdt.New(history.ServerReplica)
	if err != nil {
		return err
	}
	batches := sc.hn.Batches()
	order := make([]int, 0, len(batches))
	for i := 0; i < base; i++ {
		order = append(order, i)
	}
	for i := len(batches) - 1; i >= base; i-- {
		order = append(order, i)
	}
	for _, i := range order {
		for _, op := range batches[i].Ops {
			if err := seq.Apply(op); err != nil {
				return fmt.Errorf("reverse replay: %w", err)
			}
		}
	}
	if err := seq.CheckInvariants(); err != nil {
		return err
	}
	if seq.Text() != sc.hn.Text() {
		return fmt.Errorf("500 replicas: reverse delivery order gave a different text")
	}
	return nil
}
