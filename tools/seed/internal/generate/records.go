package generate

import (
	"strconv"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Record types, one per output file. Field order is the JSON order. Shapes
// are specified normatively by testdata/seed/schema; change both together.
//
// Conventions: timestamps are RFC 3339 UTC with milliseconds; replica IDs
// are decimal strings (they exceed 2^53, which JSON numbers in JavaScript
// cannot hold, and proto3 JSON encodes uint64 the same way); block IDs are
// "replica:counter" of the block marker; optional values are null, never
// omitted, so every record of a file has the same keys.

const timeLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func replicaString(r crdt.ReplicaID) string { return strconv.FormatUint(uint64(r), 10) }

func blockID(id crdt.ID) string { return id.String() }

// UserRecord is a line of users.jsonl.
type UserRecord struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	Locale      string `json:"locale"`
	TimeZone    string `json:"time_zone"`
	Company     string `json:"company"`
	CreatedAt   string `json:"created_at"`
	// CastKey names anchor-workspace users that evals refer to; null for
	// generated users.
	CastKey *string `json:"cast_key"`
}

// WorkspaceRecord is a line of workspaces.jsonl.
type WorkspaceRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Kind      string `json:"kind"`
	OwnerID   string `json:"owner_id"`
	CreatedAt string `json:"created_at"`
}

// MembershipRecord is a line of memberships.jsonl.
type MembershipRecord struct {
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	JoinedAt    string  `json:"joined_at"`
	RemovedAt   *string `json:"removed_at"`
	InvitedBy   *string `json:"invited_by"`
}

// ReplicaRecord is a line of replicas.jsonl.
type ReplicaRecord struct {
	ReplicaID    string  `json:"replica_id"`
	UserID       string  `json:"user_id"`
	Kind         string  `json:"kind"`
	Device       *string `json:"device"`
	AgentRunID   *string `json:"agent_run_id"`
	RegisteredAt string  `json:"registered_at"`
}

// NoteRecord is a line of notes.jsonl.
type NoteRecord struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	Kind        string  `json:"kind"`
	Language    string  `json:"language"`
	CreatedBy   string  `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Trashed     bool    `json:"trashed"`
	TrashedAt   *string `json:"trashed_at"`
	IsRoot      bool    `json:"is_root"`
	BlockCount  int     `json:"block_count"`
	CharCount   int     `json:"char_count"`
	OpCount     uint64  `json:"op_count"`
	Batches     int     `json:"batches"`
	OpsPath     string  `json:"ops_path"`
	OpsSHA256   string  `json:"ops_sha256"`
	TextSHA256  string  `json:"text_sha256"`
	DuplicateOf *string `json:"duplicate_of"`
}

// BlockRecord is a line of blocks.jsonl.
type BlockRecord struct {
	NoteID    string  `json:"note_id"`
	BlockID   string  `json:"block_id"`
	Position  int     `json:"position"`
	Type      string  `json:"type"`
	Indent    int     `json:"indent"`
	Checked   *bool   `json:"checked"`
	Language  *string `json:"language"`
	Text      string  `json:"text"`
	AuthorID  string  `json:"author_id"`
	CreatedAt string  `json:"created_at"`
}

// LinkRecord is a line of links.jsonl: one interval during which a note's
// text contained [[TargetTitle]].
type LinkRecord struct {
	WorkspaceID  string  `json:"workspace_id"`
	SourceNoteID string  `json:"source_note_id"`
	TargetTitle  string  `json:"target_title"`
	TargetNoteID *string `json:"target_note_id"`
	BlockID      *string `json:"block_id"`
	AddedAt      string  `json:"added_at"`
	RemovedAt    *string `json:"removed_at"`
}

// TagRecord is a line of tags.jsonl.
type TagRecord struct {
	WorkspaceID string `json:"workspace_id"`
	NoteID      string `json:"note_id"`
	Tag         string `json:"tag"`
}

// ACLRecord is a line of acl.jsonl: a per-note role override.
type ACLRecord struct {
	NoteID    string `json:"note_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	GrantedBy string `json:"granted_by"`
	CreatedAt string `json:"created_at"`
}

// ShareLinkRecord is a line of share_links.jsonl.
type ShareLinkRecord struct {
	ID          string  `json:"id"`
	NoteID      string  `json:"note_id"`
	Role        string  `json:"role"`
	CreatedBy   string  `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
	RevokedAt   *string `json:"revoked_at"`
	Token       string  `json:"token"`
	TokenSHA256 string  `json:"token_sha256"`
}

// CommentRecord is a line of comments.jsonl.
type CommentRecord struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	NoteID      string   `json:"note_id"`
	BlockID     string   `json:"block_id"`
	Start       int      `json:"start"`
	End         int      `json:"end"`
	Quote       string   `json:"quote"`
	ParentID    *string  `json:"parent_id"`
	AuthorID    string   `json:"author_id"`
	Body        string   `json:"body"`
	Mentions    []string `json:"mentions"`
	CreatedAt   string   `json:"created_at"`
	Resolved    bool     `json:"resolved"`
}

// Location is a BlockRef (common.proto).
type Location struct {
	NoteID  string `json:"note_id"`
	BlockID string `json:"block_id"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

// SourceRef says what caused a notification.
type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// NotificationRecord is a line of notifications.jsonl.
type NotificationRecord struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	ActorID   string    `json:"actor_id"`
	CreatedAt string    `json:"created_at"`
	Read      bool      `json:"read"`
	Location  *Location `json:"location"`
	Source    SourceRef `json:"source"`
}

// ChatEvent is one event of an agent run, mirroring SendMessageResponse.
type ChatEvent struct {
	Kind         string    `json:"kind"`
	CallID       *string   `json:"call_id"`
	Tool         *string   `json:"tool"`
	Arguments    *string   `json:"arguments_json"`
	Summary      *string   `json:"summary"`
	IsError      bool      `json:"is_error"`
	Text         *string   `json:"text"`
	Location     *Location `json:"location"`
	Outcome      *string   `json:"outcome"`
	Approved     *bool     `json:"approved"`
	Steps        int       `json:"steps"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
}

// ChatMessage is one turn of a chat.
type ChatMessage struct {
	ID     string      `json:"id"`
	Role   string      `json:"role"`
	Text   string      `json:"text"`
	At     string      `json:"at"`
	RunID  *string     `json:"run_id"`
	Events []ChatEvent `json:"events"`
}

// ChatRecord is a line of chats.jsonl.
type ChatRecord struct {
	ID          string        `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	UserID      string        `json:"user_id"`
	NoteID      *string       `json:"note_id"`
	Title       string        `json:"title"`
	EvalID      *string       `json:"eval_id"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	Messages    []ChatMessage `json:"messages"`
}

// BatchRecord is a line of batches.jsonl: one delivered op batch.
type BatchRecord struct {
	NoteID      string  `json:"note_id"`
	Seq         int     `json:"seq"`
	ReplicaID   string  `json:"replica_id"`
	UserID      string  `json:"user_id"`
	AgentRunID  *string `json:"agent_run_id"`
	Kind        string  `json:"kind"`
	CreatedAt   string  `json:"created_at"`
	DeliveredAt string  `json:"delivered_at"`
	OpCount     int     `json:"op_count"`
	Inserts     int     `json:"inserts"`
	Deletes     int     `json:"deletes"`
	ByteOffset  int     `json:"byte_offset"`
	ByteLength  int     `json:"byte_length"`
	SHA256      string  `json:"sha256"`
}

// RejectedRecord is a line of rejected_ops.jsonl: a batch the server must
// refuse.
type RejectedRecord struct {
	NoteID      string `json:"note_id"`
	Seq         int    `json:"seq"`
	ReplicaID   string `json:"replica_id"`
	UserID      string `json:"user_id"`
	Kind        string `json:"kind"`
	CreatedAt   string `json:"created_at"`
	DeliveredAt string `json:"delivered_at"`
	Reason      string `json:"reason"`
	OpCount     int    `json:"op_count"`
	OpsBase64   string `json:"ops_base64"`
	SHA256      string `json:"sha256"`
}

// VersionEntry is one version vector entry (documents.proto).
type VersionEntry struct {
	ReplicaID string `json:"replica_id"`
	Counter   uint64 `json:"counter"`
}

func versionEntries(v crdt.VersionVector) []VersionEntry {
	out := []VersionEntry{}
	for _, r := range v.Replicas() {
		out = append(out, VersionEntry{ReplicaID: replicaString(r), Counter: v.Get(r)})
	}
	return out
}

// FinalRecord is a line of truth/crdt/final.jsonl.
type FinalRecord struct {
	NoteID        string         `json:"note_id"`
	WorkspaceID   string         `json:"workspace_id"`
	Text          string         `json:"text"`
	TextSHA256    string         `json:"text_sha256"`
	Length        int            `json:"length"`
	Blocks        int            `json:"blocks"`
	VersionVector []VersionEntry `json:"version_vector"`
	Ops           uint64         `json:"ops"`
	Inserts       int            `json:"inserts"`
	Deletes       int            `json:"deletes"`
	Tombstones    int            `json:"tombstones"`
	Batches       int            `json:"batches"`
	Replicas      int            `json:"replicas"`
}
