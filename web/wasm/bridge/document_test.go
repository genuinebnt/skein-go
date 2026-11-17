package bridge

import (
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/blocks"
)

func newDoc(t *testing.T, replica uint64) *Document {
	t.Helper()
	d, err := NewDocument(replica)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func check(t *testing.T, d *Document) {
	t.Helper()
	if err := d.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func mustInsert(t *testing.T, d *Document, pos int, text string) []byte {
	t.Helper()
	batch, err := d.Insert(pos, text)
	if err != nil {
		t.Fatal(err)
	}
	check(t, d)
	return batch
}

func TestBlocksReportPositionsAfterTheirMarkers(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eTitle\u001eBody 🙂 text")
	bs, err := d.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 {
		t.Fatalf("got %d blocks, want 2", len(bs))
	}
	if bs[0].Start != 1 || bs[0].Text != "Title" {
		t.Fatalf("first block %+v", bs[0])
	}
	if bs[1].Start != 7 || bs[1].Text != "Body 🙂 text" {
		t.Fatalf("second block %+v", bs[1])
	}
	if bs[0].ID != "1:1" || bs[1].ID != "1:7" {
		t.Fatalf("block ids %s %s, want the marker op ids", bs[0].ID, bs[1].ID)
	}
}

func TestRemoteBatchesConvergeInAnyDeliveryOrder(t *testing.T) {
	a, b := newDoc(t, 1), newDoc(t, 2)
	first := mustInsert(t, a, 0, "\u001ehello")
	second := mustInsert(t, a, 6, " world")
	// Deliver out of order: the buffer holds the second until the first lands.
	pending, err := b.ApplyRemote(second)
	if err != nil {
		t.Fatal(err)
	}
	if pending == 0 {
		t.Fatal("expected the out-of-order batch to wait")
	}
	if _, err := b.ApplyRemote(first); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyRemote(first); err != nil { // repeated delivery is a no-op
		t.Fatal(err)
	}
	check(t, b)
	if a.Text() != b.Text() || b.PendingRemote() != 0 {
		t.Fatalf("diverged: %q vs %q, %d pending", a.Text(), b.Text(), b.PendingRemote())
	}
}

func TestOpsSinceSendsOnlyWhatThePeerLacks(t *testing.T) {
	a, b := newDoc(t, 1), newDoc(t, 2)
	if _, err := b.ApplyRemote(mustInsert(t, a, 0, "\u001eabc")); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, a, 4, "def")
	missing := a.OpsSince(b.Version())
	summary, err := DescribeOps(missing)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Inserted != "def" {
		t.Fatalf("peer lacks %q, want %q", summary.Inserted, "def")
	}
}

func TestIDAtAndIndexOfAnchorACursorAcrossRemoteEdits(t *testing.T) {
	a, b := newDoc(t, 1), newDoc(t, 2)
	if _, err := b.ApplyRemote(mustInsert(t, a, 0, "\u001eabcdef")); err != nil {
		t.Fatal(err)
	}
	anchor, err := b.IDAt(4) // the 'd'
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ApplyRemote(mustInsert(t, a, 1, "XYZ")); err != nil {
		t.Fatal(err)
	}
	index, visible, ok := b.IndexOf(anchor)
	if !ok || !visible || index != 7 {
		t.Fatalf("anchor at %d visible=%v ok=%v, want 7 true true", index, visible, ok)
	}
}

func TestSeedAttrsFromMarkdownRestoresBlockTypesAndLosesToRealWrites(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eHeading\u001eitem one\u001efn main() {}")
	matched, err := d.SeedAttrsFromMarkdown("## Heading\n\n- item one\n\n```rust\nfn main() {}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	check(t, d)
	if matched != 3 {
		t.Fatalf("matched %d blocks, want 3", matched)
	}
	bs, _ := d.Blocks()
	if bs[0].Attrs.Type != blocks.Heading2 || bs[1].Attrs.Type != blocks.Bulleted || bs[2].Attrs.Type != blocks.Code || bs[2].Attrs.Language != "rust" {
		t.Fatalf("attrs not seeded: %+v", bs)
	}
	want := bs[0].Attrs
	want.Type = blocks.Heading1
	batch, err := d.SetAttrs(bs[0].ID, want)
	if err != nil {
		t.Fatal(err)
	}
	check(t, d)
	writes, err := blocks.DecodeAttrs(batch)
	if err != nil || len(writes) != 1 {
		t.Fatalf("got %d writes (%v), want 1", len(writes), err)
	}
	if bs, _ = d.Blocks(); bs[0].Attrs.Type != blocks.Heading1 {
		t.Fatalf("a real write must beat the baseline, got %v", bs[0].Attrs.Type)
	}
}

func TestSeedAttrsMatchesByTextWhenBlockCountsDiffer(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eone\u001etwo")
	matched, err := d.SeedAttrsFromMarkdown("# one\n\nextra\n\n> two\n")
	if err != nil {
		t.Fatal(err)
	}
	bs, _ := d.Blocks()
	if matched != 2 || bs[0].Attrs.Type != blocks.Heading1 || bs[1].Attrs.Type != blocks.Quote {
		t.Fatalf("matched %d: %+v", matched, bs)
	}
}

func TestMarkdownAtShowsTheNoteAsItWas(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eSnapshots bound the log.")
	before := d.Version()
	mustInsert(t, d, 24, " and let followers catch up")
	then, err := d.MarkdownAt(before)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(then) != "Snapshots bound the log." {
		t.Fatalf("got %q", then)
	}
	now, _ := d.Markdown()
	if !strings.Contains(now, "catch up") {
		t.Fatalf("current markdown %q", now)
	}
}

func TestReplaceWithMarkdownRestoresAnEarlierVersionAsOneChange(t *testing.T) {
	a, b := newDoc(t, 1), newDoc(t, 2)
	if _, err := b.ApplyRemote(mustInsert(t, a, 0, "\u001eOld heading\u001eold body")); err != nil {
		t.Fatal(err)
	}
	ops, attrs, err := a.ReplaceWithMarkdown("## Restored\n\n- item\n")
	if err != nil {
		t.Fatal(err)
	}
	check(t, a)
	if _, err := b.ApplyRemote(ops); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyRemoteAttrs(attrs); err != nil {
		t.Fatal(err)
	}
	check(t, b)
	got, _ := b.Markdown()
	if strings.TrimSpace(got) != "## Restored\n\n- item" {
		t.Fatalf("peer sees %q", got)
	}
}

func TestExportMappedNamesTheElementBehindEveryCodePoint(t *testing.T) {
	a, b := newDoc(t, 1), newDoc(t, 2)
	if _, err := b.ApplyRemote(mustInsert(t, a, 0, "\u001eab")); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, b, 3, "c")
	markdown, ids, err := b.ExportMapped()
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(markdown) != len(ids) {
		t.Fatalf("%d code points but %d ids", utf8.RuneCountInString(markdown), len(ids))
	}
	if !strings.HasPrefix(ids[0], "1:") || ids[2] != "2:1" {
		t.Fatalf("ids %v for %q", ids, markdown)
	}
}

func TestDescribeOpsTurnsMarkersIntoNewlinesAndCountsKinds(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eab")
	batch, err := d.Delete(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	s, err := DescribeOps(batch)
	if err != nil || s.Deletes != 1 || s.Inserts != 0 {
		t.Fatalf("summary %+v err %v", s, err)
	}
	s, _ = DescribeOps(mustInsert(t, d, 2, "\u001exy"))
	if s.Inserted != "\nxy" || s.Inserts != 3 {
		t.Fatalf("summary %+v", s)
	}
}

func TestMalformedInputIsRefusedWithoutChangingTheNote(t *testing.T) {
	d := newDoc(t, 1)
	mustInsert(t, d, 0, "\u001eabc")
	if _, err := d.ApplyRemote([]byte{0x53, 0x4b, 0x01, 0x05}); err == nil {
		t.Fatal("expected a corrupt batch to fail")
	}
	if _, err := d.Insert(99, "x"); err == nil {
		t.Fatal("expected an out-of-range insert to fail")
	}
	if _, err := d.SetAttrs("nope", blocks.DefaultAttrs()); err == nil {
		t.Fatal("expected a malformed block id to fail")
	}
	check(t, d)
	if d.Text() != "\u001eabc" {
		t.Fatalf("text changed to %q", d.Text())
	}
}

// TestRandomEditsConvergeThroughTheBridge drives three replicas with random
// local edits and random partial delivery, then syncs everything and checks
// they agree. Replay a failure with SKEIN_SEED.
func TestRandomEditsConvergeThroughTheBridge(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("SKEIN_SEED"); s != "" {
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = parsed
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	rng := rand.New(rand.NewSource(seed))
	docs := []*Document{newDoc(t, 11), newDoc(t, 22), newDoc(t, 33)}
	alphabet := []string{"a", "b", "🙂", "漢", "\u001e", " "}
	for step := 0; step < 300; step++ {
		d := docs[rng.Intn(len(docs))]
		n := utf8.RuneCountInString(d.Text())
		var batch []byte
		var err error
		if n > 0 && rng.Intn(3) == 0 {
			pos := rng.Intn(n)
			batch, err = d.Delete(pos, 1+rng.Intn(min(3, n-pos)))
		} else {
			batch, err = d.Insert(rng.Intn(n+1), alphabet[rng.Intn(len(alphabet))])
		}
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		check(t, d)
		for _, other := range docs {
			if other != d && rng.Intn(2) == 0 {
				if _, err := other.ApplyRemote(batch); err != nil {
					t.Fatalf("step %d: %v", step, err)
				}
				check(t, other)
			}
		}
	}
	for _, from := range docs {
		for _, to := range docs {
			if _, err := to.ApplyRemote(from.OpsSince(to.Version())); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, d := range docs[1:] {
		check(t, d)
		if d.Text() != docs[0].Text() {
			t.Fatalf("diverged:\n%q\n%q", d.Text(), docs[0].Text())
		}
	}
}
