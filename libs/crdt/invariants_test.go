package crdt

import (
	"runtime/debug"
	"strings"
	"testing"
)

// Typing appends each character as the right child of the one before, so a
// note typed straight through is a chain as deep as it is long. TinyGo and
// WASM run with small fixed stacks; a recursive walk overflowed them on large
// notes. The stack cap here stands in for those targets: exceeding it is a
// fatal error, so a recursive CheckInvariants crashes this test.
func TestCheckInvariantsWalksAHundredThousandDeepChainOnASmallStack(t *testing.T) {
	const length = 100_000
	s, err := New(7)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 1000)
	for s.Len() < length {
		if _, err := s.Insert(s.Len(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	depth := 0
	for n := lastNode(s); n.parent != nil; n = n.parent {
		depth++
	}
	if depth < length {
		t.Fatalf("tree depth %d; the test needs a chain of %d", depth, length)
	}

	previous := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(previous)
	if err := s.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func lastNode(s *Sequence) *node {
	n := s.root
	for n.next != nil {
		n = n.next
	}
	return n
}

// The iterative walk reports I2 and I3 with the messages the recursive one
// had; clients match on them in debug builds.
func TestCheckInvariantsNamesTheBrokenParentAndChildOrder(t *testing.T) {
	// Two replicas typing at the start of an empty note concurrently give
	// the root two right children.
	build := func() *Sequence {
		s, err := New(7)
		if err != nil {
			t.Fatal(err)
		}
		other, err := New(8)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Insert(0, "a"); err != nil {
			t.Fatal(err)
		}
		ops, err := other.Insert(0, "b")
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range ops {
			if err := s.Apply(op); err != nil {
				t.Fatal(err)
			}
		}
		if len(s.root.right) != 2 {
			t.Fatalf("root has %d right children; the test needs 2", len(s.root.right))
		}
		return s
	}

	s := build()
	child := s.root.right[1]
	child.parent = child
	if err := s.CheckInvariants(); err == nil || err.Error() != "crdt: invariant violated: I2: node "+child.id.String()+" has wrong parent or side" {
		t.Errorf("wrong parent: %v", err)
	}

	s = build()
	s.root.right[0], s.root.right[1] = s.root.right[1], s.root.right[0]
	if err := s.CheckInvariants(); err == nil || !strings.HasSuffix(err.Error(), "I3: children of "+Root.String()+" not strictly ascending at "+s.root.right[1].id.String()) {
		t.Errorf("unsorted children: %v", err)
	}
}
