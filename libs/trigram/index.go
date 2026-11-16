package trigram

import (
	"encoding/binary"
	"fmt"
	"slices"
	"unicode/utf8"
)

// DocID identifies a document (a block) in an Index. The caller assigns
// IDs; they need not be dense, but dense IDs keep bitmaps small.
//
// Rust: `pub struct DocId(pub u32)`.
type DocID = uint32

// Doc is one indexed block.
type Doc struct {
	// Text is the block's text. It may hold any bytes; invalid UTF-8 decodes
	// to U+FFFD per byte for trigrams and offsets, as in Go's range loop.
	Text string
	// Code marks a code block. Filters can restrict a search to code.
	Code bool
	// Language is a code block's language ("go", "rust"). Compared with A-Z
	// lowercased, so "Go" and "go" are one language. Ignored unless Code.
	Language string
}

// Index is a trigram inverted index over documents: for every trigram, the
// bitmap of documents whose folded text contains it. See the package
// documentation for how queries use it.
//
// An Index is not safe for concurrent mutation; concurrent reads are safe
// while nothing mutates it.
type Index struct {
	docs      map[DocID]Doc
	postings  map[Trigram]*Bitmap
	live      Bitmap
	code      Bitmap
	languages map[string]*Bitmap // A-Z lowercased language -> code docs
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{docs: map[DocID]Doc{}, postings: map[Trigram]*Bitmap{}, languages: map[string]*Bitmap{}}
}

// Len returns the number of documents.
func (ix *Index) Len() int { return len(ix.docs) }

// TrigramCount returns the number of distinct trigrams indexed.
func (ix *Index) TrigramCount() int { return len(ix.postings) }

// Doc returns the document with id.
func (ix *Index) Doc(id DocID) (Doc, bool) {
	d, ok := ix.docs[id]
	return d, ok
}

// All returns a copy of the set of indexed document IDs.
func (ix *Index) All() *Bitmap { return ix.live.Clone() }

// Postings returns the documents containing t. The bitmap is shared with
// the index and must not be modified; nil means none.
func (ix *Index) Postings(t Trigram) *Bitmap { return ix.postings[t] }

// Add indexes doc under id. It returns ErrDuplicateDoc if id is present.
//
// Cost: O(n log n) in the text's length, plus one bitmap insert per
// distinct trigram.
func (ix *Index) Add(id DocID, doc Doc) error {
	if _, dup := ix.docs[id]; dup {
		return fmt.Errorf("%w: %d", ErrDuplicateDoc, id)
	}
	ix.insert(id, doc, Trigrams(doc.Text))
	return nil
}

// Update replaces the document with id, touching only the postings of
// trigrams that appear in one version and not the other, which is what
// makes small edits to a long block cheap. It returns ErrUnknownDoc if id
// is not present.
func (ix *Index) Update(id DocID, doc Doc) error {
	old, ok := ix.docs[id]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownDoc, id)
	}
	before, after := Trigrams(old.Text), Trigrams(doc.Text)
	i, j := 0, 0
	for i < len(before) || j < len(after) {
		switch {
		case j == len(after) || (i < len(before) && before[i] < after[j]):
			ix.unpost(before[i], id)
			i++
		case i == len(before) || after[j] < before[i]:
			ix.post(after[j], id)
			j++
		default:
			i++
			j++
		}
	}
	ix.unfilter(id, old)
	ix.filter(id, doc)
	ix.docs[id] = doc
	return nil
}

// Put adds doc under id, or updates it if id is present.
func (ix *Index) Put(id DocID, doc Doc) {
	if _, ok := ix.docs[id]; ok {
		_ = ix.Update(id, doc) // cannot fail: id is present
		return
	}
	ix.insert(id, doc, Trigrams(doc.Text))
}

// Remove deletes the document with id. It returns ErrUnknownDoc if id is
// not present.
func (ix *Index) Remove(id DocID) error {
	old, ok := ix.docs[id]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownDoc, id)
	}
	for _, t := range Trigrams(old.Text) {
		ix.unpost(t, id)
	}
	ix.unfilter(id, old)
	ix.live.Remove(id)
	delete(ix.docs, id)
	return nil
}

func (ix *Index) insert(id DocID, doc Doc, trigrams []Trigram) {
	for _, t := range trigrams {
		ix.post(t, id)
	}
	ix.filter(id, doc)
	ix.live.Add(id)
	ix.docs[id] = doc
}

func (ix *Index) post(t Trigram, id DocID) {
	b := ix.postings[t]
	if b == nil {
		b = &Bitmap{}
		ix.postings[t] = b
	}
	b.Add(id)
}

// unpost removes id from t's posting list and drops the list once empty,
// so the trigram count reflects only live trigrams (I3).
func (ix *Index) unpost(t Trigram, id DocID) {
	b := ix.postings[t]
	if b == nil {
		return
	}
	b.Remove(id)
	if b.IsEmpty() {
		delete(ix.postings, t)
	}
}

func (ix *Index) filter(id DocID, doc Doc) {
	if !doc.Code {
		return
	}
	ix.code.Add(id)
	lang := asciiLower(doc.Language)
	b := ix.languages[lang]
	if b == nil {
		b = &Bitmap{}
		ix.languages[lang] = b
	}
	b.Add(id)
}

func (ix *Index) unfilter(id DocID, doc Doc) {
	if !doc.Code {
		return
	}
	ix.code.Remove(id)
	lang := asciiLower(doc.Language)
	if b := ix.languages[lang]; b != nil {
		b.Remove(id)
		if b.IsEmpty() {
			delete(ix.languages, lang)
		}
	}
}

// CheckInvariants verifies the index against its documents:
//
//   - I1 every bitmap satisfies its own invariants (B1 to B3).
//   - I2 the live set is exactly the set of document IDs.
//   - I3 for every trigram, its posting list is exactly the documents whose
//     text contains it, and no posting list is empty.
//   - I4 the code set is exactly the code documents, and each language set
//     exactly the code documents in that language, none empty.
//
// It recomputes every document's trigrams, so it costs as much as
// rebuilding the index; tests call it after every operation.
func (ix *Index) CheckInvariants() error {
	if err := ix.live.CheckInvariants(); err != nil {
		return err
	}
	if ix.live.Len() != len(ix.docs) {
		return fmt.Errorf("%w: I2: %d live ids for %d documents", ErrInvariant, ix.live.Len(), len(ix.docs))
	}
	want := map[Trigram]*Bitmap{}
	wantCode := &Bitmap{}
	wantLang := map[string]*Bitmap{}
	for _, id := range sortedDocIDs(ix.docs) {
		doc := ix.docs[id]
		if !ix.live.Contains(id) {
			return fmt.Errorf("%w: I2: document %d is not live", ErrInvariant, id)
		}
		for _, t := range Trigrams(doc.Text) {
			if want[t] == nil {
				want[t] = &Bitmap{}
			}
			want[t].Add(id)
		}
		if doc.Code {
			wantCode.Add(id)
			lang := asciiLower(doc.Language)
			if wantLang[lang] == nil {
				wantLang[lang] = &Bitmap{}
			}
			wantLang[lang].Add(id)
		}
	}
	if len(want) != len(ix.postings) {
		return fmt.Errorf("%w: I3: %d posting lists, want %d", ErrInvariant, len(ix.postings), len(want))
	}
	for t, got := range ix.postings {
		if err := got.CheckInvariants(); err != nil {
			return fmt.Errorf("trigram %q: %w", t, err)
		}
		if !got.Equal(want[t]) {
			return fmt.Errorf("%w: I3: posting list of %q is %v, want %v", ErrInvariant, t, got.ToArray(), want[t].ToArray())
		}
	}
	if err := ix.code.CheckInvariants(); err != nil {
		return err
	}
	if !ix.code.Equal(wantCode) {
		return fmt.Errorf("%w: I4: code set %v, want %v", ErrInvariant, ix.code.ToArray(), wantCode.ToArray())
	}
	if len(ix.languages) != len(wantLang) {
		return fmt.Errorf("%w: I4: %d languages, want %d", ErrInvariant, len(ix.languages), len(wantLang))
	}
	for lang, got := range ix.languages {
		if err := got.CheckInvariants(); err != nil {
			return err
		}
		if !got.Equal(wantLang[lang]) {
			return fmt.Errorf("%w: I4: language %q set %v, want %v", ErrInvariant, lang, got.ToArray(), wantLang[lang].ToArray())
		}
	}
	return nil
}

func sortedDocIDs(docs map[DocID]Doc) []DocID {
	ids := make([]DocID, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// snapshotMagic and snapshotVersion start an encoded index.
const (
	snapshotMagic   = "SKTG"
	snapshotVersion = 1
)

// MarshalBinary encodes the index as a snapshot, so a service can restart
// without re-reading and re-tokenising every block. Layout, integers
// little-endian, lengths as unsigned LEB128 varints:
//
//	"SKTG", uint32 version 1
//	varint document count, then per document in ascending ID:
//	    uint32 id, uint8 flags (bit 0 = code), varint+bytes language, varint+bytes text
//	varint posting count, then per trigram in ascending order:
//	    uint64 trigram, one bitmap in the portable roaring format
//
// The code and language sets are derived from the documents on decode.
// Ascending order everywhere makes the encoding a function of the index's
// contents, never of map iteration order.
func (ix *Index) MarshalBinary() ([]byte, error) {
	out := append([]byte(snapshotMagic), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], snapshotVersion)
	out = binary.AppendUvarint(out, uint64(len(ix.docs)))
	for _, id := range sortedDocIDs(ix.docs) {
		doc := ix.docs[id]
		out = binary.LittleEndian.AppendUint32(out, id)
		var flags byte
		if doc.Code {
			flags = 1
		}
		out = append(out, flags)
		out = appendBytes(out, doc.Language)
		out = appendBytes(out, doc.Text)
	}
	trigrams := make([]Trigram, 0, len(ix.postings))
	for t := range ix.postings {
		trigrams = append(trigrams, t)
	}
	slices.Sort(trigrams)
	out = binary.AppendUvarint(out, uint64(len(trigrams)))
	for _, t := range trigrams {
		out = binary.LittleEndian.AppendUint64(out, uint64(t))
		enc, err := ix.postings[t].MarshalBinary()
		if err != nil {
			return nil, err
		}
		out = append(out, enc...)
	}
	return out, nil
}

func appendBytes(out []byte, s string) []byte {
	out = binary.AppendUvarint(out, uint64(len(s)))
	return append(out, s...)
}

// UnmarshalIndex decodes a snapshot written by MarshalBinary. It checks the
// structure (lengths, ordering, canonical bitmaps, every posting referring
// to a document) but not that the postings match the texts; call
// CheckInvariants for that when the bytes are not trusted.
func UnmarshalIndex(data []byte) (*Index, error) {
	r := reader{data: data}
	if string(r.take(4)) != snapshotMagic {
		return nil, fmt.Errorf("%w: not an index snapshot", ErrCorrupt)
	}
	if v := r.uint32(); v != snapshotVersion {
		return nil, fmt.Errorf("%w: snapshot version %d", ErrCorrupt, v)
	}
	ix := NewIndex()
	count := r.uvarint()
	var prev int64 = -1
	for range count {
		if r.err != nil {
			break
		}
		id := r.uint32()
		flags := r.byte()
		lang := r.str()
		text := r.str()
		if r.err != nil {
			break
		}
		if int64(id) <= prev || flags > 1 {
			return nil, fmt.Errorf("%w: document %d out of order or bad flags", ErrCorrupt, id)
		}
		prev = int64(id)
		doc := Doc{Text: text, Code: flags == 1, Language: lang}
		ix.docs[id] = doc
		ix.live.Add(id)
		ix.filter(id, doc)
	}
	postings := r.uvarint()
	var prevT int64 = -1
	for range postings {
		if r.err != nil {
			break
		}
		t := r.uint64()
		if r.err != nil {
			break
		}
		if int64(t) <= prevT || t >= 1<<63 {
			return nil, fmt.Errorf("%w: trigram %d out of order", ErrCorrupt, t)
		}
		prevT = int64(t)
		b, rest, err := decodeBitmap(r.data)
		if err != nil {
			return nil, err
		}
		r.data = rest
		if b.IsEmpty() || !AndNot(b, &ix.live).IsEmpty() {
			return nil, fmt.Errorf("%w: posting list of trigram %d is empty or names unknown documents", ErrCorrupt, t)
		}
		ix.postings[Trigram(t)] = b
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(r.data) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, len(r.data))
	}
	return ix, nil
}

// reader consumes a byte slice, recording the first error and returning
// zero values after it.
type reader struct {
	data []byte
	err  error
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = fmt.Errorf("%w: snapshot truncated", ErrCorrupt)
	}
	r.data = nil
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || len(r.data) < n {
		r.fail()
		return nil
	}
	out := r.data[:n]
	r.data = r.data[n:]
	return out
}

func (r *reader) byte() byte {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) uint32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *reader) uint64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *reader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.data)
	// Non-minimal encodings are rejected so each snapshot has one encoding,
	// as the porting guide requires of every varint format.
	if n <= 0 || n != len(binary.AppendUvarint(nil, v)) {
		r.fail()
		return 0
	}
	r.data = r.data[n:]
	return v
}

func (r *reader) str() string {
	n := r.uvarint()
	if n > uint64(len(r.data)) {
		r.fail()
		return ""
	}
	return string(r.take(int(n)))
}

// runeCount is utf8.RuneCountInString, named for what offsets count.
func runeCount(s string) int { return utf8.RuneCountInString(s) }
