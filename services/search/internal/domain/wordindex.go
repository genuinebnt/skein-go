package domain

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/libs/trigram"
)

// Posting is how often one term occurs in one note.
type Posting struct {
	Title int
	Body  int
	// Blocks counts occurrences per block ID.
	Blocks map[string]int
}

// FieldLengths are a note's token counts, for BM25 length normalization.
type FieldLengths struct {
	Title, Body int
}

// Variant is a vocabulary word close enough to a query term to count as a
// match.
type Variant struct {
	Word     string
	Distance int // Damerau–Levenshtein distance; 0 for the term itself
}

// WordIndex is the inverted index text search ranks with: for every
// lowercased token, the notes containing it and how often, per field and
// per block. Its vocabulary is itself indexed by trigrams (one document per
// distinct word), so a misspelled term finds its neighbours by the q-gram
// count filter instead of a scan of every word.
//
// Invariants (CheckInvariants):
//
//   - W1 every posting has a positive count and its per-block counts sum to
//     its body count;
//   - W2 noteTerms lists exactly the terms whose postings name the note;
//   - W3 the vocabulary index holds exactly one document per term, and the
//     ID maps are inverse.
type WordIndex struct {
	terms     map[string]map[string]*Posting // term -> note -> posting
	noteTerms map[string][]string            // note -> its distinct terms, ascending
	lengths   map[string]FieldLengths

	vocab    *trigram.Index
	vocabID  map[string]uint32
	vocabOf  map[uint32]string
	freeIDs  []uint32 // ascending; reused smallest first
	nextID   uint32
	totalLen int64 // sum of weighted note lengths
}

// NewWordIndex returns an empty index.
func NewWordIndex() *WordIndex {
	return &WordIndex{
		terms: map[string]map[string]*Posting{}, noteTerms: map[string][]string{}, lengths: map[string]FieldLengths{},
		vocab: trigram.NewIndex(), vocabID: map[string]uint32{}, vocabOf: map[uint32]string{},
	}
}

// IndexNote replaces everything indexed for note with its title and
// blocks.
func (w *WordIndex) IndexNote(note, title string, blocks []Block) {
	w.RemoveNote(note)
	postings := map[string]*Posting{}
	get := func(term string) *Posting {
		p := postings[term]
		if p == nil {
			p = &Posting{Blocks: map[string]int{}}
			postings[term] = p
		}
		return p
	}
	var lengths FieldLengths
	for _, t := range Tokenize(title) {
		get(t.Text).Title++
		lengths.Title++
	}
	for _, b := range blocks {
		for _, t := range Tokenize(b.Text) {
			p := get(t.Text)
			p.Body++
			p.Blocks[b.ID]++
			lengths.Body++
		}
	}
	if len(postings) == 0 {
		return
	}
	w.lengths[note] = lengths
	w.totalLen += int64(WeightedLength(lengths))
	terms := sortedKeys(postings)
	w.noteTerms[note] = terms
	for _, term := range terms {
		notes := w.terms[term]
		if notes == nil {
			notes = map[string]*Posting{}
			w.terms[term] = notes
			w.addWord(term)
		}
		notes[note] = postings[term]
	}
}

// RemoveNote drops note from the index.
func (w *WordIndex) RemoveNote(note string) {
	terms, ok := w.noteTerms[note]
	if !ok {
		return
	}
	for _, term := range terms {
		delete(w.terms[term], note)
		if len(w.terms[term]) == 0 {
			delete(w.terms, term)
			w.removeWord(term)
		}
	}
	w.totalLen -= int64(WeightedLength(w.lengths[note]))
	delete(w.noteTerms, note)
	delete(w.lengths, note)
}

func (w *WordIndex) addWord(term string) {
	var id uint32
	if len(w.freeIDs) > 0 {
		id, w.freeIDs = w.freeIDs[0], w.freeIDs[1:]
	} else {
		id = w.nextID
		w.nextID++
	}
	w.vocabID[term], w.vocabOf[id] = id, term
	_ = w.vocab.Add(id, trigram.Doc{Text: term}) // id is fresh
}

func (w *WordIndex) removeWord(term string) {
	id := w.vocabID[term]
	_ = w.vocab.Remove(id) // id is indexed
	delete(w.vocabID, term)
	delete(w.vocabOf, id)
	i, _ := slices.BinarySearch(w.freeIDs, id)
	w.freeIDs = slices.Insert(w.freeIDs, i, id)
}

// Postings returns the notes containing term. The map is shared and must
// not be modified.
func (w *WordIndex) Postings(term string) map[string]*Posting { return w.terms[term] }

// Lengths returns a note's field lengths.
func (w *WordIndex) Lengths(note string) FieldLengths { return w.lengths[note] }

// TitleWeight is how many body occurrences one title occurrence counts as.
// A title names what a note is about, so a term there is stronger evidence
// than one mention in the body (BM25F's field weight).
const TitleWeight = 3

// WeightedLength is a note's length as BM25 sees it.
func WeightedLength(l FieldLengths) int { return TitleWeight*l.Title + l.Body }

// Variants returns the vocabulary words within maxEdits Damerau–Levenshtein
// edits of term (the term itself at distance 0 if present), by ascending
// distance then word.
//
// Candidates come from the vocabulary's trigram index (FuzzyCandidates) and
// a length check; the banded Levenshtein distance (libs/dp,
// LevenshteinWithin with twice the budget, since a transposition is two
// Levenshtein edits) discards most of the rest cheaply, and the exact
// Damerau distance decides.
func (w *WordIndex) Variants(term string, maxEdits int) []Variant {
	var out []Variant
	if _, ok := w.terms[term]; ok {
		out = append(out, Variant{Word: term})
	}
	if maxEdits <= 0 {
		return out
	}
	candidates, err := w.vocab.FuzzyCandidates(term, maxEdits, trigram.Options{})
	if err != nil {
		return out
	}
	n := utf8.RuneCountInString(term)
	candidates.ForEach(func(id uint32) bool {
		word := w.vocabOf[id]
		if word == term || abs(utf8.RuneCountInString(word)-n) > maxEdits {
			return true
		}
		if _, ok := dp.LevenshteinWithin(term, word, 2*maxEdits); !ok {
			return true
		}
		if d := dp.DamerauLevenshtein(term, word); d <= maxEdits {
			out = append(out, Variant{Word: word, Distance: d})
		}
		return true
	})
	slices.SortFunc(out, func(a, b Variant) int {
		if a.Distance != b.Distance {
			return a.Distance - b.Distance
		}
		if a.Word < b.Word {
			return -1
		}
		if a.Word > b.Word {
			return 1
		}
		return 0
	})
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// CheckInvariants verifies W1 to W3.
func (w *WordIndex) CheckInvariants() error {
	named := map[string][]string{}
	for _, term := range sortedKeys(w.terms) {
		notes := w.terms[term]
		if len(notes) == 0 {
			return fmt.Errorf("%w: W1: term %q has no notes", ErrInvariant, term)
		}
		for _, note := range sortedKeys(notes) {
			p := notes[note]
			sum := 0
			for _, n := range p.Blocks {
				sum += n
			}
			if p.Title+p.Body <= 0 || sum != p.Body {
				return fmt.Errorf("%w: W1: posting %q in %s is %+v", ErrInvariant, term, note, *p)
			}
			named[note] = append(named[note], term)
		}
	}
	if len(named) != len(w.noteTerms) {
		return fmt.Errorf("%w: W2: %d notes indexed, %d listed", ErrInvariant, len(named), len(w.noteTerms))
	}
	for note, terms := range w.noteTerms {
		if !slices.Equal(terms, named[note]) {
			return fmt.Errorf("%w: W2: note %s lists %d terms, postings name %d", ErrInvariant, note, len(terms), len(named[note]))
		}
	}
	if w.vocab.Len() != len(w.terms) || len(w.vocabID) != len(w.terms) || len(w.vocabOf) != len(w.terms) {
		return fmt.Errorf("%w: W3: vocabulary of %d for %d terms", ErrInvariant, w.vocab.Len(), len(w.terms))
	}
	var total int64
	for term, id := range w.vocabID {
		if w.vocabOf[id] != term {
			return fmt.Errorf("%w: W3: id maps disagree on %q", ErrInvariant, term)
		}
		if doc, ok := w.vocab.Doc(id); !ok || doc.Text != term {
			return fmt.Errorf("%w: W3: vocabulary document %d is not %q", ErrInvariant, id, term)
		}
	}
	for _, l := range w.lengths {
		total += int64(WeightedLength(l))
	}
	if total != w.totalLen {
		return fmt.Errorf("%w: W1: total length %d, want %d", ErrInvariant, w.totalLen, total)
	}
	return w.vocab.CheckInvariants()
}
