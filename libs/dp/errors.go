package dp

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by this package. Callers should test them with errors.Is,
// because most are wrapped with detail.
//
// Rust: one `#[derive(thiserror::Error)] enum Error` with a variant per
// sentinel below; CycleError becomes a variant carrying the cycle.
var (
	// ErrInvalidInput means an argument is outside its documented domain: a
	// negative budget, a duplicate note ID, a chunk limit of zero, and so on.
	// The wrapped message names the offending argument.
	ErrInvalidInput = errors.New("dp: invalid input")

	// ErrUnknownPrerequisite means a note lists a prerequisite that is not
	// among the notes given to the planner. The service must pass every
	// prerequisite, including ones outside the requested topic.
	ErrUnknownPrerequisite = errors.New("dp: unknown prerequisite")

	// ErrCyclicPrerequisites means the prerequisites contain a cycle, so no
	// reading order exists. The error is a *CycleError naming the cycle.
	ErrCyclicPrerequisites = errors.New("dp: prerequisites form a cycle")

	// ErrProblemTooLarge means the exact solver would need more memory than
	// MaxPlanCells allows. Narrow the topic or lower the budget.
	ErrProblemTooLarge = errors.New("dp: problem too large")

	// ErrBlockTooLarge means a block has more tokens than the chunk limit, so
	// no chunking exists. The caller must split such blocks first.
	ErrBlockTooLarge = errors.New("dp: block exceeds chunk limit")

	// ErrPatchMismatch means a patch does not apply: an equal or delete hunk
	// does not match the input at its position, or hunks remain after the
	// input ends.
	ErrPatchMismatch = errors.New("dp: patch does not apply")

	// ErrInvariant means a result checker (CheckHunks, CheckPlan,
	// CheckChunks) found a result that breaks its contract. Coming from this
	// package's own output it is always a bug here.
	ErrInvariant = errors.New("dp: invariant violated")
)

// CycleError reports cyclic prerequisites. Cycle lists the note IDs around
// one cycle, starting at the smallest ID, each note a prerequisite of the
// next and the last a prerequisite of the first. A self-prerequisite is a
// cycle of length one.
type CycleError struct {
	Cycle []string
}

// Error names the cycle, closing it back to its first note.
func (e *CycleError) Error() string {
	return fmt.Sprintf("%v: %s -> %s", ErrCyclicPrerequisites, strings.Join(e.Cycle, " -> "), e.Cycle[0])
}

// Unwrap lets errors.Is(err, ErrCyclicPrerequisites) match.
func (e *CycleError) Unwrap() error { return ErrCyclicPrerequisites }
