// Package seed loads the notification service's share of a seed dataset
// (docs/seed-data.md, testdata/seed) and checks the producer rules against
// the dataset's expected inbox.
//
// # Loader contract
//
// Open checks the manifest first: the format version it supports and the
// sha256 of every file this package reads; any mismatch refuses the
// dataset. Nothing under truth/ is read here (the permission matrix is
// only read by tests, as an oracle for Policy).
//
// Two things are loaded, both idempotent:
//
//   - LoadInbox stores notifications.jsonl as it is, keeping the dataset's
//     IDs and read flags (read notifications get ReadAt = CreatedAt, the
//     seed does not say when), so UI and API fixtures and ground truth
//     line up. No email is enqueued. A second run finds every row present
//     and changes nothing.
//   - Preferences: the dataset has none (the generator writes no
//     preferences file), so every user has the defaults; nothing is
//     stored.
//
// # Checking the rules
//
// Replay plays the events the other services would have published for the
// dataset through this service's real consumers, with a clock fixed at the
// reference time and Policy (the seed's permission rules, evaluated at each
// event's time) as identity:
//
//	seed records            event                                    path
//	notes                   NoteCreated, NoteTrashed                 documents.events consumer
//	blocks (per note)       BlocksChanged with the final blocks      documents.events consumer
//	comments                CommentEvent (no proto payload yet)      Producer.Comment
//	acl                     AccessChanged{note, user}                identity.events consumer
//	memberships removed     AccessChanged{workspace, user, revoked}  identity.events consumer
//	chats, agent replicas   NotificationRequested, actor = the run   agent.events consumer
//
// Compare then checks set equality on (user, kind, source) with
// notifications.jsonl, as docs/seed-data.md asks. The BlocksChanged step
// also checks the mention parser against real text: the seed's blocks
// contain e-mail addresses, "recall@10" and SQL's "@@", and must notify
// nobody.
//
// Event IDs are derived from the records (a hash shaped as a UUID), so a
// second replay into the same store is all duplicates.
package seed

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FormatVersion is the dataset format this loader reads.
const FormatVersion = 1

// ErrRefused means the dataset failed the manifest checks.
var ErrRefused = errors.New("seed: dataset refused")

// Files this package reads.
var Files = []string{
	"users.jsonl", "workspaces.jsonl", "memberships.jsonl", "notes.jsonl", "blocks.jsonl", "acl.jsonl",
	"comments.jsonl", "chats.jsonl", "replicas.jsonl", "batches.jsonl", "notifications.jsonl",
}

// User is a line of users.jsonl.
type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
}

// Workspace is a line of workspaces.jsonl.
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
}

// Membership is a line of memberships.jsonl.
type Membership struct {
	WorkspaceID string     `json:"workspace_id"`
	UserID      string     `json:"user_id"`
	Role        string     `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	RemovedAt   *time.Time `json:"removed_at"`
}

// Note is a line of notes.jsonl.
type Note struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Title       string     `json:"title"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	TrashedAt   *time.Time `json:"trashed_at"`
}

// Block is a line of blocks.jsonl.
type Block struct {
	NoteID  string `json:"note_id"`
	BlockID string `json:"block_id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
}

// Override is a line of acl.jsonl.
type Override struct {
	NoteID    string    `json:"note_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	GrantedBy string    `json:"granted_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Comment is a line of comments.jsonl.
type Comment struct {
	ID        string    `json:"id"`
	NoteID    string    `json:"note_id"`
	BlockID   string    `json:"block_id"`
	Start     uint32    `json:"start"`
	End       uint32    `json:"end"`
	ParentID  *string   `json:"parent_id"`
	AuthorID  string    `json:"author_id"`
	Body      string    `json:"body"`
	Mentions  []string  `json:"mentions"`
	CreatedAt time.Time `json:"created_at"`
}

// ChatEvent is one event of an assistant message.
type ChatEvent struct {
	Kind string `json:"kind"`
}

// ChatMessage is one message of a chat.
type ChatMessage struct {
	Text   string      `json:"text"`
	At     time.Time   `json:"at"`
	RunID  *string     `json:"run_id"`
	Events []ChatEvent `json:"events"`
}

// Chat is a line of chats.jsonl.
type Chat struct {
	ID       string        `json:"id"`
	UserID   string        `json:"user_id"`
	Messages []ChatMessage `json:"messages"`
}

// Replica is a line of replicas.jsonl.
type Replica struct {
	UserID       string    `json:"user_id"`
	Kind         string    `json:"kind"`
	AgentRunID   *string   `json:"agent_run_id"`
	RegisteredAt time.Time `json:"registered_at"`
}

// Batch is the part of a batches.jsonl line this package needs.
type Batch struct {
	NoteID     string  `json:"note_id"`
	AgentRunID *string `json:"agent_run_id"`
}

// Location is a notification's location in notifications.jsonl.
type Location struct {
	NoteID  string `json:"note_id"`
	BlockID string `json:"block_id"`
	Start   uint32 `json:"start"`
	End     uint32 `json:"end"`
}

// SourceRef is a notification's source in notifications.jsonl.
type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Notification is a line of notifications.jsonl: the expected inbox.
type Notification struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	ActorID   string    `json:"actor_id"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
	Location  *Location `json:"location"`
	Source    SourceRef `json:"source"`
}

// Dataset is the part of a seed dataset this service reads.
type Dataset struct {
	Dir           string
	ReferenceTime time.Time
	Users         []User
	Workspaces    []Workspace
	Memberships   []Membership
	Notes         []Note
	Blocks        []Block
	Overrides     []Override
	Comments      []Comment
	Chats         []Chat
	Replicas      []Replica
	Batches       []Batch
	Notifications []Notification
}

type manifest struct {
	FormatVersion int       `json:"format_version"`
	ReferenceTime time.Time `json:"reference_time"`
	Files         []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// Open checks the manifest and reads the dataset in dir.
func Open(dir string) (*Dataset, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrRefused, err)
	}
	if m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("%w: format_version %d, this loader reads %d", ErrRefused, m.FormatVersion, FormatVersion)
	}
	sums := map[string]string{}
	for _, f := range m.Files {
		sums[f.Path] = f.SHA256
	}
	d := &Dataset{Dir: dir, ReferenceTime: m.ReferenceTime.UTC()}
	targets := map[string]any{
		"users.jsonl": &d.Users, "workspaces.jsonl": &d.Workspaces, "memberships.jsonl": &d.Memberships,
		"notes.jsonl": &d.Notes, "blocks.jsonl": &d.Blocks, "acl.jsonl": &d.Overrides, "comments.jsonl": &d.Comments,
		"chats.jsonl": &d.Chats, "replicas.jsonl": &d.Replicas, "batches.jsonl": &d.Batches,
		"notifications.jsonl": &d.Notifications,
	}
	for _, name := range Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRefused, err)
		}
		sum := sha256.Sum256(data)
		want, ok := sums[name]
		if !ok || want != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("%w: %s does not match the manifest's sha256", ErrRefused, name)
		}
		if err := decodeLines(data, targets[name]); err != nil {
			return nil, fmt.Errorf("seed: %s: %w", name, err)
		}
	}
	return d, nil
}

// decodeLines appends each JSON line of data to the slice dst points to.
func decodeLines(data []byte, dst any) error {
	switch d := dst.(type) {
	case *[]User:
		return lines(data, d)
	case *[]Workspace:
		return lines(data, d)
	case *[]Membership:
		return lines(data, d)
	case *[]Note:
		return lines(data, d)
	case *[]Block:
		return lines(data, d)
	case *[]Override:
		return lines(data, d)
	case *[]Comment:
		return lines(data, d)
	case *[]Chat:
		return lines(data, d)
	case *[]Replica:
		return lines(data, d)
	case *[]Batch:
		return lines(data, d)
	case *[]Notification:
		return lines(data, d)
	}
	return fmt.Errorf("no decoder for %T", dst)
}

func lines[T any](data []byte, dst *[]T) error {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
		*dst = append(*dst, v)
	}
	return sc.Err()
}
