package dp

import "fmt"

// HunkKind says what a hunk does. The values match DiffHunk.Kind in
// proto/skein/v1/documents.proto so services can convert with a cast.
//
// Rust: `enum HunkKind { Equal = 1, Insert = 2, Delete = 3 }`.
type HunkKind uint8

const (
	// HunkEqual elements appear in both a and b.
	HunkEqual HunkKind = 1
	// HunkInsert elements appear only in b.
	HunkInsert HunkKind = 2
	// HunkDelete elements appear only in a.
	HunkDelete HunkKind = 3
)

func (k HunkKind) String() string {
	switch k {
	case HunkEqual:
		return "equal"
	case HunkInsert:
		return "insert"
	case HunkDelete:
		return "delete"
	}
	return fmt.Sprintf("HunkKind(%d)", uint8(k))
}

// Hunk is a run of elements that are kept, inserted or deleted.
type Hunk[T comparable] struct {
	Kind  HunkKind
	Elems []T
}

// Diff returns a shortest edit script from a to b as hunks, using Myers'
// O(ND) algorithm ("An O(ND) Difference Algorithm and Its Variations",
// 1986) in its linear-space form.
//
// # Algorithm
//
// Edits are paths through the edit graph from (0, 0) to (N, M): right is a
// deletion, down an insertion, and a diagonal (a "snake") is a free match.
// For each d, V[k] holds the furthest x reachable on diagonal k = x - y with
// d non-diagonal steps:
//
//	V_d[k] = snake(max(V_{d-1}[k-1] + 1, V_{d-1}[k+1]))
//
// The linear-space form runs this forwards from (0, 0) and backwards from
// (N, M) at the same time until the two frontiers overlap. The overlapping
// snake (the "middle snake") lies on some shortest path, so the problem
// splits into the parts before and after it, solved recursively. Common
// prefixes and suffixes are stripped first, which keeps minimality and makes
// the usual cases (small edits in long texts) close to linear.
//
// Time O((N+M)·D), where D is the number of inserted plus deleted elements.
// Memory O(N+M).
//
// # Canonical output
//
// Shortest scripts are not unique, so the result is normalised, and the
// Rust port must produce the same hunks:
//
//   - no hunk is empty and no two adjacent hunks have the same kind;
//   - between two equal hunks (or the ends), all deleted elements come in one
//     delete hunk, followed by all inserted elements in one insert hunk.
//
// Which elements are matched still depends on the algorithm's choices; the
// conformance fixtures pin them.
//
// CheckHunks verifies a result; ApplyPatch replays one.
func Diff[T comparable](a, b []T) []Hunk[T] {
	var s script[T]
	s.diff(a, b)
	return s.hunks()
}

// script collects single-element operations in order and groups them into
// canonical hunks at the end.
type script[T comparable] struct {
	ops []scriptOp[T]
}

type scriptOp[T comparable] struct {
	kind HunkKind
	elem T
}

func (s *script[T]) emit(kind HunkKind, elems []T) {
	for _, e := range elems {
		s.ops = append(s.ops, scriptOp[T]{kind, e})
	}
}

func (s *script[T]) diff(a, b []T) {
	prefix := commonPrefix(a, b)
	s.emit(HunkEqual, a[:prefix])
	a, b = a[prefix:], b[prefix:]
	suffix := commonSuffix(a, b)
	tail := a[len(a)-suffix:]
	a, b = a[:len(a)-suffix], b[:len(b)-suffix]

	switch {
	case len(a) == 0:
		s.emit(HunkInsert, b)
	case len(b) == 0:
		s.emit(HunkDelete, a)
	default:
		// After stripping, a and b are non-empty and differ at both ends,
		// so D >= 2 and each recursive half has strictly fewer edits.
		snake := middleSnake(a, b)
		s.diff(a[:snake.x0], b[:snake.y0])
		s.emit(HunkEqual, a[snake.x0:snake.x1])
		s.diff(a[snake.x1:], b[snake.y1:])
	}
	s.emit(HunkEqual, tail)
}

// hunks groups the operations, moving deletes before inserts within each
// changed region.
func (s *script[T]) hunks() []Hunk[T] {
	var out []Hunk[T]
	var deleted, inserted []T
	flush := func() {
		if len(deleted) > 0 {
			out = append(out, Hunk[T]{HunkDelete, deleted})
		}
		if len(inserted) > 0 {
			out = append(out, Hunk[T]{HunkInsert, inserted})
		}
		deleted, inserted = nil, nil
	}
	for _, op := range s.ops {
		switch op.kind {
		case HunkDelete:
			deleted = append(deleted, op.elem)
		case HunkInsert:
			inserted = append(inserted, op.elem)
		case HunkEqual:
			flush()
			if n := len(out); n > 0 && out[n-1].Kind == HunkEqual {
				out[n-1].Elems = append(out[n-1].Elems, op.elem)
			} else {
				out = append(out, Hunk[T]{HunkEqual, []T{op.elem}})
			}
		}
	}
	flush()
	return out
}

// snake is a diagonal run of matches from (x0, y0) to (x1, y1), possibly
// empty.
type snake struct{ x0, y0, x1, y1 int }

// middleSnake finds a snake on a shortest edit path from (0, 0) to (N, M).
//
// forward[k] is the furthest x on diagonal k = x - y reached from the start;
// backward[k] is the furthest distance travelled from the end on reversed
// diagonal k, where reversed diagonal k is forward diagonal delta - k. The
// paths meet when, on a shared diagonal, forward x + backward distance >= N.
// With delta = N - M odd the meeting is detected on a forward step (total
// edits 2d-1), with delta even on a backward step (total 2d).
func middleSnake[T comparable](a, b []T) snake {
	n, m := len(a), len(b)
	delta := n - m
	odd := delta%2 != 0
	limit := (n + m + 1) / 2
	offset := limit + 1
	forward := make([]int, 2*offset+1)
	backward := make([]int, 2*offset+1)
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && forward[offset+k-1] < forward[offset+k+1]) {
				x = forward[offset+k+1]
			} else {
				x = forward[offset+k-1] + 1
			}
			y := x - k
			x0, y0 := x, y
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			forward[offset+k] = x
			if c := delta - k; odd && c >= -(d-1) && c <= d-1 && x+backward[offset+c] >= n {
				return snake{x0, y0, x, y}
			}
		}
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && backward[offset+k-1] < backward[offset+k+1]) {
				x = backward[offset+k+1]
			} else {
				x = backward[offset+k-1] + 1
			}
			y := x - k
			x0, y0 := x, y
			for x < n && y < m && a[n-1-x] == b[m-1-y] {
				x, y = x+1, y+1
			}
			backward[offset+k] = x
			if c := delta - k; !odd && c >= -d && c <= d && forward[offset+c]+x >= n {
				return snake{n - x, m - y, n - x0, m - y0}
			}
		}
	}
	panic("dp: middleSnake found no overlap") // unreachable: d = limit always overlaps
}

func commonPrefix[T comparable](a, b []T) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func commonSuffix[T comparable](a, b []T) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[len(a)-1-i] != b[len(b)-1-i] {
			return i
		}
	}
	return n
}

// ApplyPatch replays hunks against a and returns the result: equal hunks
// copy elements from a, delete hunks skip them, insert hunks add new ones.
// Every equal and delete hunk must match a at its position and the hunks
// must consume all of a, otherwise it returns ErrPatchMismatch. Applying
// Diff(a, b) to a returns b.
//
// Time and memory O(len(a) + inserted elements).
func ApplyPatch[T comparable](a []T, hunks []Hunk[T]) ([]T, error) {
	out := make([]T, 0, len(a))
	pos := 0
	for i, h := range hunks {
		switch h.Kind {
		case HunkInsert:
			out = append(out, h.Elems...)
		case HunkEqual, HunkDelete:
			end := pos + len(h.Elems)
			if end > len(a) {
				return nil, fmt.Errorf("%w: hunk %d (%s) runs past the end of the input", ErrPatchMismatch, i, h.Kind)
			}
			for j, e := range h.Elems {
				if a[pos+j] != e {
					return nil, fmt.Errorf("%w: hunk %d (%s) differs from the input at element %d", ErrPatchMismatch, i, h.Kind, pos+j)
				}
			}
			if h.Kind == HunkEqual {
				out = append(out, h.Elems...)
			}
			pos = end
		default:
			return nil, fmt.Errorf("%w: hunk %d has kind %s", ErrPatchMismatch, i, h.Kind)
		}
	}
	if pos != len(a) {
		return nil, fmt.Errorf("%w: hunks consume %d of %d input elements", ErrPatchMismatch, pos, len(a))
	}
	return out, nil
}

// InvertPatch returns hunks that turn b back into a: inserts become deletes
// and deletes become inserts. Deletes are moved before inserts again so the
// result stays canonical.
func InvertPatch[T comparable](hunks []Hunk[T]) []Hunk[T] {
	var s script[T]
	for _, h := range hunks {
		kind := h.Kind
		switch kind {
		case HunkInsert:
			kind = HunkDelete
		case HunkDelete:
			kind = HunkInsert
		}
		s.emit(kind, h.Elems)
	}
	return s.hunks()
}

// EditCount returns the number of inserted plus deleted elements, the D in
// O(ND). For a Diff result it equals len(a) + len(b) - 2·LCSLength(a, b).
func EditCount[T comparable](hunks []Hunk[T]) int {
	n := 0
	for _, h := range hunks {
		if h.Kind != HunkEqual {
			n += len(h.Elems)
		}
	}
	return n
}

// CheckHunks verifies that hunks are a canonical diff from a to b (see
// Diff): kinds are valid, no hunk is empty, adjacent hunks differ in kind, a
// delete never follows an insert directly, and applying the hunks to a gives
// b. It does not check minimality; compare EditCount with LCSLength for
// that. Errors wrap ErrInvariant.
func CheckHunks[T comparable](a, b []T, hunks []Hunk[T]) error {
	for i, h := range hunks {
		if h.Kind != HunkEqual && h.Kind != HunkInsert && h.Kind != HunkDelete {
			return fmt.Errorf("%w: hunk %d has kind %s", ErrInvariant, i, h.Kind)
		}
		if len(h.Elems) == 0 {
			return fmt.Errorf("%w: hunk %d is empty", ErrInvariant, i)
		}
		if i == 0 {
			continue
		}
		prev := hunks[i-1].Kind
		if prev == h.Kind {
			return fmt.Errorf("%w: hunks %d and %d are both %s", ErrInvariant, i-1, i, h.Kind)
		}
		if prev == HunkInsert && h.Kind == HunkDelete {
			return fmt.Errorf("%w: delete hunk %d follows an insert", ErrInvariant, i)
		}
	}
	got, err := ApplyPatch(a, hunks)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvariant, err)
	}
	if len(got) != len(b) {
		return fmt.Errorf("%w: patch produces %d elements, want %d", ErrInvariant, len(got), len(b))
	}
	for i := range got {
		if got[i] != b[i] {
			return fmt.Errorf("%w: patch result differs from b at element %d", ErrInvariant, i)
		}
	}
	return nil
}
