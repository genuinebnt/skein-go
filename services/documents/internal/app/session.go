package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// FrameKind tags a frame sent to a sync client (SyncResponse's oneof).
type FrameKind uint8

// The frame kinds.
const (
	FrameOps      FrameKind = 1
	FramePresence FrameKind = 2
	FrameRejected FrameKind = 3
)

// Frame is one message to a sync client.
type Frame struct {
	Kind     FrameKind
	Ops      []byte // FrameOps and FrameRejected: a batch in wire format v1
	Presence Presence
	Reason   string // FrameRejected
}

// Session is one live sync connection to a note (DocumentService.Sync). The
// note's actor writes frames to it; the stream's goroutine reads them with
// Frames. Closing never blocks the actor.
type Session struct {
	svc     *Service
	note    domain.NoteID
	subject domain.Subject
	replica crdt.ReplicaID

	out       chan Frame
	done      chan struct{}
	closeOnce sync.Once
	err       error
}

// Frames is the stream of frames for the client. It is never closed; watch
// Done to know when the session ends.
func (s *Session) Frames() <-chan Frame { return s.out }

// Done is closed when the session ends; Err then says why.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is the reason the session ended (nil before Done closes, or after the
// client closed it).
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

func (s *Session) offer(f Frame) bool {
	select {
	case <-s.done:
		return true
	default:
	}
	select {
	case s.out <- f:
		return true
	default:
		return false
	}
}

func (s *Session) close(err error) {
	s.closeOnce.Do(func() {
		s.err = err
		close(s.done)
		s.svc.m.sessions.Add(context.Background(), -1)
	})
}

// Submit handles an ops frame from the client. Refused operations come back
// to the client as a rejected frame with a reason (never dropped silently,
// ADR-0008); the returned error is only for failures that end the stream.
func (s *Session) Submit(ctx context.Context, data []byte) error {
	ops, err := crdt.DecodeOps(data)
	if err != nil {
		s.reject(data, "malformed", "malformed batch: "+err.Error())
		return nil
	}
	if len(ops) == 0 {
		return nil
	}
	if len(ops) > s.svc.cfg.MaxBatchOps {
		s.reject(data, "too_large", fmt.Sprintf("batch of %d operations exceeds %d", len(ops), s.svc.cfg.MaxBatchOps))
		return nil
	}
	for _, op := range ops {
		if op.ID.Replica != s.replica {
			s.reject(data, "wrong_replica", "operations must come from the replica named in the hello")
			return nil
		}
	}
	// Every batch is checked, not only the connection: a role can drop
	// while the stream is open, and the CRDT merges whatever it gets.
	switch err := s.svc.checkNote(ctx, s.subject, s.note, ActionEdit); {
	case errors.Is(err, domain.ErrNotFound):
		s.reject(data, "access_revoked", ReasonAccessRevoked)
		s.close(fmt.Errorf("%w: access to the note was revoked", domain.ErrPermissionDenied))
		return nil
	case errors.Is(err, domain.ErrPermissionDenied):
		s.reject(data, "insufficient_role", ReasonInsufficientRole)
		return nil
	case err != nil:
		return err
	}
	var res deliverResult
	err = s.svc.reg.do(ctx, s.note, func(st *noteState) error {
		var err error
		res, err = st.deliver(ctx, s.sender(), s.subject, ops)
		return err
	})
	if errors.Is(err, domain.ErrFailedPrecondition) {
		s.reject(data, "trashed", "the note is in the trash")
		return nil
	}
	if err != nil {
		return err
	}
	if len(res.Rejected) > 0 {
		s.reject(crdt.EncodeOps(res.Rejected), "crdt", res.Reason)
	}
	return nil
}

// Reasons in rejected frames for refused permission; the seed's
// rejected_ops.jsonl uses the same two cases.
const (
	ReasonAccessRevoked    = "access to the note was revoked"
	ReasonInsufficientRole = "your role on the note does not allow editing"
)

func (s *Session) sender() domain.Sender {
	kind := domain.KindEdit
	if s.subject.IsAgent() {
		kind = domain.KindSuggestion
	}
	return domain.Sender{Replica: s.replica, UserID: s.subject.UserID, AgentRunID: s.subject.AgentRunID, Kind: kind}
}

// reject sends a rejected frame; class is the metric's fixed reason.
func (s *Session) reject(ops []byte, class, reason string) {
	s.svc.m.reject(context.Background(), class)
	if !s.offer(Frame{Kind: FrameRejected, Ops: ops, Reason: reason}) {
		s.close(fmt.Errorf("%w: sync client fell behind; reconnect", domain.ErrResourceExhausted))
	}
}

// maxAnchor bounds an element anchor ("replica:counter" is at most 41 bytes).
const maxAnchor = 64

// SetPresence publishes the client's cursor. The user and replica always
// come from the session, never from the frame, so they cannot be forged.
// Presence is best effort: a storage failure is logged, not returned.
func (s *Session) SetPresence(ctx context.Context, anchor, head string) error {
	if len(anchor) > maxAnchor || len(head) > maxAnchor || strings.ContainsAny(anchor+head, "\n\x00") {
		return fmt.Errorf("%w: presence anchors must be element ids", domain.ErrInvalidArgument)
	}
	p := Presence{UserID: s.subject.UserID, Replica: s.replica, Anchor: anchor, Head: head}
	if err := s.svc.presence.Put(ctx, s.note, p, s.svc.cfg.PresenceTTL); err != nil {
		s.svc.logger.Warn("storing presence failed", "note_id", string(s.note), "error", err)
	}
	return s.svc.reg.do(ctx, s.note, func(st *noteState) error {
		st.broadcastPresence(s, p)
		return nil
	})
}

// Close ends the session from the client side and clears its presence.
func (s *Session) Close(ctx context.Context) {
	_ = s.svc.reg.ifLoaded(ctx, s.note, func(st *noteState) error {
		if _, ok := st.sessions[s]; ok {
			st.dropSession(s, nil)
			// An empty anchor tells the others the cursor is gone.
			st.broadcastPresence(s, Presence{UserID: s.subject.UserID, Replica: s.replica})
		}
		return nil
	})
	s.close(nil)
	if err := s.svc.presence.Remove(ctx, s.note, s.replica); err != nil {
		s.svc.logger.Warn("clearing presence failed", "note_id", string(s.note), "error", err)
	}
}

func (st *noteState) broadcastPresence(from *Session, p Presence) {
	for s := range st.sessions {
		if s != from {
			st.send(s, Frame{Kind: FramePresence, Presence: p})
		}
	}
}
