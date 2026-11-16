package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Run does the service's background work until ctx ends: it first repairs
// projections a crash left unpublished, then periodically re-checks open
// sync sessions and purges notes trashed longer than TrashRetention. On
// return every actor has flushed and stopped.
func (s *Service) Run(ctx context.Context) error {
	if err := s.RecoverProjections(ctx); err != nil {
		s.logger.Warn("recovering projections failed", "error", err)
	}
	recheck := time.NewTicker(s.cfg.RecheckEvery)
	defer recheck.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			return s.reg.stopAll(stopCtx)
		case <-recheck.C:
			s.recheckSessions(ctx, "", "")
		case <-purge.C:
			if n, err := s.PurgeTrashed(ctx, s.clock.Now().Add(-s.cfg.TrashRetention)); err != nil {
				s.logger.Warn("purging trashed notes failed", "purged", n, "error", err)
			}
		}
	}
}

// RecoverProjections loads every note whose op log is ahead of its
// published projection; loading marks it dirty and flushing publishes it.
func (s *Service) RecoverProjections(ctx context.Context) error {
	for {
		ids, err := s.store.StaleProjections(ctx, 100)
		if err != nil || len(ids) == 0 {
			return err
		}
		for _, id := range ids {
			err := s.reg.do(ctx, id, func(st *noteState) error { return st.flushProjection(ctx, true) })
			if err != nil {
				return fmt.Errorf("note %s: %w", id, err)
			}
		}
	}
}

// FlushAll publishes every pending projection now (tests and shutdown).
func (s *Service) FlushAll(ctx context.Context) error {
	var errs []error
	for _, a := range s.reg.all() {
		err := a.call(ctx, func(st *noteState) error { return st.flushProjection(ctx, true) })
		if err != nil && !errors.Is(err, errActorStopped) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// AccessChanged reacts to identity's AccessChanged event: cached decisions
// about the user are dropped, and when access was revoked, the user's open
// sync sessions on affected notes are re-checked and closed if they can no
// longer read (ADR-0008). Instances that did not receive the event catch up
// within RecheckEvery plus the cache TTL.
func (s *Service) AccessChanged(ctx context.Context, workspace domain.WorkspaceID, note domain.NoteID, userID string, revoked bool) error {
	if s.authCache != nil {
		if err := s.authCache.InvalidateUser(ctx, userID); err != nil {
			return err
		}
	}
	if revoked {
		s.recheckSessions(ctx, userID, func() string {
			if note != "" {
				return string(note)
			}
			return "ws:" + string(workspace)
		}())
	}
	return nil
}

// recheckSessions re-checks read access of open sessions, optionally only
// one user's on one note ("<id>") or one workspace ("ws:<id>"). The checks
// run outside the actors, which only list and close.
func (s *Service) recheckSessions(ctx context.Context, userID, scope string) {
	type candidate struct {
		sess *Session
		note domain.NoteID
	}
	for _, a := range s.reg.all() {
		var found []candidate
		_ = a.call(ctx, func(st *noteState) error {
			if scope != "" && scope != string(st.note.ID) && scope != "ws:"+string(st.note.WorkspaceID) {
				return nil
			}
			for sess := range st.sessions {
				if userID == "" || sess.subject.UserID == userID {
					found = append(found, candidate{sess, st.note.ID})
				}
			}
			return nil
		})
		for _, c := range found {
			err := s.checkNote(ctx, c.sess.subject, c.note, ActionRead)
			if !errors.Is(err, domain.ErrNotFound) {
				continue
			}
			_ = a.call(ctx, func(st *noteState) error {
				if _, ok := st.sessions[c.sess]; ok {
					st.dropSession(c.sess, fmt.Errorf("%w: access to the note was revoked", domain.ErrPermissionDenied))
				}
				return nil
			})
		}
	}
}

// PurgeTrashed erases notes trashed before cutoff (ADR-0006's purge path):
// the op log, snapshots and projection go, NotePurged tells every
// projection to erase the note, and the ID stays NOT_FOUND.
func (s *Service) PurgeTrashed(ctx context.Context, cutoff time.Time) (int, error) {
	ids, err := s.store.TrashedBefore(ctx, cutoff, 100)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, id := range ids {
		err := s.reg.do(ctx, id, func(st *noteState) error {
			if !st.note.Trashed() || !st.note.TrashedAt.Before(cutoff) {
				return nil // restored meanwhile
			}
			ev := domain.Event{Kind: domain.EventNotePurged, NoteID: id, WorkspaceID: st.note.WorkspaceID, At: s.clock.Now()}
			if err := s.store.PurgeNote(ctx, id, []domain.Event{ev}); err != nil {
				return err
			}
			st.closeSessions(fmt.Errorf("%w: note purged", domain.ErrNotFound))
			// The state must not outlive the data: nothing may be served
			// or flushed from it.
			return poisoned{fmt.Errorf("%w: note purged", domain.ErrNotFound)}
		})
		var p poisoned
		if err != nil && !errors.As(err, &p) {
			return purged, err
		}
		s.notes.Delete(ctx, id)
		purged++
	}
	return purged, nil
}

// kill stops every actor without flushing or snapshotting, as a crash
// would. Tests use it, then load the same store into a new Service.
func (s *Service) kill() {
	for _, a := range s.reg.all() {
		_ = a.call(context.Background(), func(*noteState) error {
			return poisoned{errors.New("killed")}
		})
	}
	s.reg.wg.Wait()
}
