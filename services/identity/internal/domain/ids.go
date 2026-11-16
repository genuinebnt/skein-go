package domain

import (
	"strconv"

	"github.com/google/uuid"
)

// UserID identifies a user (UUIDv7, canonical lowercase form).
type UserID string

// WorkspaceID identifies a workspace (UUIDv7).
type WorkspaceID string

// NoteID identifies a note (UUIDv7). Notes belong to documents; identity
// keeps only the access-relevant projection (NoteAccess).
type NoteID string

// ShareLinkID identifies a share link (UUIDv7).
type ShareLinkID string

// SessionID identifies a session: one sign-in and every refresh token
// rotated from it (the "family").
type SessionID string

// AgentRunID identifies one agent run acting for a user (ADR-0009).
type AgentRunID string

// ReplicaID is a CRDT replica's random 64-bit ID (ADR-0004). Zero is
// reserved by the CRDT spec and never names a replica.
type ReplicaID uint64

// String returns the decimal form used in JSON and the seed files.
func (r ReplicaID) String() string { return strconv.FormatUint(uint64(r), 10) }

// ParseUUID checks that s is a UUID and returns its canonical lowercase
// form, so IDs compare equal however a client spelled them. field names
// the request field for the error.
func ParseUUID(field, s string) (string, error) {
	if s == "" {
		return "", InvalidField(field, "is required")
	}
	// uuid.Parse also accepts braces and urn: prefixes; requiring the
	// 36-character form keeps one spelling per ID.
	if len(s) != 36 {
		return "", InvalidField(field, "must be a UUID")
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return "", InvalidField(field, "must be a UUID")
	}
	return u.String(), nil
}

// ParseUserID parses a user ID.
func ParseUserID(field, s string) (UserID, error) {
	v, err := ParseUUID(field, s)
	return UserID(v), err
}

// ParseWorkspaceID parses a workspace ID.
func ParseWorkspaceID(field, s string) (WorkspaceID, error) {
	v, err := ParseUUID(field, s)
	return WorkspaceID(v), err
}

// ParseNoteID parses a note ID.
func ParseNoteID(field, s string) (NoteID, error) {
	v, err := ParseUUID(field, s)
	return NoteID(v), err
}

// ParseShareLinkID parses a share link ID.
func ParseShareLinkID(field, s string) (ShareLinkID, error) {
	v, err := ParseUUID(field, s)
	return ShareLinkID(v), err
}

// ParseAgentRunID parses an agent run ID.
func ParseAgentRunID(field, s string) (AgentRunID, error) {
	v, err := ParseUUID(field, s)
	return AgentRunID(v), err
}

// ParseReplicaID rejects the reserved replica 0.
func ParseReplicaID(field string, v uint64) (ReplicaID, error) {
	if v == 0 {
		return 0, InvalidField(field, "must not be 0 (reserved)")
	}
	return ReplicaID(v), nil
}

// NewID returns a fresh UUIDv7 string. UUIDv7 sorts by creation time, which
// keeps B-tree inserts local and makes cursor pagination by ID stable.
func NewID() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
