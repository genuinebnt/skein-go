package crdt

import "fmt"

// CheckInvariants verifies every structural invariant listed in the package
// documentation (I1 to I7) and returns an error wrapping ErrInvariant for the
// first one that fails.
//
// It costs O(n log n) and is meant for tests, fuzzing and debug builds, not
// for production hot paths. The Rust port should expose the same check (for
// example as `fn check_invariants(&self) -> Result<(), Error>`) and its tests
// should call it as often as these do.
func (s *Sequence) CheckInvariants() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
	}

	// I1: the root.
	if s.root.id != Root || s.root.parent != nil || s.root.prev != nil {
		return fail("I1: root has an id, parent or predecessor")
	}
	if len(s.root.left) != 0 {
		return fail("I1: root has %d left children", len(s.root.left))
	}
	if s.nodes[Root] != s.root {
		return fail("I1: root missing from the node index")
	}

	// I2, I3, and the in-order traversal used by I4. The walk keeps its own
	// stack: typing builds a chain as deep as the note is long, and a
	// recursive walk overflows small fixed stacks (TinyGo, WASM) on large
	// notes. Each node's children are checked when the node is entered,
	// before its left subtree, as the recursive form did, so the first
	// error reported is the same.
	order, err := s.inOrder(fail)
	if err != nil {
		return err
	}
	if len(order) != len(s.nodes) {
		return fail("I4: tree has %d nodes, index has %d", len(order), len(s.nodes))
	}

	// I4: the list matches the traversal, and prev/next agree.
	var prev *node
	i := 0
	visible := 0
	for n := s.root; n != nil; n = n.next {
		if i >= len(order) || order[i] != n {
			return fail("I4: list and tree disagree at position %d", i)
		}
		if n.prev != prev {
			return fail("I4: node %s has a wrong prev pointer", n.id)
		}
		if s.nodes[n.id] != n {
			return fail("I4: node %s missing from the node index", n.id)
		}
		if n != s.root && !n.deleted {
			visible++
		}
		prev = n
		i++
	}
	if i != len(order) {
		return fail("I4: list has %d nodes, tree has %d", i, len(order))
	}

	// I5: the visible count.
	if visible != s.visible {
		return fail("I5: counted %d visible characters, cached %d", visible, s.visible)
	}

	// I6 and I7: the log and the clocks.
	seen := VersionVector{}
	for pos, op := range s.log {
		if op.ID.Counter != seen[op.ID.Replica]+1 {
			return fail("I6: log position %d holds %s out of counter order", pos, op.ID)
		}
		seen[op.ID.Replica] = op.ID.Counter
		if s.logPos[op.ID] != pos {
			return fail("I6: op %s indexed at the wrong log position", op.ID)
		}
		if op.Kind == OpInsert {
			if _, ok := s.nodes[op.ID]; !ok {
				return fail("I7: insert %s has no node", op.ID)
			}
		}
		if op.Lamport > s.lamport {
			return fail("I7: op %s has lamport %d above the clock %d", op.ID, op.Lamport, s.lamport)
		}
	}
	if !seen.Equal(s.vv) {
		return fail("I6: version vector does not match the log")
	}
	if len(s.logPos) != len(s.log) {
		return fail("I6: log index has %d entries, log has %d", len(s.logPos), len(s.log))
	}
	return nil
}

// walkFrame is one node on inOrder's explicit stack.
type walkFrame struct {
	n     *node
	phase uint8 // 0: enter, 1: left children, 2: right children
	next  int   // next child to visit in the current phase
}

// inOrder returns every node in traversal order (left subtree, node, right
// subtree), checking I2 and I3 on entry to each node. It uses O(depth) heap
// memory and no recursion.
func (s *Sequence) inOrder(fail func(format string, args ...any) error) ([]*node, error) {
	order := make([]*node, 0, len(s.nodes))
	stack := []walkFrame{{n: s.root}}
	for len(stack) > 0 {
		top := len(stack) - 1
		f := &stack[top]
		switch f.phase {
		case 0:
			for side, children := range [2][]*node{f.n.left, f.n.right} {
				for i, c := range children {
					if c.parent != f.n || int(c.side) != side {
						return nil, fail("I2: node %s has wrong parent or side", c.id)
					}
					if i > 0 && !children[i-1].id.Less(c.id) {
						return nil, fail("I3: children of %s not strictly ascending at %s", f.n.id, c.id)
					}
				}
			}
			f.phase = 1
		case 1:
			if f.next < len(f.n.left) {
				child := f.n.left[f.next]
				f.next++
				stack = append(stack, walkFrame{n: child})
				continue
			}
			order = append(order, f.n)
			f.phase, f.next = 2, 0
		case 2:
			if f.next < len(f.n.right) {
				child := f.n.right[f.next]
				f.next++
				stack = append(stack, walkFrame{n: child})
				continue
			}
			stack = stack[:top]
		}
	}
	return order, nil
}
