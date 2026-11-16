package blocks

import (
	"encoding/binary"
	"fmt"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Attribute wire format, version 1 (docs/spec/blocks.md section 4). It
// mirrors the CRDT batch format so both can share framing and the Rust port
// can reuse its varint code:
//
//	batch = 'S' 'A' version(1) count(uvarint) write*
//	write = key(1) block.replica(uvarint) block.counter(uvarint)
//	        lamport(uvarint) replica(uvarint) value
//	value = type: type(1) | indent: uvarint | checked: 0 or 1 (1)
//	      | language: length(uvarint) UTF-8 bytes
//
// Every uvarint must use the shortest encoding, so each batch has exactly one
// valid encoding.
const (
	attrMagic0  = 'S'
	attrMagic1  = 'A'
	attrVersion = 1

	// minEncodedWrite is the smallest encoded write: one byte of key, one
	// byte for each of the four uvarint fields and one byte of value.
	minEncodedWrite = 6
)

// EncodeAttrs encodes a batch of attribute writes.
func EncodeAttrs(writes []AttrWrite) []byte {
	buf := make([]byte, 0, 3+binary.MaxVarintLen64+len(writes)*16)
	buf = append(buf, attrMagic0, attrMagic1, attrVersion)
	buf = binary.AppendUvarint(buf, uint64(len(writes)))
	for _, w := range writes {
		buf = append(buf, byte(w.Key))
		buf = binary.AppendUvarint(buf, uint64(w.Block.Replica))
		buf = binary.AppendUvarint(buf, w.Block.Counter)
		buf = binary.AppendUvarint(buf, w.Lamport)
		buf = binary.AppendUvarint(buf, uint64(w.Replica))
		buf = appendAttrValue(buf, w)
	}
	return buf
}

// appendAttrValue appends the value part of w's encoding. Wins compares these
// bytes to break exact stamp ties.
func appendAttrValue(buf []byte, w AttrWrite) []byte {
	switch w.Key {
	case AttrType:
		return append(buf, byte(w.Type))
	case AttrIndent:
		return binary.AppendUvarint(buf, uint64(w.Indent))
	case AttrChecked:
		if w.Checked {
			return append(buf, 1)
		}
		return append(buf, 0)
	case AttrLanguage:
		buf = binary.AppendUvarint(buf, uint64(len(w.Language)))
		return append(buf, w.Language...)
	}
	return buf
}

// DecodeAttrs decodes a batch produced by EncodeAttrs. It never panics:
// malformed input returns an error wrapping ErrCorrupt, and a write that
// decodes but could never be valid returns one wrapping ErrInvalidAttr.
func DecodeAttrs(data []byte) ([]AttrWrite, error) {
	d := attrDecoder{data: data}
	if len(data) < 3 || data[0] != attrMagic0 || data[1] != attrMagic1 {
		return nil, fmt.Errorf("%w: missing magic", ErrCorrupt)
	}
	if data[2] != attrVersion {
		return nil, fmt.Errorf("%w: unknown version %d", ErrCorrupt, data[2])
	}
	d.pos = 3
	count, err := d.uvarint()
	if err != nil {
		return nil, err
	}
	if count > uint64(len(data)-d.pos)/minEncodedWrite {
		return nil, fmt.Errorf("%w: count %d exceeds what %d bytes can hold", ErrCorrupt, count, len(data)-d.pos)
	}
	out := make([]AttrWrite, 0, count)
	for i := uint64(0); i < count; i++ {
		w, err := d.write()
		if err != nil {
			return nil, fmt.Errorf("write %d: %w", i, err)
		}
		if err := w.Validate(); err != nil {
			return nil, fmt.Errorf("write %d: %w", i, err)
		}
		out = append(out, w)
	}
	if d.pos != len(data) {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, len(data)-d.pos)
	}
	return out, nil
}

type attrDecoder struct {
	data []byte
	pos  int
}

func (d *attrDecoder) byte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, fmt.Errorf("%w: truncated", ErrCorrupt)
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

// uvarint reads a shortest-form unsigned LEB128 value.
func (d *attrDecoder) uvarint() (uint64, error) {
	v, n := binary.Uvarint(d.data[d.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("%w: bad uvarint at byte %d", ErrCorrupt, d.pos)
	}
	if n != len(binary.AppendUvarint(nil, v)) {
		return 0, fmt.Errorf("%w: non-minimal uvarint at byte %d", ErrCorrupt, d.pos)
	}
	d.pos += n
	return v, nil
}

func (d *attrDecoder) write() (AttrWrite, error) {
	var w AttrWrite
	key, err := d.byte()
	if err != nil {
		return w, err
	}
	w.Key = AttrKey(key)
	var fields [4]uint64
	for i := range fields {
		if fields[i], err = d.uvarint(); err != nil {
			return w, err
		}
	}
	w.Block = crdt.ID{Replica: crdt.ReplicaID(fields[0]), Counter: fields[1]}
	w.Lamport, w.Replica = fields[2], crdt.ReplicaID(fields[3])
	switch w.Key {
	case AttrType:
		t, err := d.byte()
		if err != nil {
			return w, err
		}
		w.Type = Type(t)
	case AttrIndent:
		v, err := d.uvarint()
		if err != nil {
			return w, err
		}
		if v > MaxIndent {
			return w, fmt.Errorf("%w: indent %d exceeds %d", ErrInvalidAttr, v, MaxIndent)
		}
		w.Indent = uint32(v)
	case AttrChecked:
		b, err := d.byte()
		if err != nil {
			return w, err
		}
		if b > 1 {
			return w, fmt.Errorf("%w: checked byte %d", ErrCorrupt, b)
		}
		w.Checked = b == 1
	case AttrLanguage:
		n, err := d.uvarint()
		if err != nil {
			return w, err
		}
		if n > MaxLanguageBytes || n > uint64(len(d.data)-d.pos) {
			return w, fmt.Errorf("%w: language length %d", ErrCorrupt, n)
		}
		w.Language = string(d.data[d.pos : d.pos+int(n)])
		d.pos += int(n)
	default:
		return w, fmt.Errorf("%w: unknown key %d", ErrCorrupt, key)
	}
	return w, nil
}
