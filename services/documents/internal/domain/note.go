package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// NoteID identifies a note (UUIDv7, ADR-0004).
type NoteID string

// WorkspaceID identifies a workspace (UUIDv7).
type WorkspaceID string

// ParseNoteID validates a note ID.
func ParseNoteID(s string) (NoteID, error) {
	if err := parseUUID(s); err != nil {
		return "", fmt.Errorf("%w: note id: %v", ErrInvalidArgument, err)
	}
	return NoteID(strings.ToLower(s)), nil
}

// ParseWorkspaceID validates a workspace ID.
func ParseWorkspaceID(s string) (WorkspaceID, error) {
	if err := parseUUID(s); err != nil {
		return "", fmt.Errorf("%w: workspace id: %v", ErrInvalidArgument, err)
	}
	return WorkspaceID(strings.ToLower(s)), nil
}

func parseUUID(s string) error {
	// Only the canonical 36-character form: IDs are compared as strings.
	if len(s) != 36 {
		return fmt.Errorf("want a 36-character UUID, got %d characters", len(s))
	}
	_, err := uuid.Parse(s)
	return err
}

// NewNoteID returns a fresh UUIDv7.
func NewNoteID() (NoteID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("%w: note id: %v", ErrUnavailable, err)
	}
	return NoteID(id.String()), nil
}

// Subject is who a request acts for: a user, optionally through an agent run
// (ADR-0009).
type Subject struct {
	UserID     string
	AgentRunID string
}

// IsAgent reports whether an agent run acts for the user.
func (s Subject) IsAgent() bool { return s.AgentRunID != "" }

// MaxTitleRunes bounds a note title.
const MaxTitleRunes = 300

// NormalizeTitle trims a title and checks it: not empty, at most
// MaxTitleRunes code points, valid UTF-8, no control characters (so a title
// is one line and never contains the block marker).
func NormalizeTitle(title string) (string, error) {
	t := strings.TrimSpace(title)
	switch {
	case t == "":
		return "", fmt.Errorf("%w: title must not be empty", ErrInvalidArgument)
	case !utf8.ValidString(t):
		return "", fmt.Errorf("%w: title is not valid UTF-8", ErrInvalidArgument)
	case utf8.RuneCountInString(t) > MaxTitleRunes:
		return "", fmt.Errorf("%w: title is longer than %d characters", ErrInvalidArgument, MaxTitleRunes)
	case strings.IndexFunc(t, unicode.IsControl) >= 0:
		return "", fmt.Errorf("%w: title contains control characters", ErrInvalidArgument)
	}
	return t, nil
}

// Note is a note's metadata. Its text lives in the op log.
type Note struct {
	ID          NoteID
	WorkspaceID WorkspaceID
	Title       string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// TrashedAt is zero unless the note is in the trash.
	TrashedAt time.Time
}

// Trashed reports whether the note is in the trash.
func (n Note) Trashed() bool { return !n.TrashedAt.IsZero() }

// Rename sets the title. Titles are not part of the CRDT text, so a rename
// is last-writer-wins (docs/api/documents.md). A trashed note cannot be
// renamed; it reports whether anything changed.
func (n *Note) Rename(title string, now time.Time) (bool, error) {
	t, err := NormalizeTitle(title)
	if err != nil {
		return false, err
	}
	if n.Trashed() {
		return false, fmt.Errorf("%w: note is in the trash", ErrFailedPrecondition)
	}
	if t == n.Title {
		return false, nil
	}
	n.Title, n.UpdatedAt = t, now
	return true, nil
}

// Trash moves the note to the trash. Trashing a trashed note succeeds and
// changes nothing; it reports whether anything changed.
func (n *Note) Trash(now time.Time) bool {
	if n.Trashed() {
		return false
	}
	n.TrashedAt, n.UpdatedAt = now, now
	return true
}

// Restore takes the note out of the trash, reporting whether it was in it.
func (n *Note) Restore(now time.Time) bool {
	if !n.Trashed() {
		return false
	}
	n.TrashedAt, n.UpdatedAt = time.Time{}, now
	return true
}

// CheckEditable reports why the note's text may not change.
func (n Note) CheckEditable() error {
	if n.Trashed() {
		return fmt.Errorf("%w: note is in the trash", ErrFailedPrecondition)
	}
	return nil
}
