package trigram

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"slices"
)

// Bitmap is a compressed set of uint32 values in the layout of roaring
// bitmaps (Chambi, Lemire, Kaser and Godin, "Better bitmap performance with
// Roaring bitmaps"): values are split by their high 16 bits into chunks, and
// each chunk's low 16 bits live in a container that is either a sorted
// array (at most 4096 values, 2 bytes each) or a 65536-bit bitset (8 KiB),
// whichever is smaller. Posting lists of rare trigrams stay tiny arrays,
// common ones become bitsets, and intersections run container by container.
//
// Only what the index needs is implemented: add, remove, membership,
// cardinality, and, or, and-not, ordered iteration, and serialization in the
// portable roaring format without run containers (see MarshalBinary), which
// the Rust `roaring` crate reads and writes. Writing it here instead of
// depending on a roaring package keeps the library dependency-free and
// buildable for WebAssembly, and keeps every line under the same oracle
// tests (bitmap_test.go compares every operation with a map).
//
// The zero value is an empty bitmap ready to use. A Bitmap is not safe for
// concurrent mutation.
//
// Invariants (CheckInvariants):
//
//   - B1 keys are strictly ascending and parallel to containers.
//   - B2 no container is empty.
//   - B3 an array container is strictly ascending and holds at most
//     arrayMax values; a bitset container holds more than arrayMax values,
//     and its cached cardinality equals its population count.
//
// B3 makes the representation canonical: equal sets have equal containers,
// which is what lets the serialized form be compared byte for byte.
//
// Rust: `struct Bitmap { keys: Vec<u16>, containers: Vec<Container> }` with
// `enum Container { Array(Vec<u16>), Bitset { words: Box<[u64; 1024]>, len: u32 } }`.
type Bitmap struct {
	keys       []uint16
	containers []container
}

// arrayMax is the largest array container; one more value converts it to a
// bitset, which at 8 KiB is then no larger than the array would be.
const arrayMax = 4096

// bitsetWords is the number of 64-bit words in a bitset container.
const bitsetWords = 1 << 16 / 64

// container holds the low 16 bits of the values in one chunk. Exactly one
// of array and words is in use: array when words is nil.
type container struct {
	array []uint16
	words []uint64 // len bitsetWords when in use
	card  int      // cardinality of words; unused for arrays
}

func (c *container) isBitset() bool { return c.words != nil }

func (c *container) cardinality() int {
	if c.isBitset() {
		return c.card
	}
	return len(c.array)
}

func (c *container) contains(low uint16) bool {
	if c.isBitset() {
		return c.words[low>>6]&(1<<(low&63)) != 0
	}
	_, found := slices.BinarySearch(c.array, low)
	return found
}

// add inserts low and reports whether it was new.
func (c *container) add(low uint16) bool {
	if c.isBitset() {
		w, bit := low>>6, uint64(1)<<(low&63)
		if c.words[w]&bit != 0 {
			return false
		}
		c.words[w] |= bit
		c.card++
		return true
	}
	i, found := slices.BinarySearch(c.array, low)
	if found {
		return false
	}
	c.array = slices.Insert(c.array, i, low)
	if len(c.array) > arrayMax {
		c.toBitset()
	}
	return true
}

// remove deletes low and reports whether it was present.
func (c *container) remove(low uint16) bool {
	if c.isBitset() {
		w, bit := low>>6, uint64(1)<<(low&63)
		if c.words[w]&bit == 0 {
			return false
		}
		c.words[w] &^= bit
		c.card--
		if c.card <= arrayMax {
			c.toArray()
		}
		return true
	}
	i, found := slices.BinarySearch(c.array, low)
	if !found {
		return false
	}
	c.array = slices.Delete(c.array, i, i+1)
	return true
}

func (c *container) toBitset() {
	words := make([]uint64, bitsetWords)
	for _, v := range c.array {
		words[v>>6] |= 1 << (v & 63)
	}
	c.words, c.card, c.array = words, len(c.array), nil
}

func (c *container) toArray() {
	array := make([]uint16, 0, c.card)
	for w, word := range c.words {
		for word != 0 {
			array = append(array, uint16(w<<6+bits.TrailingZeros64(word)))
			word &= word - 1
		}
	}
	c.array, c.words, c.card = array, nil, 0
}

func (c container) clone() container {
	if c.isBitset() {
		return container{words: slices.Clone(c.words), card: c.card}
	}
	return container{array: slices.Clone(c.array)}
}

// fromWords builds a canonical container from a bitset, or reports false
// when the bitset is empty.
func fromWords(words []uint64) (container, bool) {
	card := 0
	for _, w := range words {
		card += bits.OnesCount64(w)
	}
	if card == 0 {
		return container{}, false
	}
	c := container{words: words, card: card}
	if card <= arrayMax {
		c.toArray()
	}
	return c, true
}

// asWords returns the container as a bitset; the result may alias c.
func (c *container) asWords() []uint64 {
	if c.isBitset() {
		return c.words
	}
	words := make([]uint64, bitsetWords)
	for _, v := range c.array {
		words[v>>6] |= 1 << (v & 63)
	}
	return words
}

func andContainers(a, b *container) (container, bool) {
	if !a.isBitset() || !b.isBitset() {
		small, other := a, b
		if small.isBitset() {
			small, other = b, a
		}
		var out []uint16
		for _, v := range small.array {
			if other.contains(v) {
				out = append(out, v)
			}
		}
		return container{array: out}, len(out) > 0
	}
	words := make([]uint64, bitsetWords)
	for i := range words {
		words[i] = a.words[i] & b.words[i]
	}
	return fromWords(words)
}

func orContainers(a, b *container) container {
	if !a.isBitset() && !b.isBitset() && len(a.array)+len(b.array) <= arrayMax {
		out := make([]uint16, 0, len(a.array)+len(b.array))
		i, j := 0, 0
		for i < len(a.array) && j < len(b.array) {
			switch x, y := a.array[i], b.array[j]; {
			case x < y:
				out = append(out, x)
				i++
			case y < x:
				out = append(out, y)
				j++
			default:
				out = append(out, x)
				i++
				j++
			}
		}
		out = append(out, a.array[i:]...)
		out = append(out, b.array[j:]...)
		return container{array: out}
	}
	aw, bw := a.asWords(), b.asWords()
	words := make([]uint64, bitsetWords)
	for i := range words {
		words[i] = aw[i] | bw[i]
	}
	c, _ := fromWords(words) // a union of non-empty containers is non-empty
	return c
}

func andNotContainers(a, b *container) (container, bool) {
	if !a.isBitset() {
		var out []uint16
		for _, v := range a.array {
			if !b.contains(v) {
				out = append(out, v)
			}
		}
		return container{array: out}, len(out) > 0
	}
	bw := b.asWords()
	words := make([]uint64, bitsetWords)
	for i := range words {
		words[i] = a.words[i] &^ bw[i]
	}
	return fromWords(words)
}

// BitmapOf returns a bitmap holding values.
func BitmapOf(values ...uint32) *Bitmap {
	b := &Bitmap{}
	for _, v := range values {
		b.Add(v)
	}
	return b
}

func (b *Bitmap) find(key uint16) (int, bool) { return slices.BinarySearch(b.keys, key) }

// Add inserts v and reports whether it was not already present.
func (b *Bitmap) Add(v uint32) bool {
	key, low := uint16(v>>16), uint16(v)
	i, found := b.find(key)
	if !found {
		b.keys = slices.Insert(b.keys, i, key)
		b.containers = slices.Insert(b.containers, i, container{array: []uint16{low}})
		return true
	}
	return b.containers[i].add(low)
}

// Remove deletes v and reports whether it was present.
func (b *Bitmap) Remove(v uint32) bool {
	key, low := uint16(v>>16), uint16(v)
	i, found := b.find(key)
	if !found || !b.containers[i].remove(low) {
		return false
	}
	if b.containers[i].cardinality() == 0 {
		b.keys = slices.Delete(b.keys, i, i+1)
		b.containers = slices.Delete(b.containers, i, i+1)
	}
	return true
}

// Contains reports whether v is in the bitmap. A nil bitmap is empty.
func (b *Bitmap) Contains(v uint32) bool {
	if b == nil {
		return false
	}
	i, found := b.find(uint16(v >> 16))
	return found && b.containers[i].contains(uint16(v))
}

// Len returns the number of values. A nil bitmap is empty.
func (b *Bitmap) Len() int {
	if b == nil {
		return 0
	}
	n := 0
	for i := range b.containers {
		n += b.containers[i].cardinality()
	}
	return n
}

// IsEmpty reports whether the bitmap holds no values.
func (b *Bitmap) IsEmpty() bool { return b == nil || len(b.keys) == 0 }

// Clone returns an independent copy. Cloning nil gives an empty bitmap.
func (b *Bitmap) Clone() *Bitmap {
	out := &Bitmap{}
	if b == nil {
		return out
	}
	out.keys = slices.Clone(b.keys)
	out.containers = make([]container, len(b.containers))
	for i, c := range b.containers {
		out.containers[i] = c.clone()
	}
	return out
}

// And returns the values in both a and b. Nil operands are empty.
func And(a, b *Bitmap) *Bitmap {
	out := &Bitmap{}
	if a == nil || b == nil {
		return out
	}
	i, j := 0, 0
	for i < len(a.keys) && j < len(b.keys) {
		switch x, y := a.keys[i], b.keys[j]; {
		case x < y:
			i++
		case y < x:
			j++
		default:
			if c, ok := andContainers(&a.containers[i], &b.containers[j]); ok {
				out.keys = append(out.keys, x)
				out.containers = append(out.containers, c)
			}
			i++
			j++
		}
	}
	return out
}

// Or returns the values in a or b. Nil operands are empty.
func Or(a, b *Bitmap) *Bitmap {
	if a == nil {
		return b.Clone()
	}
	if b == nil {
		return a.Clone()
	}
	out := &Bitmap{}
	i, j := 0, 0
	for i < len(a.keys) || j < len(b.keys) {
		switch {
		case j == len(b.keys) || (i < len(a.keys) && a.keys[i] < b.keys[j]):
			out.keys = append(out.keys, a.keys[i])
			out.containers = append(out.containers, a.containers[i].clone())
			i++
		case i == len(a.keys) || b.keys[j] < a.keys[i]:
			out.keys = append(out.keys, b.keys[j])
			out.containers = append(out.containers, b.containers[j].clone())
			j++
		default:
			out.keys = append(out.keys, a.keys[i])
			out.containers = append(out.containers, orContainers(&a.containers[i], &b.containers[j]))
			i++
			j++
		}
	}
	return out
}

// AndNot returns the values in a that are not in b. Nil operands are empty.
func AndNot(a, b *Bitmap) *Bitmap {
	if a == nil {
		return &Bitmap{}
	}
	if b == nil {
		return a.Clone()
	}
	out := &Bitmap{}
	j := 0
	for i, key := range a.keys {
		for j < len(b.keys) && b.keys[j] < key {
			j++
		}
		if j == len(b.keys) || b.keys[j] != key {
			out.keys = append(out.keys, key)
			out.containers = append(out.containers, a.containers[i].clone())
			continue
		}
		if c, ok := andNotContainers(&a.containers[i], &b.containers[j]); ok {
			out.keys = append(out.keys, key)
			out.containers = append(out.containers, c)
		}
	}
	return out
}

// ForEach calls fn with every value in ascending order until fn returns
// false.
func (b *Bitmap) ForEach(fn func(v uint32) bool) {
	if b == nil {
		return
	}
	for i, key := range b.keys {
		high := uint32(key) << 16
		c := &b.containers[i]
		if !c.isBitset() {
			for _, low := range c.array {
				if !fn(high | uint32(low)) {
					return
				}
			}
			continue
		}
		for w, word := range c.words {
			for word != 0 {
				if !fn(high | uint32(w<<6+bits.TrailingZeros64(word))) {
					return
				}
				word &= word - 1
			}
		}
	}
}

// ToArray returns every value in ascending order.
func (b *Bitmap) ToArray() []uint32 {
	out := make([]uint32, 0, b.Len())
	b.ForEach(func(v uint32) bool {
		out = append(out, v)
		return true
	})
	return out
}

// Equal reports whether b and other hold the same values.
func (b *Bitmap) Equal(other *Bitmap) bool {
	if b.IsEmpty() || other.IsEmpty() {
		return b.IsEmpty() && other.IsEmpty()
	}
	if !slices.Equal(b.keys, other.keys) {
		return false
	}
	for i := range b.containers {
		x, y := &b.containers[i], &other.containers[i]
		// Canonical form (B3) means equal sets use the same kind.
		if x.isBitset() != y.isBitset() {
			return false
		}
		if x.isBitset() && !slices.Equal(x.words, y.words) || !x.isBitset() && !slices.Equal(x.array, y.array) {
			return false
		}
	}
	return true
}

// CheckInvariants verifies B1 to B3 from the type documentation.
func (b *Bitmap) CheckInvariants() error {
	if b == nil {
		return nil
	}
	if len(b.keys) != len(b.containers) {
		return fmt.Errorf("%w: B1: %d keys but %d containers", ErrInvariant, len(b.keys), len(b.containers))
	}
	for i, key := range b.keys {
		if i > 0 && b.keys[i-1] >= key {
			return fmt.Errorf("%w: B1: key %d follows %d", ErrInvariant, key, b.keys[i-1])
		}
		c := &b.containers[i]
		if c.isBitset() {
			if len(c.words) != bitsetWords || c.array != nil {
				return fmt.Errorf("%w: B3: chunk %d bitset has %d words", ErrInvariant, key, len(c.words))
			}
			pop := 0
			for _, w := range c.words {
				pop += bits.OnesCount64(w)
			}
			if pop != c.card {
				return fmt.Errorf("%w: B3: chunk %d caches %d values but holds %d", ErrInvariant, key, c.card, pop)
			}
			if pop <= arrayMax {
				return fmt.Errorf("%w: B3: chunk %d is a bitset of only %d values", ErrInvariant, key, pop)
			}
			continue
		}
		if len(c.array) == 0 {
			return fmt.Errorf("%w: B2: chunk %d is empty", ErrInvariant, key)
		}
		if len(c.array) > arrayMax {
			return fmt.Errorf("%w: B3: chunk %d array holds %d values", ErrInvariant, key, len(c.array))
		}
		for j := 1; j < len(c.array); j++ {
			if c.array[j-1] >= c.array[j] {
				return fmt.Errorf("%w: B3: chunk %d array not strictly ascending at %d", ErrInvariant, key, j)
			}
		}
	}
	return nil
}

// serialCookieNoRuns starts the portable roaring format when the bitmap has
// no run containers, which is always the case here.
const serialCookieNoRuns = 12346

// MarshalBinary encodes the bitmap in the portable roaring serialization
// (github.com/RoaringBitmap/RoaringFormatSpec), without run containers, all
// integers little-endian:
//
//	uint32 cookie 12346, uint32 container count n
//	n × (uint16 key, uint16 cardinality-1)
//	n × uint32 byte offset of the container from the start of the encoding
//	n containers: an array is cardinality × uint16, a bitset 1024 × uint64
//
// A container is a bitset exactly when its cardinality exceeds 4096, so the
// encoding of a set is unique (B3).
func (b *Bitmap) MarshalBinary() ([]byte, error) {
	if b == nil {
		b = &Bitmap{}
	}
	n := len(b.keys)
	size := 8 + 8*n
	for i := range b.containers {
		size += containerBytes(b.containers[i].cardinality())
	}
	out := make([]byte, 0, size)
	out = binary.LittleEndian.AppendUint32(out, serialCookieNoRuns)
	out = binary.LittleEndian.AppendUint32(out, uint32(n))
	for i, key := range b.keys {
		out = binary.LittleEndian.AppendUint16(out, key)
		out = binary.LittleEndian.AppendUint16(out, uint16(b.containers[i].cardinality()-1))
	}
	offset := 8 + 8*n
	for i := range b.containers {
		out = binary.LittleEndian.AppendUint32(out, uint32(offset))
		offset += containerBytes(b.containers[i].cardinality())
	}
	for i := range b.containers {
		c := &b.containers[i]
		if c.isBitset() {
			for _, w := range c.words {
				out = binary.LittleEndian.AppendUint64(out, w)
			}
			continue
		}
		for _, v := range c.array {
			out = binary.LittleEndian.AppendUint16(out, v)
		}
	}
	return out, nil
}

func containerBytes(card int) int {
	if card > arrayMax {
		return 8 * bitsetWords
	}
	return 2 * card
}

// UnmarshalBinary replaces b with the bitmap encoded in data (see
// MarshalBinary). It accepts only canonical encodings with no trailing
// bytes and returns ErrCorrupt otherwise, leaving b unchanged.
func (b *Bitmap) UnmarshalBinary(data []byte) error {
	decoded, rest, err := decodeBitmap(data)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("%w: %d trailing bytes after bitmap", ErrCorrupt, len(rest))
	}
	*b = *decoded
	return nil
}

// decodeBitmap decodes one bitmap from the front of data and returns the
// bytes after it.
func decodeBitmap(data []byte) (*Bitmap, []byte, error) {
	if len(data) < 8 {
		return nil, nil, fmt.Errorf("%w: bitmap header truncated", ErrCorrupt)
	}
	if cookie := binary.LittleEndian.Uint32(data); cookie != serialCookieNoRuns {
		return nil, nil, fmt.Errorf("%w: bitmap cookie %d", ErrCorrupt, cookie)
	}
	n64 := uint64(binary.LittleEndian.Uint32(data[4:]))
	if n64 > 1<<16 || uint64(len(data)) < 8+8*n64 {
		return nil, nil, fmt.Errorf("%w: bitmap of %d containers truncated", ErrCorrupt, n64)
	}
	n := int(n64)
	out := &Bitmap{keys: make([]uint16, n), containers: make([]container, n)}
	offset := 8 + 8*n
	for i := range n {
		key := binary.LittleEndian.Uint16(data[8+4*i:])
		card := int(binary.LittleEndian.Uint16(data[10+4*i:])) + 1
		if i > 0 && out.keys[i-1] >= key {
			return nil, nil, fmt.Errorf("%w: bitmap keys not ascending", ErrCorrupt)
		}
		if got := binary.LittleEndian.Uint32(data[8+4*n+4*i:]); int64(got) != int64(offset) {
			return nil, nil, fmt.Errorf("%w: container %d offset %d, want %d", ErrCorrupt, i, got, offset)
		}
		size := containerBytes(card)
		if len(data) < offset+size {
			return nil, nil, fmt.Errorf("%w: container %d truncated", ErrCorrupt, i)
		}
		body := data[offset : offset+size]
		offset += size
		out.keys[i] = key
		if card <= arrayMax {
			array := make([]uint16, card)
			for j := range array {
				array[j] = binary.LittleEndian.Uint16(body[2*j:])
				if j > 0 && array[j-1] >= array[j] {
					return nil, nil, fmt.Errorf("%w: container %d not ascending", ErrCorrupt, i)
				}
			}
			out.containers[i] = container{array: array}
			continue
		}
		words := make([]uint64, bitsetWords)
		pop := 0
		for j := range words {
			words[j] = binary.LittleEndian.Uint64(body[8*j:])
			pop += bits.OnesCount64(words[j])
		}
		if pop != card {
			return nil, nil, fmt.Errorf("%w: container %d holds %d values, header says %d", ErrCorrupt, i, pop, card)
		}
		out.containers[i] = container{words: words, card: card}
	}
	return out, data[offset:], nil
}
