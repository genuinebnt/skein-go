package domain

// SocketKind is the kind of a live WebSocket, one per streaming endpoint.
type SocketKind string

// The three socket kinds.
const (
	SocketSync          SocketKind = "sync"
	SocketAgent         SocketKind = "agent"
	SocketNotifications SocketKind = "notifications"
)

// Revocation is a user's lowered access, from an identity AccessChanged
// event with revoked set: removal, or a demotion that may still read.
type Revocation struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// NoteID is empty when access to the whole workspace was revoked.
	NoteID string `json:"note_id,omitempty"`
}

// Affects reports whether a socket must be re-checked or closed because of
// r (app.Revocations decides which).
//
// A note-level revocation affects the user's sync sockets for that note. A
// workspace-level one affects all of the user's sync and agent sockets: the
// gateway does not know which workspace a note or chat is in, so every
// sync socket is re-checked and agent sockets reconnect, which is cheap
// and re-authorised. Notification sockets are never affected, because they
// belong to the user, not to a workspace.
func (r Revocation) Affects(kind SocketKind, userID, noteID string) bool {
	if r.UserID == "" || r.UserID != userID {
		return false
	}
	if r.NoteID != "" {
		return kind == SocketSync && noteID == r.NoteID
	}
	return kind == SocketSync || kind == SocketAgent
}
