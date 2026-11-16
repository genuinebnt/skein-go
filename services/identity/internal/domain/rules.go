package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CheckRoleChange decides whether actor (with role actorRole in the
// workspace) may give target the role next. current is the target's role
// now (WorkspaceRoleNone when they are not a member) and owners is the
// number of active owners now.
func CheckRoleChange(actorRole, current, next WorkspaceRole, owners int) error {
	if !next.Valid() {
		return InvalidField("role", "must be guest, member, admin or owner; use RemoveMember to remove")
	}
	if actorRole < WorkspaceRoleAdmin {
		return fmt.Errorf("%w: managing members needs admin", ErrPermissionDenied)
	}
	if (next == WorkspaceRoleOwner || current == WorkspaceRoleOwner) && actorRole != WorkspaceRoleOwner && current != next {
		return fmt.Errorf("%w: only an owner can grant or remove owner", ErrPermissionDenied)
	}
	if current == WorkspaceRoleOwner && next != WorkspaceRoleOwner && owners <= 1 {
		return fmt.Errorf("%w: a workspace must keep at least one owner", ErrFailedPrecondition)
	}
	return nil
}

// CheckRemoval decides whether actor may remove a member whose role is
// current.
func CheckRemoval(actorRole, current WorkspaceRole, owners int) error {
	if actorRole < WorkspaceRoleAdmin {
		return fmt.Errorf("%w: managing members needs admin", ErrPermissionDenied)
	}
	if current == WorkspaceRoleOwner {
		if actorRole != WorkspaceRoleOwner {
			return fmt.Errorf("%w: only an owner can remove an owner", ErrPermissionDenied)
		}
		if owners <= 1 {
			return fmt.Errorf("%w: a workspace must keep at least one owner", ErrFailedPrecondition)
		}
	}
	return nil
}

// CheckOverrideTarget decides whether a per-note override may be set for a
// user whose workspace role is target. Overrides only mean something for
// members and guests: owners and admins ignore them (rule 2), and a
// non-member has no access to override (rule 1).
func CheckOverrideTarget(target WorkspaceRole) error {
	switch {
	case target == WorkspaceRoleNone:
		return fmt.Errorf("%w: the user is not a member of the note's workspace", ErrFailedPrecondition)
	case target >= WorkspaceRoleAdmin:
		return fmt.Errorf("%w: owners and admins are editors on every note; overrides do not apply", ErrFailedPrecondition)
	}
	return nil
}

// AccessLowered reports whether a change from before to after reduces
// access; it becomes AccessChanged.revoked, which tells consumers to close
// streams as well as drop caches.
func AccessLowered[R ~int32](before, after R) bool { return after < before }

// NewShareLinkExpiry validates a share link request and returns its expiry.
// Links grant viewer or commenter only. requested nil means the default
// lifetime; defaultTTL 0 means links without an explicit expiry never
// expire (the behaviour identity.proto documents).
func NewShareLinkExpiry(role NoteRole, requested *time.Time, now time.Time, defaultTTL time.Duration) (*time.Time, error) {
	switch role {
	case NoteRoleViewer, NoteRoleCommenter:
	case NoteRoleEditor:
		return nil, InvalidField("role", "share links cannot grant editor; give the person a note role instead")
	default:
		return nil, InvalidField("role", "must be viewer or commenter")
	}
	if requested != nil {
		if !requested.After(now) {
			return nil, InvalidField("expires_at", "must be in the future")
		}
		at := *requested
		return &at, nil
	}
	if defaultTTL <= 0 {
		return nil, nil
	}
	at := now.Add(defaultTTL)
	return &at, nil
}

// Limits on free-text fields, in code points.
const (
	MaxEmailBytes       = 254 // RFC 5321 path limit
	MaxDisplayNameRunes = 100
	MaxWorkspaceRunes   = 100
)

// NormalizeEmail trims and lowercases an email and checks its shape. Emails
// compare case-insensitively everywhere in identity; real mail systems
// treat the local part as case-sensitive in theory and never in practice.
func NormalizeEmail(s string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	if e == "" {
		return "", InvalidField("email", "is required")
	}
	if len(e) > MaxEmailBytes || !utf8.ValidString(e) {
		return "", InvalidField("email", "is too long or not UTF-8")
	}
	at := strings.LastIndexByte(e, '@')
	if at <= 0 || at == len(e)-1 || strings.Count(e, "@") != 1 {
		return "", InvalidField("email", "must look like name@domain")
	}
	for _, r := range e {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", InvalidField("email", "must not contain spaces or control characters")
		}
	}
	domain := e[at+1:]
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", InvalidField("email", "has a malformed domain")
	}
	return e, nil
}

// NormalizeName trims a display or workspace name and checks it is 1 to
// limit code points with no control characters.
func NormalizeName(field, s string, limit int) (string, error) {
	n := strings.TrimSpace(s)
	if n == "" {
		return "", InvalidField(field, "is required")
	}
	if !utf8.ValidString(n) || utf8.RuneCountInString(n) > limit {
		return "", InvalidField(field, fmt.Sprintf("must be at most %d characters", limit))
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", InvalidField(field, "must not contain control characters")
		}
	}
	return n, nil
}
