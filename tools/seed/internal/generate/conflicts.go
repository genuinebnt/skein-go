package generate

import (
	"encoding/base64"
	"fmt"
	"sort"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Conflict scenarios: small hand-designed concurrent histories whose merged
// text was derived by hand from the spec before the engine ran them. They
// use the conformance fixture step format (docs/spec/crdt-sequence.md
// section 8) with block markers written as "\u001e", plus one extension:
//
//	{"reject": "C"}  the server refuses replica C's unsynced ops; C is
//	                 never synced to anyone afterwards
//
// The generator replays each one through libs/crdt and fails if the engine
// disagrees with the expectation, so these double as an end-to-end check of
// the engine on block-shaped text.

// ConflictStep is one step of a scenario.
type ConflictStep struct {
	Replica string            `json:"replica,omitempty"`
	Insert  *ConflictEdit     `json:"insert,omitempty"`
	Delete  *ConflictEdit     `json:"delete,omitempty"`
	Sync    any               `json:"sync,omitempty"`
	Reject  string            `json:"reject,omitempty"`
	Expect  map[string]string `json:"expect,omitempty"`
}

// ConflictEdit is a local insert or delete.
type ConflictEdit struct {
	Index int    `json:"index"`
	Text  string `json:"text,omitempty"`
	Count int    `json:"count,omitempty"`
}

// ConflictBatch is the ops a replica sent, encoded, in a valid delivery
// order for a server that applies batches one by one.
type ConflictBatch struct {
	Replica   string `json:"replica"`
	OpsBase64 string `json:"ops_base64"`
	Rejected  bool   `json:"rejected"`
}

// ConflictRecord is a line of truth/crdt/conflicts.jsonl.
type ConflictRecord struct {
	Name          string            `json:"name"`
	Why           string            `json:"why"`
	Replicas      map[string]uint64 `json:"replicas"`
	Steps         []ConflictStep    `json:"steps"`
	Expect        map[string]string `json:"expect"`
	Batches       []ConflictBatch   `json:"batches"`
	VersionVector []VersionEntry    `json:"version_vector"`
}

func ins(r string, i int, t string) ConflictStep {
	return ConflictStep{Replica: r, Insert: &ConflictEdit{Index: i, Text: t}}
}
func del(r string, i, n int) ConflictStep {
	return ConflictStep{Replica: r, Delete: &ConflictEdit{Index: i, Count: n}}
}
func syncAll() ConflictStep { return ConflictStep{Sync: "all"} }
func syncPair(from, to string) ConflictStep {
	return ConflictStep{Sync: map[string]string{"from": from, "to": to}}
}

var conflictScenarios = []ConflictRecord{
	{Name: "same-spot-typing-in-one-block",
		Why:      "Both runs are right children of the final 'o'. Siblings sort by ID and replica 101 < 202, so A's run comes first, each run contiguous (Fugue never interleaves).",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eHello"), syncAll(), ins("A", 6, " world"), ins("B", 6, " there"), syncAll()},
		Expect:   map[string]string{"all": "\u001eHello world there"}},
	{Name: "insert-inside-concurrently-deleted-word",
		Why:      "B inserts before 'brown': ' ' already has a right child ('b'), so B's text becomes the left child of 'b' and stays before it. A deletes 'brown ' (tombstones); B's 'big ' is not part of the deletion and survives.",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eThe brown fox"), syncAll(), del("A", 5, 6), ins("B", 5, "big "), syncAll()},
		Expect:   map[string]string{"all": "\u001eThe big fox"}},
	{Name: "split-block-while-other-types-at-its-end",
		Why:      "A presses Enter after 'abc' (a marker between 'c' and 'd'); B appends 'XYZ' after 'f'. B's text follows 'f' wherever 'f' ends up, which is now the second block.",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eabcdef"), syncAll(), ins("A", 4, "\u001e"), ins("B", 7, "XYZ"), syncAll()},
		Expect:   map[string]string{"all": "\u001eabc\u001edefXYZ"}},
	{Name: "merge-blocks-while-other-types-in-second",
		Why:      "A deletes the second block's marker (Backspace at its start); B appends to the second block. The marker becomes a tombstone, so B's text joins the merged block.",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eab\u001ecd"), syncAll(), del("A", 3, 1), ins("B", 6, "EF"), syncAll()},
		Expect:   map[string]string{"all": "\u001eabcdEF"}},
	{Name: "both-delete-the-same-character",
		Why:      "Deleting an already deleted element changes nothing but still counts in the version vector: both deletes are applied, the text loses one character.",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eaxy"), syncAll(), del("A", 2, 1), del("B", 2, 1), syncAll()},
		Expect:   map[string]string{"all": "\u001eay"}},
	{Name: "offline-append-meets-new-block",
		Why:      "B, offline, appends ' later' after the final 'o'; A appends a new block there. Both are right children of 'o'; A (101) sorts first, so A's whole new block precedes B's run and B's text lands at the end of A's new block, not in 'two'. A's '!' goes before the second marker, because 'e' already has that marker as its right child.",
		Replicas: map[string]uint64{"A": 101, "B": 202},
		Steps:    []ConflictStep{ins("A", 0, "\u001eone\u001etwo"), syncAll(), ins("B", 8, " later"), ins("A", 4, "!"), ins("A", 9, "\u001ethree"), syncAll()},
		Expect:   map[string]string{"all": "\u001eone!\u001etwo\u001ethree later"}},
	{Name: "revoked-replica-offline-edit-is-refused",
		Why:      "C edited offline after losing access; the server refuses its batch, so A and B never see it. The text is what A and B wrote.",
		Replicas: map[string]uint64{"A": 101, "B": 202, "C": 303},
		Steps: []ConflictStep{ins("A", 0, "\u001eplan"), syncAll(), ins("C", 5, " (C was here)"), ins("B", 5, " v2"), ConflictStep{Reject: "C"},
			syncPair("A", "B"), syncPair("B", "A"), {Expect: map[string]string{"A": "\u001eplan v2", "B": "\u001eplan v2", "C": "\u001eplan (C was here)"}}},
		Expect: map[string]string{"A": "\u001eplan v2", "B": "\u001eplan v2"}},
	{Name: "three-replicas-split-merge-and-type",
		Why:      "A splits 'abcd' after 'ab' (a new marker, left child of 'c'); B deletes the original second marker; C appends 'Z' after 'y'. The three edits touch different elements, so all survive: blocks 'ab' and 'cdxyZ'.",
		Replicas: map[string]uint64{"A": 101, "B": 202, "C": 303},
		Steps:    []ConflictStep{ins("A", 0, "\u001eabcd\u001exy"), syncAll(), ins("A", 3, "\u001e"), del("B", 5, 1), ins("C", 8, "Z"), syncAll()},
		Expect:   map[string]string{"all": "\u001eab\u001ecdxyZ"}},
}

// runConflicts replays every scenario through libs/crdt, checks each
// expectation and fills in the encoded batches and final version vector.
func runConflicts() ([]ConflictRecord, error) {
	out := make([]ConflictRecord, len(conflictScenarios))
	for si, sc := range conflictScenarios {
		names := make([]string, 0, len(sc.Replicas))
		for n := range sc.Replicas {
			names = append(names, n)
		}
		sort.Strings(names)
		seqs := map[string]*crdt.Sequence{}
		for _, n := range names {
			s, err := crdt.New(crdt.ReplicaID(sc.Replicas[n]))
			if err != nil {
				return nil, err
			}
			seqs[n] = s
		}
		rejected := map[string]bool{}
		var batches []ConflictBatch
		emitted := map[string]uint64{} // ops of each replica already emitted
		emit := func(n string, reject bool) {
			own := crdt.VersionVector{crdt.ReplicaID(sc.Replicas[n]): emitted[n]}
			var ops []crdt.Op
			for _, op := range seqs[n].OpsSince(own) {
				if op.ID.Replica == crdt.ReplicaID(sc.Replicas[n]) && op.ID.Counter > emitted[n] {
					ops = append(ops, op)
				}
			}
			if len(ops) > 0 {
				emitted[n] = ops[len(ops)-1].ID.Counter
				batches = append(batches, ConflictBatch{Replica: n, OpsBase64: base64.StdEncoding.EncodeToString(crdt.EncodeOps(ops)), Rejected: reject})
			}
		}
		syncOne := func(from, to string) error {
			if rejected[from] || rejected[to] {
				return nil
			}
			emit(from, false)
			for _, op := range seqs[from].OpsSince(seqs[to].Version()) {
				if err := seqs[to].Apply(op); err != nil {
					return err
				}
			}
			return seqs[to].CheckInvariants()
		}
		check := func(want map[string]string) error {
			for k, v := range want {
				targets := []string{k}
				if k == "all" {
					targets = nil
					for _, n := range names {
						if !rejected[n] {
							targets = append(targets, n)
						}
					}
				}
				for _, n := range targets {
					if got := seqs[n].Text(); got != v {
						return fmt.Errorf("scenario %q: replica %s has %q, want %q", sc.Name, n, got, v)
					}
				}
			}
			return nil
		}
		for _, st := range sc.Steps {
			var err error
			switch {
			case st.Insert != nil:
				_, err = seqs[st.Replica].Insert(st.Insert.Index, st.Insert.Text)
			case st.Delete != nil:
				_, err = seqs[st.Replica].Delete(st.Delete.Index, st.Delete.Count)
			case st.Reject != "":
				emit(st.Reject, true)
				rejected[st.Reject] = true
			case st.Sync == "all":
				err = func() error {
					for _, to := range names {
						for _, from := range names {
							if from != to {
								if err := syncOne(from, to); err != nil {
									return err
								}
							}
						}
					}
					return nil
				}()
			case st.Sync != nil:
				m := st.Sync.(map[string]string)
				err = syncOne(m["from"], m["to"])
			case st.Expect != nil:
				err = check(st.Expect)
			}
			if err != nil {
				return nil, fmt.Errorf("scenario %q: %w", sc.Name, err)
			}
			for _, n := range names {
				if err := seqs[n].CheckInvariants(); err != nil {
					return nil, fmt.Errorf("scenario %q: %w", sc.Name, err)
				}
			}
		}
		if err := check(sc.Expect); err != nil {
			return nil, err
		}
		rec := sc
		rec.Batches = batches
		var ref string
		for _, n := range names {
			if !rejected[n] {
				ref = n
				break
			}
		}
		rec.VersionVector = versionEntries(seqs[ref].Version())
		out[si] = rec
	}
	return out, nil
}
