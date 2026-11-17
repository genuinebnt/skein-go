package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// GetUser returns a user the caller shares an active workspace with (or
// the caller themself); anyone else is ErrNotFound.
func (s *Service) GetUser(ctx context.Context, caller domain.Subject, id domain.UserID) (domain.User, error) {
	if err := requireUser(caller); err != nil {
		return domain.User{}, err
	}
	var u domain.User
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if caller.UserID != id {
			shared, err := tx.ShareActiveWorkspace(ctx, caller.UserID, id)
			if err != nil {
				return err
			}
			if !shared {
				return domain.ErrNotFound
			}
		}
		var err error
		u, err = tx.UserByID(ctx, id)
		return err
	})
	return u, err
}

// GetUserInternal is GetUser for a token-less internal caller (the
// notifications consumer reading names and addresses for an event). It is
// refused unless AllowUnauthenticatedInternalCalls is set; see
// CheckInternalCaller.
func (s *Service) GetUserInternal(ctx context.Context, id domain.UserID) (domain.User, error) {
	if !s.cfg.AllowUnauthenticatedInternalCalls {
		return domain.User{}, domain.ErrUnauthenticated
	}
	var u domain.User
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		u, err = tx.UserByID(ctx, id)
		return err
	})
	return u, err
}

// CreateWorkspace creates a workspace owned by the caller.
func (s *Service) CreateWorkspace(ctx context.Context, caller domain.Subject, name string) (domain.WorkspaceID, error) {
	if err := requireUser(caller); err != nil {
		return "", err
	}
	name, err := domain.NormalizeName("name", name, domain.MaxWorkspaceRunes)
	if err != nil {
		return "", err
	}
	id, err := domain.NewID()
	if err != nil {
		return "", err
	}
	ws := domain.WorkspaceID(id)
	now := s.now()
	change := domain.AccessChange{WorkspaceID: ws, UserID: caller.UserID, Actor: caller, At: now}
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.InsertWorkspace(ctx, domain.Workspace{ID: ws, Name: name, OwnerID: caller.UserID, CreatedAt: now}); err != nil {
			return err
		}
		m := domain.Membership{WorkspaceID: ws, UserID: caller.UserID, Role: domain.WorkspaceRoleOwner, JoinedAt: now}
		if err := tx.UpsertMembership(ctx, m); err != nil {
			return err
		}
		return tx.EnqueueAccessChanged(ctx, change)
	})
	if err != nil {
		return "", err
	}
	s.invalidateChanges(ctx, []domain.AccessChange{change})
	return ws, nil
}

// Page sizes, per docs/api/README.md "Pagination".
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// memberCursor is the decoded ListMembers page token. It names the
// workspace so a token cannot be replayed against another one.
type memberCursor struct {
	Workspace string `json:"w"`
	After     string `json:"a"`
}

func encodeMemberCursor(ws domain.WorkspaceID, after domain.UserID) string {
	b, _ := json.Marshal(memberCursor{Workspace: string(ws), After: string(after)})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeMemberCursor(ws domain.WorkspaceID, token string) (domain.UserID, error) {
	if token == "" {
		return "", nil
	}
	invalid := domain.InvalidField("page.page_token", "is not a token from this listing")
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return "", invalid
	}
	var c memberCursor
	if err := json.Unmarshal(b, &c); err != nil || c.Workspace != string(ws) {
		return "", invalid
	}
	after, err := domain.ParseUserID("page.page_token", c.After)
	if err != nil {
		return "", invalid
	}
	return after, nil
}

// MemberPage is one page of ListMembers.
type MemberPage struct {
	Members       []domain.Member
	NextPageToken string
}

// ListMembers lists a workspace's active members, ordered by user ID. The
// caller must be an active member; to anyone else the workspace does not
// exist.
func (s *Service) ListMembers(ctx context.Context, caller domain.Subject, ws domain.WorkspaceID, pageSize uint32, pageToken string) (MemberPage, error) {
	if err := requireUser(caller); err != nil {
		return MemberPage{}, err
	}
	now := s.now()
	return s.listMembers(ctx, ws, pageSize, pageToken, func(ctx context.Context, tx Tx) error {
		role, err := workspaceRole(ctx, tx, ws, caller.UserID, now)
		if err != nil {
			return err
		}
		if role == domain.WorkspaceRoleNone {
			return domain.ErrNotFound
		}
		return nil
	})
}

// ListMembersInternal is ListMembers for a token-less internal caller (the
// notifications consumer resolving @handles while handling an event). It
// is refused unless AllowUnauthenticatedInternalCalls is set; see
// CheckInternalCaller.
func (s *Service) ListMembersInternal(ctx context.Context, ws domain.WorkspaceID, pageSize uint32, pageToken string) (MemberPage, error) {
	if !s.cfg.AllowUnauthenticatedInternalCalls {
		return MemberPage{}, domain.ErrUnauthenticated
	}
	return s.listMembers(ctx, ws, pageSize, pageToken, func(ctx context.Context, tx Tx) error {
		_, err := tx.Workspace(ctx, ws)
		return err
	})
}

// listMembers pages through a workspace's active members after allowed
// (run in the same transaction) lets the caller see them.
func (s *Service) listMembers(ctx context.Context, ws domain.WorkspaceID, pageSize uint32, pageToken string, allowed func(context.Context, Tx) error) (MemberPage, error) {
	after, err := decodeMemberCursor(ws, pageToken)
	if err != nil {
		return MemberPage{}, err
	}
	size := int(min(pageSize, MaxPageSize))
	if size == 0 {
		size = DefaultPageSize
	}
	var page MemberPage
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := allowed(ctx, tx); err != nil {
			return err
		}
		members, err := tx.ListActiveMembers(ctx, ws, after, size+1)
		if err != nil {
			return err
		}
		if len(members) > size {
			members = members[:size]
			page.NextPageToken = encodeMemberCursor(ws, members[size-1].User.ID)
		}
		page.Members = members
		return nil
	})
	return page, err
}

// SetWorkspaceRole adds a member with a role or changes an existing
// member's role. Re-adding a removed member starts a fresh membership.
func (s *Service) SetWorkspaceRole(ctx context.Context, caller domain.Subject, ws domain.WorkspaceID, target domain.UserID, role domain.WorkspaceRole) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	if !role.Valid() {
		return domain.InvalidField("role", "must be guest, member, admin or owner; use RemoveMember to remove")
	}
	now := s.now()
	var changes []domain.AccessChange
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		actor, err := s.actorRole(ctx, tx, ws, caller.UserID, now)
		if err != nil {
			return err
		}
		if _, err := tx.UserByID(ctx, target); err != nil {
			return err
		}
		// Owner rows first, then the target's: every role change takes its
		// locks in this order, so two concurrent changes cannot deadlock.
		owners, err := tx.CountActiveOwners(ctx, ws)
		if err != nil {
			return err
		}
		current, existing, err := lockedMembership(ctx, tx, ws, target, now)
		if err != nil {
			return err
		}
		if err := domain.CheckRoleChange(actor, current, role, owners); err != nil {
			return err
		}
		if current == role {
			return nil
		}
		m := domain.Membership{WorkspaceID: ws, UserID: target, Role: role, JoinedAt: now, InvitedBy: caller.UserID}
		if current != domain.WorkspaceRoleNone {
			m.JoinedAt, m.InvitedBy = existing.JoinedAt, existing.InvitedBy
		}
		if err := tx.UpsertMembership(ctx, m); err != nil {
			return err
		}
		c := domain.AccessChange{WorkspaceID: ws, UserID: target, Revoked: domain.AccessLowered(current, role), Actor: caller, At: now}
		changes = append(changes, c)
		return tx.EnqueueAccessChanged(ctx, c)
	})
	if err != nil {
		return err
	}
	s.invalidateChanges(ctx, changes)
	return nil
}

// RemoveMember ends a membership. The member's per-note overrides in the
// workspace are deleted with it, so being invited back starts from the
// workspace role alone.
func (s *Service) RemoveMember(ctx context.Context, caller domain.Subject, ws domain.WorkspaceID, target domain.UserID) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	now := s.now()
	var changes []domain.AccessChange
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		actor, err := s.actorRole(ctx, tx, ws, caller.UserID, now)
		if err != nil {
			return err
		}
		owners, err := tx.CountActiveOwners(ctx, ws)
		if err != nil {
			return err
		}
		current, existing, err := lockedMembership(ctx, tx, ws, target, now)
		if err != nil {
			return err
		}
		if current == domain.WorkspaceRoleNone {
			return fmt.Errorf("%w: not a member", domain.ErrNotFound)
		}
		if err := domain.CheckRemoval(actor, current, owners); err != nil {
			return err
		}
		existing.RemovedAt = &now
		if err := tx.UpsertMembership(ctx, existing); err != nil {
			return err
		}
		if err := tx.DeleteOverridesInWorkspace(ctx, ws, target); err != nil {
			return err
		}
		c := domain.AccessChange{WorkspaceID: ws, UserID: target, Revoked: true, Actor: caller, At: now}
		changes = append(changes, c)
		return tx.EnqueueAccessChanged(ctx, c)
	})
	if err != nil {
		return err
	}
	s.invalidateChanges(ctx, changes)
	return nil
}

// actorRole returns the caller's role, or ErrNotFound when they are not a
// member (the workspace is not theirs to know about).
func (s *Service) actorRole(ctx context.Context, tx Tx, ws domain.WorkspaceID, user domain.UserID, now time.Time) (domain.WorkspaceRole, error) {
	role, err := workspaceRole(ctx, tx, ws, user, now)
	if err != nil {
		return role, err
	}
	if role == domain.WorkspaceRoleNone {
		return role, fmt.Errorf("%w: workspace", domain.ErrNotFound)
	}
	return role, nil
}

// lockedMembership locks and returns the target's membership row, with the
// role it currently grants (none when absent or removed).
func lockedMembership(ctx context.Context, tx Tx, ws domain.WorkspaceID, user domain.UserID, now time.Time) (domain.WorkspaceRole, domain.Membership, error) {
	m, err := tx.MembershipForUpdate(ctx, ws, user)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.WorkspaceRoleNone, domain.Membership{}, nil
	case err != nil:
		return domain.WorkspaceRoleNone, domain.Membership{}, err
	case !m.ActiveAt(now):
		return domain.WorkspaceRoleNone, m, nil
	}
	return m.Role, m, nil
}

// requireNoteShare loads the note and checks the caller may share it. A
// caller who cannot read the note gets ErrNotFound (IDs cannot be probed);
// one who can read but not share gets ErrPermissionDenied.
func (s *Service) requireNoteShare(ctx context.Context, tx Tx, caller domain.Subject, note domain.NoteID, now time.Time) (domain.NoteAccess, error) {
	facts, err := loadFacts(ctx, tx, caller, []domain.Check{{Action: domain.ActionNoteShare, ResourceID: string(note)}})
	if err != nil {
		return domain.NoteAccess{}, err
	}
	n, ok := facts.Note(note)
	if !ok || !domain.Decide(facts, caller, domain.Check{Action: domain.ActionNoteRead, ResourceID: string(note)}, now).Allowed {
		return domain.NoteAccess{}, fmt.Errorf("%w: note", domain.ErrNotFound)
	}
	if d := domain.Decide(facts, caller, domain.Check{Action: domain.ActionNoteShare, ResourceID: string(note)}, now); !d.Allowed {
		return domain.NoteAccess{}, fmt.Errorf("%w: sharing this note needs editor (%s)", domain.ErrPermissionDenied, d.Rule)
	}
	return n, nil
}

// noteRoleOf returns the user's effective role on the note and their stored
// override (NoteRoleNone when there is none), as of now in tx.
func noteRoleOf(ctx context.Context, tx Tx, user domain.UserID, note domain.NoteID, now time.Time) (domain.NoteRole, domain.NoteRole, error) {
	subj := domain.UserSubject(user, "")
	facts, err := loadFacts(ctx, tx, subj, []domain.Check{{Action: domain.ActionNoteRead, ResourceID: string(note)}})
	if err != nil {
		return 0, 0, err
	}
	overrides, err := tx.OverridesOf(ctx, user, []domain.NoteID{note})
	if err != nil {
		return 0, 0, err
	}
	stored := domain.NoteRoleNone
	if len(overrides) > 0 {
		stored = overrides[0].Role
	}
	return domain.EffectiveNoteRole(facts, note, user, now), stored, nil
}

// SetNoteRole sets or (with NoteRoleNone) clears a user's override on a
// note. Overrides apply only to members and guests.
func (s *Service) SetNoteRole(ctx context.Context, caller domain.Subject, note domain.NoteID, target domain.UserID, role domain.NoteRole) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	if role != domain.NoteRoleNone && !role.Valid() {
		return domain.InvalidField("role", "must be viewer, commenter or editor, or unspecified to clear")
	}
	now := s.now()
	var changes []domain.AccessChange
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		n, err := s.requireNoteShare(ctx, tx, caller, note, now)
		if err != nil {
			return err
		}
		targetRole, err := workspaceRole(ctx, tx, n.WorkspaceID, target, now)
		if err != nil {
			return err
		}
		before, stored, err := noteRoleOf(ctx, tx, target, note, now)
		if err != nil {
			return err
		}
		if role == stored {
			return nil
		}
		if role == domain.NoteRoleNone {
			err = tx.DeleteNoteRoleOverride(ctx, note, target)
		} else {
			if err := domain.CheckOverrideTarget(targetRole); err != nil {
				return err
			}
			err = tx.UpsertNoteRoleOverride(ctx, domain.NoteRoleOverride{
				NoteID: note, WorkspaceID: n.WorkspaceID, UserID: target, Role: role, GrantedBy: caller.UserID, CreatedAt: now,
			})
		}
		if err != nil {
			return err
		}
		after, _, err := noteRoleOf(ctx, tx, target, note, now)
		if err != nil {
			return err
		}
		c := domain.AccessChange{WorkspaceID: n.WorkspaceID, NoteID: note, UserID: target, Revoked: domain.AccessLowered(before, after), Actor: caller, At: now}
		changes = append(changes, c)
		return tx.EnqueueAccessChanged(ctx, c)
	})
	if err != nil {
		return err
	}
	s.invalidateChanges(ctx, changes)
	return nil
}

// CreateShareLink creates a link granting role on the note and returns its
// ID and token. The token is returned only here.
func (s *Service) CreateShareLink(ctx context.Context, caller domain.Subject, note domain.NoteID, role domain.NoteRole, expiresAt *time.Time) (domain.ShareLinkID, string, error) {
	if err := requireUser(caller); err != nil {
		return "", "", err
	}
	now := s.now()
	expiry, err := domain.NewShareLinkExpiry(role, expiresAt, now, s.cfg.ShareLinkDefaultTTL)
	if err != nil {
		return "", "", err
	}
	token, err := credential.NewToken(credential.KindShareLink, s.random)
	if err != nil {
		return "", "", err
	}
	id, err := domain.NewID()
	if err != nil {
		return "", "", err
	}
	var changes []domain.AccessChange
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		n, err := s.requireNoteShare(ctx, tx, caller, note, now)
		if err != nil {
			return err
		}
		link := domain.ShareLink{
			ID: domain.ShareLinkID(id), NoteID: note, WorkspaceID: n.WorkspaceID, Role: role,
			TokenHash: credential.HashToken(token), CreatedBy: caller.UserID, CreatedAt: now, ExpiresAt: expiry,
		}
		if err := tx.InsertShareLink(ctx, link); err != nil {
			return err
		}
		c := domain.AccessChange{WorkspaceID: n.WorkspaceID, NoteID: note, Actor: caller, At: now}
		changes = append(changes, c)
		return tx.EnqueueAccessChanged(ctx, c)
	})
	if err != nil {
		return "", "", err
	}
	s.invalidateChanges(ctx, changes)
	return domain.ShareLinkID(id), token, nil
}

// RevokeShareLink revokes a link. Revoking a revoked link succeeds.
func (s *Service) RevokeShareLink(ctx context.Context, caller domain.Subject, id domain.ShareLinkID) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	now := s.now()
	var changes []domain.AccessChange
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		link, err := tx.ShareLink(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.requireNoteShare(ctx, tx, caller, link.NoteID, now); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("%w: share link", domain.ErrNotFound)
			}
			return err
		}
		if link.RevokedAt != nil {
			return nil
		}
		if err := tx.RevokeShareLink(ctx, id, now); err != nil {
			return err
		}
		c := domain.AccessChange{WorkspaceID: link.WorkspaceID, NoteID: link.NoteID, Revoked: true, Actor: caller, At: now}
		changes = append(changes, c)
		return tx.EnqueueAccessChanged(ctx, c)
	})
	if err != nil {
		return err
	}
	s.invalidateChanges(ctx, changes)
	return nil
}

// ResolveShareLink turns a presented share-link token into the subject
// Authorize decides for. It does not check expiry or revocation; Decide
// does, so the answer is consistent with every other check. There is no RPC
// for it yet (see the service README).
func (s *Service) ResolveShareLink(ctx context.Context, raw string) (domain.Subject, domain.NoteID, error) {
	token, err := credential.ParseToken(credential.KindShareLink, raw)
	if err != nil {
		return domain.Subject{}, "", domain.ErrUnauthenticated
	}
	var link domain.ShareLink
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		link, err = tx.ShareLinkByTokenHash(ctx, credential.HashToken(token))
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Subject{}, "", domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Subject{}, "", err
	}
	return domain.LinkSubject(link.ID), link.NoteID, nil
}

// RegisterReplica records that the replica ID belongs to the caller (and,
// for an agent run, to that run). Registering one's own ID again succeeds;
// an ID owned by someone else is ErrAlreadyExists.
func (s *Service) RegisterReplica(ctx context.Context, caller domain.Subject, id domain.ReplicaID) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	now := s.now()
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		existing, err := tx.Replica(ctx, id)
		switch {
		case err == nil && existing.UserID == caller.UserID:
			return nil
		case err == nil:
			return fmt.Errorf("%w: replica %s belongs to another user; mint a new ID", domain.ErrAlreadyExists, id)
		case !errors.Is(err, domain.ErrNotFound):
			return err
		}
		return tx.InsertReplica(ctx, domain.Replica{ID: id, UserID: caller.UserID, AgentRunID: caller.AgentRunID, RegisteredAt: now})
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		return err
	}
	// A concurrent registration may have inserted the row between our read
	// and insert. Re-read in a fresh transaction (the failed one is
	// aborted in Postgres) and succeed if it was the caller's own.
	var owner domain.UserID
	if rerr := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Replica(ctx, id)
		owner = r.UserID
		return err
	}); rerr == nil && owner == caller.UserID {
		return nil
	}
	return err
}
