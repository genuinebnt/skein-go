package domain

// SocketKind is the kind of a live WebSocket, one per streaming endpoint.
type SocketKind string

// The three socket kinds.
const (
	SocketSync          SocketKind = "sync"
	SocketAgent         SocketKind = "agent"
	SocketNotifications SocketKind = "notifications"
)

// Revocation is a user's lost access, from an identity AccessChanged event
// with revoked set.
type Revocation struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// NoteID is empty when access to the whole workspace was revoked.
	NoteID string `json:"note_id,omitempty"`
}

// Affects reports whether a socket must close because of r.
//
// A note-level revocation closes the user's sync sockets for that note. A
// workspace-level one closes all of the user's sync and agent sockets: the
// gateway does not know which workspace a note or chat is in, and a
// reconnect is cheap and re-authorised. Notification sockets stay open,
// because they belong to the user, not to a workspace.
func (r Revocation) Affects(kind SocketKind, userID, noteID string) bool {
	if r.UserID == "" || r.UserID != userID {
		return false
	}
	if r.NoteID != "" {
		return kind == SocketSync && noteID == r.NoteID
	}
	return kind == SocketSync || kind == SocketAgent
}
