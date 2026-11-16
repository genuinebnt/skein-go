package trigram

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// QueryKind tags the variants of Query.
//
// Rust: `enum Query { All, None, And { trigrams: Vec<Trigram>, subs: Vec<Query> },
// Or { trigrams: Vec<Trigram>, subs: Vec<Query> } }`.
type QueryKind uint8

const (
	// QueryAll matches every document: the index cannot narrow the search.
	QueryAll QueryKind = 1
	// QueryNone matches no document: the expression can never match.
	QueryNone QueryKind = 2
	// QueryAnd matches documents holding every trigram in Trigrams and
	// matching every query in Subs.
	QueryAnd QueryKind = 3
	// QueryOr matches documents holding any trigram in Trigrams or matching
	// any query in Subs.
	QueryOr QueryKind = 4
)

// Query is a boolean formula over trigrams: which documents could possibly
// match. It is always in the normal form CheckInvariants describes, so two
// equal formulas built the same way print the same.
type Query struct {
	Kind     QueryKind
	Trigrams []Trigram // ascending, distinct
	Subs     []Query   // And holds only Or subs, Or only And subs; ordered by String
}

var (
	allQuery  = Query{Kind: QueryAll}
	noneQuery = Query{Kind: QueryNone}
)

// AllQuery returns the query matching every document.
func AllQuery() Query { return allQuery }

// NoneQuery returns the query matching no document.
func NoneQuery() Query { return noneQuery }

// TrigramQuery returns the query requiring every trigram of s: the
// candidate query for a literal.
func TrigramQuery(s string) Query {
	return andOf(Trigrams(s), nil)
}

// andOf builds a normalized And from trigrams and Or subs.
func andOf(trigrams []Trigram, subs []Query) Query {
	q := Query{Kind: QueryAnd, Trigrams: trigrams}
	for _, s := range subs {
		q = AndQuery(q, s)
	}
	return normalizeAnd(q)
}

// AndQuery returns a query matching what both a and b match.
func AndQuery(a, b Query) Query {
	switch {
	case a.Kind == QueryNone || b.Kind == QueryNone:
		return noneQuery
	case a.Kind == QueryAll:
		return b
	case b.Kind == QueryAll:
		return a
	}
	out := Query{Kind: QueryAnd}
	for _, q := range []Query{a, b} {
		if q.Kind == QueryAnd {
			out.Trigrams = append(out.Trigrams, q.Trigrams...)
			out.Subs = append(out.Subs, q.Subs...)
		} else {
			out.Subs = append(out.Subs, q)
		}
	}
	return normalizeAnd(out)
}

// normalizeAnd sorts and dedupes, drops Or subs already implied by the
// required trigrams, and collapses trivial Ands.
func normalizeAnd(q Query) Query {
	q.Trigrams = sortedTrigrams(q.Trigrams)
	var subs []Query
	for _, s := range q.Subs {
		if !impliedBy(s, q.Trigrams) {
			subs = append(subs, s)
		}
	}
	q.Subs = sortedQueries(subs)
	switch {
	case len(q.Trigrams) == 0 && len(q.Subs) == 0:
		return allQuery
	case len(q.Trigrams) == 0 && len(q.Subs) == 1:
		return q.Subs[0]
	}
	return q
}

// impliedBy reports whether the Or query s holds whenever every trigram in
// required (ascending) is present: one of its alternatives needs nothing
// more than required.
func impliedBy(s Query, required []Trigram) bool {
	for _, t := range s.Trigrams {
		if _, found := slices.BinarySearch(required, t); found {
			return true
		}
	}
	for _, sub := range s.Subs {
		if len(sub.Subs) == 0 && isSubset(sub.Trigrams, required) {
			return true
		}
	}
	return false
}

// OrQuery returns a query matching what a or b matches.
func OrQuery(a, b Query) Query {
	switch {
	case a.Kind == QueryAll || b.Kind == QueryAll:
		return allQuery
	case a.Kind == QueryNone:
		return b
	case b.Kind == QueryNone:
		return a
	}
	var singles []Trigram
	var alternatives []Query
	for _, q := range []Query{a, b} {
		switch {
		case q.Kind == QueryOr:
			singles = append(singles, q.Trigrams...)
			alternatives = append(alternatives, q.Subs...)
		case len(q.Trigrams) == 1 && len(q.Subs) == 0:
			singles = append(singles, q.Trigrams[0])
		default:
			alternatives = append(alternatives, q)
		}
	}
	singles = sortedTrigrams(singles)
	alternatives = sortedQueries(alternatives)
	// Absorption: x OR (x AND y) is x. An alternative is dropped when a
	// single trigram or a pure-trigram alternative it contains is already
	// an alternative itself.
	var kept []Query
	for i, alt := range alternatives {
		absorbed := impliedBy(Query{Trigrams: singles}, alt.Trigrams)
		for j, other := range alternatives {
			if absorbed {
				break
			}
			if i != j && len(other.Subs) == 0 && isSubset(other.Trigrams, alt.Trigrams) &&
				(len(other.Trigrams) < len(alt.Trigrams) || len(alt.Subs) > 0) {
				absorbed = true
			}
		}
		if !absorbed {
			kept = append(kept, alt)
		}
	}
	switch {
	case len(singles) == 1 && len(kept) == 0:
		return Query{Kind: QueryAnd, Trigrams: singles}
	case len(singles) == 0 && len(kept) == 1:
		return kept[0]
	}
	return Query{Kind: QueryOr, Trigrams: singles, Subs: kept}
}

func sortedTrigrams(ts []Trigram) []Trigram {
	if len(ts) == 0 {
		return nil
	}
	out := slices.Clone(ts)
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedQueries(qs []Query) []Query {
	if len(qs) == 0 {
		return nil
	}
	keyed := make([]struct {
		key string
		q   Query
	}, len(qs))
	for i, q := range qs {
		keyed[i].key, keyed[i].q = q.String(), q
	}
	slices.SortFunc(keyed, func(x, y struct {
		key string
		q   Query
	}) int {
		return strings.Compare(x.key, y.key)
	})
	out := make([]Query, 0, len(qs))
	for i, k := range keyed {
		if i > 0 && keyed[i-1].key == k.key {
			continue
		}
		out = append(out, k.q)
	}
	return out
}

// isSubset reports whether every element of a (ascending) is in b
// (ascending).
func isSubset(a, b []Trigram) bool {
	j := 0
	for _, x := range a {
		for j < len(b) && b[j] < x {
			j++
		}
		if j == len(b) || b[j] != x {
			return false
		}
	}
	return true
}

// Matches evaluates q against one document's trigram set, given as a
// membership test. The oracle tests and fuzz targets use it to check that
// a query never rules out a document that matches.
func (q Query) Matches(has func(Trigram) bool) bool {
	switch q.Kind {
	case QueryAll:
		return true
	case QueryNone:
		return false
	case QueryAnd:
		for _, t := range q.Trigrams {
			if !has(t) {
				return false
			}
		}
		for _, s := range q.Subs {
			if !s.Matches(has) {
				return false
			}
		}
		return true
	case QueryOr:
		for _, t := range q.Trigrams {
			if has(t) {
				return true
			}
		}
		for _, s := range q.Subs {
			if s.Matches(has) {
				return true
			}
		}
		return false
	}
	panic(fmt.Sprintf("trigram: query kind %d", q.Kind))
}

// Eval returns the documents of ix that q allows. Required trigrams are
// intersected rarest first, so a selective trigram shrinks the work early.
func (q Query) Eval(ix *Index) *Bitmap {
	switch q.Kind {
	case QueryAll:
		return ix.live.Clone()
	case QueryNone:
		return &Bitmap{}
	case QueryAnd:
		lists := make([]*Bitmap, 0, len(q.Trigrams))
		for _, t := range q.Trigrams {
			p := ix.postings[t]
			if p == nil {
				return &Bitmap{}
			}
			lists = append(lists, p)
		}
		slices.SortStableFunc(lists, func(a, b *Bitmap) int { return a.Len() - b.Len() })
		var out *Bitmap
		if len(lists) > 0 {
			out = lists[0].Clone()
			for _, p := range lists[1:] {
				out = And(out, p)
			}
		}
		for _, s := range q.Subs {
			if out != nil && out.IsEmpty() {
				break
			}
			sub := s.Eval(ix)
			if out == nil {
				out = sub
			} else {
				out = And(out, sub)
			}
		}
		if out == nil {
			return ix.live.Clone()
		}
		return out
	case QueryOr:
		out := &Bitmap{}
		for _, t := range q.Trigrams {
			out = Or(out, ix.postings[t])
		}
		for _, s := range q.Subs {
			out = Or(out, s.Eval(ix))
		}
		return out
	}
	panic(fmt.Sprintf("trigram: query kind %d", q.Kind))
}

// String renders q for logs and fixtures: "+" for All, "-" for None, a
// trigram as its three folded code points in double quotes, an And as its
// trigrams then subs separated by spaces, an Or as its trigrams then subs
// separated by "|" inside parentheses. Inside a quoted trigram, '"' and '\'
// are escaped with '\', and code points that are not graphic (Unicode L, M,
// N, P, S or the ASCII space) are written \u{hex}. A sub that is an And of
// more than one element is parenthesized inside an Or.
func (q Query) String() string {
	var b strings.Builder
	q.write(&b, false)
	return b.String()
}

func (q Query) write(b *strings.Builder, inOr bool) {
	switch q.Kind {
	case QueryAll:
		b.WriteString("+")
		return
	case QueryNone:
		b.WriteString("-")
		return
	}
	sep := " "
	if q.Kind == QueryOr {
		sep = "|"
		b.WriteString("(")
	}
	wrap := inOr && q.Kind == QueryAnd && len(q.Trigrams)+len(q.Subs) > 1
	if wrap {
		b.WriteString("(")
	}
	n := 0
	for _, t := range q.Trigrams {
		if n > 0 {
			b.WriteString(sep)
		}
		writeTrigram(b, t)
		n++
	}
	for _, s := range q.Subs {
		if n > 0 {
			b.WriteString(sep)
		}
		s.write(b, q.Kind == QueryOr)
		n++
	}
	if wrap || q.Kind == QueryOr {
		b.WriteString(")")
	}
}

func writeTrigram(b *strings.Builder, t Trigram) {
	b.WriteByte('"')
	for _, r := range t.Runes() {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == ' ' || (r > ' ' && unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)):
			b.WriteRune(r)
		default:
			b.WriteString(`\u{` + strconv.FormatInt(int64(r), 16) + `}`)
		}
	}
	b.WriteByte('"')
}

// CheckInvariants verifies the normal form:
//
//   - Q1 All and None carry no trigrams or subs.
//   - Q2 Trigrams are strictly ascending; subs are strictly ascending by
//     String and never All or None.
//   - Q3 an And's subs are Ors and an Or's subs are Ands (nesting of the
//     same kind is flattened).
//   - Q4 an And has at least one trigram or two subs; an Or has at least
//     two alternatives (trigrams plus subs).
func (q Query) CheckInvariants() error {
	switch q.Kind {
	case QueryAll, QueryNone:
		if len(q.Trigrams) > 0 || len(q.Subs) > 0 {
			return fmt.Errorf("%w: Q1: %s carries operands", ErrInvariant, q)
		}
		return nil
	case QueryAnd, QueryOr:
	default:
		return fmt.Errorf("%w: unknown query kind %d", ErrInvariant, q.Kind)
	}
	for i := 1; i < len(q.Trigrams); i++ {
		if q.Trigrams[i-1] >= q.Trigrams[i] {
			return fmt.Errorf("%w: Q2: trigrams of %s not ascending", ErrInvariant, q)
		}
	}
	want := QueryOr
	if q.Kind == QueryOr {
		want = QueryAnd
	}
	for i, s := range q.Subs {
		if s.Kind != want {
			return fmt.Errorf("%w: Q3: %s nests kind %d", ErrInvariant, q, s.Kind)
		}
		if i > 0 && q.Subs[i-1].String() >= s.String() {
			return fmt.Errorf("%w: Q2: subs of %s not ascending", ErrInvariant, q)
		}
		if err := s.CheckInvariants(); err != nil {
			return err
		}
	}
	n := len(q.Trigrams) + len(q.Subs)
	if q.Kind == QueryAnd && len(q.Trigrams) == 0 && len(q.Subs) < 2 || q.Kind == QueryOr && n < 2 {
		return fmt.Errorf("%w: Q4: %s is trivial", ErrInvariant, q)
	}
	return nil
}
