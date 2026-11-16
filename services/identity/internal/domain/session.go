package domain

import "time"

// User is an account. PasswordHash is empty for accounts that only sign in
// with OAuth (or were loaded from seed data without a password).
type User struct {
	ID              UserID
	Email           string
	DisplayName     string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
}

// RevokeReason records why a session ended, for audit.
type RevokeReason string

// Revoke reasons.
const (
	RevokeSignOut       RevokeReason = "sign_out"
	RevokeTokenReuse    RevokeReason = "refresh_token_reuse"
	RevokePasswordReset RevokeReason = "password_reset"
	RevokeAccountTaken  RevokeReason = "account_claimed_by_oauth"
)

// Session is one sign-in and the chain ("family") of refresh tokens rotated
// from it. Revoking the session invalidates every token in the family.
type Session struct {
	ID           SessionID
	UserID       UserID
	CreatedAt    time.Time
	RevokedAt    *time.Time
	RevokeReason RevokeReason
}

// RefreshToken is one link of a session's chain. Only the SHA-256 of the
// opaque token is stored. RotatedAt is set when it is exchanged; from then
// on, presenting it again is reuse.
type RefreshToken struct {
	Hash      string
	SessionID SessionID
	IssuedAt  time.Time
	ExpiresAt time.Time
	RotatedAt *time.Time
}

// RefreshOutcome tags what Refresh must do with a presented token.
type RefreshOutcome int

// Refresh outcomes.
const (
	// RefreshRotate: mark the token rotated, issue a new one in the family.
	RefreshRotate RefreshOutcome = iota + 1
	// RefreshReuseDetected: the token was already rotated, so someone holds
	// a copy. Revoke the whole session; both holders must sign in again.
	RefreshReuseDetected
	// RefreshExpired: the token's lifetime is over.
	RefreshExpired
	// RefreshSessionRevoked: the session was signed out or revoked.
	RefreshSessionRevoked
)

// DecideRefresh classifies a refresh attempt. Order matters: a revoked
// session wins over everything (nothing to protect any more), and reuse is
// checked before expiry so that a stolen token replayed late still revokes
// the family it came from.
func DecideRefresh(s Session, t RefreshToken, now time.Time) RefreshOutcome {
	switch {
	case s.RevokedAt != nil:
		return RefreshSessionRevoked
	case t.RotatedAt != nil:
		return RefreshReuseDetected
	case !t.ExpiresAt.After(now):
		return RefreshExpired
	default:
		return RefreshRotate
	}
}

// TokenPurpose says what a one-time token is for.
type TokenPurpose int

// One-time token purposes.
const (
	PurposeEmailVerification TokenPurpose = iota + 1
	PurposePasswordReset
)

// String names the purpose in storage.
func (p TokenPurpose) String() string {
	switch p {
	case PurposeEmailVerification:
		return "email_verification"
	case PurposePasswordReset:
		return "password_reset"
	default:
		return "unknown"
	}
}

// Lifetimes of one-time tokens. A reset link is a password-equivalent, so it
// lives for an hour; a verification link only proves an address, so a day.
const (
	EmailVerificationTTL = 24 * time.Hour
	PasswordResetTTL     = time.Hour
)

// OneTimeToken is a hashed single-use token sent to a user's email.
type OneTimeToken struct {
	Hash      string
	UserID    UserID
	Purpose   TokenPurpose
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// DecideRedeem reports whether the token can be used for purpose now. Every
// failure is ErrUnauthenticated with no detail, so a token cannot be probed.
func DecideRedeem(t OneTimeToken, purpose TokenPurpose, now time.Time) error {
	if t.Purpose != purpose || t.UsedAt != nil || !t.ExpiresAt.After(now) {
		return ErrUnauthenticated
	}
	return nil
}

// Replica records which user owns a CRDT replica ID (ADR-0004), and the
// agent run when the replica belongs to one.
type Replica struct {
	ID           ReplicaID
	UserID       UserID
	AgentRunID   AgentRunID
	RegisteredAt time.Time
}

// Workspace is a workspace's identity-side record.
type Workspace struct {
	ID        WorkspaceID
	Name      string
	OwnerID   UserID // the creator; current owners are memberships
	CreatedAt time.Time
}

// Member is an active member with their profile, for listing.
type Member struct {
	User User
	Role WorkspaceRole
}

// AccessChange describes one change to who can do what; it becomes an
// AccessChanged event on identity.events. NoteID is empty for
// workspace-level changes and UserID is empty for share-link changes.
type AccessChange struct {
	WorkspaceID WorkspaceID
	NoteID      NoteID
	UserID      UserID
	Revoked     bool
	Actor       Subject
	At          time.Time
}

// CacheTags are the cache tags a change invalidates: every cached decision
// about the user, about the note, or (for workspace-wide changes) about the
// workspace. Over-invalidating is safe; under-invalidating is not.
func (c AccessChange) CacheTags() []string {
	var tags []string
	if c.UserID != "" {
		tags = append(tags, UserTag(c.UserID))
	}
	if c.NoteID != "" {
		tags = append(tags, NoteTag(c.NoteID))
	}
	if c.UserID == "" && c.NoteID == "" {
		tags = append(tags, WorkspaceTag(c.WorkspaceID))
	}
	return tags
}

// UserTag, NoteTag and WorkspaceTag name decision-cache tags.
func UserTag(id UserID) string { return "u:" + string(id) }

// NoteTag names the cache tag of a note's decisions.
func NoteTag(id NoteID) string { return "n:" + string(id) }

// WorkspaceTag names the cache tag of a workspace's decisions.
func WorkspaceTag(id WorkspaceID) string { return "w:" + string(id) }
