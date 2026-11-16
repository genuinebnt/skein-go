package crdt

import (
	"encoding/binary"
	"fmt"
)

// Wire format, version 1. The full byte-level description is in
// docs/spec/crdt-sequence.md; the Rust port must produce identical bytes.
//
//	batch  = magic(2) version(1) count(uvarint) op*
//	magic  = 'S' 'K'
//	op     = kind(1) replica(uvarint) counter(uvarint) lamport(uvarint) body
//	body   = insert: parent.replica(uvarint) parent.counter(uvarint) side(1) value(uvarint)
//	       | delete: target.replica(uvarint) target.counter(uvarint)
//
// Every uvarint must use the shortest encoding. The decoder rejects longer
// ones, so each batch has exactly one valid encoding; that lets tests compare
// bytes and lets both languages hash encoded batches.
const (
	codecMagic0  = 'S'
	codecMagic1  = 'K'
	codecVersion = 1

	// minEncodedOp is the smallest possible encoded op: one byte of kind and
	// one byte for each of the five smallest uvarint fields. The decoder uses
	// it to reject a count that the remaining bytes cannot possibly hold,
	// before allocating anything.
	minEncodedOp = 6
)

// EncodeOps encodes a batch of operations.
func EncodeOps(ops []Op) []byte {
	buf := make([]byte, 0, 3+binary.MaxVarintLen64+len(ops)*12)
	buf = append(buf, codecMagic0, codecMagic1, codecVersion)
	buf = binary.AppendUvarint(buf, uint64(len(ops)))
	for _, op := range ops {
		buf = append(buf, byte(op.Kind))
		buf = binary.AppendUvarint(buf, uint64(op.ID.Replica))
		buf = binary.AppendUvarint(buf, op.ID.Counter)
		buf = binary.AppendUvarint(buf, op.Lamport)
		switch op.Kind {
		case OpInsert:
			buf = binary.AppendUvarint(buf, uint64(op.Parent.Replica))
			buf = binary.AppendUvarint(buf, op.Parent.Counter)
			buf = append(buf, byte(op.Side))
			buf = binary.AppendUvarint(buf, uint64(op.Value))
		case OpDelete:
			buf = binary.AppendUvarint(buf, uint64(op.Target.Replica))
			buf = binary.AppendUvarint(buf, op.Target.Counter)
		}
	}
	return buf
}

// DecodeOps decodes a batch produced by EncodeOps. It never panics on any
// input: malformed or hostile bytes return an error wrapping ErrCorrupt (or
// ErrInvalidOp for an op that decodes but could never be valid).
func DecodeOps(data []byte) ([]Op, error) {
	d := decoder{data: data}
	if len(data) < 3 || data[0] != codecMagic0 || data[1] != codecMagic1 {
		return nil, fmt.Errorf("%w: missing magic bytes", ErrCorrupt)
	}
	if data[2] != codecVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorrupt, data[2])
	}
	d.pos = 3
	count, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	if count > uint64(len(data)-d.pos)/minEncodedOp {
		return nil, fmt.Errorf("%w: count %d cannot fit in %d bytes", ErrCorrupt, count, len(data)-d.pos)
	}
	ops := make([]Op, 0, count)
	for i := uint64(0); i < count; i++ {
		op, err := d.op()
		if err != nil {
			return nil, fmt.Errorf("op %d: %w", i, err)
		}
		if err := op.Validate(); err != nil {
			return nil, fmt.Errorf("op %d: %w", i, err)
		}
		ops = append(ops, op)
	}
	if d.pos != len(data) {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, len(data)-d.pos)
	}
	return ops, nil
}

// decoder reads fields from a byte slice and tracks the position for error
// messages.
type decoder struct {
	data []byte
	pos  int
}

func (d *decoder) byte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, fmt.Errorf("%w: unexpected end at byte %d", ErrCorrupt, d.pos)
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

// uvarint reads one minimally encoded unsigned varint.
func (d *decoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.data[d.pos:])
	switch {
	case n == 0:
		return 0, fmt.Errorf("%w: unexpected end at byte %d", ErrCorrupt, d.pos)
	case n < 0:
		return 0, fmt.Errorf("%w: varint overflow at byte %d", ErrCorrupt, d.pos)
	}
	var tmp [binary.MaxVarintLen64]byte
	if binary.PutUvarint(tmp[:], v) != n {
		return 0, fmt.Errorf("%w: non-minimal varint at byte %d", ErrCorrupt, d.pos)
	}
	d.pos += n
	return v, nil
}

func (d *decoder) id() (ID, error) {
	r, err := d.uvarint()
	if err != nil {
		return ID{}, err
	}
	c, err := d.uvarint()
	if err != nil {
		return ID{}, err
	}
	return ID{Replica: ReplicaID(r), Counter: c}, nil
}

func (d *decoder) op() (Op, error) {
	kind, err := d.byte()
	if err != nil {
		return Op{}, err
	}
	var op Op
	op.Kind = OpKind(kind)
	if op.ID, err = d.id(); err != nil {
		return Op{}, err
	}
	if op.Lamport, err = d.uvarint(); err != nil {
		return Op{}, err
	}
	switch op.Kind {
	case OpInsert:
		if op.Parent, err = d.id(); err != nil {
			return Op{}, err
		}
		side, err := d.byte()
		if err != nil {
			return Op{}, err
		}
		op.Side = Side(side)
		value, err := d.uvarint()
		if err != nil {
			return Op{}, err
		}
		if value > 0x10FFFF {
			return Op{}, fmt.Errorf("%w: value %d above U+10FFFF", ErrInvalidOp, value)
		}
		op.Value = rune(value)
	case OpDelete:
		if op.Target, err = d.id(); err != nil {
			return Op{}, err
		}
	default:
		return Op{}, fmt.Errorf("%w: unknown op kind %d at byte %d", ErrCorrupt, kind, d.pos-1)
	}
	return op, nil
}
