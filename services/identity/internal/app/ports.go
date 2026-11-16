package app

import (
	"context"
	"errors"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// Store runs work in transactions. The Postgres adapter and the in-memory
// fake implement it; everything a use case reads or writes goes through the
// Tx it is given, so a state change and its outbox event commit together.
type Store interface {
	// InTx runs fn in one transaction, committing when it returns nil and
	// rolling back otherwise (including on panic).
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is one transaction's view of identity's data. Lookups return
// domain.ErrNotFound when nothing matches. Methods whose names end in
// ForUpdate lock the row until the transaction ends, so concurrent refreshes
// of one token serialise (the second one sees the rotation and is reuse).
type Tx interface {
	UserRepository
	OAuthRepository
	SessionRepository
	WorkspaceRepository
	MembershipRepository
	NoteRoleRepository
	ShareLinkRepository
	NoteProjection
	ReplicaRepository
	OneTimeTokenRepository
	Outbox
}

// UserRepository stores accounts.
type UserRepository interface {
	// InsertUser fails with domain.ErrAlreadyExists when the email is taken.
	InsertUser(ctx context.Context, u domain.User) error
	UserByID(ctx context.Context, id domain.UserID) (domain.User, error)
	UserByEmail(ctx context.Context, email string) (domain.User, error)
	// SetPasswordHash replaces the hash; "" removes the password.
	SetPasswordHash(ctx context.Context, id domain.UserID, hash string) error
	MarkEmailVerified(ctx context.Context, id domain.UserID, at time.Time) error
}

// OAuthIdentity links a provider account to a user.
type OAuthIdentity struct {
	Provider  OAuthProviderKind
	Subject   string // the provider's stable user ID, never the email
	UserID    domain.UserID
	CreatedAt time.Time
}

// OAuthRepository stores provider links.
type OAuthRepository interface {
	OAuthIdentity(ctx context.Context, provider OAuthProviderKind, subject string) (OAuthIdentity, error)
	InsertOAuthIdentity(ctx context.Context, link OAuthIdentity) error
}

// SessionRepository stores sessions and their refresh token chains.
type SessionRepository interface {
	InsertSession(ctx context.Context, s domain.Session) error
	SessionByIDForUpdate(ctx context.Context, id domain.SessionID) (domain.Session, error)
	RevokeSession(ctx context.Context, id domain.SessionID, at time.Time, reason domain.RevokeReason) error
	// RevokeUserSessions revokes every live session of the user and returns
	// how many it revoked.
	RevokeUserSessions(ctx context.Context, user domain.UserID, at time.Time, reason domain.RevokeReason) (int, error)
	InsertRefreshToken(ctx context.Context, t domain.RefreshToken) error
	RefreshTokenForUpdate(ctx context.Context, hash string) (domain.RefreshToken, error)
	MarkRefreshTokenRotated(ctx context.Context, hash string, at time.Time) error
}

// WorkspaceRepository stores workspaces.
type WorkspaceRepository interface {
	InsertWorkspace(ctx context.Context, w domain.Workspace) error
	Workspace(ctx context.Context, id domain.WorkspaceID) (domain.Workspace, error)
}

// MembershipRepository stores memberships, one row per (workspace, user).
type MembershipRepository interface {
	Membership(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error)
	// MembershipForUpdate locks the row, so two role changes cannot both
	// pass the last-owner check.
	MembershipForUpdate(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error)
	UpsertMembership(ctx context.Context, m domain.Membership) error
	// MembershipsOf returns the user's memberships (active or not) of the
	// given workspaces.
	MembershipsOf(ctx context.Context, user domain.UserID, workspaces []domain.WorkspaceID) ([]domain.Membership, error)
	// ListActiveMembers returns active members ordered by user ID, after
	// the given user ID (exclusive; "" for the first page), at most limit.
	ListActiveMembers(ctx context.Context, ws domain.WorkspaceID, after domain.UserID, limit int) ([]domain.Member, error)
	// CountActiveOwners locks the workspace's owner rows and counts them.
	CountActiveOwners(ctx context.Context, ws domain.WorkspaceID) (int, error)
	// ShareActiveWorkspace reports whether both users are active members
	// of at least one common workspace.
	ShareActiveWorkspace(ctx context.Context, a, b domain.UserID) (bool, error)
}

// NoteRoleRepository stores per-note overrides.
type NoteRoleRepository interface {
	UpsertNoteRoleOverride(ctx context.Context, o domain.NoteRoleOverride) error
	DeleteNoteRoleOverride(ctx context.Context, note domain.NoteID, user domain.UserID) error
	// DeleteOverridesInWorkspace removes every override the user holds on
	// the workspace's notes (a removed member loses them).
	DeleteOverridesInWorkspace(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) error
	// OverridesOf returns the user's overrides on the given notes.
	OverridesOf(ctx context.Context, user domain.UserID, notes []domain.NoteID) ([]domain.NoteRoleOverride, error)
	// OverridesInWorkspace returns the user's overrides on the workspace's
	// notes.
	OverridesInWorkspace(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) ([]domain.NoteRoleOverride, error)
}

// ShareLinkRepository stores share links (token hashes only).
type ShareLinkRepository interface {
	InsertShareLink(ctx context.Context, l domain.ShareLink) error
	ShareLink(ctx context.Context, id domain.ShareLinkID) (domain.ShareLink, error)
	ShareLinkByTokenHash(ctx context.Context, hash string) (domain.ShareLink, error)
	RevokeShareLink(ctx context.Context, id domain.ShareLinkID, at time.Time) error
}

// NoteProjection is identity's copy of note metadata, fed by documents'
// events (and by the seed loader when identity runs alone).
type NoteProjection interface {
	UpsertNote(ctx context.Context, n domain.NoteAccess) error
	SetNoteTrashed(ctx context.Context, id domain.NoteID, at *time.Time) error
	// PurgeNote deletes the note and every override and share link on it.
	PurgeNote(ctx context.Context, id domain.NoteID) error
	Notes(ctx context.Context, ids []domain.NoteID) ([]domain.NoteAccess, error)
	NotesInWorkspace(ctx context.Context, ws domain.WorkspaceID) ([]domain.NoteAccess, error)
	// MarkEventProcessed records an event ID for a consumer and reports
	// whether this is its first time (idempotent consumption, ADR-0006).
	MarkEventProcessed(ctx context.Context, consumer, eventID string) (bool, error)
}

// ReplicaRepository stores replica ownership.
type ReplicaRepository interface {
	Replica(ctx context.Context, id domain.ReplicaID) (domain.Replica, error)
	// InsertReplica fails with domain.ErrAlreadyExists when the ID is
	// taken, so two concurrent registrations cannot both win.
	InsertReplica(ctx context.Context, r domain.Replica) error
}

// OneTimeTokenRepository stores email verification and reset tokens.
type OneTimeTokenRepository interface {
	InsertOneTimeToken(ctx context.Context, t domain.OneTimeToken) error
	OneTimeTokenForUpdate(ctx context.Context, hash string) (domain.OneTimeToken, error)
	MarkOneTimeTokenUsed(ctx context.Context, hash string, at time.Time) error
}

// Outbox enqueues events in the current transaction (ADR-0006).
type Outbox interface {
	// EnqueueAccessChanged publishes an AccessChanged event to
	// identity.events once the transaction commits.
	EnqueueAccessChanged(ctx context.Context, c domain.AccessChange) error
}

// OAuthProviderKind names an OAuth provider. Values equal
// skein.v1.OAuthProvider.
type OAuthProviderKind int32

// OAuth providers.
const (
	OAuthGoogle OAuthProviderKind = 1
	OAuthGitHub OAuthProviderKind = 2
)

// String names the provider in storage and logs.
func (k OAuthProviderKind) String() string {
	switch k {
	case OAuthGoogle:
		return "google"
	case OAuthGitHub:
		return "github"
	default:
		return "unknown"
	}
}

// ExternalIdentity is what a provider says about the person who signed in.
type ExternalIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

// OAuthProvider completes an authorization-code flow with PKCE: it sends
// the code, the verifier and the redirect URI to the provider's token
// endpoint (which checks the verifier against the challenge the flow
// started with) and reads the user's profile. Adapters talk HTTP; tests use
// fakes.
type OAuthProvider interface {
	Exchange(ctx context.Context, code, codeVerifier, redirectURI string) (ExternalIdentity, error)
}

// ErrOAuthRejected is returned by providers when the provider refused the
// code or verifier; it becomes UNAUTHENTICATED.
var ErrOAuthRejected = domain.ErrUnauthenticated

// ErrProviderUnavailable is returned when a provider could not be reached
// or answered with a server error; it becomes UNAVAILABLE, so clients retry.
var ErrProviderUnavailable = errors.New("oauth provider unavailable")

// DecisionCache caches Authorize decisions per (subject, action, resource)
// with tags for invalidation. Implementations fail open: an error is
// reported but the caller just decides without the cache.
type DecisionCache interface {
	Get(ctx context.Context, key string) (allowed bool, found bool, err error)
	Set(ctx context.Context, key string, allowed bool, tags ...string) error
	InvalidateTags(ctx context.Context, tags ...string) error
}

// TokenIssuer signs access tokens with the current key.
type TokenIssuer interface {
	Issue(subject domain.Subject, ttl time.Duration) (token string, expiresAt time.Time, err error)
}

// Clock returns the current time. Tests and the seed loader fix it.
type Clock func() time.Time

// Limiter is platform's rate limiter (GCRA in Redis, or the in-process
// token bucket in tests).
type Limiter = ratelimit.Limiter
