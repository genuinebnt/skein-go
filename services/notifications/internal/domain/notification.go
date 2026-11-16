package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on stored text, in code points. Producers' text is truncated to
// fit (Truncate) rather than refused: a notification with a shortened body
// is better than none.
const (
	MaxTitleLength = 200
	MaxBodyLength  = 2000
)

// Location points into a note: the proto's BlockRef. The zero value means
// the notification is not about a place in a note.
type Location struct {
	NoteID  string
	BlockID string
	// Start and End are a half-open code point range in the block;
	// 0, 0 means the whole block.
	Start uint32
	End   uint32
}

// IsZero reports whether the location is empty.
func (l Location) IsZero() bool { return l == Location{} }

// Validate checks that a location either names a note or is entirely empty,
// and that its range is ordered.
func (l Location) Validate() error {
	if l.NoteID == "" {
		if !l.IsZero() {
			return fmt.Errorf("%w: location has a block or range but no note", ErrInvalidNotification)
		}
		return nil
	}
	if !ValidUUID(l.NoteID) {
		return fmt.Errorf("%w: location note id is not a UUID", ErrInvalidNotification)
	}
	if l.Start > l.End {
		return fmt.Errorf("%w: location range is reversed", ErrInvalidNotification)
	}
	if l.BlockID == "" && (l.Start != 0 || l.End != 0) {
		return fmt.Errorf("%w: location range without a block", ErrInvalidNotification)
	}
	return nil
}

// SourceKind names what caused a notification.
type SourceKind string

// Source kinds. The first four are the seed's (notifications.schema.json).
const (
	SourceComment           SourceKind = "comment"            // ID: the comment
	SourceNoteRole          SourceKind = "note_role"          // ID: note "/" user
	SourceMembership        SourceKind = "membership"         // ID: workspace "/" user
	SourceAgentRun          SourceKind = "agent_run"          // ID: the run
	SourceAgentConfirmation SourceKind = "agent_confirmation" // ID: the event
	SourceBlockMention      SourceKind = "block_mention"      // ID: the event
	SourceRequest           SourceKind = "request"            // ID: the event
)

// Source says what caused a notification. A recipient has at most one
// notification per source.
type Source struct {
	Kind SourceKind
	ID   string
}

// Valid reports whether both parts are set.
func (s Source) Valid() bool { return s.Kind != "" && s.ID != "" }

// Notification is one entry of a user's inbox, or an email-only entry when
// the recipient's preference turns the inbox off for its kind.
type Notification struct {
	ID          string
	RecipientID string
	Kind        Kind
	Title       string
	Body        string
	Location    Location
	// ActorID is who caused it; empty for system notifications.
	ActorID     string
	Source      Source
	CoalesceKey string
	// Occurrences counts the facts coalesced into this notification, from 1.
	Occurrences uint32
	// InInbox is false for email-only notifications, which ListInbox, the
	// unread count and live push never show.
	InInbox   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	// ReadAt is zero while unread.
	ReadAt time.Time
}

// Read reports whether the notification has been marked read.
func (n Notification) Read() bool { return !n.ReadAt.IsZero() }

// Validate checks the notification's invariants.
func (n Notification) Validate() error {
	switch {
	case !ValidUUID(n.ID):
		return fmt.Errorf("%w: id is not a UUID", ErrInvalidNotification)
	case !ValidUUID(n.RecipientID):
		return fmt.Errorf("%w: recipient is not a UUID", ErrInvalidNotification)
	case !n.Kind.Known():
		return fmt.Errorf("%w: kind %d", ErrInvalidNotification, n.Kind)
	case n.Title == "" || utf8.RuneCountInString(n.Title) > MaxTitleLength:
		return fmt.Errorf("%w: title empty or too long", ErrInvalidNotification)
	case utf8.RuneCountInString(n.Body) > MaxBodyLength:
		return fmt.Errorf("%w: body too long", ErrInvalidNotification)
	case n.ActorID != "" && !ValidUUID(n.ActorID):
		return fmt.Errorf("%w: actor is not a UUID", ErrInvalidNotification)
	case !n.Source.Valid():
		return fmt.Errorf("%w: source missing", ErrInvalidNotification)
	case n.Occurrences == 0:
		return fmt.Errorf("%w: occurrences is zero", ErrInvalidNotification)
	case n.CreatedAt.IsZero() || n.UpdatedAt.Before(n.CreatedAt):
		return fmt.Errorf("%w: updated before created", ErrInvalidNotification)
	case n.Read() && n.ReadAt.Before(n.CreatedAt):
		// Read before created would mean a clock bug; it would also break
		// "unread first" reasoning in digests.
		return fmt.Errorf("%w: read before created", ErrInvalidNotification)
	}
	return n.Location.Validate()
}

// Candidate is a notification a rule wants to create, before permission
// and preference checks and before it has an ID.
type Candidate struct {
	RecipientID string
	Kind        Kind
	Title       string
	Body        string
	Location    Location
	ActorID     string
	Source      Source
	CoalesceKey string
	At          time.Time
	// NeedsRead names a note the recipient must be able to read at At, or
	// is empty.
	NeedsRead string
}

// Build turns a candidate into a notification with the given ID.
func (c Candidate) Build(id string, inInbox bool) Notification {
	at := Microseconds(c.At)
	return Notification{
		ID: id, RecipientID: c.RecipientID, Kind: c.Kind,
		Title: Truncate(c.Title, MaxTitleLength), Body: Truncate(c.Body, MaxBodyLength),
		Location: c.Location, ActorID: c.ActorID, Source: c.Source, CoalesceKey: c.CoalesceKey,
		Occurrences: 1, InInbox: inInbox, CreatedAt: at, UpdatedAt: at,
	}
}

// Microseconds truncates t to the precision Postgres stores, in UTC.
func Microseconds(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Truncate shortens s to at most limit code points, ending with an ellipsis
// when it cut anything. limit must be at least 1.
func Truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:limit-1]), " ") + "…"
}

// ValidUUID reports whether s is a UUID in canonical lowercase or uppercase
// hex form (8-4-4-4-12). Any version is accepted: IDs are UUIDv7 by
// convention (ADR-0004) but other services choose them.
func ValidUUID(s string) bool {
	_, ok := ParseUUID(s)
	return ok
}

// ParseUUID returns the 16 bytes of a canonical UUID string.
func ParseUUID(s string) ([16]byte, bool) {
	var out [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return out, false
	}
	j := 0
	for i := 0; i < 36; {
		if s[i] == '-' {
			i++
			continue
		}
		hi, ok1 := hexValue(s[i])
		lo, ok2 := hexValue(s[i+1])
		if !ok1 || !ok2 {
			return out, false
		}
		out[j] = hi<<4 | lo
		j++
		i += 2
	}
	return out, j == 16
}

// FormatUUID is the inverse of ParseUUID, in lowercase.
func FormatUUID(b [16]byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigits[v>>4], hexDigits[v&0x0f])
	}
	return string(out)
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
