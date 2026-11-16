// Package postgres implements identity's Store over pgx, in the identity
// schema created by migrations/0001_identity.sql.
//
// Every use case runs in one transaction (platform/postgres.WithTx) at READ
// COMMITTED. The places where that isolation is not enough take row locks:
// refresh tokens and sessions (ForUpdate reads, so two refreshes of one
// token serialise and the second sees the rotation), membership rows and
// the workspace's owner rows (so two demotions cannot both pass the
// last-owner check), and one-time tokens (single use). Uniqueness that
// matters (emails, replica IDs, OAuth subjects, token hashes) is enforced
// by keys, and a violation becomes domain.ErrAlreadyExists.
//
// Events are written with platform/outbox.Enqueue in the same transaction;
// after a commit that enqueued something, OnEnqueue (the relay's Notify)
// wakes the relay so events leave promptly instead of at the next poll.
//
// Replica IDs are unsigned 64-bit; the bigint column holds their bits
// reinterpreted as signed (int64(id) and back), so the full range fits.
//
// # Porting notes
//
// Rust: sqlx with runtime-checked queries (`query_as` and `FromRow`), as the
// owner's other projects do; `SELECT ... FOR UPDATE` and the error-code
// mapping carry over unchanged. `id as i64` / `id as u64` for replicas.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/genuinebnt/skein-go/platform/outbox"
	"github.com/genuinebnt/skein-go/platform/postgres"

	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/events"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// Store implements app.Store.
type Store struct {
	db postgres.Beginner
	// OnEnqueue, when set, is called after a commit that enqueued events.
	OnEnqueue func()
}

var _ app.Store = (*Store)(nil)

// NewStore returns a store on db (a *pgxpool.Pool in production).
func NewStore(db postgres.Beginner) *Store { return &Store{db: db} }

// InTx implements app.Store.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	var enqueued bool
	err := postgres.WithTx(ctx, s.db, func(ctx context.Context, ptx pgx.Tx) error {
		t := &Tx{tx: ptx}
		err := fn(ctx, t)
		enqueued = t.enqueued
		return err
	})
	if err == nil && enqueued && s.OnEnqueue != nil {
		s.OnEnqueue()
	}
	return err
}

// Tx implements app.Tx on one pgx transaction. NewTx exposes it so the seed
// loader and tests can drive repositories inside their own transaction.
type Tx struct {
	tx       pgx.Tx
	enqueued bool
}

var _ app.Tx = (*Tx)(nil)

// NewTx wraps a pgx transaction.
func NewTx(tx pgx.Tx) *Tx { return &Tx{tx: tx} }

// translate maps pgx errors to domain sentinels.
func translate(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, what)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %s", domain.ErrAlreadyExists, what)
	}
	return fmt.Errorf("postgres: %s: %w", what, err)
}

// exec runs a statement and reports ErrNotFound when it touched no row and
// mustTouch is set.
func (t *Tx) exec(ctx context.Context, what string, mustTouch bool, sql string, args ...any) error {
	tag, err := t.tx.Exec(ctx, sql, args...)
	if err != nil {
		return translate(err, what)
	}
	if mustTouch && tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, what)
	}
	return nil
}

// nullable turns an empty ID into SQL NULL.
func nullable[S ~string](s S) any {
	if s == "" {
		return nil
	}
	return string(s)
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func idStrings[S ~string](ids []S) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// Users.

const userColumns = `id::text, email, display_name, password_hash, email_verified_at, created_at`

func scanUser(row pgx.Row) (domain.User, error) {
	var u domain.User
	var id string
	err := row.Scan(&id, &u.Email, &u.DisplayName, &u.PasswordHash, &u.EmailVerifiedAt, &u.CreatedAt)
	u.ID, u.EmailVerifiedAt, u.CreatedAt = domain.UserID(id), utc(u.EmailVerifiedAt), u.CreatedAt.UTC()
	return u, translate(err, "user")
}

// InsertUser implements app.UserRepository.
func (t *Tx) InsertUser(ctx context.Context, u domain.User) error {
	return t.exec(ctx, "user email", false,
		`INSERT INTO users (id, email, display_name, password_hash, email_verified_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		string(u.ID), u.Email, u.DisplayName, u.PasswordHash, u.EmailVerifiedAt, u.CreatedAt)
}

// UserByID implements app.UserRepository.
func (t *Tx) UserByID(ctx context.Context, id domain.UserID) (domain.User, error) {
	return scanUser(t.tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, string(id)))
}

// UserByEmail implements app.UserRepository.
func (t *Tx) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(t.tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

// SetPasswordHash implements app.UserRepository.
func (t *Tx) SetPasswordHash(ctx context.Context, id domain.UserID, hash string) error {
	return t.exec(ctx, "user", true, `UPDATE users SET password_hash = $2 WHERE id = $1`, string(id), hash)
}

// MarkEmailVerified implements app.UserRepository.
func (t *Tx) MarkEmailVerified(ctx context.Context, id domain.UserID, at time.Time) error {
	return t.exec(ctx, "user", true,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, $2) WHERE id = $1`, string(id), at)
}

// OAuth identities.

func providerFromName(s string) app.OAuthProviderKind {
	for _, k := range []app.OAuthProviderKind{app.OAuthGoogle, app.OAuthGitHub} {
		if k.String() == s {
			return k
		}
	}
	return 0
}

// OAuthIdentity implements app.OAuthRepository.
func (t *Tx) OAuthIdentity(ctx context.Context, p app.OAuthProviderKind, subject string) (app.OAuthIdentity, error) {
	var l app.OAuthIdentity
	var provider, user string
	err := t.tx.QueryRow(ctx, `SELECT provider, subject, user_id::text, created_at FROM oauth_identities WHERE provider = $1 AND subject = $2`,
		p.String(), subject).Scan(&provider, &l.Subject, &user, &l.CreatedAt)
	l.Provider, l.UserID, l.CreatedAt = providerFromName(provider), domain.UserID(user), l.CreatedAt.UTC()
	return l, translate(err, "oauth identity")
}

// InsertOAuthIdentity implements app.OAuthRepository.
func (t *Tx) InsertOAuthIdentity(ctx context.Context, l app.OAuthIdentity) error {
	return t.exec(ctx, "oauth identity", false,
		`INSERT INTO oauth_identities (provider, subject, user_id, created_at) VALUES ($1, $2, $3, $4)`,
		l.Provider.String(), l.Subject, string(l.UserID), l.CreatedAt)
}

// Sessions.

// InsertSession implements app.SessionRepository.
func (t *Tx) InsertSession(ctx context.Context, s domain.Session) error {
	return t.exec(ctx, "session", false, `INSERT INTO sessions (id, user_id, created_at) VALUES ($1, $2, $3)`,
		string(s.ID), string(s.UserID), s.CreatedAt)
}

// SessionByIDForUpdate implements app.SessionRepository.
func (t *Tx) SessionByIDForUpdate(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	var s domain.Session
	var sid, user, reason string
	err := t.tx.QueryRow(ctx, `SELECT id::text, user_id::text, created_at, revoked_at, revoke_reason FROM sessions WHERE id = $1 FOR UPDATE`,
		string(id)).Scan(&sid, &user, &s.CreatedAt, &s.RevokedAt, &reason)
	s.ID, s.UserID, s.RevokeReason = domain.SessionID(sid), domain.UserID(user), domain.RevokeReason(reason)
	s.CreatedAt, s.RevokedAt = s.CreatedAt.UTC(), utc(s.RevokedAt)
	return s, translate(err, "session")
}

// RevokeSession implements app.SessionRepository. Revoking a revoked
// session keeps the first revocation's time and reason.
func (t *Tx) RevokeSession(ctx context.Context, id domain.SessionID, at time.Time, reason domain.RevokeReason) error {
	return t.exec(ctx, "session", false,
		`UPDATE sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1 AND revoked_at IS NULL`, string(id), at, string(reason))
}

// RevokeUserSessions implements app.SessionRepository.
func (t *Tx) RevokeUserSessions(ctx context.Context, user domain.UserID, at time.Time, reason domain.RevokeReason) (int, error) {
	tag, err := t.tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2, revoke_reason = $3 WHERE user_id = $1 AND revoked_at IS NULL`,
		string(user), at, string(reason))
	return int(tag.RowsAffected()), translate(err, "sessions")
}

// InsertRefreshToken implements app.SessionRepository.
func (t *Tx) InsertRefreshToken(ctx context.Context, r domain.RefreshToken) error {
	return t.exec(ctx, "refresh token", false,
		`INSERT INTO refresh_tokens (hash, session_id, issued_at, expires_at) VALUES ($1, $2, $3, $4)`,
		r.Hash, string(r.SessionID), r.IssuedAt, r.ExpiresAt)
}

// RefreshTokenForUpdate implements app.SessionRepository.
func (t *Tx) RefreshTokenForUpdate(ctx context.Context, hash string) (domain.RefreshToken, error) {
	var r domain.RefreshToken
	var session string
	err := t.tx.QueryRow(ctx, `SELECT hash, session_id::text, issued_at, expires_at, rotated_at FROM refresh_tokens WHERE hash = $1 FOR UPDATE`,
		hash).Scan(&r.Hash, &session, &r.IssuedAt, &r.ExpiresAt, &r.RotatedAt)
	r.SessionID, r.IssuedAt, r.ExpiresAt, r.RotatedAt = domain.SessionID(session), r.IssuedAt.UTC(), r.ExpiresAt.UTC(), utc(r.RotatedAt)
	return r, translate(err, "refresh token")
}

// MarkRefreshTokenRotated implements app.SessionRepository.
func (t *Tx) MarkRefreshTokenRotated(ctx context.Context, hash string, at time.Time) error {
	return t.exec(ctx, "refresh token", true, `UPDATE refresh_tokens SET rotated_at = $2 WHERE hash = $1`, hash, at)
}

// Workspaces and memberships.

// InsertWorkspace implements app.WorkspaceRepository.
func (t *Tx) InsertWorkspace(ctx context.Context, w domain.Workspace) error {
	return t.exec(ctx, "workspace", false, `INSERT INTO workspaces (id, name, owner_id, created_at) VALUES ($1, $2, $3, $4)`,
		string(w.ID), w.Name, string(w.OwnerID), w.CreatedAt)
}

// Workspace implements app.WorkspaceRepository.
func (t *Tx) Workspace(ctx context.Context, id domain.WorkspaceID) (domain.Workspace, error) {
	var w domain.Workspace
	var wid, owner string
	err := t.tx.QueryRow(ctx, `SELECT id::text, name, owner_id::text, created_at FROM workspaces WHERE id = $1`, string(id)).
		Scan(&wid, &w.Name, &owner, &w.CreatedAt)
	w.ID, w.OwnerID, w.CreatedAt = domain.WorkspaceID(wid), domain.UserID(owner), w.CreatedAt.UTC()
	return w, translate(err, "workspace")
}

const membershipColumns = `workspace_id::text, user_id::text, role, joined_at, removed_at, COALESCE(invited_by::text, '')`

func scanMembership(row pgx.Row) (domain.Membership, error) {
	var m domain.Membership
	var ws, user, invitedBy string
	var role int16
	err := row.Scan(&ws, &user, &role, &m.JoinedAt, &m.RemovedAt, &invitedBy)
	m.WorkspaceID, m.UserID, m.Role, m.InvitedBy = domain.WorkspaceID(ws), domain.UserID(user), domain.WorkspaceRole(role), domain.UserID(invitedBy)
	m.JoinedAt, m.RemovedAt = m.JoinedAt.UTC(), utc(m.RemovedAt)
	return m, err
}

// Membership implements app.MembershipRepository.
func (t *Tx) Membership(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error) {
	m, err := scanMembership(t.tx.QueryRow(ctx, `SELECT `+membershipColumns+` FROM memberships WHERE workspace_id = $1 AND user_id = $2`,
		string(ws), string(user)))
	return m, translate(err, "membership")
}

// MembershipForUpdate implements app.MembershipRepository.
func (t *Tx) MembershipForUpdate(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error) {
	m, err := scanMembership(t.tx.QueryRow(ctx, `SELECT `+membershipColumns+` FROM memberships WHERE workspace_id = $1 AND user_id = $2 FOR UPDATE`,
		string(ws), string(user)))
	return m, translate(err, "membership")
}

// UpsertMembership implements app.MembershipRepository.
func (t *Tx) UpsertMembership(ctx context.Context, m domain.Membership) error {
	return t.exec(ctx, "membership", false, `
INSERT INTO memberships (workspace_id, user_id, role, joined_at, removed_at, invited_by) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, user_id) DO UPDATE
SET role = EXCLUDED.role, joined_at = EXCLUDED.joined_at, removed_at = EXCLUDED.removed_at, invited_by = EXCLUDED.invited_by`,
		string(m.WorkspaceID), string(m.UserID), int16(m.Role), m.JoinedAt, m.RemovedAt, nullable(m.InvitedBy))
}

// MembershipsOf implements app.MembershipRepository.
func (t *Tx) MembershipsOf(ctx context.Context, user domain.UserID, wss []domain.WorkspaceID) ([]domain.Membership, error) {
	if len(wss) == 0 {
		return nil, nil
	}
	rows, err := t.tx.Query(ctx, `SELECT `+membershipColumns+` FROM memberships WHERE user_id = $1 AND workspace_id = ANY($2::uuid[])`,
		string(user), idStrings(wss))
	if err != nil {
		return nil, translate(err, "memberships")
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Membership, error) { return scanMembership(r) })
	return out, translate(err, "memberships")
}

// ListActiveMembers implements app.MembershipRepository.
func (t *Tx) ListActiveMembers(ctx context.Context, ws domain.WorkspaceID, after domain.UserID, limit int) ([]domain.Member, error) {
	rows, err := t.tx.Query(ctx, `
SELECT u.id::text, u.email, u.display_name, u.email_verified_at, u.created_at, m.role
FROM memberships m JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1 AND m.removed_at IS NULL AND ($2::uuid IS NULL OR m.user_id > $2::uuid)
ORDER BY m.user_id
LIMIT $3`, string(ws), nullable(after), limit)
	if err != nil {
		return nil, translate(err, "members")
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Member, error) {
		var m domain.Member
		var id string
		var role int16
		err := r.Scan(&id, &m.User.Email, &m.User.DisplayName, &m.User.EmailVerifiedAt, &m.User.CreatedAt, &role)
		m.User.ID, m.Role = domain.UserID(id), domain.WorkspaceRole(role)
		m.User.EmailVerifiedAt, m.User.CreatedAt = utc(m.User.EmailVerifiedAt), m.User.CreatedAt.UTC()
		return m, err
	})
	return out, translate(err, "members")
}

// CountActiveOwners implements app.MembershipRepository. It locks the owner
// rows in user order, the first lock every role change takes.
func (t *Tx) CountActiveOwners(ctx context.Context, ws domain.WorkspaceID) (int, error) {
	var n int
	err := t.tx.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT 1 FROM memberships WHERE workspace_id = $1 AND removed_at IS NULL AND role = 4 ORDER BY user_id FOR UPDATE
) owners`, string(ws)).Scan(&n)
	return n, translate(err, "owners")
}

// ShareActiveWorkspace implements app.MembershipRepository.
func (t *Tx) ShareActiveWorkspace(ctx context.Context, a, b domain.UserID) (bool, error) {
	var shared bool
	err := t.tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM memberships x JOIN memberships y ON x.workspace_id = y.workspace_id
  WHERE x.user_id = $1 AND y.user_id = $2 AND x.removed_at IS NULL AND y.removed_at IS NULL
)`, string(a), string(b)).Scan(&shared)
	return shared, translate(err, "shared workspace")
}

// Note role overrides.

const overrideColumns = `note_id::text, workspace_id::text, user_id::text, role, COALESCE(granted_by::text, ''), created_at`

func scanOverride(row pgx.Row) (domain.NoteRoleOverride, error) {
	var o domain.NoteRoleOverride
	var note, ws, user, grantedBy string
	var role int16
	err := row.Scan(&note, &ws, &user, &role, &grantedBy, &o.CreatedAt)
	o.NoteID, o.WorkspaceID, o.UserID, o.Role, o.GrantedBy = domain.NoteID(note), domain.WorkspaceID(ws), domain.UserID(user), domain.NoteRole(role), domain.UserID(grantedBy)
	o.CreatedAt = o.CreatedAt.UTC()
	return o, err
}

func (t *Tx) queryOverrides(ctx context.Context, sql string, args ...any) ([]domain.NoteRoleOverride, error) {
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, translate(err, "overrides")
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.NoteRoleOverride, error) { return scanOverride(r) })
	return out, translate(err, "overrides")
}

// UpsertNoteRoleOverride implements app.NoteRoleRepository.
func (t *Tx) UpsertNoteRoleOverride(ctx context.Context, o domain.NoteRoleOverride) error {
	return t.exec(ctx, "override", false, `
INSERT INTO note_role_overrides (note_id, user_id, workspace_id, role, granted_by, created_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (note_id, user_id) DO UPDATE
SET workspace_id = EXCLUDED.workspace_id, role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, created_at = EXCLUDED.created_at`,
		string(o.NoteID), string(o.UserID), string(o.WorkspaceID), int16(o.Role), nullable(o.GrantedBy), o.CreatedAt)
}

// DeleteNoteRoleOverride implements app.NoteRoleRepository.
func (t *Tx) DeleteNoteRoleOverride(ctx context.Context, note domain.NoteID, user domain.UserID) error {
	return t.exec(ctx, "override", false, `DELETE FROM note_role_overrides WHERE note_id = $1 AND user_id = $2`, string(note), string(user))
}

// DeleteOverridesInWorkspace implements app.NoteRoleRepository.
func (t *Tx) DeleteOverridesInWorkspace(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) error {
	return t.exec(ctx, "overrides", false, `DELETE FROM note_role_overrides WHERE workspace_id = $1 AND user_id = $2`, string(ws), string(user))
}

// OverridesOf implements app.NoteRoleRepository.
func (t *Tx) OverridesOf(ctx context.Context, user domain.UserID, notes []domain.NoteID) ([]domain.NoteRoleOverride, error) {
	if len(notes) == 0 {
		return nil, nil
	}
	return t.queryOverrides(ctx, `SELECT `+overrideColumns+` FROM note_role_overrides WHERE user_id = $1 AND note_id = ANY($2::uuid[])`,
		string(user), idStrings(notes))
}

// OverridesInWorkspace implements app.NoteRoleRepository.
func (t *Tx) OverridesInWorkspace(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) ([]domain.NoteRoleOverride, error) {
	return t.queryOverrides(ctx, `SELECT `+overrideColumns+` FROM note_role_overrides WHERE workspace_id = $1 AND user_id = $2 ORDER BY note_id`,
		string(ws), string(user))
}

// Share links.

const linkColumns = `id::text, note_id::text, workspace_id::text, role, token_hash, COALESCE(created_by::text, ''), created_at, expires_at, revoked_at`

func scanLink(row pgx.Row) (domain.ShareLink, error) {
	var l domain.ShareLink
	var id, note, ws, createdBy string
	var role int16
	err := row.Scan(&id, &note, &ws, &role, &l.TokenHash, &createdBy, &l.CreatedAt, &l.ExpiresAt, &l.RevokedAt)
	l.ID, l.NoteID, l.WorkspaceID, l.Role, l.CreatedBy = domain.ShareLinkID(id), domain.NoteID(note), domain.WorkspaceID(ws), domain.NoteRole(role), domain.UserID(createdBy)
	l.CreatedAt, l.ExpiresAt, l.RevokedAt = l.CreatedAt.UTC(), utc(l.ExpiresAt), utc(l.RevokedAt)
	return l, translate(err, "share link")
}

// InsertShareLink implements app.ShareLinkRepository.
func (t *Tx) InsertShareLink(ctx context.Context, l domain.ShareLink) error {
	return t.exec(ctx, "share link", false, `
INSERT INTO share_links (id, note_id, workspace_id, role, token_hash, created_by, created_at, expires_at, revoked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		string(l.ID), string(l.NoteID), string(l.WorkspaceID), int16(l.Role), l.TokenHash, nullable(l.CreatedBy), l.CreatedAt, l.ExpiresAt, l.RevokedAt)
}

// ShareLink implements app.ShareLinkRepository.
func (t *Tx) ShareLink(ctx context.Context, id domain.ShareLinkID) (domain.ShareLink, error) {
	return scanLink(t.tx.QueryRow(ctx, `SELECT `+linkColumns+` FROM share_links WHERE id = $1`, string(id)))
}

// ShareLinkByTokenHash implements app.ShareLinkRepository.
func (t *Tx) ShareLinkByTokenHash(ctx context.Context, hash string) (domain.ShareLink, error) {
	return scanLink(t.tx.QueryRow(ctx, `SELECT `+linkColumns+` FROM share_links WHERE token_hash = $1`, hash))
}

// RevokeShareLink implements app.ShareLinkRepository.
func (t *Tx) RevokeShareLink(ctx context.Context, id domain.ShareLinkID, at time.Time) error {
	return t.exec(ctx, "share link", true, `UPDATE share_links SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, string(id), at)
}

// Note projection.

const noteColumns = `id::text, workspace_id::text, created_by::text, created_at, trashed_at`

func (t *Tx) queryNotes(ctx context.Context, sql string, args ...any) ([]domain.NoteAccess, error) {
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, translate(err, "notes")
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.NoteAccess, error) {
		var n domain.NoteAccess
		var id, ws, by string
		err := r.Scan(&id, &ws, &by, &n.CreatedAt, &n.TrashedAt)
		n.ID, n.WorkspaceID, n.CreatedBy, n.CreatedAt, n.TrashedAt = domain.NoteID(id), domain.WorkspaceID(ws), domain.UserID(by), n.CreatedAt.UTC(), utc(n.TrashedAt)
		return n, err
	})
	return out, translate(err, "notes")
}

// UpsertNote implements app.NoteProjection.
func (t *Tx) UpsertNote(ctx context.Context, n domain.NoteAccess) error {
	return t.exec(ctx, "note", false, `
INSERT INTO notes (id, workspace_id, created_by, created_at, trashed_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE
SET workspace_id = EXCLUDED.workspace_id, created_by = EXCLUDED.created_by, created_at = EXCLUDED.created_at, trashed_at = EXCLUDED.trashed_at`,
		string(n.ID), string(n.WorkspaceID), string(n.CreatedBy), n.CreatedAt, n.TrashedAt)
}

// SetNoteTrashed implements app.NoteProjection.
func (t *Tx) SetNoteTrashed(ctx context.Context, id domain.NoteID, at *time.Time) error {
	return t.exec(ctx, "note", true, `UPDATE notes SET trashed_at = $2 WHERE id = $1`, string(id), at)
}

// PurgeNote implements app.NoteProjection.
func (t *Tx) PurgeNote(ctx context.Context, id domain.NoteID) error {
	for _, sql := range []string{
		`DELETE FROM note_role_overrides WHERE note_id = $1`,
		`DELETE FROM share_links WHERE note_id = $1`,
		`DELETE FROM notes WHERE id = $1`,
	} {
		if err := t.exec(ctx, "purge note", false, sql, string(id)); err != nil {
			return err
		}
	}
	return nil
}

// Notes implements app.NoteProjection.
func (t *Tx) Notes(ctx context.Context, ids []domain.NoteID) ([]domain.NoteAccess, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return t.queryNotes(ctx, `SELECT `+noteColumns+` FROM notes WHERE id = ANY($1::uuid[])`, idStrings(ids))
}

// NotesInWorkspace implements app.NoteProjection.
func (t *Tx) NotesInWorkspace(ctx context.Context, ws domain.WorkspaceID) ([]domain.NoteAccess, error) {
	return t.queryNotes(ctx, `SELECT `+noteColumns+` FROM notes WHERE workspace_id = $1 ORDER BY id`, string(ws))
}

// MarkEventProcessed implements app.NoteProjection.
func (t *Tx) MarkEventProcessed(ctx context.Context, consumer, eventID string) (bool, error) {
	return outbox.ProcessOnce(ctx, t.tx, consumer, eventID)
}

// Replicas.

// Replica implements app.ReplicaRepository.
func (t *Tx) Replica(ctx context.Context, id domain.ReplicaID) (domain.Replica, error) {
	var r domain.Replica
	var raw int64
	var user, run string
	err := t.tx.QueryRow(ctx, `SELECT id, user_id::text, COALESCE(agent_run_id::text, ''), registered_at FROM replicas WHERE id = $1`,
		int64(id)).Scan(&raw, &user, &run, &r.RegisteredAt)
	r.ID, r.UserID, r.AgentRunID, r.RegisteredAt = domain.ReplicaID(uint64(raw)), domain.UserID(user), domain.AgentRunID(run), r.RegisteredAt.UTC()
	return r, translate(err, "replica")
}

// InsertReplica implements app.ReplicaRepository.
func (t *Tx) InsertReplica(ctx context.Context, r domain.Replica) error {
	return t.exec(ctx, "replica", false, `INSERT INTO replicas (id, user_id, agent_run_id, registered_at) VALUES ($1, $2, $3, $4)`,
		int64(r.ID), string(r.UserID), nullable(r.AgentRunID), r.RegisteredAt)
}

// One-time tokens.

func purposeFromName(s string) domain.TokenPurpose {
	for _, p := range []domain.TokenPurpose{domain.PurposeEmailVerification, domain.PurposePasswordReset} {
		if p.String() == s {
			return p
		}
	}
	return 0
}

// InsertOneTimeToken implements app.OneTimeTokenRepository.
func (t *Tx) InsertOneTimeToken(ctx context.Context, o domain.OneTimeToken) error {
	return t.exec(ctx, "one-time token", false,
		`INSERT INTO one_time_tokens (hash, user_id, purpose, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		o.Hash, string(o.UserID), o.Purpose.String(), o.CreatedAt, o.ExpiresAt)
}

// OneTimeTokenForUpdate implements app.OneTimeTokenRepository.
func (t *Tx) OneTimeTokenForUpdate(ctx context.Context, hash string) (domain.OneTimeToken, error) {
	var o domain.OneTimeToken
	var user, purpose string
	err := t.tx.QueryRow(ctx, `SELECT hash, user_id::text, purpose, created_at, expires_at, used_at FROM one_time_tokens WHERE hash = $1 FOR UPDATE`,
		hash).Scan(&o.Hash, &user, &purpose, &o.CreatedAt, &o.ExpiresAt, &o.UsedAt)
	o.UserID, o.Purpose, o.CreatedAt, o.ExpiresAt, o.UsedAt = domain.UserID(user), purposeFromName(purpose), o.CreatedAt.UTC(), o.ExpiresAt.UTC(), utc(o.UsedAt)
	return o, translate(err, "one-time token")
}

// MarkOneTimeTokenUsed implements app.OneTimeTokenRepository.
func (t *Tx) MarkOneTimeTokenUsed(ctx context.Context, hash string, at time.Time) error {
	return t.exec(ctx, "one-time token", true, `UPDATE one_time_tokens SET used_at = $2 WHERE hash = $1`, hash, at)
}

// Outbox.

// EnqueueAccessChanged implements app.Outbox.
func (t *Tx) EnqueueAccessChanged(ctx context.Context, c domain.AccessChange) error {
	env, err := events.AccessChanged(ctx, c)
	if err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, t.tx, events.Topic, env); err != nil {
		return err
	}
	t.enqueued = true
	return nil
}
