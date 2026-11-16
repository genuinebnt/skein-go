package domain

import (
	"fmt"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Inverse generates, as local edits of doc's own replica, the operations
// that undo ops (one change group, or one undo batch for a redo), and
// applies them to doc:
//
//   - each element ops inserted that is still visible is deleted;
//   - each element ops deleted that is still deleted is inserted again, as
//     a copy at the position it had, and a copied block marker gets the
//     original block's attributes.
//
// Elements other replicas changed since are left alone: something the group
// inserted and someone else already deleted stays deleted.
//
// The CRDT has no restore operation yet (crdt-sequence.md section 10), so a
// deleted element comes back as a new element with a new ID. Consequences,
// all listed as a CRDT follow-up: concurrent inserts anchored inside the
// deleted text stay where they were, next to the tombstones; cursors,
// comments and links anchored to the old IDs do not move to the copies; and
// blame credits the copies to whoever undid. Elements both inserted and
// deleted inside ops cancel out and are not copied.
func Inverse(doc *blocks.Document, ops []crdt.Op) ([]crdt.Op, []blocks.AttrWrite, error) {
	elems, err := doc.Elements()
	if err != nil {
		return nil, nil, err
	}
	inserted := map[crdt.ID]bool{}
	deleted := map[crdt.ID]bool{}
	for _, op := range ops {
		if op.Kind == crdt.OpInsert {
			inserted[op.ID] = true
		} else {
			deleted[op.Target] = true
		}
	}

	type run struct {
		pos       int
		values    []rune
		originals []crdt.ID
	}
	var runs []run
	visibleNow := map[crdt.ID]bool{}
	visible, open := 0, false
	for _, e := range elems {
		switch {
		case !e.Deleted:
			visibleNow[e.ID] = true
			visible++
			open = false
		case deleted[e.ID] && !inserted[e.ID]:
			if !open {
				runs = append(runs, run{pos: visible})
				open = true
			}
			r := &runs[len(runs)-1]
			r.values = append(r.values, e.Value)
			r.originals = append(r.originals, e.ID)
		}
		// Other tombstones do not break a run: the copy goes where the
		// deleted text was, whatever was deleted around it.
	}

	seq := doc.Sequence()
	var out []crdt.Op
	var writes []blocks.AttrWrite
	shift := 0
	for _, r := range runs {
		copies, err := seq.Insert(r.pos+shift, string(r.values))
		if err != nil {
			return out, writes, fmt.Errorf("undo: reinsert: %w", err)
		}
		out = append(out, copies...)
		shift += len(r.values)
		for k, v := range r.values {
			if v != blocks.Marker {
				continue
			}
			ws, err := doc.SetAttrs(copies[k].ID, doc.Attributes().Get(r.originals[k]))
			if err != nil {
				return out, writes, err
			}
			writes = append(writes, ws...)
		}
	}
	for _, op := range ops {
		if op.Kind != crdt.OpInsert || !visibleNow[op.ID] {
			continue
		}
		del := crdt.Op{
			ID:      crdt.ID{Replica: seq.Replica(), Counter: seq.Version().Get(seq.Replica()) + 1},
			Lamport: seq.Lamport() + 1,
			Kind:    crdt.OpDelete,
			Target:  op.ID,
		}
		if err := seq.Apply(del); err != nil {
			return out, writes, fmt.Errorf("undo: delete: %w", err)
		}
		out = append(out, del)
	}
	return out, writes, nil
}
