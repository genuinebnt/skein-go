// Package storetest is a contract test suite for app.Store
// implementations. The in-memory fake runs it in unit tests and the
// Postgres store runs it in the integration suite (build tag
// "integration"), so the fake cannot drift from the real store in the ways
// the use cases depend on: not-found and already-exists errors, rollback on
// error, upsert semantics, filtering by activity, ordering and the
// projection's purge.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// at is a fixed instant, truncated to microseconds (Postgres precision).
var at = time.Date(2026, 11, 1, 9, 30, 0, 123456000, time.UTC)

func id(t *testing.T) string {
	t.Helper()
	s, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func inTx(t *testing.T, s app.Store, fn func(ctx context.Context, tx app.Tx) error) {
	t.Helper()
	if err := s.InTx(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

// Run runs the contract against a fresh store from newStore per subtest.
func Run(t *testing.T, newStore func(t *testing.T) app.Store) {
	t.Run("UsersAreUniqueByEmailAndRoundTrip", func(t *testing.T) { usersRoundTrip(t, newStore(t)) })
	t.Run("ErrorsRollBackTheWholeTransaction", func(t *testing.T) { rollback(t, newStore(t)) })
	t.Run("MembershipsUpsertAndListOnlyActiveMembersInOrder", func(t *testing.T) { memberships(t, newStore(t)) })
	t.Run("OverridesAndLinksFollowTheirNotes", func(t *testing.T) { overridesAndLinks(t, newStore(t)) })
	t.Run("SessionsRotateAndRevoke", func(t *testing.T) { sessions(t, newStore(t)) })
	t.Run("ReplicasAndProcessedEventsAreUnique", func(t *testing.T) { replicasAndEvents(t, newStore(t)) })
}

func newUser(t *testing.T, email string) domain.User {
	verified := at
	return domain.User{ID: domain.UserID(id(t)), Email: email, DisplayName: "Name " + email, PasswordHash: "", EmailVerifiedAt: &verified, CreatedAt: at}
}

func usersRoundTrip(t *testing.T, s app.Store) {
	u := newUser(t, "ada@example.com")
	inTx(t, s, func(ctx context.Context, tx app.Tx) error { return tx.InsertUser(ctx, u) })
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		got, err := tx.UserByEmail(ctx, "ada@example.com")
		if err != nil {
			return err
		}
		if got.ID != u.ID || !got.CreatedAt.Equal(at) || got.EmailVerifiedAt == nil || !got.EmailVerifiedAt.Equal(at) {
			return fmt.Errorf("round trip: %+v", got)
		}
		if err := tx.SetPasswordHash(ctx, u.ID, "$argon2id$x"); err != nil {
			return err
		}
		if _, err := tx.UserByID(ctx, domain.UserID(id(t))); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("unknown user: %v", err)
		}
		if err := tx.SetPasswordHash(ctx, domain.UserID(id(t)), "x"); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("password of unknown user: %v", err)
		}
		return nil
	})
	err := s.InTx(context.Background(), func(ctx context.Context, tx app.Tx) error {
		return tx.InsertUser(ctx, newUser(t, "ada@example.com"))
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate email: %v", err)
	}
}

func rollback(t *testing.T, s app.Store) {
	u := newUser(t, "ada@example.com")
	boom := errors.New("boom")
	err := s.InTx(context.Background(), func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertUser(ctx, u); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx returned %v", err)
	}
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		if _, err := tx.UserByID(ctx, u.ID); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("rolled-back insert is visible: %v", err)
		}
		return nil
	})
}

func memberships(t *testing.T, s app.Store) {
	owner, a, b, gone := newUser(t, "o@example.com"), newUser(t, "a@example.com"), newUser(t, "b@example.com"), newUser(t, "g@example.com")
	ws := domain.WorkspaceID(id(t))
	other := domain.WorkspaceID(id(t))
	removed := at.Add(time.Hour)
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		for _, u := range []domain.User{owner, a, b, gone} {
			if err := tx.InsertUser(ctx, u); err != nil {
				return err
			}
		}
		for _, w := range []domain.WorkspaceID{ws, other} {
			if err := tx.InsertWorkspace(ctx, domain.Workspace{ID: w, Name: "W", OwnerID: owner.ID, CreatedAt: at}); err != nil {
				return err
			}
		}
		for _, m := range []domain.Membership{
			{WorkspaceID: ws, UserID: owner.ID, Role: domain.WorkspaceRoleOwner, JoinedAt: at},
			{WorkspaceID: ws, UserID: a.ID, Role: domain.WorkspaceRoleGuest, JoinedAt: at, InvitedBy: owner.ID},
			{WorkspaceID: ws, UserID: b.ID, Role: domain.WorkspaceRoleOwner, JoinedAt: at},
			{WorkspaceID: ws, UserID: gone.ID, Role: domain.WorkspaceRoleOwner, JoinedAt: at, RemovedAt: &removed},
			{WorkspaceID: other, UserID: a.ID, Role: domain.WorkspaceRoleMember, JoinedAt: at},
		} {
			if err := tx.UpsertMembership(ctx, m); err != nil {
				return err
			}
		}
		// Upsert replaces the row.
		return tx.UpsertMembership(ctx, domain.Membership{WorkspaceID: ws, UserID: a.ID, Role: domain.WorkspaceRoleMember, JoinedAt: at, InvitedBy: owner.ID})
	})
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		m, err := tx.MembershipForUpdate(ctx, ws, a.ID)
		if err != nil || m.Role != domain.WorkspaceRoleMember || m.InvitedBy != owner.ID {
			return fmt.Errorf("upserted membership %+v %v", m, err)
		}
		g, err := tx.Membership(ctx, ws, gone.ID)
		if err != nil || g.RemovedAt == nil || !g.RemovedAt.Equal(removed) {
			return fmt.Errorf("removed membership %+v %v", g, err)
		}
		if n, err := tx.CountActiveOwners(ctx, ws); err != nil || n != 2 {
			return fmt.Errorf("active owners %d %v (a removed owner does not count)", n, err)
		}
		list, err := tx.ListActiveMembers(ctx, ws, "", 10)
		if err != nil || len(list) != 3 {
			return fmt.Errorf("active members %+v %v", list, err)
		}
		for i := 1; i < len(list); i++ {
			if list[i-1].User.ID >= list[i].User.ID {
				return fmt.Errorf("members not ordered by ID: %v then %v", list[i-1].User.ID, list[i].User.ID)
			}
		}
		page, err := tx.ListActiveMembers(ctx, ws, list[0].User.ID, 1)
		if err != nil || len(page) != 1 || page[0].User.ID != list[1].User.ID {
			return fmt.Errorf("page after %s: %+v %v", list[0].User.ID, page, err)
		}
		ms, err := tx.MembershipsOf(ctx, a.ID, []domain.WorkspaceID{ws, other, domain.WorkspaceID(id(t))})
		if err != nil || len(ms) != 2 {
			return fmt.Errorf("memberships of a: %+v %v", ms, err)
		}
		if shared, err := tx.ShareActiveWorkspace(ctx, a.ID, b.ID); err != nil || !shared {
			return fmt.Errorf("a and b share ws: %v %v", shared, err)
		}
		if shared, err := tx.ShareActiveWorkspace(ctx, a.ID, gone.ID); err != nil || shared {
			return fmt.Errorf("a and a removed member share nothing: %v %v", shared, err)
		}
		if _, err := tx.Membership(ctx, other, b.ID); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("missing membership: %v", err)
		}
		return nil
	})
}

func overridesAndLinks(t *testing.T, s app.Store) {
	u := newUser(t, "u@example.com")
	ws := domain.WorkspaceID(id(t))
	n1, n2 := domain.NoteID(id(t)), domain.NoteID(id(t))
	link := domain.ShareLink{ID: domain.ShareLinkID(id(t)), NoteID: n1, WorkspaceID: ws, Role: domain.NoteRoleViewer,
		TokenHash: "8345f6b3900d5391e6473198670fa745e5b61f186f95eb77817d8605845c0f0c", CreatedBy: u.ID, CreatedAt: at}
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertUser(ctx, u); err != nil {
			return err
		}
		if err := tx.InsertWorkspace(ctx, domain.Workspace{ID: ws, Name: "W", OwnerID: u.ID, CreatedAt: at}); err != nil {
			return err
		}
		if err := tx.UpsertMembership(ctx, domain.Membership{WorkspaceID: ws, UserID: u.ID, Role: domain.WorkspaceRoleMember, JoinedAt: at}); err != nil {
			return err
		}
		for _, n := range []domain.NoteID{n1, n2} {
			if err := tx.UpsertNote(ctx, domain.NoteAccess{ID: n, WorkspaceID: ws, CreatedBy: u.ID, CreatedAt: at}); err != nil {
				return err
			}
			if err := tx.UpsertNoteRoleOverride(ctx, domain.NoteRoleOverride{NoteID: n, WorkspaceID: ws, UserID: u.ID, Role: domain.NoteRoleViewer, CreatedAt: at}); err != nil {
				return err
			}
		}
		if err := tx.UpsertNoteRoleOverride(ctx, domain.NoteRoleOverride{NoteID: n1, WorkspaceID: ws, UserID: u.ID, Role: domain.NoteRoleCommenter, CreatedAt: at}); err != nil {
			return err
		}
		return tx.InsertShareLink(ctx, link)
	})
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		os, err := tx.OverridesOf(ctx, u.ID, []domain.NoteID{n1})
		if err != nil || len(os) != 1 || os[0].Role != domain.NoteRoleCommenter {
			return fmt.Errorf("upserted override %+v %v", os, err)
		}
		if all, err := tx.OverridesInWorkspace(ctx, ws, u.ID); err != nil || len(all) != 2 {
			return fmt.Errorf("overrides in workspace %+v %v", all, err)
		}
		got, err := tx.ShareLinkByTokenHash(ctx, link.TokenHash)
		if err != nil || got.ID != link.ID || got.ExpiresAt != nil {
			return fmt.Errorf("link by hash %+v %v", got, err)
		}
		if err := tx.RevokeShareLink(ctx, link.ID, at); err != nil {
			return err
		}
		if err := tx.RevokeShareLink(ctx, link.ID, at.Add(time.Hour)); err != nil {
			return err
		}
		got, _ = tx.ShareLink(ctx, link.ID)
		if got.RevokedAt == nil || !got.RevokedAt.Equal(at) {
			return fmt.Errorf("revoking twice must keep the first time: %v", got.RevokedAt)
		}
		trashed := at.Add(time.Minute)
		if err := tx.SetNoteTrashed(ctx, n2, &trashed); err != nil {
			return err
		}
		if err := tx.SetNoteTrashed(ctx, domain.NoteID(id(t)), &trashed); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("trashing an unknown note: %v", err)
		}
		if err := tx.PurgeNote(ctx, n1); err != nil {
			return err
		}
		notes, err := tx.NotesInWorkspace(ctx, ws)
		if err != nil || len(notes) != 1 || notes[0].ID != n2 || notes[0].TrashedAt == nil {
			return fmt.Errorf("notes after purge %+v %v", notes, err)
		}
		if _, err := tx.ShareLink(ctx, link.ID); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("purge kept the link: %v", err)
		}
		if err := tx.DeleteOverridesInWorkspace(ctx, ws, u.ID); err != nil {
			return err
		}
		if all, _ := tx.OverridesInWorkspace(ctx, ws, u.ID); len(all) != 0 {
			return fmt.Errorf("overrides left %+v", all)
		}
		return nil
	})
}

func sessions(t *testing.T, s app.Store) {
	u := newUser(t, "u@example.com")
	sess := domain.Session{ID: domain.SessionID(id(t)), UserID: u.ID, CreatedAt: at}
	hash := "66f79f984707e1a66524ed4130e7216e9a1e583b5b8f6c584ad51733bb018387"
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertUser(ctx, u); err != nil {
			return err
		}
		if err := tx.InsertSession(ctx, sess); err != nil {
			return err
		}
		return tx.InsertRefreshToken(ctx, domain.RefreshToken{Hash: hash, SessionID: sess.ID, IssuedAt: at, ExpiresAt: at.Add(time.Hour)})
	})
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		if err := tx.MarkRefreshTokenRotated(ctx, hash, at); err != nil {
			return err
		}
		rt, err := tx.RefreshTokenForUpdate(ctx, hash)
		if err != nil || rt.RotatedAt == nil || !rt.ExpiresAt.Equal(at.Add(time.Hour)) {
			return fmt.Errorf("rotated token %+v %v", rt, err)
		}
		if n, err := tx.RevokeUserSessions(ctx, u.ID, at, domain.RevokePasswordReset); err != nil || n != 1 {
			return fmt.Errorf("revoked %d %v", n, err)
		}
		if err := tx.RevokeSession(ctx, sess.ID, at.Add(time.Hour), domain.RevokeSignOut); err != nil {
			return err
		}
		got, err := tx.SessionByIDForUpdate(ctx, sess.ID)
		if err != nil || got.RevokeReason != domain.RevokePasswordReset {
			return fmt.Errorf("a second revocation replaced the first: %+v %v", got, err)
		}
		if _, err := tx.RefreshTokenForUpdate(ctx, "00"); !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("unknown token: %v", err)
		}
		return nil
	})
}

func replicasAndEvents(t *testing.T, s app.Store) {
	u, v := newUser(t, "u@example.com"), newUser(t, "v@example.com")
	high := domain.ReplicaID(1<<63 + 12345) // above int64: checks the bigint mapping
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		for _, x := range []domain.User{u, v} {
			if err := tx.InsertUser(ctx, x); err != nil {
				return err
			}
		}
		return tx.InsertReplica(ctx, domain.Replica{ID: high, UserID: u.ID, AgentRunID: domain.AgentRunID(id(t)), RegisteredAt: at})
	})
	err := s.InTx(context.Background(), func(ctx context.Context, tx app.Tx) error {
		return tx.InsertReplica(ctx, domain.Replica{ID: high, UserID: v.ID, RegisteredAt: at})
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("second owner for a replica: %v", err)
	}
	inTx(t, s, func(ctx context.Context, tx app.Tx) error {
		r, err := tx.Replica(ctx, high)
		if err != nil || r.ID != high || r.UserID != u.ID || r.AgentRunID == "" {
			return fmt.Errorf("replica %+v %v", r, err)
		}
		event := id(t)
		first, err := tx.MarkEventProcessed(ctx, "c", event)
		if err != nil || !first {
			return fmt.Errorf("first delivery %v %v", first, err)
		}
		again, err := tx.MarkEventProcessed(ctx, "c", event)
		if err != nil || again {
			return fmt.Errorf("second delivery %v %v", again, err)
		}
		other, err := tx.MarkEventProcessed(ctx, "d", event)
		if err != nil || !other {
			return fmt.Errorf("another consumer %v %v", other, err)
		}
		return nil
	})
}
