package blocks

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// The shared fixtures in testdata/conformance/blocks define behaviour the
// Rust port must match. The format of each kind is in docs/spec/blocks.md
// section 7.
type fixture struct {
	Name   string          `json:"name"`
	Why    string          `json:"why"`
	Kind   string          `json:"kind"`
	Input  json.RawMessage `json:"input"`
	Expect json.RawMessage `json:"expect"`
}

type fixtureNode struct {
	Kind         string        `json:"kind"`
	Start        int           `json:"start"`
	End          int           `json:"end"`
	ContentStart int           `json:"content_start"`
	ContentEnd   int           `json:"content_end"`
	Value        string        `json:"value,omitempty"`
	Children     []fixtureNode `json:"children,omitempty"`
}

type fixtureBlock struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Indent   uint32 `json:"indent"`
	Checked  bool   `json:"checked"`
	Language string `json:"language"`
	Text     string `json:"text"`
}

type fixtureWrite struct {
	Block   string          `json:"block"`
	Key     string          `json:"key"`
	Lamport uint64          `json:"lamport"`
	Replica uint64          `json:"replica"`
	Value   json.RawMessage `json:"value"`
}

type fixtureEdit struct {
	Insert *struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	} `json:"insert"`
	Delete *struct {
		Index int `json:"index"`
		Count int `json:"count"`
	} `json:"delete"`
	SetAttrs *struct {
		Block int          `json:"block"`
		Attrs fixtureBlock `json:"attrs"`
	} `json:"set_attrs"`
}

func TestConformanceFixtures(t *testing.T) {
	files, err := filepath.Glob("../../testdata/conformance/blocks/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fx fixture
		if err := json.Unmarshal(data, &fx); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if fx.Name == "" || fx.Why == "" {
			t.Fatalf("%s: every fixture needs a name and a why", path)
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			switch fx.Kind {
			case "inline":
				runInlineFixture(t, fx)
			case "markdown_import":
				runImportFixture(t, fx)
			case "markdown_export":
				runExportFixture(t, fx)
			case "attrs":
				runAttrsFixture(t, fx)
			case "layout":
				runLayoutFixture(t, fx)
			default:
				t.Fatalf("unknown kind %q", fx.Kind)
			}
		})
	}
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func toFixtureNodes(ns []Node) []fixtureNode {
	var out []fixtureNode
	for _, n := range ns {
		out = append(out, fixtureNode{Kind: n.Kind.String(), Start: n.Start, End: n.End, ContentStart: n.ContentStart,
			ContentEnd: n.ContentEnd, Value: n.Value, Children: toFixtureNodes(n.Children)})
	}
	return out
}

func runInlineFixture(t *testing.T, fx fixture) {
	in := decode[struct{ Text string }](t, fx.Input)
	want := decode[struct {
		Nodes    []fixtureNode `json:"nodes"`
		Plain    string        `json:"plain"`
		Links    []string      `json:"links"`
		Mentions []string      `json:"mentions"`
	}](t, fx.Expect)
	nodes := mustParse(t, in.Text)
	if got := toFixtureNodes(nodes); !jsonEqual(got, want.Nodes) {
		t.Errorf("nodes\n got %s\nwant %s", mustJSON(got), mustJSON(want.Nodes))
	}
	if got := PlainText(nodes); got != want.Plain {
		t.Errorf("plain %q, want %q", got, want.Plain)
	}
	bs := []Block{{Attrs: DefaultAttrs(), Text: in.Text}}
	if got := Links(bs); !slices.Equal(got, nonNil(want.Links)) {
		t.Errorf("links %q, want %q", got, want.Links)
	}
	if got := Mentions(bs); !slices.Equal(got, nonNil(want.Mentions)) {
		t.Errorf("mentions %q, want %q", got, want.Mentions)
	}
}

func runImportFixture(t *testing.T, fx fixture) {
	in := decode[struct{ Markdown string }](t, fx.Input)
	want := decode[struct {
		Blocks []fixtureBlock `json:"blocks"`
		Export string         `json:"export"`
	}](t, fx.Expect)
	got := Import(in.Markdown)
	if fb := toFixtureBlocks(got); !jsonEqual(fb, want.Blocks) {
		t.Errorf("blocks\n got %s\nwant %s", mustJSON(fb), mustJSON(want.Blocks))
	}
	if md := Export(got); md != want.Export {
		t.Errorf("export %q, want %q", md, want.Export)
	}
}

func runExportFixture(t *testing.T, fx fixture) {
	in := decode[struct {
		Blocks []fixtureBlock `json:"blocks"`
	}](t, fx.Input)
	want := decode[struct {
		Markdown string `json:"markdown"`
		Lossless bool   `json:"lossless"`
	}](t, fx.Expect)
	bs := fromFixtureBlocks(t, in.Blocks)
	if md := Export(bs); md != want.Markdown {
		t.Errorf("markdown %q, want %q", md, want.Markdown)
	}
	if lossless := CheckLossless(bs) == nil; lossless != want.Lossless {
		t.Errorf("lossless %v, want %v", lossless, want.Lossless)
	}
	if want.Lossless && !slices.EqualFunc(Import(want.Markdown), bs, blockContentEqual) {
		t.Errorf("a lossless export must import back to the same blocks")
	}
}

func runAttrsFixture(t *testing.T, fx fixture) {
	in := decode[struct {
		Writes []fixtureWrite `json:"writes"`
	}](t, fx.Input)
	want := decode[struct {
		Blocks     map[string]fixtureBlock `json:"blocks"`
		EncodedHex string                  `json:"encoded_hex"`
	}](t, fx.Expect)
	var writes []AttrWrite
	for _, fw := range in.Writes {
		writes = append(writes, fromFixtureWrite(t, fw))
	}
	if got := hex.EncodeToString(EncodeAttrs(writes)); got != want.EncodedHex {
		t.Errorf("encoding %s, want %s", got, want.EncodedHex)
	}
	reversed := slices.Clone(writes)
	slices.Reverse(reversed)
	for _, order := range [][]AttrWrite{writes, reversed} {
		a := NewAttributes()
		for _, w := range order {
			mustApplyAttr(t, a, w)
		}
		for id, fb := range want.Blocks {
			got := a.Get(parseID(t, id))
			if typ, _ := ParseType(fb.Type); got != (Attrs{Type: typ, Indent: fb.Indent, Checked: fb.Checked, Language: fb.Language}) {
				t.Errorf("block %s: %+v, want %+v", id, got, fb)
			}
		}
	}
}

func runLayoutFixture(t *testing.T, fx fixture) {
	in := decode[struct {
		Replica uint64        `json:"replica"`
		Edits   []fixtureEdit `json:"edits"`
	}](t, fx.Input)
	want := decode[struct {
		Blocks   []fixtureBlock `json:"blocks"`
		Markdown string         `json:"markdown"`
	}](t, fx.Expect)
	d := newDoc(t, crdt.ReplicaID(in.Replica))
	for _, e := range in.Edits {
		var err error
		switch {
		case e.Insert != nil:
			_, err = d.Sequence().Insert(e.Insert.Index, e.Insert.Text)
		case e.Delete != nil:
			_, err = d.Sequence().Delete(e.Delete.Index, e.Delete.Count)
		case e.SetAttrs != nil:
			b := fromFixtureBlocks(t, []fixtureBlock{e.SetAttrs.Attrs})[0]
			_, err = d.SetAttrs(mustBlocks(t, d)[e.SetAttrs.Block].ID, b.Attrs)
		}
		if err != nil {
			t.Fatal(err)
		}
		mustValid(t, d)
	}
	bs := mustBlocks(t, d)
	if got := toFixtureBlocks(bs); !jsonEqual(got, want.Blocks) {
		t.Errorf("blocks\n got %s\nwant %s", mustJSON(got), mustJSON(want.Blocks))
	}
	if md := Export(bs); md != want.Markdown {
		t.Errorf("markdown %q, want %q", md, want.Markdown)
	}
}

func toFixtureBlocks(bs []Block) []fixtureBlock {
	out := []fixtureBlock{}
	for _, b := range bs {
		fb := fixtureBlock{Type: b.Type.String(), Indent: b.Indent, Checked: b.Checked, Language: b.Language, Text: b.Text}
		if len(b.Chars) > 0 || b.ID != crdt.Root {
			fb.ID = b.ID.String()
		}
		out = append(out, fb)
	}
	return out
}

func fromFixtureBlocks(t *testing.T, fbs []fixtureBlock) []Block {
	var out []Block
	for _, fb := range fbs {
		typ, ok := ParseType(fb.Type)
		if !ok {
			t.Fatalf("unknown type %q", fb.Type)
		}
		out = append(out, Block{Attrs: Attrs{Type: typ, Indent: fb.Indent, Checked: fb.Checked, Language: fb.Language}, Text: fb.Text})
	}
	return out
}

func fromFixtureWrite(t *testing.T, fw fixtureWrite) AttrWrite {
	w := AttrWrite{Block: parseID(t, fw.Block), Lamport: fw.Lamport, Replica: crdt.ReplicaID(fw.Replica)}
	var err error
	switch fw.Key {
	case "type":
		w.Key = AttrType
		var name string
		err = json.Unmarshal(fw.Value, &name)
		w.Type, _ = ParseType(name)
	case "indent":
		w.Key = AttrIndent
		err = json.Unmarshal(fw.Value, &w.Indent)
	case "checked":
		w.Key = AttrChecked
		err = json.Unmarshal(fw.Value, &w.Checked)
	case "language":
		w.Key = AttrLanguage
		err = json.Unmarshal(fw.Value, &w.Language)
	default:
		t.Fatalf("unknown key %q", fw.Key)
	}
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// parseID reads "replica:counter", the form of crdt.ID.String and of block
// IDs in events.
func parseID(t *testing.T, s string) crdt.ID {
	r, c, ok := strings.Cut(s, ":")
	replica, err1 := strconv.ParseUint(r, 10, 64)
	counter, err2 := strconv.ParseUint(c, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		t.Fatalf("bad id %q", s)
	}
	return crdt.ID{Replica: crdt.ReplicaID(replica), Counter: counter}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return string(data)
}

func jsonEqual(a, b any) bool { return mustJSON(a) == mustJSON(b) }
