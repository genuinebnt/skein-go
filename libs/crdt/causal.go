package crdt

import "errors"

// DefaultMaxPending is the CausalBuffer limit used when none is given. A peer
// that needs more than this many out-of-order operations held at once is
// either broken or attacking; the right response is to resync it.
const DefaultMaxPending = 100_000

// CausalBuffer sits between the network and a Sequence. Networks reorder and
// duplicate messages; the buffer holds each operation that arrives before its
// dependencies and applies it as soon as they have been applied.
//
// Like Sequence, it is not safe for concurrent use.
type CausalBuffer struct {
	seq        *Sequence
	pending    []Op
	maxPending int
}

// NewCausalBuffer returns a buffer that delivers into seq. maxPending <= 0
// selects DefaultMaxPending.
func NewCausalBuffer(seq *Sequence, maxPending int) *CausalBuffer {
	if maxPending <= 0 {
		maxPending = DefaultMaxPending
	}
	return &CausalBuffer{seq: seq, maxPending: maxPending}
}

// Pending returns the number of operations waiting for their dependencies.
func (b *CausalBuffer) Pending() int { return len(b.pending) }

// Deliver accepts operations in any order, applies every one that is ready,
// and keeps the rest until they are.
//
// It returns the first permanent error (ErrInvalidOp, ErrIDConflict or
// ErrBufferFull). Operations that failed permanently are dropped; every other
// operation in the call is still processed, so one bad op does not block the
// good ones around it.
func (b *CausalBuffer) Deliver(ops ...Op) error {
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	progressed := false
	for _, op := range ops {
		err := b.seq.Apply(op)
		switch {
		case err == nil:
			progressed = true
		case errors.Is(err, ErrNotReady):
			if len(b.pending) >= b.maxPending {
				keep(ErrBufferFull)
				continue
			}
			b.pending = append(b.pending, op)
		default:
			keep(err)
		}
	}
	// Each successful apply can make pending ops ready. Repeat until a full
	// pass applies nothing. Worst case O(p²) for p pending ops; fine because
	// p is small in practice and bounded by maxPending.
	for progressed && len(b.pending) > 0 {
		progressed = false
		still := b.pending[:0]
		for _, op := range b.pending {
			err := b.seq.Apply(op)
			switch {
			case err == nil:
				progressed = true
			case errors.Is(err, ErrNotReady):
				still = append(still, op)
			default:
				keep(err)
			}
		}
		// Clear the tail so dropped ops do not stay reachable.
		for i := len(still); i < len(b.pending); i++ {
			b.pending[i] = Op{}
		}
		b.pending = still
	}
	return firstErr
}
