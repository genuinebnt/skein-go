package crdt

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// node is one element of the sequence: the root sentinel or one character.
//
// Nodes are never removed. A deleted character keeps its node, marked as a
// tombstone, because operations generated concurrently with the delete may
// still use it as a parent.
type node struct {
	id      ID
	value   rune
	deleted bool

	// Tree view: defines the order of the text.
	parent *node
	side   Side
	left   []*node // left children, ascending by id (invariant I3)
	right  []*node // right children, ascending by id (invariant I3)

	// List view: every node in traversal order, tombstones included.
	prev *node
	next *node
}

// Sequence is one replica's copy of a replicated text.
//
// A Sequence is not safe for concurrent use. Services that share one between
// goroutines wrap it in a single owner goroutine (one actor per document),
// which is also how the Rust port should own it.
//
// Rust: `pub struct Sequence` holding nodes in an arena (`Vec<Node>` indexed
// by a `NodeIdx(u32)`) instead of pointers; parent, children, prev and next
// become indexes. That avoids `Rc<RefCell<..>>` and keeps the same algorithm.
type Sequence struct {
	replica ReplicaID
	lamport uint64

	root  *node
	nodes map[ID]*node // every node, the root included

	vv      VersionVector
	log     []Op       // every applied op, in the order applied (always causal)
	logPos  map[ID]int // op id -> index in log, for duplicate detection
	visible int        // number of visible (not deleted) characters
}

// New returns an empty sequence owned by the given replica.
func New(replica ReplicaID) (*Sequence, error) {
	if replica == 0 {
		return nil, ErrInvalidReplica
	}
	root := &node{id: Root}
	return &Sequence{
		replica: replica,
		root:    root,
		nodes:   map[ID]*node{Root: root},
		vv:      VersionVector{},
		logPos:  map[ID]int{},
	}, nil
}

// Replica returns the ID of the replica that owns this sequence.
func (s *Sequence) Replica() ReplicaID { return s.replica }

// Len returns the number of visible characters. O(1).
func (s *Sequence) Len() int { return s.visible }

// Lamport returns the largest Lamport timestamp seen so far.
func (s *Sequence) Lamport() uint64 { return s.lamport }

// Version returns a copy of the version vector. O(replicas).
func (s *Sequence) Version() VersionVector { return s.vv.Clone() }

// Has reports whether the operation with this ID has been applied.
func (s *Sequence) Has(id ID) bool { return s.vv.Contains(id) }

// Text returns the visible text. O(n) in the number of nodes.
func (s *Sequence) Text() string {
	var b strings.Builder
	b.Grow(s.visible)
	for n := s.root.next; n != nil; n = n.next {
		if !n.deleted {
			b.WriteRune(n.value)
		}
	}
	return b.String()
}

// Insert inserts text so that its first character ends up at visible index
// index, and returns the operations to send to other replicas. The operations
// have already been applied to s.
//
// index may equal Len() to append. O(n + k·depth) for k inserted characters.
func (s *Sequence) Insert(index int, text string) ([]Op, error) {
	if index < 0 || index > s.visible {
		return nil, fmt.Errorf("%w: insert at %d in text of length %d", ErrIndexOutOfRange, index, s.visible)
	}
	if !utf8.ValidString(text) {
		return nil, ErrInvalidText
	}
	left := s.root
	if index > 0 {
		left = s.visibleAt(index - 1)
	}
	ops := make([]Op, 0, utf8.RuneCountInString(text))
	for _, r := range text {
		op := s.newInsert(left, r)
		if err := s.Apply(op); err != nil {
			// A locally generated op is always valid and ready. Reaching
			// this line means the sequence's state is broken.
			return ops, fmt.Errorf("%w: local insert rejected: %v", ErrInvariant, err)
		}
		ops = append(ops, op)
		left = s.nodes[op.ID]
	}
	return ops, nil
}

// Delete deletes count visible characters starting at visible index index,
// and returns the operations to send to other replicas. The operations have
// already been applied to s. O(n + count).
func (s *Sequence) Delete(index, count int) ([]Op, error) {
	if index < 0 || count < 0 || index+count > s.visible {
		return nil, fmt.Errorf("%w: delete %d at %d in text of length %d", ErrIndexOutOfRange, count, index, s.visible)
	}
	if count == 0 {
		return nil, nil
	}
	// Collect the targets first: applying a delete changes which node sits at
	// a given visible index.
	targets := make([]*node, 0, count)
	for n := s.visibleAt(index); n != nil && len(targets) < count; n = n.next {
		if !n.deleted {
			targets = append(targets, n)
		}
	}
	ops := make([]Op, 0, count)
	for _, t := range targets {
		op := Op{ID: s.nextID(), Lamport: s.lamport + 1, Kind: OpDelete, Target: t.id}
		if err := s.Apply(op); err != nil {
			return ops, fmt.Errorf("%w: local delete rejected: %v", ErrInvariant, err)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// Apply applies one operation from any replica, including this one.
//
// It returns ErrNotReady, without changing anything, when the op's causal
// dependencies have not been applied yet. Applying an op that was already
// applied is a no-op and returns nil, so delivery may repeat messages.
func (s *Sequence) Apply(op Op) error {
	if err := op.Validate(); err != nil {
		return err
	}
	if s.vv.Contains(op.ID) {
		return s.checkDuplicate(op)
	}
	if want := s.vv[op.ID.Replica] + 1; op.ID.Counter != want {
		return fmt.Errorf("%w: %s arrived before %s", ErrNotReady, op.ID, ID{op.ID.Replica, want})
	}
	switch op.Kind {
	case OpInsert:
		return s.applyInsert(op)
	default: // Validate guarantees the kind is OpDelete
		return s.applyDelete(op)
	}
}

// OpsSince returns, in causal order, every applied operation that a replica
// with version vector v has not seen. Sending these to that replica brings it
// up to date. O(log length).
func (s *Sequence) OpsSince(v VersionVector) []Op {
	var out []Op
	for _, op := range s.log {
		if op.ID.Counter > v.Get(op.ID.Replica) {
			out = append(out, op)
		}
	}
	return out
}

// IDAt returns the ID of the character at visible index index. Editors use it
// to anchor cursors and selections to characters rather than to indexes, so a
// remote edit elsewhere does not move them. O(n).
func (s *Sequence) IDAt(index int) (ID, error) {
	if index < 0 || index >= s.visible {
		return ID{}, fmt.Errorf("%w: index %d in text of length %d", ErrIndexOutOfRange, index, s.visible)
	}
	return s.visibleAt(index).id, nil
}

// IndexOf returns the visible index of the character with the given ID. If
// the character is deleted, it returns the index the character would have,
// which is where a cursor anchored to it should land, and visible false.
// ok is false if no such character exists. O(n).
func (s *Sequence) IndexOf(id ID) (index int, visible bool, ok bool) {
	target, exists := s.nodes[id]
	if !exists || target == s.root {
		return 0, false, false
	}
	for n := s.root.next; n != nil; n = n.next {
		if n == target {
			return index, !n.deleted, true
		}
		if !n.deleted {
			index++
		}
	}
	return 0, false, false // unreachable while invariant I4 holds
}

// nextID returns the ID the next local operation will use.
func (s *Sequence) nextID() ID {
	return ID{Replica: s.replica, Counter: s.vv[s.replica] + 1}
}

// newInsert builds the op that inserts r immediately after node left,
// following Fugue's rule (see the package documentation).
func (s *Sequence) newInsert(left *node, r rune) Op {
	op := Op{ID: s.nextID(), Lamport: s.lamport + 1, Kind: OpInsert, Value: r}
	if len(left.right) == 0 {
		op.Parent, op.Side = left.id, SideRight
	} else {
		// left.next is the first node of left's first right subtree. It has
		// no left children, so the new node becomes its only left child.
		op.Parent, op.Side = left.next.id, SideLeft
	}
	return op
}

func (s *Sequence) applyInsert(op Op) error {
	parent, ok := s.nodes[op.Parent]
	if !ok {
		return fmt.Errorf("%w: parent %s of %s not applied", ErrNotReady, op.Parent, op.ID)
	}
	n := &node{id: op.ID, value: op.Value, parent: parent, side: op.Side}
	if op.Side == SideRight {
		i := insertSorted(&parent.right, n)
		// In traversal order the node comes after its parent and after the
		// whole subtree of every smaller right sibling.
		after := parent
		if i > 0 {
			after = lastInSubtree(parent.right[i-1])
		}
		linkAfter(after, n)
	} else {
		i := insertSorted(&parent.left, n)
		// It comes before its parent and before the whole subtree of every
		// larger left sibling.
		before := parent
		if i+1 < len(parent.left) {
			before = firstInSubtree(parent.left[i+1])
		}
		linkBefore(before, n)
	}
	s.nodes[n.id] = n
	s.visible++
	s.record(op)
	return nil
}

func (s *Sequence) applyDelete(op Op) error {
	target, ok := s.nodes[op.Target]
	if !ok {
		return fmt.Errorf("%w: target %s of %s not applied", ErrNotReady, op.Target, op.ID)
	}
	// Deleting an already deleted character is allowed: two replicas can
	// delete it concurrently. The op is still recorded, because it used a
	// counter and the version vector must account for it.
	if !target.deleted {
		target.deleted = true
		s.visible--
	}
	s.record(op)
	return nil
}

// record adds an applied op to the log and advances the clocks.
func (s *Sequence) record(op Op) {
	s.vv[op.ID.Replica] = op.ID.Counter
	if op.Lamport > s.lamport {
		s.lamport = op.Lamport
	}
	s.logPos[op.ID] = len(s.log)
	s.log = append(s.log, op)
}

// checkDuplicate handles an op whose ID was already applied: identical is a
// harmless redelivery, different is a replica ID reuse.
func (s *Sequence) checkDuplicate(op Op) error {
	if i, ok := s.logPos[op.ID]; ok && s.log[i] == op {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrIDConflict, op.ID)
}

// visibleAt returns the node at visible index i. The caller guarantees
// 0 <= i < s.visible. O(n).
func (s *Sequence) visibleAt(i int) *node {
	for n := s.root.next; n != nil; n = n.next {
		if n.deleted {
			continue
		}
		if i == 0 {
			return n
		}
		i--
	}
	panic("crdt: visibleAt index out of range") // callers check bounds first
}

// insertSorted inserts n into an ascending slice of siblings and returns its
// index. IDs are unique, so there are no ties.
func insertSorted(siblings *[]*node, n *node) int {
	s := *siblings
	i := len(s)
	for i > 0 && n.id.Less(s[i-1].id) {
		i--
	}
	s = append(s, nil)
	copy(s[i+1:], s[i:])
	s[i] = n
	*siblings = s
	return i
}

// firstInSubtree returns the first node of n's subtree in traversal order.
func firstInSubtree(n *node) *node {
	for len(n.left) > 0 {
		n = n.left[0]
	}
	return n
}

// lastInSubtree returns the last node of n's subtree in traversal order.
func lastInSubtree(n *node) *node {
	for len(n.right) > 0 {
		n = n.right[len(n.right)-1]
	}
	return n
}

func linkAfter(at, n *node) {
	n.prev, n.next = at, at.next
	if at.next != nil {
		at.next.prev = n
	}
	at.next = n
}

func linkBefore(at, n *node) {
	// The root is first in the list and never has left children, so every
	// node that something is linked before has a predecessor.
	linkAfter(at.prev, n)
}
