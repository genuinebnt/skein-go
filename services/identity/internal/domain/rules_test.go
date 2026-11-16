package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

var roles = []WorkspaceRole{WorkspaceRoleNone, WorkspaceRoleGuest, WorkspaceRoleMember, WorkspaceRoleAdmin, WorkspaceRoleOwner}

// TestRoleChangesFollowTheOwnershipRules enumerates every (actor, current,
// next, owner count) and checks each outcome against the rules stated in
// docs/api/identity.md, written independently of CheckRoleChange.
func TestRoleChangesFollowTheOwnershipRules(t *testing.T) {
	for _, actor := range roles {
		for _, current := range roles {
			for _, next := range roles {
				for owners := 1; owners <= 2; owners++ {
					err := CheckRoleChange(actor, current, next, owners)
					var want error
					switch {
					case !next.Valid():
						want = ErrInvalidArgument
					case actor < WorkspaceRoleAdmin:
						want = ErrPermissionDenied
					case current != next && (next == WorkspaceRoleOwner || current == WorkspaceRoleOwner) && actor != WorkspaceRoleOwner:
						want = ErrPermissionDenied
					case current == WorkspaceRoleOwner && next != WorkspaceRoleOwner && owners == 1:
						want = ErrFailedPrecondition
					}
					if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
						t.Errorf("actor=%s current=%s next=%s owners=%d: got %v, want %v", actor, current, next, owners, err, want)
					}
				}
			}
		}
	}
}

func TestRemovalKeepsAnOwnerAndNeedsAnOwnerToRemoveOne(t *testing.T) {
	cases := []struct {
		actor, current WorkspaceRole
		owners         int
		want           error
	}{
		{WorkspaceRoleAdmin, WorkspaceRoleMember, 1, nil},
		{WorkspaceRoleAdmin, WorkspaceRoleAdmin, 1, nil},
		{WorkspaceRoleMember, WorkspaceRoleGuest, 1, ErrPermissionDenied},
		{WorkspaceRoleAdmin, WorkspaceRoleOwner, 2, ErrPermissionDenied},
		{WorkspaceRoleOwner, WorkspaceRoleOwner, 2, nil},
		{WorkspaceRoleOwner, WorkspaceRoleOwner, 1, ErrFailedPrecondition},
	}
	for _, c := range cases {
		err := CheckRemoval(c.actor, c.current, c.owners)
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%+v: got %v", c, err)
		}
	}
}

func TestOverridesApplyOnlyToMembersAndGuests(t *testing.T) {
	for _, r := range roles {
		err := CheckOverrideTarget(r)
		if ok := r == WorkspaceRoleMember || r == WorkspaceRoleGuest; ok != (err == nil) {
			t.Errorf("%s: %v", r, err)
		}
	}
}

func TestShareLinksNeverGrantEditorAndDefaultToTheConfiguredLifetime(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	if _, err := NewShareLinkExpiry(NoteRoleEditor, nil, now, week); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("editor link: %v", err)
	}
	if _, err := NewShareLinkExpiry(NoteRoleNone, nil, now, week); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("unspecified role: %v", err)
	}
	exp, err := NewShareLinkExpiry(NoteRoleViewer, nil, now, week)
	if err != nil || exp == nil || !exp.Equal(now.Add(week)) {
		t.Errorf("default expiry = %v, %v; want now + 7 days", exp, err)
	}
	if exp, err := NewShareLinkExpiry(NoteRoleViewer, nil, now, 0); err != nil || exp != nil {
		t.Errorf("zero default lifetime should mean no expiry, got %v %v", exp, err)
	}
	later := now.Add(time.Hour)
	if exp, err := NewShareLinkExpiry(NoteRoleCommenter, &later, now, week); err != nil || !exp.Equal(later) {
		t.Errorf("explicit expiry = %v %v", exp, err)
	}
	if _, err := NewShareLinkExpiry(NoteRoleViewer, &now, now, week); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("expiry at now must be refused: %v", err)
	}
}

func TestDecideRefreshChecksRevocationThenReuseThenExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	live, revoked := Session{}, Session{RevokedAt: &past}
	cases := []struct {
		name string
		s    Session
		t    RefreshToken
		want RefreshOutcome
	}{
		{"fresh token rotates", live, RefreshToken{ExpiresAt: future}, RefreshRotate},
		{"rotated token is reuse", live, RefreshToken{ExpiresAt: future, RotatedAt: &past}, RefreshReuseDetected},
		{"rotated and expired token is still reuse", live, RefreshToken{ExpiresAt: past, RotatedAt: &past}, RefreshReuseDetected},
		{"token expiring now is expired", live, RefreshToken{ExpiresAt: now}, RefreshExpired},
		{"revoked session wins over reuse", revoked, RefreshToken{ExpiresAt: future, RotatedAt: &past}, RefreshSessionRevoked},
	}
	for _, c := range cases {
		if got := DecideRefresh(c.s, c.t, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOneTimeTokensAreSingleUseShortLivedAndPurposeBound(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	fresh := OneTimeToken{Purpose: PurposePasswordReset, ExpiresAt: now.Add(time.Minute)}
	if err := DecideRedeem(fresh, PurposePasswordReset, now); err != nil {
		t.Errorf("fresh token: %v", err)
	}
	if err := DecideRedeem(fresh, PurposeEmailVerification, now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("wrong purpose: %v", err)
	}
	used := fresh
	used.UsedAt = &now
	if err := DecideRedeem(used, PurposePasswordReset, now); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("used token: %v", err)
	}
	if err := DecideRedeem(fresh, PurposePasswordReset, now.Add(time.Minute)); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expired token: %v", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		" Ada@Example.COM ":          "ada@example.com",
		"mira@kestrel-lab.example":   "mira@kestrel-lab.example",
		"a.b+tag@sub.domain.example": "a.b+tag@sub.domain.example",
	}
	for in, want := range good {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ada", "@example.com", "ada@", "a@b@c", "ada @example.com", "ada@.example", "ada@example..com", "ada@example.", strings.Repeat("a", 250) + "@x.example", "a\x00@x.example"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
		}
	}
}

func TestNormalizeNameTrimsAndBoundsInCodePoints(t *testing.T) {
	if got, err := NormalizeName("name", "  Kyoto 旅行  ", 8); err != nil || got != "Kyoto 旅行" {
		t.Errorf("got %q %v", got, err)
	}
	if _, err := NormalizeName("name", strings.Repeat("旅", 9), 8); err == nil {
		t.Error("9 code points accepted with a limit of 8")
	}
	for _, bad := range []string{"", "   ", "tab\there"} {
		if _, err := NormalizeName("name", bad, 100); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseUUIDCanonicalisesAndRejectsOtherSpellings(t *testing.T) {
	got, err := ParseUUID("id", "019B7892-1C00-75EB-B4C7-D9D89D6544C6")
	if err != nil || got != "019b7892-1c00-75eb-b4c7-d9d89d6544c6" {
		t.Errorf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "nope", "{019b7892-1c00-75eb-b4c7-d9d89d6544c6}", "urn:uuid:019b7892-1c00-75eb-b4c7-d9d89d6544c6", "019b78921c0075ebb4c7d9d89d6544c6"} {
		if _, err := ParseUUID("id", bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseReplicaID("replica_id", 0); !errors.Is(err, ErrInvalidArgument) {
		t.Error("replica 0 accepted")
	}
}

func TestAccessChangeTagsCoverWhatItAffects(t *testing.T) {
	cases := []struct {
		c    AccessChange
		want string
	}{
		{AccessChange{WorkspaceID: "w", UserID: "u"}, "u:u"},
		{AccessChange{WorkspaceID: "w", NoteID: "n", UserID: "u"}, "u:u n:n"},
		{AccessChange{WorkspaceID: "w", NoteID: "n"}, "n:n"},
		{AccessChange{WorkspaceID: "w"}, "w:w"},
	}
	for _, c := range cases {
		if got := strings.Join(c.c.CacheTags(), " "); got != c.want {
			t.Errorf("%+v: tags %q, want %q", c.c, got, c.want)
		}
	}
}
