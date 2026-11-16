package domain

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

// Page sizes, from the API conventions (docs/api/README.md).
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// PageSize turns a requested size into the one used: 0 means the default,
// and anything above the maximum is clamped, not refused.
func PageSize(requested uint32) int {
	if requested == 0 {
		return DefaultPageSize
	}
	return int(min(requested, MaxPageSize))
}

// InboxCursor is the position after the last item of a page. The inbox is
// ordered newest first by (CreatedAt, ID), both descending, so a cursor
// names the last item returned and the next page starts strictly after it.
type InboxCursor struct {
	CreatedAt time.Time
	ID        string
	// UnreadOnly is the filter the token was issued for; reusing the token
	// with another filter is INVALID_ARGUMENT.
	UnreadOnly bool
}

// pageTokenVersion is the first byte of every token, so the format can
// change without misreading old tokens.
const pageTokenVersion = 1

// Encode returns the opaque page token: version, flags, CreatedAt as Unix
// microseconds (big-endian int64) and the 16 ID bytes, in unpadded
// base64url. 26 bytes, 35 characters.
func (c InboxCursor) Encode() string {
	id, _ := ParseUUID(c.ID)
	buf := make([]byte, 0, 26)
	flags := byte(0)
	if c.UnreadOnly {
		flags = 1
	}
	buf = append(buf, pageTokenVersion, flags)
	buf = binary.BigEndian.AppendUint64(buf, uint64(c.CreatedAt.UnixMicro()))
	buf = append(buf, id[:]...)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// DecodeInboxCursor parses a page token and checks it was issued for the
// same filter.
func DecodeInboxCursor(token string, unreadOnly bool) (InboxCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 26 || raw[0] != pageTokenVersion || raw[1] > 1 {
		return InboxCursor{}, fmt.Errorf("%w: %w: malformed", ErrInvalidArgument, ErrInvalidPageToken)
	}
	c := InboxCursor{
		CreatedAt:  time.UnixMicro(int64(binary.BigEndian.Uint64(raw[2:10]))).UTC(),
		ID:         FormatUUID([16]byte(raw[10:26])),
		UnreadOnly: raw[1] == 1,
	}
	if c.UnreadOnly != unreadOnly {
		return InboxCursor{}, fmt.Errorf("%w: %w: issued for unreadOnly=%t", ErrInvalidArgument, ErrInvalidPageToken, c.UnreadOnly)
	}
	return c, nil
}

// After reports whether n comes after the cursor in inbox order (older, or
// equally old with a smaller ID).
func (c InboxCursor) After(n Notification) bool {
	if n.CreatedAt.Equal(c.CreatedAt) {
		return n.ID < c.ID
	}
	return n.CreatedAt.Before(c.CreatedAt)
}
