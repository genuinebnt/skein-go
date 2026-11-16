package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// ListVersions returns a note's versions, newest first, paginated by the
// last batch of each version (stable while the note grows).
func (s *Service) ListVersions(ctx context.Context, subject domain.Subject, id domain.NoteID, size uint32, token string) ([]domain.Version, string, error) {
	if err := s.checkNote(ctx, subject, id, ActionRead); err != nil {
		return nil, "", err
	}
	fp := fingerprint("versions", string(id))
	after, err := parsePageToken(token, fp)
	if err != nil {
		return nil, "", err
	}
	before := int64(-1)
	if after != "" {
		if before, err = strconv.ParseInt(after, 10, 64); err != nil {
			return nil, "", fmt.Errorf("%w: page token", domain.ErrInvalidArgument)
		}
	}
	var all []domain.Version
	err = s.reg.do(ctx, id, func(st *noteState) error {
		all = domain.GroupVersions(st.batches, s.cfg.VersionWindow)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	limit := pageSize(size)
	var out []domain.Version
	for i := len(all) - 1; i >= 0 && len(out) <= limit; i-- {
		if before < 0 || all[i].LastSeq < before {
			out = append(out, all[i])
		}
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = pageToken(strconv.FormatInt(out[limit-1].LastSeq, 10), fp)
	}
	return out, next, nil
}

// textAt rebuilds the note's blocks at version v. The version must be a
// consistent cut: every operation it includes must have its parent or
// target included too.
func (st *noteState) blocksAt(v crdt.VersionVector) ([]blocks.Block, error) {
	current := st.doc.Sequence().Version()
	for r, c := range v {
		if c > current.Get(r) {
			return nil, fmt.Errorf("%w: version has %d operations from replica %d, the note has %d", domain.ErrOutOfRange, c, r, current.Get(r))
		}
	}
	var ops []crdt.Op
	var maxLamport uint64
	for _, op := range st.doc.Sequence().OpsSince(crdt.VersionVector{}) {
		if !v.Contains(op.ID) {
			continue
		}
		if dep := op.Dependency(); !dep.IsRoot() && !v.Contains(dep) {
			return nil, fmt.Errorf("%w: version includes %s but not %s, which it depends on", domain.ErrInvalidArgument, op.ID, dep)
		}
		ops = append(ops, op)
		maxLamport = max(maxLamport, op.Lamport)
	}
	elems, err := blocks.Elements(ops)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
	}
	return blocks.Layout(elems, st.doc.Attributes().At(maxLamport)), nil
}

// GetTextAt returns the note's Markdown at a version.
func (s *Service) GetTextAt(ctx context.Context, subject domain.Subject, id domain.NoteID, v crdt.VersionVector) (string, error) {
	if err := s.checkNote(ctx, subject, id, ActionRead); err != nil {
		return "", err
	}
	var md string
	err := s.reg.do(ctx, id, func(st *noteState) error {
		bs, err := st.blocksAt(v)
		md = blocks.Export(bs)
		return err
	})
	return md, err
}

// Diff returns the hunks from one version's Markdown to another's.
func (s *Service) Diff(ctx context.Context, subject domain.Subject, id domain.NoteID, from, to crdt.VersionVector) ([]dp.TextHunk, error) {
	if err := s.checkNote(ctx, subject, id, ActionRead); err != nil {
		return nil, err
	}
	var a, b string
	err := s.reg.do(ctx, id, func(st *noteState) error {
		before, err := st.blocksAt(from)
		if err != nil {
			return err
		}
		after, err := st.blocksAt(to)
		a, b = blocks.Export(before), blocks.Export(after)
		return err
	})
	if err != nil {
		return nil, err
	}
	// Diffing runs outside the actor: it is the expensive part and needs
	// only the two strings.
	return domain.DiffText(a, b), nil
}

// Blame says who wrote each span of the note's current Markdown.
func (s *Service) Blame(ctx context.Context, subject domain.Subject, id domain.NoteID) ([]domain.BlameSpan, error) {
	if err := s.checkNote(ctx, subject, id, ActionRead); err != nil {
		return nil, err
	}
	var spans []domain.BlameSpan
	err := s.reg.do(ctx, id, func(st *noteState) error {
		bs, err := st.blocks()
		if err != nil {
			return err
		}
		spans = domain.Blame(bs, domain.NewAuthorship(st.batches))
		return nil
	})
	return spans, err
}

// Undo undoes the replica's last change group and returns the generated
// operations. The caller must own the replica, which includes the replicas
// of the caller's agent runs, so a user can reject a suggestion.
func (s *Service) Undo(ctx context.Context, subject domain.Subject, id domain.NoteID, replica crdt.ReplicaID) ([]byte, error) {
	return s.undoRedo(ctx, subject, id, replica, domain.KindUndo)
}

// Redo re-applies the replica's most recently undone change group.
func (s *Service) Redo(ctx context.Context, subject domain.Subject, id domain.NoteID, replica crdt.ReplicaID) ([]byte, error) {
	return s.undoRedo(ctx, subject, id, replica, domain.KindRedo)
}

func (s *Service) undoRedo(ctx context.Context, subject domain.Subject, id domain.NoteID, replica crdt.ReplicaID, kind domain.BatchKind) ([]byte, error) {
	if err := s.checkNote(ctx, subject, id, ActionEdit); err != nil {
		return nil, err
	}
	if err := s.ensureReplica(ctx, subject, replica); err != nil {
		return nil, err
	}
	var out []byte
	err := s.reg.do(ctx, id, func(st *noteState) error {
		stacks := domain.UndoStacks(st.batches, replica, s.cfg.UndoWindow)
		var err error
		switch {
		case kind == domain.KindUndo && len(stacks.Undo) > 0:
			g := stacks.Undo[len(stacks.Undo)-1]
			out, err = st.invert(ctx, subject, replica, g.Batches, kind, g.ID())
		case kind == domain.KindRedo && len(stacks.Redo) > 0:
			undo := stacks.Redo[len(stacks.Redo)-1]
			out, err = st.invert(ctx, subject, replica, []int64{undo}, kind, undo)
		default:
			err = fmt.Errorf("%w: nothing to %s for replica %d", domain.ErrFailedPrecondition, kind, replica)
		}
		return err
	})
	return out, err
}

// Hello is the first frame of a sync stream.
type Hello struct {
	Note    domain.NoteID
	Replica crdt.ReplicaID
	Version crdt.VersionVector
}

// OpenSession starts a sync stream: it checks read access and replica
// ownership, queues the operations the client is missing (in frames of at
// most MaxFrameOps) and the current presence, and registers the session for
// live operations.
func (s *Service) OpenSession(ctx context.Context, subject domain.Subject, h Hello) (*Session, error) {
	if err := s.checkNote(ctx, subject, h.Note, ActionRead); err != nil {
		return nil, err
	}
	if err := s.ensureReplica(ctx, subject, h.Replica); err != nil {
		return nil, err
	}
	present, err := s.presence.List(ctx, h.Note)
	if err != nil {
		s.logger.Warn("listing presence failed", "note_id", string(h.Note), "error", err)
	}
	sess := &Session{svc: s, note: h.Note, subject: subject, replica: h.Replica, done: make(chan struct{})}
	err = s.reg.do(ctx, h.Note, func(st *noteState) error {
		missing := st.doc.Sequence().OpsSince(h.Version)
		frames := (len(missing)+s.cfg.MaxFrameOps-1)/s.cfg.MaxFrameOps + len(present)
		// Catch-up frames must all fit, whatever the live buffer size.
		sess.out = make(chan Frame, frames+s.cfg.SessionBuffer)
		for i := 0; i < len(missing); i += s.cfg.MaxFrameOps {
			sess.out <- Frame{Kind: FrameOps, Ops: crdt.EncodeOps(missing[i:min(i+s.cfg.MaxFrameOps, len(missing))])}
		}
		for _, p := range present {
			if p.Replica != h.Replica {
				sess.out <- Frame{Kind: FramePresence, Presence: p}
			}
		}
		st.sessions[sess] = struct{}{}
		st.lastUsed = s.clock.Now()
		s.m.sessions.Add(ctx, 1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}
