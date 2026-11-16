package domain

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/trigram"
)

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

func mustApply(t testing.TB, state *NoteState, m Mutation) *NoteState {
	t.Helper()
	next, err := ApplyMutation(state, m)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestApplyMutationIsIdempotent(t *testing.T) {
	ms := []Mutation{
		{Kind: MutationCreate, NoteID: "n1", WorkspaceID: "w", Title: "Raft"},
		{Kind: MutationBlocks, NoteID: "n1", Blocks: []Block{{ID: "b1", Kind: "paragraph", Text: "leader election"}}},
		{Kind: MutationLinks, NoteID: "n1", Links: []string{"n2", "n1", "n2", ""}},
		{Kind: MutationTrash, NoteID: "n1"},
		{Kind: MutationRestore, NoteID: "n1"},
	}
	var state *NoteState
	for _, m := range ms {
		once := mustApply(t, state, m)
		twice := mustApply(t, once, m)
		if !slices.Equal(once.Blocks, twice.Blocks) || !slices.Equal(once.Links, twice.Links) || once.Title != twice.Title || once.Trashed != twice.Trashed {
			t.Fatalf("%s applied twice differs", m.Kind)
		}
		state = once
	}
	if !slices.Equal(state.Links, []string{"n2"}) {
		t.Fatalf("links %v: self-links and repeats must be dropped", state.Links)
	}
}

func TestBlocksKeepTheirOrdinalAndNewBlocksGoLast(t *testing.T) {
	s := mustApply(t, nil, Mutation{Kind: MutationBlocks, NoteID: "n", Blocks: []Block{{ID: "a", Text: "1"}, {ID: "b", Text: "2"}}})
	s = mustApply(t, s, Mutation{Kind: MutationBlocks, NoteID: "n", Blocks: []Block{{ID: "c", Text: "3"}, {ID: "a", Text: "1'"}}, RemovedBlocks: []string{"b"}})
	got := fmt.Sprint(s.Blocks)
	if got != "[{a   1' 0} {c   3 1}]" {
		t.Fatalf("blocks %s", got)
	}
}

func TestPurgedNoteIgnoresEveryLaterEvent(t *testing.T) {
	s := mustApply(t, nil, Mutation{Kind: MutationCreate, NoteID: "n", WorkspaceID: "w", Title: "secret"})
	s = mustApply(t, s, Mutation{Kind: MutationPurge, NoteID: "n"})
	s = mustApply(t, s, Mutation{Kind: MutationBlocks, NoteID: "n", Blocks: []Block{{ID: "b", Text: "secret text"}}})
	s = mustApply(t, s, Mutation{Kind: MutationCreate, NoteID: "n", WorkspaceID: "w", Title: "secret"})
	if !s.Purged || s.Title != "" || len(s.Blocks) != 0 || s.WorkspaceID != "" {
		t.Fatalf("tombstone revived: %+v", s)
	}
}

func TestApplyMutationRejectsMalformedMutations(t *testing.T) {
	for _, m := range []Mutation{
		{Kind: MutationRename},
		{Kind: 99, NoteID: "n"},
		{Kind: MutationCreate, NoteID: "n"},
		{Kind: MutationBlocks, NoteID: "n", Blocks: []Block{{Text: "x"}}},
	} {
		if _, err := ApplyMutation(nil, m); !errors.Is(err, ErrInvalidMutation) {
			t.Errorf("%+v: %v", m, err)
		}
	}
}

func TestTokenizeMatchesTheSeedTruthTokenizer(t *testing.T) {
	got := Tokenize("Raft's key-range, café 東京 x_y Ünïcode42")
	var words []string
	for _, tok := range got {
		words = append(words, tok.Text)
	}
	want := []string{"raft", "s", "key", "range", "café", "東京", "x", "y", "ünïcode42"}
	if !slices.Equal(words, want) {
		t.Fatalf("tokens %q, want %q", words, want)
	}
	if got[4].Start != 18 || got[4].End != 22 {
		t.Fatalf("café at %d..%d, want code points 18..22", got[4].Start, got[4].End)
	}
	terms := ParseTextQuery("Tombstone key-range オフライン", false)
	if len(terms) != 4 || !terms[3].Substring || terms[1].Text != "key" {
		t.Fatalf("terms %+v", terms)
	}
}

// --- randomized projection with oracles ---

var vocabulary = []string{"raft", "log", "leader", "lease", "compaction", "snapshot", "tombstone", "store", "Close", "cafe", "東京"}

type model struct {
	states map[string]*NoteState
}

func randomMutation(r *rand.Rand, notes []string) Mutation {
	note := notes[r.IntN(len(notes))]
	ws := []string{"w1", "w2"}[r.IntN(2)]
	words := func(n int) string {
		var parts []string
		for range n {
			parts = append(parts, vocabulary[r.IntN(len(vocabulary))])
		}
		return strings.Join(parts, " ")
	}
	switch r.IntN(10) {
	case 0:
		return Mutation{Kind: MutationCreate, NoteID: note, WorkspaceID: ws, Title: words(1 + r.IntN(2))}
	case 1:
		return Mutation{Kind: MutationRename, NoteID: note, Title: words(1 + r.IntN(2))}
	case 2:
		return Mutation{Kind: MutationTrash, NoteID: note}
	case 3:
		return Mutation{Kind: MutationRestore, NoteID: note}
	case 4:
		if r.IntN(4) == 0 {
			return Mutation{Kind: MutationPurge, NoteID: note}
		}
		return Mutation{Kind: MutationRoot, NoteID: note, Root: r.IntN(2) == 0}
	case 5, 6:
		var links []string
		for range r.IntN(4) {
			links = append(links, notes[r.IntN(len(notes))])
		}
		return Mutation{Kind: MutationLinks, NoteID: note, Links: links}
	default:
		m := Mutation{Kind: MutationBlocks, NoteID: note}
		for range 1 + r.IntN(3) {
			b := Block{ID: fmt.Sprint("b", r.IntN(4)), Kind: "paragraph", Text: words(r.IntN(6))}
			if r.IntN(3) == 0 {
				b.Kind, b.Language = "code", []string{"go", "rust"}[r.IntN(2)]
			}
			m.Blocks = append(m.Blocks, b)
		}
		if r.IntN(3) == 0 {
			m.RemovedBlocks = []string{fmt.Sprint("b", r.IntN(4))}
		}
		return m
	}
}

func buildRandomProjection(t *testing.T, r *rand.Rand, steps int, check bool) (*Projection, *model) {
	t.Helper()
	notes := []string{"n0", "n1", "n2", "n3", "n4", "n5"}
	p := NewProjection()
	m := &model{states: map[string]*NoteState{}}
	for step := range steps {
		mu := randomMutation(r, notes)
		next := mustApply(t, m.states[mu.NoteID], mu)
		m.states[mu.NoteID] = next
		p.Apply(mu.NoteID, next, int64(step+1))
		p.Position = int64(step + 1)
		if check {
			if err := p.CheckInvariants(); err != nil {
				t.Fatalf("step %d (%s on %s): %v", step, mu.Kind, mu.NoteID, err)
			}
		}
	}
	return p, m
}

func TestProjectionInvariantsHoldAfterEveryChange(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 1))
	for range 40 {
		buildRandomProjection(t, r, 150, true)
	}
}

func randomReadable(r *rand.Rand, m *model) Readable {
	readable := Readable{}
	for id := range m.states {
		if r.IntN(4) != 0 {
			readable[id] = true
		}
	}
	return readable
}

func oracleTextNotes(m *model, ws string, terms []Term, readable Readable) []string {
	var out []string
	for _, id := range sortedKeys(m.states) {
		n := m.states[id]
		if !n.Visible() || n.WorkspaceID != ws || !readable[id] {
			continue
		}
		texts := []string{n.Title}
		for _, b := range n.Blocks {
			texts = append(texts, b.Text)
		}
		all := true
		for _, term := range terms {
			found := false
			for _, text := range texts {
				for _, tok := range Tokenize(text) {
					found = found || tok.Text == term.Text
				}
				found = found || term.Substring && strings.Contains(strings.ToLower(text), term.Text)
			}
			all = all && found
		}
		if all {
			out = append(out, id)
		}
	}
	return out
}

func TestTextSearchFindsExactlyTheNotesHoldingEveryTermExactly(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 2))
	// Words of three letters or fewer get no typo budget, so the oracle is
	// exact matching. Longer words are tested in the typo test below.
	// raft has four letters and tolerates one edit, but no other vocabulary
	// word is that close to it, so exact matching is still the oracle.
	short := []string{"log", "東京", "raft log"}
	for range 60 {
		p, m := buildRandomProjection(t, r, 120, false)
		for _, ws := range p.WorkspaceIDs() {
			w, _ := p.Workspace(ws)
			readable := randomReadable(r, m)
			query := short[r.IntN(len(short))]
			res, err := w.SearchText(query, readable, Filters{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, mt := range res.Matches {
				got = append(got, mt.Note)
				if !readable[mt.Note] {
					t.Fatalf("unreadable note %s returned", mt.Note)
				}
				w.RenderText(res, mt)
			}
			slices.Sort(got)
			want := oracleTextNotes(m, ws, ParseTextQuery(query, false), readable)
			if !slices.Equal(got, want) {
				t.Fatalf("query %q: notes %v, oracle %v", query, got, want)
			}
		}
	}
}

func TestTextSearchToleratesTyposAndRanksExactMatchesFirst(t *testing.T) {
	p := NewProjection()
	seq := int64(0)
	apply := func(ms ...Mutation) {
		for _, mu := range ms {
			seq++
			ws, _ := p.WorkspaceOf(mu.NoteID)
			var cur *NoteState
			if ws != nil {
				cur, _ = ws.Note(mu.NoteID)
			}
			p.Apply(mu.NoteID, mustApply(t, cur, mu), seq)
		}
	}
	apply(
		Mutation{Kind: MutationCreate, NoteID: "a", WorkspaceID: "w", Title: "Compaction"},
		Mutation{Kind: MutationBlocks, NoteID: "a", Blocks: []Block{{ID: "1", Text: "Leveled compaction merges runs."}}},
		Mutation{Kind: MutationCreate, NoteID: "b", WorkspaceID: "w", Title: "Notes"},
		Mutation{Kind: MutationBlocks, NoteID: "b", Blocks: []Block{{ID: "1", Text: "we discussed compactions briefly"}}},
		Mutation{Kind: MutationCreate, NoteID: "c", WorkspaceID: "w", Title: "Unrelated"},
		Mutation{Kind: MutationBlocks, NoteID: "c", Blocks: []Block{{ID: "1", Text: "zebra crossing"}}},
	)
	w, _ := p.Workspace("w")
	all := Readable{"a": true, "b": true, "c": true}
	res, err := w.SearchText("copmaction", all, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	// One transposition reaches compaction; compactions is two edits away,
	// inside the ten-letter budget, and ranks below.
	if len(res.Matches) != 2 || res.Matches[0].Note != "a" || res.Matches[1].Note != "b" {
		t.Fatalf("transposition typo: %+v", res.Matches)
	}
	res, _ = w.SearchText("compaction", all, Filters{})
	if len(res.Matches) != 2 || res.Matches[0].Note != "a" || res.Matches[1].Note != "b" {
		t.Fatalf("exact before one-edit variant: %+v", res.Matches)
	}
	hit := w.RenderText(res, res.Matches[0])
	if hit.Snippet != "Leveled compaction merges runs." || len(hit.Highlights) != 1 || hit.Highlights[0] != (trigram.Span{Start: 8, End: 18}) {
		t.Fatalf("hit %+v", hit)
	}
	res, _ = w.SearchText("compaction", Readable{"b": true}, Filters{})
	if len(res.Matches) != 1 || res.Matches[0].Note != "b" || res.Candidates != 1 {
		t.Fatalf("permission filter: %+v", res)
	}
	res, _ = w.SearchText("Compaction", all, Filters{CaseSensitive: true})
	if len(res.Matches) != 1 || res.Matches[0].Note != "a" {
		t.Fatalf("case-sensitive: %+v", res.Matches)
	}
}

func TestRegexSearchMatchesBruteForceOverReadableBlocks(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 3))
	patterns := []string{`lea\w+`, `\bClose\b`, `raft (log|lease)`, `東.`, `o`}
	for range 60 {
		p, m := buildRandomProjection(t, r, 120, false)
		for _, ws := range p.WorkspaceIDs() {
			w, _ := p.Workspace(ws)
			readable := randomReadable(r, m)
			pattern := patterns[r.IntN(len(patterns))]
			f := Filters{CaseSensitive: r.IntN(2) == 0, CodeOnly: r.IntN(4) == 0}
			re, err := trigram.CompileRegexp(pattern, f.CaseSensitive)
			if err != nil {
				t.Fatal(err)
			}
			res := w.SearchRegex(re, readable, f)
			full := pattern
			if !f.CaseSensitive {
				full = "(?i)" + full
			}
			std := regexp.MustCompile(full)
			var want []string
			for _, id := range sortedKeys(m.states) {
				n := m.states[id]
				if !n.Visible() || n.WorkspaceID != ws || !readable[id] {
					continue
				}
				for _, b := range n.Blocks {
					if f.allows(b) {
						for _, loc := range std.FindAllStringIndex(b.Text, -1) {
							s := utf8.RuneCountInString(b.Text[:loc[0]])
							want = append(want, fmt.Sprint(id, "/", b.ID, "@", s))
						}
					}
				}
			}
			var got []string
			for _, mt := range res.Matches {
				for _, s := range mt.Spans {
					got = append(got, fmt.Sprint(mt.Ref.Note, "/", mt.Ref.Block, "@", s.Start))
				}
				w.RenderRegex(mt)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pattern %q: %v, oracle %v", full, got, want)
			}
		}
	}
}

func TestSnapshotRoundTripsTheProjection(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 4))
	for range 30 {
		p, _ := buildRandomProjection(t, r, 200, false)
		data, err := EncodeSnapshot(p)
		if err != nil {
			t.Fatal(err)
		}
		back, err := DecodeSnapshot(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := back.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		if !EqualContent(p, back) || back.Position != p.Position {
			t.Fatal("snapshot changed the projection's content")
		}
		again, _ := EncodeSnapshot(back)
		if !bytes.Equal(again, data) {
			t.Fatal("re-encoding a decoded snapshot differs")
		}
		for n := 0; n < len(data); n += 1 + len(data)/50 {
			if _, err := DecodeSnapshot(data[:n]); !errors.Is(err, ErrCorruptSnapshot) {
				t.Fatalf("truncated to %d bytes: %v", n, err)
			}
		}
	}
}

func TestSuggestTitlesCompletesPrefixesAndToleratesTypos(t *testing.T) {
	p := NewProjection()
	for i, title := range []string{"Raft log compaction", "Raft leader election", "Rafting trip", "Consensus overview"} {
		id := fmt.Sprint("n", i)
		p.Apply(id, &NoteState{ID: id, WorkspaceID: "w", Title: title}, int64(i+1))
	}
	w, _ := p.Workspace("w")
	all := Readable{"n0": true, "n1": true, "n2": true, "n3": true}
	got := w.SuggestTitles("raft l", all, 10)
	if len(got) != 2 || got[0].Title != "Raft leader election" || got[1].Title != "Raft log compaction" || got[0].Distance != 0 {
		t.Fatalf("exact prefixes %+v", got)
	}
	got = w.SuggestTitles("raft lgo", all, 10)
	if len(got) == 0 || got[0].Title != "Raft log compaction" || got[0].Distance != 1 {
		t.Fatalf("typo prefix %+v", got)
	}
	got = w.SuggestTitles("raft l", Readable{"n1": true}, 10)
	if len(got) != 1 || got[0].Note != "n1" {
		t.Fatalf("permission filter %+v", got)
	}
}

func TestLinkGraphHoldsOnlyReadableVisibleNotes(t *testing.T) {
	p := NewProjection()
	p.Apply("a", &NoteState{ID: "a", WorkspaceID: "w", Links: []string{"b", "c", "x"}}, 1)
	p.Apply("b", &NoteState{ID: "b", WorkspaceID: "w", Links: []string{"c"}}, 2)
	p.Apply("c", &NoteState{ID: "c", WorkspaceID: "w", Trashed: true}, 3)
	w, _ := p.Workspace("w")
	g, err := w.LinkGraph(Readable{"a": true, "c": true}.Has)
	if err != nil {
		t.Fatal(err)
	}
	if g.Len() != 1 || g.EdgeCount() != 0 {
		t.Fatalf("graph %s", g)
	}
	before := w.GraphVersion
	p.Apply("b", &NoteState{ID: "b", WorkspaceID: "w", Title: "renamed", Links: []string{"c"}}, 4)
	if w.GraphVersion != before {
		t.Fatal("a rename changed the graph version")
	}
	p.Apply("b", &NoteState{ID: "b", WorkspaceID: "w", Links: nil}, 5)
	if w.GraphVersion == before {
		t.Fatal("a link change kept the graph version")
	}
}
