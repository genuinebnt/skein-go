package domain

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// testSeed returns SKEIN_SEED when set, so a failing randomised test can be
// replayed, and a fresh seed otherwise. It logs the seed either way.
func testSeed(t *testing.T) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if s := os.Getenv("SKEIN_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED: %v", err)
		}
		seed = v
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	return seed
}

func uuidFor(n int) string { return fmt.Sprintf("0193%04x-0000-7000-8000-%012x", n%0x10000, n) }

var t0 = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func TestUUIDRoundTripsThroughBytes(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 1))
	for range 1000 {
		var b [16]byte
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		s := FormatUUID(b)
		got, ok := ParseUUID(s)
		if !ok || got != b {
			t.Fatalf("round trip of %x failed: %q", b, s)
		}
		upper, ok := ParseUUID(strings.ToUpper(s))
		if !ok || upper != b {
			t.Fatalf("uppercase %q not accepted", s)
		}
	}
	for _, bad := range []string{"", "01936f60-5e6f-7a80-9b92-0d1e2f30415", "01936f60x5e6f-7a80-9b92-0d1e2f304152",
		"01936f60-5e6f-7a80-9b92-0d1e2f30415g", "01936f605e6f7a809b920d1e2f304152ffff"} {
		if ValidUUID(bad) {
			t.Errorf("ValidUUID(%q) = true", bad)
		}
	}
}

func TestTruncateKeepsCodePointLimitAndMarksTheCut(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("Truncate changed a short string: %q", got)
	}
	got := Truncate(strings.Repeat("日本", 10), 5)
	if utf8.RuneCountInString(got) != 5 || !strings.HasSuffix(got, "…") {
		t.Errorf("Truncate = %q, want 5 code points ending in an ellipsis", got)
	}
}

func validNotification() Notification {
	return Candidate{RecipientID: uuidFor(1), Kind: KindMention, Title: "t", Source: Source{Kind: SourceComment, ID: "c"}, At: t0}.
		Build(uuidFor(2), true)
}

func TestValidateRejectsBrokenNotifications(t *testing.T) {
	if err := validNotification().Validate(); err != nil {
		t.Fatalf("valid notification rejected: %v", err)
	}
	breakers := map[string]func(*Notification){
		"bad id":             func(n *Notification) { n.ID = "x" },
		"unknown kind":       func(n *Notification) { n.Kind = 9 },
		"empty title":        func(n *Notification) { n.Title = "" },
		"no source":          func(n *Notification) { n.Source = Source{} },
		"zero occurrences":   func(n *Notification) { n.Occurrences = 0 },
		"updated early":      func(n *Notification) { n.UpdatedAt = n.CreatedAt.Add(-time.Second) },
		"read before create": func(n *Notification) { n.ReadAt = n.CreatedAt.Add(-time.Second) },
		"block without note": func(n *Notification) { n.Location = Location{BlockID: "1:2"} },
		"reversed range":     func(n *Notification) { n.Location = Location{NoteID: uuidFor(3), BlockID: "1:2", Start: 4, End: 2} },
		"range without block": func(n *Notification) {
			n.Location = Location{NoteID: uuidFor(3), End: 2}
		},
	}
	for name, breakIt := range breakers {
		n := validNotification()
		breakIt(&n)
		if err := n.Validate(); !errors.Is(err, ErrInvalidNotification) {
			t.Errorf("%s: Validate = %v, want ErrInvalidNotification", name, err)
		}
	}
}

func TestBuildTruncatesTimesToMicrosecondsInUTC(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("x", 3600))
	n := Candidate{RecipientID: uuidFor(1), Kind: KindShare, Title: "t", Source: Source{Kind: SourceComment, ID: "c"}, At: at}.Build(uuidFor(2), true)
	if n.CreatedAt.Nanosecond() != 123456000 || n.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v", n.CreatedAt)
	}
}

func TestDefaultPreferencesMatchTheDocumentedDefaults(t *testing.T) {
	// docs/api/notifications.md, GetPreferences example.
	want := []Preference{
		{KindMention, true, true}, {KindComment, true, false}, {KindShare, true, true},
		{KindAgentRunFinished, true, false}, {KindAccessChanged, true, true},
	}
	p := DefaultPreferences()
	if err := p.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	if got := p.List(); !slices.Equal(got, want) {
		t.Errorf("defaults %v, want %v", got, want)
	}
}

func TestPreferenceUpdateReplacesOnlyListedKinds(t *testing.T) {
	update := []Preference{{Kind: KindComment, InApp: true, Email: true}, {Kind: KindAgentRunFinished}}
	if err := ValidateUpdate(update); err != nil {
		t.Fatal(err)
	}
	p := DefaultPreferences().With(update)
	if err := p.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	if got := p.For(KindComment); !got.Email {
		t.Errorf("comment email not enabled: %+v", got)
	}
	if got := p.For(KindAgentRunFinished); got.InApp || got.Email {
		t.Errorf("agent runs not turned off: %+v", got)
	}
	if p.For(KindMention) != DefaultPreference(KindMention) {
		t.Errorf("unlisted kind changed")
	}
}

func TestPreferenceUpdateRejectsDuplicateAndUnknownKinds(t *testing.T) {
	if err := ValidateUpdate([]Preference{{Kind: KindShare}, {Kind: KindShare, InApp: true}}); !errors.Is(err, ErrDuplicateKind) || !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("duplicate: %v", err)
	}
	for _, k := range []Kind{KindUnspecified, 6, 200} {
		if err := ValidateUpdate([]Preference{{Kind: k}}); !errors.Is(err, ErrUnknownKind) {
			t.Errorf("kind %d: %v", k, err)
		}
	}
}

func TestStoredPreferencesWithUnknownKindsAreIgnored(t *testing.T) {
	p := PreferencesFrom([]Preference{{Kind: 42, InApp: true}, {Kind: KindShare}})
	if err := p.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	if p.For(KindShare).InApp {
		t.Error("stored share preference not applied")
	}
}

func TestKindProtoNamesRoundTrip(t *testing.T) {
	for _, k := range Kinds {
		got, err := ParseKind(k.ProtoName())
		if err != nil || got != k {
			t.Errorf("ParseKind(%q) = %v, %v", k.ProtoName(), got, err)
		}
	}
	if _, err := ParseKind("NOTIFICATION_KIND_UNSPECIFIED"); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unspecified parsed: %v", err)
	}
}

func TestCommentNotifiesMentionedReadersCreatorAndParentAuthorOnce(t *testing.T) {
	author, creator, parent, mentioned := uuidFor(1), uuidFor(2), uuidFor(3), uuidFor(4)
	c := Comment{ID: "c1", NoteID: uuidFor(9), NoteTitle: "Raft", NoteCreatorID: creator, AuthorID: author, AuthorName: "Grace",
		ParentAuthorID: parent, Mentions: []string{mentioned, creator, author}, Body: "@ada look", At: t0}
	got := CommentCandidates(c)
	type rk struct {
		user string
		kind Kind
	}
	var keys []rk
	for _, cand := range got {
		keys = append(keys, rk{cand.RecipientID, cand.Kind})
		if cand.NeedsRead != c.NoteID || cand.Source != (Source{SourceComment, "c1"}) || cand.ActorID != author {
			t.Errorf("candidate %+v lacks read check, source or actor", cand)
		}
	}
	want := []rk{{mentioned, KindMention}, {creator, KindMention}, {parent, KindComment}}
	if !slices.Equal(keys, want) {
		t.Errorf("recipients %v, want %v (creator mentioned gets MENTION only; author never)", keys, want)
	}
}

func TestEveryCommentByOthersNotifiesTheNotesCreator(t *testing.T) {
	author, creator := uuidFor(1), uuidFor(2)
	got := CommentCandidates(Comment{ID: "c1", NoteID: uuidFor(9), NoteTitle: "Raft", NoteCreatorID: creator, AuthorID: author, AuthorName: "Grace", At: t0})
	if len(got) != 1 || got[0].RecipientID != creator || got[0].Kind != KindComment || got[0].Title != "Grace commented on Raft" {
		t.Fatalf("got %+v, want one COMMENT for the creator", got)
	}
}

func TestCommentByTheCreatorOnTheirOwnNoteNotifiesNobody(t *testing.T) {
	me := uuidFor(1)
	if got := CommentCandidates(Comment{ID: "c", NoteCreatorID: me, AuthorID: me, At: t0}); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestAccessRulesDistinguishSharesFromChangesAndRemovals(t *testing.T) {
	base := AccessChange{WorkspaceID: uuidFor(1), NoteID: uuidFor(2), NoteTitle: "Plan", UserID: uuidFor(3), ActorID: uuidFor(4),
		ActorName: "Mira", Role: "viewer", At: t0}
	cases := []struct {
		name     string
		change   func(*AccessChange)
		wantKind Kind
		source   Source
	}{
		{"guest given a role", func(a *AccessChange) {}, KindShare, Source{SourceNoteRole, uuidFor(2) + "/" + uuidFor(3)}},
		{"member lowered", func(a *AccessChange) { a.RecipientIsMember = true }, KindAccessChanged, Source{SourceNoteRole, uuidFor(2) + "/" + uuidFor(3)}},
		{"note access revoked", func(a *AccessChange) { a.Revoked = true; a.Role = "" }, KindAccessChanged, Source{SourceNoteRole, uuidFor(2) + "/" + uuidFor(3)}},
		{"member removed", func(a *AccessChange) { a.NoteID = ""; a.Revoked = true }, KindAccessChanged, Source{SourceMembership, uuidFor(1) + "/" + uuidFor(3)}},
		{"joined workspace", func(a *AccessChange) { a.NoteID = "" }, KindUnspecified, Source{}},
		{"own change", func(a *AccessChange) { a.ActorID = a.UserID }, KindUnspecified, Source{}},
	}
	for _, c := range cases {
		a := base
		c.change(&a)
		got := AccessCandidates(a)
		if c.wantKind == KindUnspecified {
			if len(got) != 0 {
				t.Errorf("%s: want nothing, got %+v", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Kind != c.wantKind || got[0].Source != c.source || got[0].RecipientID != a.UserID {
			t.Errorf("%s: got %+v, want one %s from %v", c.name, got, c.wantKind, c.source)
		}
	}
}

func TestAgentRunsNotifyTheirUserAndConfirmationsHaveTheirOwnSource(t *testing.T) {
	finished := AgentCandidates(AgentRun{EventID: "e1", RunID: "r1", UserID: uuidFor(1), At: t0})
	if len(finished) != 1 || finished[0].Source != (Source{SourceAgentRun, "r1"}) || finished[0].Title == "" || finished[0].NeedsRead != "" {
		t.Errorf("finished: %+v", finished)
	}
	confirm := AgentCandidates(AgentRun{EventID: "e2", RunID: "r1", UserID: uuidFor(1), Confirmation: true, Location: Location{NoteID: uuidFor(5)}, At: t0})
	if len(confirm) != 1 || confirm[0].Source != (Source{SourceAgentConfirmation, "e2"}) || confirm[0].NeedsRead != uuidFor(5) {
		t.Errorf("confirmation: %+v", confirm)
	}
}

func TestRequestsAreValidated(t *testing.T) {
	good := Request{EventID: "e", RecipientID: uuidFor(1), Kind: KindComment, Title: "hi", At: t0}
	if got, err := RequestCandidates(good); err != nil || len(got) != 1 || got[0].CoalesceKey == "" {
		t.Fatalf("good request: %v %v", got, err)
	}
	bad := map[string]func(*Request){
		"recipient":  func(r *Request) { r.RecipientID = "nope" },
		"kind":       func(r *Request) { r.Kind = 0 },
		"title":      func(r *Request) { r.Title = "  " },
		"actor":      func(r *Request) { r.ActorID = "x" },
		"location":   func(r *Request) { r.Location = Location{BlockID: "1:1"} },
		"range only": func(r *Request) { r.Location = Location{NoteID: uuidFor(2), Start: 3, End: 1, BlockID: "1:1"} },
	}
	for name, f := range bad {
		r := good
		f(&r)
		if _, err := RequestCandidates(r); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIdenticalRequestsShareACoalescingKeyAndDifferentOnesDoNot(t *testing.T) {
	a := Request{EventID: "e1", RecipientID: uuidFor(1), Kind: KindComment, Title: "same", At: t0}
	b := a
	b.EventID = "e2"
	c := a
	c.Title = "other"
	ka, _ := RequestCandidates(a)
	kb, _ := RequestCandidates(b)
	kc, _ := RequestCandidates(c)
	if ka[0].CoalesceKey != kb[0].CoalesceKey || ka[0].CoalesceKey == kc[0].CoalesceKey {
		t.Errorf("keys %q %q %q", ka[0].CoalesceKey, kb[0].CoalesceKey, kc[0].CoalesceKey)
	}
}

func TestMentionCandidatesGiveOneNotificationPerUserAndSkipTheActor(t *testing.T) {
	actor, ada, bob := uuidFor(1), uuidFor(2), uuidFor(3)
	m := BlockMentions{EventID: "e", NoteID: uuidFor(9), NoteTitle: "N", ActorID: actor, ActorName: "Grace", At: t0, Blocks: []MentionedBlock{
		{BlockID: "1:1", Text: "@ada and @grace", Users: []MentionedUser{{ada, "ada"}, {actor, "grace"}}},
		{BlockID: "1:2", Text: "@ada @bob", Users: []MentionedUser{{ada, "ada"}, {bob, "bob"}}},
	}}
	got := MentionCandidates(m)
	if len(got) != 2 || got[0].RecipientID != ada || got[0].Location.BlockID != "1:1" || got[1].RecipientID != bob {
		t.Fatalf("got %+v", got)
	}
	if got[0].CoalesceKey != got[1].CoalesceKey || got[0].NeedsRead != m.NoteID {
		t.Errorf("mentions in one note by one actor must share a key and need read access")
	}
}

func TestNewlyMentionedMatchesSetDifference(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 2))
	pick := func() []string {
		var s []string
		for i := range 8 {
			if r.IntN(2) == 0 {
				s = append(s, strconv.Itoa(i))
			}
		}
		return s
	}
	for range 500 {
		prev, cur := pick(), pick()
		var want []string
		for _, u := range cur {
			if !slices.Contains(prev, u) {
				want = append(want, u)
			}
		}
		if got := NewlyMentioned(prev, cur); !slices.Equal(got, want) {
			t.Fatalf("NewlyMentioned(%v, %v) = %v, want %v", prev, cur, got, want)
		}
	}
}

func TestExcerptCentresOnTheMentionAndCountsCodePoints(t *testing.T) {
	long := strings.Repeat("日", 300) + " @Ada " + strings.Repeat("x", 300)
	got := Excerpt(long, "ada")
	if !strings.Contains(got, "@Ada") {
		t.Errorf("excerpt lost the mention: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > ExcerptLength+2 {
		t.Errorf("excerpt has %d code points", n)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("excerpt does not mark both cuts: %q", got)
	}
}

func TestPlanEmailsKeepsItsInvariantsOnRandomInput(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 3))
	for round := range 300 {
		var pending []PendingEmail
		for i := range r.IntN(40) {
			created := t0.Add(time.Duration(r.IntN(600)) * time.Second)
			pending = append(pending, PendingEmail{
				JobID: fmt.Sprintf("j%03d", i), RecipientID: fmt.Sprintf("u%d", r.IntN(5)), NotificationID: "n",
				CreatedAt: created, DueAt: created.Add(time.Minute), Read: r.IntN(5) == 0,
			})
		}
		now := t0.Add(time.Duration(r.IntN(700)) * time.Second)
		maxPer := 1 + r.IntN(6)
		plan := PlanEmails(pending, now, maxPer)
		if err := plan.CheckInvariants(pending, now, maxPer); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

func TestRetryDelayDoublesUpToTheCap(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := RetryDelay(i+1, time.Second, 10*time.Second); got != w {
			t.Errorf("attempt %d: %v, want %v", i+1, got, w)
		}
	}
}

func TestPageTokensRoundTripAndAreBoundToTheirFilter(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 4))
	for range 500 {
		c := InboxCursor{CreatedAt: time.UnixMicro(r.Int64N(1 << 50)).UTC(), ID: uuidFor(r.IntN(1 << 30)), UnreadOnly: r.IntN(2) == 0}
		got, err := DecodeInboxCursor(c.Encode(), c.UnreadOnly)
		if err != nil || got != c {
			t.Fatalf("round trip %+v: %+v, %v", c, got, err)
		}
		if _, err := DecodeInboxCursor(c.Encode(), !c.UnreadOnly); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("token reused with another filter: %v", err)
		}
	}
	for _, bad := range []string{"", "!!", "AAAA", strings.Repeat("A", 35)} {
		if _, err := DecodeInboxCursor(bad, false); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestPageSizeDefaultsAndClamps(t *testing.T) {
	for req, want := range map[uint32]int{0: 50, 1: 1, 500: 500, 501: 500, 1 << 31: 500} {
		if got := PageSize(req); got != want {
			t.Errorf("PageSize(%d) = %d, want %d", req, got, want)
		}
	}
}

func FuzzExtractHandles(f *testing.F) {
	for _, s := range []string{"@ada", "x@y", "`@a` @b", "``@a` `` @c", "(@kaito.varga.)", "@@a", "ü@a @b@c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		handles := ExtractHandles(text)
		if !slices.IsSorted(handles) || len(slices.Compact(slices.Clone(handles))) != len(handles) {
			t.Fatalf("handles not sorted and distinct: %q", handles)
		}
		lower := strings.ToLower(text)
		for _, h := range handles {
			if h == "" || len(h) > MaxHandleLength || !isASCIILetter(rune(h[0])) || strings.ContainsAny(h[len(h)-1:], "._-") {
				t.Fatalf("malformed handle %q from %q", h, text)
			}
			if !strings.Contains(lower, "@"+h) {
				t.Fatalf("handle %q not in text %q", h, text)
			}
		}
	})
}

func FuzzDecodeInboxCursor(f *testing.F) {
	f.Add(InboxCursor{CreatedAt: t0, ID: uuidFor(1)}.Encode(), false)
	f.Add("", true)
	f.Fuzz(func(t *testing.T, token string, unreadOnly bool) {
		c, err := DecodeInboxCursor(token, unreadOnly)
		if err != nil {
			return
		}
		if c.UnreadOnly != unreadOnly || !ValidUUID(c.ID) {
			t.Fatalf("accepted cursor %+v for unreadOnly=%t", c, unreadOnly)
		}
		again, err := DecodeInboxCursor(c.Encode(), unreadOnly)
		if err != nil || again != c {
			t.Fatalf("re-encoding %+v changed it: %+v %v", c, again, err)
		}
	})
}
