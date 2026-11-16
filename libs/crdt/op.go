package crdt

import (
	"fmt"
	"unicode/utf8"
)

// OpKind says what an operation does. The numeric values are part of the wire
// format and must not change.
type OpKind uint8

const (
	// OpInsert creates one character as a child of an existing element.
	OpInsert OpKind = 1
	// OpDelete marks one existing character as deleted.
	OpDelete OpKind = 2
)

// String returns the kind's name.
func (k OpKind) String() string {
	switch k {
	case OpInsert:
		return "insert"
	case OpDelete:
		return "delete"
	default:
		return fmt.Sprintf("OpKind(%d)", uint8(k))
	}
}

// Side says whether an inserted node is its parent's left or right child. The
// numeric values are part of the wire format and must not change.
type Side uint8

const (
	// SideLeft places the node before its parent in the text.
	SideLeft Side = 0
	// SideRight places the node after its parent in the text.
	SideRight Side = 1
)

// Op is one replicated operation. Ops are values: they are created by a local
// edit, sent to other replicas as they are, and stored in the op log.
//
// Only the fields for the op's kind are meaningful; the others must be zero
// so that equal ops compare equal and encode identically.
//
// Rust: an enum is the natural shape,
//
//	pub enum OpBody { Insert { parent: Id, side: Side, value: char }, Delete { target: Id } }
//	pub struct Op { pub id: Id, pub lamport: u64, pub body: OpBody }
//
// Go has no sum types, so the kind is a tag and the fields sit side by side.
type Op struct {
	// ID identifies the operation. For an insert it also identifies the new
	// character.
	ID ID
	// Lamport is a logical timestamp, larger than every timestamp the
	// generating replica had seen. Sequences do not need it to merge text; it
	// is carried so that later features (last-writer-wins block attributes,
	// formatting) can order concurrent changes without a format change.
	Lamport uint64
	// Kind selects which of the fields below are meaningful.
	Kind OpKind

	// Parent is the element the new character hangs off (insert only).
	Parent ID
	// Side says which side of Parent (insert only).
	Side Side
	// Value is the inserted character (insert only).
	Value rune

	// Target is the character to delete (delete only).
	Target ID
}

// Validate checks the op on its own, without looking at any sequence. It
// rejects anything a well-behaved replica could never produce, so the decoder
// and Apply can refuse malformed or hostile input early.
func (op Op) Validate() error {
	if op.ID.Replica == 0 {
		return fmt.Errorf("%w: replica 0 is reserved for the root", ErrInvalidOp)
	}
	if op.ID.Counter == 0 {
		return fmt.Errorf("%w: counters start at 1", ErrInvalidOp)
	}
	switch op.Kind {
	case OpInsert:
		if op.Side != SideLeft && op.Side != SideRight {
			return fmt.Errorf("%w: unknown side %d", ErrInvalidOp, op.Side)
		}
		if !utf8.ValidRune(op.Value) {
			return fmt.Errorf("%w: value %U is not a Unicode scalar value", ErrInvalidOp, op.Value)
		}
		if op.Parent == op.ID {
			return fmt.Errorf("%w: an element cannot be its own parent", ErrInvalidOp)
		}
		if op.Parent.IsRoot() && op.Side == SideLeft {
			return fmt.Errorf("%w: the root has no left children", ErrInvalidOp)
		}
		if op.Parent.Replica == op.ID.Replica && op.Parent.Counter > op.ID.Counter {
			return fmt.Errorf("%w: parent %s is newer than the op that uses it", ErrInvalidOp, op.Parent)
		}
		if op.Target != (ID{}) {
			return fmt.Errorf("%w: insert must not set target", ErrInvalidOp)
		}
	case OpDelete:
		if op.Target.IsRoot() {
			return fmt.Errorf("%w: the root cannot be deleted", ErrInvalidOp)
		}
		if op.Target.Counter == 0 {
			return fmt.Errorf("%w: delete target has counter 0", ErrInvalidOp)
		}
		if op.Target.Replica == op.ID.Replica && op.Target.Counter >= op.ID.Counter {
			return fmt.Errorf("%w: target %s is not older than the op that deletes it", ErrInvalidOp, op.Target)
		}
		if op.Parent != (ID{}) || op.Side != 0 || op.Value != 0 {
			return fmt.Errorf("%w: delete must not set insert fields", ErrInvalidOp)
		}
	default:
		return fmt.Errorf("%w: unknown kind %d", ErrInvalidOp, op.Kind)
	}
	return nil
}

// Dependency returns the element the op refers to: the parent of an insert or
// the target of a delete. An op is causally ready only once its dependency has
// been applied.
func (op Op) Dependency() ID {
	if op.Kind == OpInsert {
		return op.Parent
	}
	return op.Target
}

// String formats the op for logs and test failures.
func (op Op) String() string {
	switch op.Kind {
	case OpInsert:
		side := "right"
		if op.Side == SideLeft {
			side = "left"
		}
		return fmt.Sprintf("insert %s %q %s of %s (lamport %d)", op.ID, op.Value, side, op.Parent, op.Lamport)
	case OpDelete:
		return fmt.Sprintf("delete %s target %s (lamport %d)", op.ID, op.Target, op.Lamport)
	default:
		return fmt.Sprintf("op %s kind %d", op.ID, op.Kind)
	}
}
