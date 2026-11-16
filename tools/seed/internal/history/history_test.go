package history

import (
	"errors"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
)

var t0 = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

func testSeed(t *testing.T) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		return s
	}
	return rand.Uint64()
}

func newNote(t *testing.T, ids ...crdt.ReplicaID) *Note {
	t.Helper()
	n := NewNote()
	for _, id := range ids {
		if err := n.AddReplica(Replica{ID: id, UserID: "u" + strconv.FormatUint(uint64(id), 10)}); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func open(t *testing.T, n *Note, id crdt.ReplicaID, kind Kind) *Session {
	t.Helper()
	s, err := n.Open(id, kind, t0)
	if err != nil {
		t.Fatalf("Open(%d): %v", id, err)
	}
	return s
}

func deliver(t *testing.T, n *Note, s *Session) {
	t.Helper()
	if _, err := s.Deliver(t0); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := n.Verify(); err != nil {
		t.Fatalf("Verify after delivery %d: %v", n.Delivered(), err)
	}
}

const (
	rA = MinReplicaID + 1
	rB = MinReplicaID + 2
	rC = MinReplicaID + 3
)

// oracleText lays out the oracle's block texts the way the note does.
func oracleText(texts [][]rune) string {
	var b strings.Builder
	for _, t := range texts {
		b.WriteRune(blocks.Marker)
		b.WriteString(string(t))
	}
	return b.String()
}

func TestSequentialSessionsMatchRuneOracle(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	r := rand.New(rand.NewPCG(seed, seed^0x5EED))
	alphabet := []rune("abcxyz é漢🙂")
	word := func() string {
		out := make([]rune, 1+r.IntN(6))
		for i := range out {
			out[i] = alphabet[r.IntN(len(alphabet))]
		}
		return string(out)
	}
	n := newNote(t, rA, rB, rC)
	var oracle [][]rune
	// At most one draft is tracked, and only while it is the tail of its
	// block: any edit that could move text after it or reshape blocks
	// forgets it, so the oracle can remove it by length.
	var draft *Draft
	draftBlock := -1
	for step := 0; step < 120; step++ {
		s := open(t, n, []crdt.ReplicaID{rA, rB, rC}[r.IntN(3)], KindType)
		if len(oracle) == 0 || r.IntN(4) == 0 {
			text := word()
			if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, text, nil); err != nil {
				t.Fatal(err)
			}
			oracle = append(oracle, []rune(text))
		} else {
			i := r.IntN(len(oracle))
			switch r.IntN(5) {
			case 0:
				text := word()
				var typos []Typo
				if rs := []rune(text); len(rs) > 1 && rs[1] != 'Q' {
					typos = []Typo{{Offset: 1, Wrong: 'Q'}}
				}
				if _, err := s.AppendText(i, text, typos); err != nil {
					t.Fatal(err)
				}
				oracle[i] = append(oracle[i], []rune(text)...)
				if i == draftBlock {
					draft, draftBlock = nil, -1
				}
			case 1:
				if len(oracle[i]) > 0 {
					off := r.IntN(len(oracle[i]))
					cnt := 1 + r.IntN(len(oracle[i])-off)
					if err := s.DeleteRange(i, off, cnt); err != nil {
						t.Fatal(err)
					}
					oracle[i] = append(oracle[i][:off:off], oracle[i][off+cnt:]...)
					draft, draftBlock = nil, -1
				}
			case 2:
				off := r.IntN(len(oracle[i]) + 1)
				if err := s.Split(i, off, blocks.Block{Type: blocks.Bulleted}); err != nil {
					t.Fatal(err)
				}
				tail := append([]rune(nil), oracle[i][off:]...)
				oracle[i] = oracle[i][:off:off]
				oracle = append(oracle[:i+1], append([][]rune{tail}, oracle[i+1:]...)...)
				draft, draftBlock = nil, -1
			case 3:
				if i > 0 {
					if err := s.Merge(i); err != nil {
						t.Fatal(err)
					}
					oracle[i-1] = append(oracle[i-1], oracle[i]...)
					oracle = append(oracle[:i], oracle[i+1:]...)
					draft, draftBlock = nil, -1
				}
			case 4:
				if draft != nil {
					if err := s.DeleteDraft(*draft); err != nil {
						t.Fatal(err)
					}
					oracle[draftBlock] = oracle[draftBlock][:len(oracle[draftBlock])-draft.Len]
					draft, draftBlock = nil, -1
				} else {
					text := word()
					d, err := s.InsertDraft(i, text)
					if err != nil {
						t.Fatal(err)
					}
					oracle[i] = append(oracle[i], []rune(text)...)
					draft, draftBlock = &d, i
				}
			}
		}
		deliver(t, n, s)
		if got, want := n.Text(), oracleText(oracle); got != want {
			t.Fatalf("step %d: text %q, oracle %q", step, got, want)
		}
		if _, texts, err := n.FinalBlocks(); err != nil || len(texts) != len(oracle) {
			t.Fatalf("step %d: FinalBlocks: %v (%d blocks, oracle %d)", step, err, len(texts), len(oracle))
		}
	}
}

func TestConcurrentAppendsAtSameSpotStayContiguousLowerReplicaFirst(t *testing.T) {
	n := newNote(t, rA, rB)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "base", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	// Both sessions open on the same base, so their appends are concurrent.
	sb := open(t, n, rB, KindConcurrent)
	sa := open(t, n, rA, KindConcurrent)
	if _, err := sb.AppendText(0, " bee", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sa.AppendText(0, " ant", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, sb)
	deliver(t, n, sa)
	// Both runs are right children of 'e'; siblings sort by ID, and rA < rB.
	if got, want := n.Text(), "\x1ebase ant bee"; got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
}

func TestOfflineSessionMergesAfterLaterOnlineSessions(t *testing.T) {
	n := newNote(t, rA, rB)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "two", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	offline := open(t, n, rB, KindOffline)
	for i := 0; i < 3; i++ {
		online := open(t, n, rA, KindType)
		if _, err := online.AppendText(1, "!", nil); err != nil {
			t.Fatal(err)
		}
		deliver(t, n, online)
	}
	if _, err := offline.AppendText(0, " (offline)", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, offline)
	if got, want := n.Text(), "\x1eone (offline)\x1etwo!!!"; got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
}

func TestStructuralEditInStaleSessionIsRefused(t *testing.T) {
	n := newNote(t, rA, rB)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "x", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	stale := open(t, n, rB, KindType)
	fresh := open(t, n, rA, KindType)
	if _, err := fresh.AppendText(0, "y", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, fresh)
	if err := stale.Split(0, 0, blocks.Block{Type: blocks.Paragraph}); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Deliver(t0); !errors.Is(err, ErrStaleStructure) {
		t.Fatalf("Deliver = %v, want ErrStaleStructure", err)
	}
}

func TestRejectedBatchIsNotAppliedAndRetiresReplica(t *testing.T) {
	n := newNote(t, rA, rB)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "keep", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	bad := open(t, n, rB, KindOffline)
	if _, err := bad.AppendText(0, " not allowed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Reject(t0, "access_revoked"); err != nil {
		t.Fatal(err)
	}
	if got := n.Text(); got != "\x1ekeep" {
		t.Fatalf("text %q after rejection", got)
	}
	if len(n.Rejected()) != 1 || n.Rejected()[0].Reason != "access_revoked" {
		t.Fatalf("rejected = %+v", n.Rejected())
	}
	if _, err := n.Open(rB, KindType, t0); !errors.Is(err, ErrDeadReplica) {
		t.Fatalf("Open after reject = %v, want ErrDeadReplica", err)
	}
	if err := n.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestTypoIsTypedDeletedAndCorrected(t *testing.T) {
	n := newNote(t, rA)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "the cat", []Typo{{Offset: 1, Wrong: 'e'}, {Offset: 5, Wrong: 'x'}}); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	b := n.Batches()[0]
	if n.Text() != "\x1ethe cat" || b.Deletes != 2 || b.Inserts != 10 {
		t.Fatalf("text %q, %d inserts, %d deletes", n.Text(), b.Inserts, b.Deletes)
	}
}

func TestReplayRejectsTruncatedFrame(t *testing.T) {
	n := newNote(t, rA)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	file, _ := FrameBatches(n.Batches())
	if _, err := Replay(file[:len(file)-1]); err == nil {
		t.Fatal("Replay accepted a truncated file")
	}
}

func TestDeleteRangeRefusesToRemoveMarker(t *testing.T) {
	n := newNote(t, rA)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "ab", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRange(0, 1, 2); !errors.Is(err, ErrBadEdit) {
		t.Fatalf("DeleteRange past block end = %v, want ErrBadEdit", err)
	}
}

func TestIdleReplicasDropTheirCopyAndRebuildIdentically(t *testing.T) {
	n := newNote(t, rA, rB)
	s := open(t, n, rA, KindType)
	if _, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, "hello", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	if c := n.ReplicaCopies(); c != 0 {
		t.Fatalf("%d replicas still hold a copy after delivery", c)
	}
	// Reopening rebuilds the replica, and its next op continues its own
	// counter instead of reusing one.
	s = open(t, n, rA, KindType)
	if _, err := s.AppendText(0, " again", nil); err != nil {
		t.Fatal(err)
	}
	deliver(t, n, s)
	if got := n.Version().Get(rA); got != 12 {
		t.Fatalf("replica counter %d after 12 ops", got)
	}
	if n.Text() != "\x1ehello again" {
		t.Fatalf("text %q", n.Text())
	}
}
