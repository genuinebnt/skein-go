package domain

import "fmt"

// Preference is how one kind of notification reaches a user.
type Preference struct {
	Kind Kind
	// InApp shows it in the inbox and pushes it live.
	InApp bool
	// Email also sends an email.
	Email bool
}

// DefaultPreference is the setting a user has until they change it, as
// documented in docs/api/notifications.md: everything in the app; email for
// things addressed to the user personally (mentions, shares, access
// changes) but not for the higher-volume comments and agent runs.
func DefaultPreference(k Kind) Preference {
	switch k {
	case KindMention, KindShare, KindAccessChanged:
		return Preference{Kind: k, InApp: true, Email: true}
	default:
		return Preference{Kind: k, InApp: true}
	}
}

// Preferences is a user's full setting, one entry per known kind. The zero
// value is not valid; start from DefaultPreferences.
type Preferences struct {
	byKind [kindCount]Preference
}

// DefaultPreferences returns every kind at its default.
func DefaultPreferences() Preferences {
	var p Preferences
	for _, k := range Kinds {
		p.byKind[k] = DefaultPreference(k)
	}
	return p
}

// PreferencesFrom applies stored overrides to the defaults. Stored rows
// with unknown kinds (written by a newer version) are ignored, so a
// rollback cannot break reads.
func PreferencesFrom(stored []Preference) Preferences {
	p := DefaultPreferences()
	for _, s := range stored {
		if s.Kind.Known() {
			p.byKind[s.Kind] = s
		}
	}
	return p
}

// For returns the setting for kind k, or both channels off for an unknown
// kind.
func (p Preferences) For(k Kind) Preference {
	if !k.Known() {
		return Preference{Kind: k}
	}
	return p.byKind[k]
}

// List returns one entry per known kind, in ascending kind order.
func (p Preferences) List() []Preference {
	out := make([]Preference, 0, len(Kinds))
	for _, k := range Kinds {
		out = append(out, p.byKind[k])
	}
	return out
}

// ValidateUpdate checks an UpdatePreferences request: every kind known, and
// none listed twice.
func ValidateUpdate(updates []Preference) error {
	var seen [kindCount]bool
	for _, u := range updates {
		if !u.Kind.Known() {
			return fmt.Errorf("%w: %w: kind %d", ErrInvalidArgument, ErrUnknownKind, u.Kind)
		}
		if seen[u.Kind] {
			return fmt.Errorf("%w: %w: %s", ErrInvalidArgument, ErrDuplicateKind, u.Kind.ProtoName())
		}
		seen[u.Kind] = true
	}
	return nil
}

// With returns p with the listed kinds replaced; kinds not listed keep
// their setting. Call ValidateUpdate first.
func (p Preferences) With(updates []Preference) Preferences {
	for _, u := range updates {
		if u.Kind.Known() {
			p.byKind[u.Kind] = u
		}
	}
	return p
}

// CheckInvariants verifies that every known kind's entry names its kind.
func (p Preferences) CheckInvariants() error {
	for _, k := range Kinds {
		if p.byKind[k].Kind != k {
			return fmt.Errorf("preferences: entry for %s holds kind %d", k, p.byKind[k].Kind)
		}
	}
	return nil
}
