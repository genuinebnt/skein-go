package generate

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus/anchor"
	"github.com/genuinebnt/skein-go/tools/seed/internal/history"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
	"github.com/genuinebnt/skein-go/tools/seed/internal/uuid7"
)

// The plan is everything decided before any note's content is written:
// people, workspaces, roles, note metadata, the link graph and the planted
// search and discovery structures. It is built sequentially and is small
// (a few hundred bytes per note), so the large profile can hold it in memory
// while note content streams through.

type devicePlan struct {
	Replica crdt.ReplicaID
	Device  string
	At      time.Time
}

type userPlan struct {
	ID, Email, Name, Handle, Locale, TZ, Company, CastKey string
	CreatedAt                                             time.Time
	Devices                                               []devicePlan
}

type memberPlan struct {
	User      int
	Role      policy.WorkspaceRole
	JoinedAt  time.Time
	RemovedAt *time.Time
	InvitedBy int
}

type overridePlan struct {
	Note, User, GrantedBy int
	Role                  policy.NoteRole
	At                    time.Time
}

type shareLinkPlan struct {
	ID, Token            string
	Note, CreatedBy      int
	Role                 policy.NoteRole
	CreatedAt            time.Time
	ExpiresAt, RevokedAt *time.Time
}

// plantedSCC is a set of notes the generator made strongly connected on
// purpose; ground truth asserts it is exactly an SCC of the final graph.
type plantedSCC struct {
	Label   string
	Members []int
}

type wsPlan struct {
	Index                int
	ID, Name, Slug, Kind string
	Company              corpus.Company
	Owner                int
	CreatedAt            time.Time
	Members              []memberPlan
	Topics               []int
	Codenames            []string
	Notes                []int
	TitleIndex           map[string]int // unique titles only; ambiguous titles map to -1
	SCCs                 []plantedSCC
	Cycles               [][]int
	Orphans              [][]int
	Hubs                 []int
	Chain                []int
	TwinPairs            [][2]int
	Queries              []textQuery
	Regexes              []regexQuery
}

type notePlan struct {
	Index           int
	WS              int
	ID, Title, Kind string
	Lang            string
	Topic, Topic2   int
	Mix             float64
	CreatedBy       int
	CreatedAt       time.Time
	Root, Trashed   bool
	TrashedAt       time.Time
	Tags            []string
	Links           []int
	DraftLinks      []int
	Dangling        []string
	Codenames       []string
	CodeIdents      []string
	TwinTerms       []string
	DupOf           int
	Blocks          int
	Editors         []int
	Anchor          *anchor.Note
	Agent           bool
	RejectRevoked   int // user index whose offline edit is refused after removal, or -1
	RejectViewer    int // user index whose edit is refused for lack of role, or -1
	Stress          string
	TitleShared     bool // another note in the workspace has the same title
	NoLinks         bool // excluded from the link graph (orphan groups manage their own)
}

type plan struct {
	prof       Profile
	src        prng.Source
	users      []userPlan
	workspaces []*wsPlan
	notes      []*notePlan
	overrides  []overridePlan
	links      []shareLinkPlan
	policy     *policy.State
	handles    map[string]int
	chats      []chatPlan
	// keep marks notes whose final blocks the writer retains (anchor notes
	// and notes chats are about).
	keep map[int]bool
	// needShingles marks notes in a near-duplicate pair.
	needShingles map[int]bool
}

func (p *plan) ws(n *notePlan) *wsPlan { return p.workspaces[n.WS] }

func (p *plan) newID(label string, idx int, at time.Time) string {
	return uuid7.New(at, p.src.Stream("id."+label, uint64(idx)))
}

// between returns a time uniformly between a and b.
func between(r *prng.Rand, a, b time.Time) time.Time {
	if !b.After(a) {
		return a
	}
	return a.Add(time.Duration(r.Float64() * float64(b.Sub(a))))
}

func newPlan(prof Profile, seed uint64) (*plan, error) {
	p := &plan{prof: prof, src: prng.New(seed), policy: policy.NewState(), handles: map[string]int{}, needShingles: map[int]bool{}}
	notes, err := anchor.Notes()
	if err != nil {
		return nil, err
	}
	p.planUsers()
	if err := p.planAnchor(notes); err != nil {
		return nil, err
	}
	if prof.Stress {
		if err := p.planStress(); err != nil {
			return nil, err
		}
	} else {
		procedural := prof.Workspaces - 1
		remaining := prof.Notes - len(notes)
		for w := 0; w < procedural; w++ {
			n := remaining / (procedural - w)
			remaining -= n
			if err := p.planProcedural(w, n); err != nil {
				return nil, err
			}
		}
	}
	p.planChats()
	return p, p.check()
}

// check verifies counts the profile promises.
func (p *plan) check() error {
	if len(p.users) != p.prof.Users || len(p.workspaces) != p.prof.Workspaces || len(p.notes) != p.prof.Notes {
		return fmt.Errorf("plan has %d users, %d workspaces, %d notes; profile %s wants %d, %d, %d",
			len(p.users), len(p.workspaces), len(p.notes), p.prof.Name, p.prof.Users, p.prof.Workspaces, p.prof.Notes)
	}
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// asciiFold maps the few non-ASCII letters in the name lists to ASCII, for
// email local parts and handles.
func asciiFold(s string) string {
	repl := strings.NewReplacer("ą", "a", "ö", "o", "ä", "a", "ü", "u", "ç", "c", "á", "a", "é", "e", "ć", "c", "ő", "o")
	return repl.Replace(strings.ToLower(s))
}

func (p *plan) addHandle(base string, user int) string {
	h := slugify(asciiFold(base))
	h = strings.ReplaceAll(h, "-", ".")
	cand := h
	for i := 2; ; i++ {
		if _, taken := p.handles[cand]; !taken {
			p.handles[cand] = user
			return cand
		}
		cand = fmt.Sprintf("%s%d", h, i)
	}
}

func (p *plan) planUsers() {
	r := p.src.Stream("plan.users", 0)
	seen := map[crdt.ReplicaID]bool{}
	newReplica := func(rr *prng.Rand) crdt.ReplicaID {
		for {
			id := crdt.ReplicaID(rr.Uint64())
			if id >= history.MinReplicaID && !seen[id] {
				seen[id] = true
				return id
			}
		}
	}
	for i := 0; i < p.prof.Users; i++ {
		u := userPlan{}
		u.CreatedAt = between(r, DataStart.Add(-30*24*time.Hour), DataStart.Add(20*24*time.Hour))
		if i < len(corpus.AnchorCast) {
			c := corpus.AnchorCast[i]
			u.Name, u.CastKey, u.Locale, u.TZ, u.Company = c.DisplayName, c.Key, c.Locale, c.TimeZone, corpus.AnchorCompany.Name
			u.Handle = p.addHandle(c.Handle, i)
			u.Email = u.Handle + "@" + corpus.AnchorCompany.Domain
			if c.Key == "guest" {
				u.Company, u.Email = "Inkwell Translation", u.Handle+"@inkwell-tx.example"
			}
			u.CreatedAt = DataStart.Add(-time.Duration(60-i) * 24 * time.Hour)
		} else {
			given, family := prng.Pick(r, corpus.GivenNames), prng.Pick(r, corpus.FamilyNames)
			company := corpus.Companies[i%len(corpus.Companies)]
			loc := corpus.Locales[r.IntN(len(corpus.Locales))]
			u.Name, u.Locale, u.TZ, u.Company = given+" "+family, loc.Locale, loc.TimeZone, company.Name
			u.Handle = p.addHandle(given+"."+family, i)
			u.Email = u.Handle + "@" + company.Domain
		}
		u.ID = p.newID("user", i, u.CreatedAt)
		devices := []string{"laptop", "browser", "phone"}[:1+r.IntN(3)]
		for _, d := range devices {
			u.Devices = append(u.Devices, devicePlan{Replica: newReplica(r), Device: d, At: u.CreatedAt.Add(time.Duration(r.IntN(72)) * time.Hour)})
		}
		p.users = append(p.users, u)
	}
}

func (p *plan) addWorkspace(name, kind string, company corpus.Company, owner int, at time.Time) *wsPlan {
	w := &wsPlan{Index: len(p.workspaces), Name: name, Slug: slugify(name), Kind: kind, Company: company, Owner: owner, CreatedAt: at, TitleIndex: map[string]int{}}
	w.ID = p.newID("workspace", w.Index, at)
	p.workspaces = append(p.workspaces, w)
	p.policy.AddWorkspace(w.ID)
	return w
}

func (p *plan) addMember(w *wsPlan, m memberPlan) {
	w.Members = append(w.Members, m)
	p.policy.AddMembership(policy.Membership{WorkspaceID: w.ID, UserID: p.users[m.User].ID, Role: m.Role, JoinedAt: m.JoinedAt, RemovedAt: m.RemovedAt})
}

func (p *plan) addNote(w *wsPlan, n *notePlan) {
	n.Index = len(p.notes)
	n.WS = w.Index
	n.ID = p.newID("note", n.Index, n.CreatedAt)
	p.notes = append(p.notes, n)
	w.Notes = append(w.Notes, n.Index)
	if prev, ok := w.TitleIndex[n.Title]; ok {
		if prev >= 0 {
			p.notes[prev].TitleShared = true
		}
		n.TitleShared = true
		w.TitleIndex[n.Title] = -1
	} else {
		w.TitleIndex[n.Title] = n.Index
	}
	var trashed *time.Time
	if n.Trashed {
		// Trashed a few days before the reference time, after its history.
		at := ReferenceTime.Add(-time.Duration(2+n.Index%5) * 24 * time.Hour)
		if !at.After(n.CreatedAt.Add(48 * time.Hour)) {
			at = n.CreatedAt.Add(48 * time.Hour)
		}
		n.TrashedAt = at
		trashed = &n.TrashedAt
	}
	p.policy.AddNote(policy.Note{ID: n.ID, WorkspaceID: w.ID, CreatedBy: p.users[n.CreatedBy].ID, TrashedAt: trashed})
}

func (p *plan) addOverride(o overridePlan) {
	p.overrides = append(p.overrides, o)
	p.policy.AddOverride(policy.Override{NoteID: p.notes[o.Note].ID, UserID: p.users[o.User].ID, Role: o.Role, CreatedAt: o.At})
}

func (p *plan) addShareLink(l shareLinkPlan) {
	r := p.src.Stream("plan.sharelink", uint64(len(p.links)))
	l.ID = uuid7.New(l.CreatedAt, r)
	l.Token = fmt.Sprintf("shl_%016x%016x", r.Uint64(), r.Uint64())
	p.links = append(p.links, l)
	p.policy.AddShareLink(policy.ShareLink{ID: l.ID, NoteID: p.notes[l.Note].ID, Role: l.Role, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, RevokedAt: l.RevokedAt})
}

func daysBefore(d int) time.Time { return ReferenceTime.Add(-time.Duration(d) * 24 * time.Hour) }

func timePtr(t time.Time) *time.Time { return &t }

// planAnchor lays out the hand-written workspace from the anchor package.
func (p *plan) planAnchor(notes []anchor.Note) error {
	cast := map[string]int{}
	for i, c := range corpus.AnchorCast {
		cast[c.Key] = i
	}
	w := p.addWorkspace(corpus.AnchorCompany.Name+" Engineering", "anchor", corpus.AnchorCompany, 0, daysBefore(250))
	for i, c := range corpus.AnchorCast {
		m := memberPlan{User: i, Role: policy.ParseWorkspaceRole(c.Role), JoinedAt: daysBefore(248 - i), InvitedBy: 0}
		if i == 0 {
			m.InvitedBy = -1
		}
		if c.RemovedDays > 0 {
			m.RemovedAt = timePtr(daysBefore(c.RemovedDays))
		}
		p.addMember(w, m)
	}
	topicOf := map[string]int{}
	for i, t := range corpus.Topics {
		topicOf[t.ID] = i
	}
	tagTopic := map[string]string{"crdt": "crdt", "raft": "consensus", "rust": "rust-async", "lsm": "lsm", "vector-search": "vector", "observability": "observability"}
	for i := range notes {
		a := &notes[i]
		n := &notePlan{Title: a.Title, Kind: "anchor", Topic: -1, Topic2: -1, CreatedAt: daysBefore(a.CreatedDays).Add(time.Duration(9+i%8) * time.Hour),
			Root: a.Root, Trashed: a.Trashed, Tags: a.Tags, Anchor: a, DupOf: -1, Blocks: len(a.Blocks), RejectRevoked: -1, RejectViewer: -1}
		for _, tag := range a.Tags {
			if id, ok := tagTopic[tag]; ok {
				n.Topic = topicOf[id]
				break
			}
		}
		au, ok := cast[a.Author]
		if !ok {
			return fmt.Errorf("anchor %s: unknown author %q", a.File, a.Author)
		}
		n.CreatedBy = au
		n.Editors = []int{au}
		for _, e := range a.Editors {
			ei, ok := cast[e]
			if !ok {
				return fmt.Errorf("anchor %s: unknown editor %q", a.File, e)
			}
			n.Editors = append(n.Editors, ei)
		}
		p.addNote(w, n)
		for _, acl := range a.ACL {
			ui, ok := cast[acl.CastKey]
			if !ok {
				return fmt.Errorf("anchor %s: unknown acl user %q", a.File, acl.CastKey)
			}
			role := policy.ParseNoteRole(acl.Role)
			p.addOverride(overridePlan{Note: n.Index, User: ui, Role: role, GrantedBy: au, At: n.CreatedAt.Add(time.Minute)})
			if role == policy.Editor {
				n.Editors = append(n.Editors, ui)
			} else if corpus.AnchorCast[ui].Role == "member" {
				n.RejectViewer = ui
			}
		}
		if a.Title == "Ideas backlog" {
			n.RejectRevoked = cast["leaver"]
		}
	}
	for _, l := range anchor.ShareLinks {
		idx, ok := w.TitleIndex[l.Title]
		if !ok || idx < 0 {
			return fmt.Errorf("anchor share link: no note titled %q", l.Title)
		}
		sl := shareLinkPlan{Note: idx, CreatedBy: cast[l.CreatedBy], Role: policy.ParseNoteRole(l.Role), CreatedAt: daysBefore(l.CreatedDays)}
		if l.ExpiresDays != 0 {
			sl.ExpiresAt = timePtr(ReferenceTime.Add(time.Duration(l.ExpiresDays) * 24 * time.Hour))
		}
		if l.RevokedDays != 0 {
			sl.RevokedAt = timePtr(daysBefore(l.RevokedDays))
		}
		p.addShareLink(sl)
	}
	// Links in anchor notes are whatever the author wrote; the content step
	// resolves them.
	w.Queries = anchorQueries()
	w.Regexes = append(anchorRegexes(), genericRegexes()...)
	return nil
}

// codename makes a pronounceable invented word that does not occur in any
// topic vocabulary.
func codename(r *prng.Rand) string {
	n := 2 + r.IntN(2)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prng.Pick(r, corpus.Syllables))
	}
	return b.String()
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// fillTitle fills a title template's {W}, {w} and {n} slots in order.
func fillTitle(r *prng.Rand, t corpus.Topic) string {
	tpl := prng.Pick(r, t.Titles)
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		if tpl[i] != '{' || i+2 >= len(tpl) || tpl[i+2] != '}' {
			b.WriteByte(tpl[i])
			continue
		}
		switch tpl[i+1] {
		case 'W':
			b.WriteString(capitalize(prng.Pick(r, t.Vocab)))
		case 'w':
			b.WriteString(prng.Pick(r, t.Vocab))
		case 'n':
			b.WriteString(fmt.Sprint(1 + r.IntN(4)))
		}
		i += 2
	}
	return b.String()
}

var titleSuffixes = []string{"", " notes", " (draft)", " v2", " retro", " follow-up", " FAQ", " checklist", " deep dive", " for newcomers"}

// planProcedural plans one generated workspace of n notes.
func (p *plan) planProcedural(wi, n int) error {
	r := p.src.Stream("plan.workspace", uint64(wi))
	company := corpus.Companies[(wi*7+3)%len(corpus.Companies)]
	created := between(r, DataStart, DataStart.Add(20*24*time.Hour))
	// Members: everyone from the company plus a random slice of others,
	// so most users belong to more than one workspace.
	var pool []int
	for u := len(corpus.AnchorCast); u < len(p.users); u++ {
		if p.users[u].Company == company.Name || r.Chance(1.6/float64(max(1, p.prof.Workspaces-1))) {
			pool = append(pool, u)
		}
	}
	for len(pool) < 6 {
		pool = append(pool, len(corpus.AnchorCast)+r.IntN(len(p.users)-len(corpus.AnchorCast)))
		pool = uniqueInts(pool)
	}
	prng.Shuffle(r, pool)
	owner := pool[0]
	w := p.addWorkspace(company.Name, "procedural", company, owner, created)
	nAdmins := 1 + len(pool)/25
	nGuests := max(1, len(pool)/8)
	for i, u := range pool {
		role := policy.Member
		switch {
		case i == 0:
			role = policy.Owner
		case i <= nAdmins:
			role = policy.Admin
		case i > len(pool)-nGuests-1:
			role = policy.Guest
		}
		m := memberPlan{User: u, Role: role, JoinedAt: created.Add(time.Duration(i) * time.Hour), InvitedBy: owner}
		if i == 0 {
			m.InvitedBy = -1
		}
		// Added to the policy below, once removal times are known.
		w.Members = append(w.Members, m)
	}
	// Removed members: ordinary members who leave part way through.
	nRemoved := min(4, 1+n/400)
	removable := []int{}
	for i, m := range w.Members {
		if m.Role == policy.Member {
			removable = append(removable, i)
		}
	}
	removed := map[int]time.Time{}
	for k := 0; k < nRemoved && k < len(removable)-2; k++ {
		mi := removable[len(removable)-1-k]
		at := between(r, DataStart.Add(90*24*time.Hour), ReferenceTime.Add(-5*24*time.Hour))
		w.Members[mi].RemovedAt = timePtr(at)
		removed[w.Members[mi].User] = at
	}
	for _, m := range w.Members {
		p.policy.AddMembership(policy.Membership{WorkspaceID: w.ID, UserID: p.users[m.User].ID, Role: m.Role, JoinedAt: m.JoinedAt, RemovedAt: m.RemovedAt})
	}
	// Topics: a subset of the global list, with skewed weights.
	nTopics := min(len(corpus.Topics), 5+n/150)
	w.Topics = append(w.Topics, r.Sample(len(corpus.Topics), nTopics)...)
	topicZipf := prng.NewZipf(nTopics, 0.6)
	for k := 0; k < min(16, 6+n/200); k++ {
		w.Codenames = append(w.Codenames, codename(r))
	}
	w.Codenames = uniqueStrings(w.Codenames)

	// Notes, in creation order.
	times := make([]time.Time, n)
	for i := range times {
		times[i] = between(r, created.Add(time.Hour), ReferenceTime.Add(-48*time.Hour))
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	authors := []int{}
	for _, m := range w.Members {
		if m.Role >= policy.Member {
			authors = append(authors, m.User)
		}
	}
	editorPool := func(at time.Time) []int {
		var out []int
		for _, u := range authors {
			if p.policy.WorkspaceRoleAt(w.ID, p.users[u].ID, at) >= policy.Member {
				out = append(out, u)
			}
		}
		return out
	}
	usedTitles := map[string]bool{}
	first := len(p.notes)
	for i := 0; i < n; i++ {
		np := &notePlan{CreatedAt: times[i], Kind: "procedural", Topic2: -1, DupOf: -1, RejectRevoked: -1, RejectViewer: -1}
		np.Topic = w.Topics[topicZipf.Draw(r)]
		if r.Chance(0.15) {
			np.Topic2 = w.Topics[r.IntN(len(w.Topics))]
			if np.Topic2 == np.Topic {
				np.Topic2 = -1
			} else {
				np.Mix = 0.25 + 0.25*r.Float64()
				np.Kind = "mixed"
			}
		}
		if r.Chance(0.03) {
			np.Lang = []string{"hi", "ja", "de"}[r.IntN(3)]
			np.Kind = "multilingual"
		}
		pool := editorPool(np.CreatedAt)
		if len(pool) == 0 {
			pool = []int{owner}
		}
		np.CreatedBy = prng.Pick(r, pool)
		np.Editors = []int{np.CreatedBy}
		for _, k := range r.Sample(len(pool), r.Range(0, 3)) {
			if pool[k] != np.CreatedBy {
				np.Editors = append(np.Editors, pool[k])
			}
		}
		switch {
		case i == 0:
			np.Title, np.Root, np.Topic = company.Name+" Home", true, w.Topics[0]
		case i <= 2:
			np.Title, np.Root = corpus.Topics[w.Topics[i]].Name+" index", true
			np.Topic = w.Topics[i]
		default:
			t := corpus.Topics[np.Topic]
			title := fillTitle(r, t)
			for tries := 0; usedTitles[title]; tries++ {
				title = fillTitle(r, t) + titleSuffixes[r.IntN(len(titleSuffixes))]
				if tries > 20 {
					title = fmt.Sprintf("%s (%d)", title, i)
				}
			}
			np.Title = title
		}
		usedTitles[np.Title] = true
		np.Trashed = !np.Root && r.Chance(0.01)
		np.Blocks = 1
		p.addNote(w, np)
	}
	notes := p.notes[first:]
	p.planTitleCollisions(r, w, notes, usedTitles)
	p.planBlockCounts(r, notes)
	p.planGraph(r, w, notes)
	p.planSearchPlants(r, w, notes)
	p.planTags(r, w, notes)
	p.planAccess(r, w, notes, removed)
	w.Queries = proceduralQueries(r, w, p)
	w.Regexes = append(codenameRegexes(w), genericRegexes()...)
	return nil
}

// planTitleCollisions renames a few notes so that some titles collide
// exactly (never link targets) and some nearly (for fuzzy title search).
func (p *plan) planTitleCollisions(r *prng.Rand, w *wsPlan, notes []*notePlan, used map[string]bool) {
	if len(notes) < 20 {
		return
	}
	rename := func(n *notePlan, title string) {
		if w.TitleIndex[n.Title] == n.Index {
			delete(w.TitleIndex, n.Title)
		}
		n.Title = title
		if prev, ok := w.TitleIndex[title]; ok {
			if prev >= 0 {
				p.notes[prev].TitleShared = true
			}
			n.TitleShared = true
			w.TitleIndex[title] = -1
		} else {
			w.TitleIndex[title] = n.Index
		}
	}
	exact := max(1, len(notes)/70)
	near := max(2, len(notes)/35)
	for k := 0; k < exact+near; k++ {
		a, b := notes[3+r.IntN(len(notes)-3)], notes[3+r.IntN(len(notes)-3)]
		if a == b || a.TitleShared || b.TitleShared {
			continue
		}
		if k < exact {
			rename(b, a.Title)
			continue
		}
		variants := []string{strings.ToLower(a.Title), a.Title + "s", strings.Replace(a.Title, " ", "  ", 1), a.Title + " (old)", swapAdjacent(r, a.Title)}
		v := variants[r.IntN(len(variants))]
		if !used[v] && v != a.Title {
			used[v] = true
			rename(b, v)
		}
	}
}

// swapAdjacent swaps two adjacent letters inside a word: a realistic typo.
func swapAdjacent(r *prng.Rand, s string) string {
	rs := []rune(s)
	for tries := 0; tries < 10 && len(rs) > 3; tries++ {
		i := 1 + r.IntN(len(rs)-2)
		if unicode.IsLetter(rs[i]) && unicode.IsLetter(rs[i+1]) && rs[i] != rs[i+1] {
			rs[i], rs[i+1] = rs[i+1], rs[i]
			return string(rs)
		}
	}
	return s + "x"
}

// planBlockCounts spreads the profile's block target over the procedural
// notes so the total lands close to it.
func (p *plan) planBlockCounts(r *prng.Rand, notes []*notePlan) {
	anchorBlocks := 0
	for _, n := range p.notes {
		if n.Anchor != nil {
			anchorBlocks += n.Blocks
		}
	}
	procedural := p.prof.Notes - (len(p.workspaces[0].Notes))
	mean := float64(p.prof.Blocks-anchorBlocks) / float64(max(1, procedural))
	for _, n := range notes {
		// A skewed distribution: most notes short, a few long.
		x := mean * (0.35 + 1.3*r.Float64()*r.Float64()*2)
		n.Blocks = max(3, int(x+0.5))
		if n.Lang != "" {
			n.Blocks = max(3, n.Blocks/2)
		}
	}
}

func uniqueInts(xs []int) []int {
	sort.Ints(xs)
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func uniqueStrings(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// planGraph plants the link graph for a procedural workspace.
//
// Every note gets a hidden position. All links except those inside planted
// strongly connected components point from a lower position to a higher one,
// and each planted component occupies consecutive positions. Contracting each
// planted component to one node then leaves a graph whose edges all point
// forward, which has no cycles, so the planted components are exactly the
// non-trivial SCCs of the final graph. The generator re-checks this on the
// final text with Tarjan's algorithm and fails if it does not hold.
func (p *plan) planGraph(r *prng.Rand, w *wsPlan, notes []*notePlan) {
	var main []*notePlan
	isTarget := func(n *notePlan) bool { return !n.Trashed && !n.TitleShared }
	for _, n := range notes {
		if !n.Trashed {
			main = append(main, n)
		}
	}
	if len(main) < 12 {
		return
	}
	// Reserve orphan groups (excluded from everything else).
	orphanSizes := []int{1, 2, 3, 4}
	if len(main) > 600 {
		orphanSizes = append(orphanSizes, 2, 5, 7)
	}
	taken := map[int]bool{}
	pickFree := func() *notePlan {
		for {
			n := main[3+r.IntN(len(main)-3)]
			if !taken[n.Index] && isTarget(n) {
				taken[n.Index] = true
				return n
			}
		}
	}
	for _, size := range orphanSizes {
		var group []int
		for k := 0; k < size; k++ {
			n := pickFree()
			n.NoLinks = true
			group = append(group, n.Index)
		}
		switch size {
		case 1:
		case 2:
			p.notes[group[0]].Links = []int{group[1]}
			p.notes[group[1]].Links = []int{group[0]}
			w.SCCs = append(w.SCCs, plantedSCC{Label: "orphan-pair", Members: sortedCopy(group)})
		default:
			if size%2 == 0 {
				for k := range group {
					p.notes[group[k]].Links = []int{group[(k+1)%size]}
				}
				w.SCCs = append(w.SCCs, plantedSCC{Label: "orphan-cycle", Members: sortedCopy(group)})
				w.Cycles = append(w.Cycles, append(append([]int{}, group...), group[0]))
			} else {
				for k := 0; k+1 < size; k++ {
					p.notes[group[k]].Links = []int{group[k+1]}
				}
			}
		}
		w.Orphans = append(w.Orphans, sortedCopy(group))
	}
	// Build the position order: shuffled, roots first, hubs last, planted
	// structures as consecutive runs.
	var free []*notePlan
	for _, n := range main {
		if !n.NoLinks {
			free = append(free, n)
		}
	}
	roots := free[:3]
	rest := append([]*notePlan{}, free[3:]...)
	prng.Shuffle(r, rest)
	var hubs []*notePlan
	nHubs := 2 + len(rest)/700
	for k := len(rest) - 1; k >= 0 && len(hubs) < nHubs; k-- {
		if isTarget(rest[k]) {
			hubs = append(hubs, rest[k])
			rest = append(rest[:k], rest[k+1:]...)
		}
	}
	cursor := 0
	takeRun := func(k int) []*notePlan {
		var run []*notePlan
		for len(run) < k && cursor < len(rest) {
			if isTarget(rest[cursor]) {
				run = append(run, rest[cursor])
			}
			cursor++
		}
		return run
	}
	order := append([]*notePlan{}, roots...)
	addRun := func(run []*notePlan) {
		order = append(order, run...)
	}
	// Chain: a long series.
	chainLen := max(6, len(rest)/12)
	chain := takeRun(chainLen)
	series := fmt.Sprintf("%s series", capitalize(prng.Pick(r, corpus.Topics[w.Topics[0]].Vocab)))
	for k, n := range chain {
		if w.TitleIndex[n.Title] == n.Index {
			delete(w.TitleIndex, n.Title)
		}
		n.Title = fmt.Sprintf("%s, part %d", series, k+1)
		w.TitleIndex[n.Title] = n.Index
		w.Chain = append(w.Chain, n.Index)
	}
	addRun(chain)
	// Planted SCCs: cycles with chords, a clique and a bare cycle.
	sccSizes := []int{3, 5, 8}
	if len(rest) > 500 {
		sccSizes = append(sccSizes, 13, 21)
	}
	for _, size := range sccSizes {
		run := takeRun(size)
		if len(run) < size {
			break
		}
		for k, n := range run {
			n.Links = append(n.Links, run[(k+1)%size].Index)
			if size > 3 && r.Chance(0.5) {
				n.Links = append(n.Links, run[r.IntN(size)].Index)
			}
		}
		w.SCCs = append(w.SCCs, plantedSCC{Label: fmt.Sprintf("scc-%d", size), Members: indexes(run)})
		addRun(run)
	}
	cliqueSize := 5
	if len(rest) > 500 {
		cliqueSize = 9
	}
	if clique := takeRun(cliqueSize); len(clique) == cliqueSize {
		for _, a := range clique {
			for _, b := range clique {
				if a != b {
					a.Links = append(a.Links, b.Index)
				}
			}
		}
		w.SCCs = append(w.SCCs, plantedSCC{Label: fmt.Sprintf("clique-%d", cliqueSize), Members: indexes(clique)})
		addRun(clique)
	}
	if cycle := takeRun(6); len(cycle) == 6 {
		for k, n := range cycle {
			n.Links = append(n.Links, cycle[(k+1)%6].Index)
		}
		w.SCCs = append(w.SCCs, plantedSCC{Label: "cycle-6", Members: indexes(cycle)})
		var order []int // cycle order, not sorted
		for _, n := range cycle {
			order = append(order, n.Index)
		}
		w.Cycles = append(w.Cycles, append(order, cycle[0].Index))
		addRun(cycle)
	}
	for ; cursor < len(rest); cursor++ {
		order = append(order, rest[cursor])
	}
	order = append(order, hubs...)
	pos := map[int]int{}
	for i, n := range order {
		pos[n.Index] = i
	}
	for i := 1; i < len(chain); i++ {
		chain[i-1].Links = append(chain[i-1].Links, chain[i].Index)
	}
	// Twin pairs must stay unlinked in both directions.
	twin := map[[2]int]bool{}
	nTwins := min(10, 3+len(rest)/300)
	for k := 0; k < nTwins; k++ {
		a := rest[r.IntN(len(rest))]
		for _, b := range rest {
			if b != a && b.Topic == a.Topic && b.TwinTerms == nil && a.TwinTerms == nil && b.Lang == "" && a.Lang == "" && b.Kind == "procedural" && a.Kind == "procedural" {
				t1, t2 := codename(r), codename(r)
				a.TwinTerms, b.TwinTerms = []string{t1, t2}, []string{t1, t2}
				twin[[2]int{a.Index, b.Index}], twin[[2]int{b.Index, a.Index}] = true, true
				w.TwinPairs = append(w.TwinPairs, [2]int{a.Index, b.Index})
				break
			}
		}
	}
	// Background links, mostly within a topic community.
	byTopic := map[int][]*notePlan{}
	for _, n := range order[3:] {
		if isTarget(n) {
			byTopic[n.Topic] = append(byTopic[n.Topic], n)
		}
	}
	degree := prng.NewZipf(8, 1.3)
	// link adds an edge between a and b, oriented forward in position
	// order; the later note must be a valid target ([[Title]] unambiguous).
	link := func(a, b *notePlan) {
		if a == b || twin[[2]int{a.Index, b.Index}] {
			return
		}
		if pos[a.Index] > pos[b.Index] {
			a, b = b, a
		}
		if isTarget(b) {
			a.Links = append(a.Links, b.Index)
		}
	}
	for _, n := range order[3 : len(order)-len(hubs)] {
		d := 1 + degree.Draw(r)
		for k := 0; k < d; k++ {
			var cand []*notePlan
			if r.Chance(0.85) {
				cand = byTopic[n.Topic]
			} else {
				cand = byTopic[w.Topics[r.IntN(len(w.Topics))]]
			}
			if len(cand) > 0 {
				link(n, cand[r.IntN(len(cand))])
			}
		}
		for hi, h := range hubs {
			if r.Chance(0.12 / float64(hi+1)) {
				n.Links = append(n.Links, h.Index)
			}
		}
	}
	// Roots link out: Home to the index notes and a sample, index notes to
	// a slice of their topic.
	roots[0].Links = append(roots[0].Links, roots[1].Index, roots[2].Index)
	for _, k := range r.Sample(len(order)-3, min(12, len(order)-3)) {
		link(roots[0], order[3+k])
	}
	for _, idx := range roots[1:] {
		for _, n := range byTopic[idx.Topic] {
			if r.Chance(0.15) {
				link(idx, n)
			}
		}
	}
	for _, h := range hubs {
		w.Hubs = append(w.Hubs, h.Index)
	}
	// Links that exist for a while and are then removed, and dangling links.
	for _, n := range order[3:] {
		if r.Chance(0.08) {
			t := order[r.IntN(len(order))]
			if t != n && isTarget(t) {
				n.DraftLinks = append(n.DraftLinks, t.Index)
			}
		}
		if r.Chance(0.04) {
			n.Dangling = append(n.Dangling, capitalize(prng.Pick(r, corpus.Topics[n.Topic].Vocab))+" "+codename(r)+" plan")
		}
	}
	for _, n := range notes {
		n.Links = uniqueInts(n.Links)
	}
	// Near-duplicates copy a note's content; nothing links to them, so they
	// cannot join a cycle. Notes in planted structures are never turned
	// into copies, because that would remove their planted links.
	planted := map[int]bool{}
	for _, n := range roots {
		planted[n.Index] = true
	}
	for _, n := range hubs {
		planted[n.Index] = true
	}
	for _, c := range w.Chain {
		planted[c] = true
	}
	for _, s := range w.SCCs {
		for _, m := range s.Members {
			planted[m] = true
		}
	}
	wantDups := max(2, len(rest)/50)
	dups := 0
	for _, k := range r.Sample(len(rest), len(rest)) {
		if dups == wantDups {
			break
		}
		n := rest[k]
		if planted[n.Index] || n.TwinTerms != nil || n.NoLinks || n.Codenames != nil {
			continue
		}
		orig := main[3+r.IntN(len(main)-3)]
		if orig.Index >= n.Index || orig.Kind != "procedural" || orig.TwinTerms != nil || orig.Trashed || orig.NoLinks || orig.DupOf >= 0 {
			continue
		}
		dups++
		n.DupOf = orig.Index
		n.Kind = "near-duplicate"
		p.needShingles[n.Index], p.needShingles[orig.Index] = true, true
		n.Topic, n.Topic2, n.Lang = orig.Topic, orig.Topic2, orig.Lang
		n.Blocks = orig.Blocks
		n.Links = nil
		n.DraftLinks, n.Dangling = nil, nil
		newTitle := []string{"Copy of " + orig.Title, orig.Title + " (copy)", orig.Title + " v2"}[r.IntN(3)]
		if w.TitleIndex[n.Title] == n.Index {
			delete(w.TitleIndex, n.Title)
		}
		n.Title = newTitle
		if _, exists := w.TitleIndex[newTitle]; exists {
			n.Title = fmt.Sprintf("%s %d", newTitle, n.Index)
		}
		w.TitleIndex[n.Title] = n.Index
	}
	// A near-duplicate's links come from its original's text; drop any
	// link that points at a near-duplicate, keeping them free of in-links.
	for _, n := range notes {
		out := n.Links[:0]
		for _, t := range n.Links {
			if p.notes[t].DupOf < 0 {
				out = append(out, t)
			}
		}
		n.Links = out
		dl := n.DraftLinks[:0]
		for _, t := range n.DraftLinks {
			if p.notes[t].DupOf < 0 {
				dl = append(dl, t)
			}
		}
		n.DraftLinks = dl
	}
}

func indexes(ns []*notePlan) []int {
	out := make([]int, len(ns))
	for i, n := range ns {
		out[i] = n.Index
	}
	sort.Ints(out)
	return out
}

func sortedCopy(xs []int) []int {
	out := append([]int{}, xs...)
	sort.Ints(out)
	return out
}

// planSearchPlants puts each codename into a few notes (some in the title)
// and codename identifiers into those notes' code.
func (p *plan) planSearchPlants(r *prng.Rand, w *wsPlan, notes []*notePlan) {
	var eligible []*notePlan
	for _, n := range notes[3:] {
		if n.Kind == "procedural" || n.Kind == "mixed" {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		return
	}
	for ci, c := range w.Codenames {
		k := 1 + (ci*5)%12
		for j, idx := range r.Sample(len(eligible), k) {
			n := eligible[idx]
			n.Codenames = append(n.Codenames, c)
			if ci%3 == 0 {
				n.CodeIdents = append(n.CodeIdents, capitalize(c)+"Shard")
			}
			if j == 0 && ci%2 == 0 && !n.TitleShared && w.TitleIndex[n.Title] == n.Index {
				delete(w.TitleIndex, n.Title)
				n.Title = capitalize(c) + " " + n.Title
				w.TitleIndex[n.Title] = n.Index
			}
		}
	}
}

var generalTags = []string{"draft", "idea", "meeting", "decision", "rfc", "howto", "archive", "review", "q3", "q4",
	"oncall", "design", "research", "followup", "blocked", "wip", "reference", "onboarding", "retro", "okr"}

func (p *plan) planTags(r *prng.Rand, w *wsPlan, notes []*notePlan) {
	vocab := append([]string{}, generalTags...)
	for _, t := range w.Topics {
		vocab = append(vocab, corpus.Topics[t].ID)
		vocab = append(vocab, corpus.Topics[t].Vocab[:3]...)
	}
	vocab = uniqueStrings(vocab)
	z := prng.NewZipf(len(vocab), 1.15)
	for _, n := range notes {
		k := 0
		for k < 6 && r.Chance(0.62) {
			k++
		}
		var tags []string
		for j := 0; j < k; j++ {
			tags = append(tags, vocab[z.Draw(r)])
		}
		n.Tags = uniqueStrings(tags)
	}
}

// planAccess adds per-note overrides, share links and the edits that will be
// rejected.
func (p *plan) planAccess(r *prng.Rand, w *wsPlan, notes []*notePlan, removed map[int]time.Time) {
	var guests, members []int
	for _, m := range w.Members {
		switch m.Role {
		case policy.Guest:
			guests = append(guests, m.User)
		case policy.Member:
			if m.RemovedAt == nil {
				members = append(members, m.User)
			}
		}
	}
	for _, n := range notes {
		if n.Trashed {
			continue
		}
		at := n.CreatedAt.Add(time.Minute)
		if len(guests) > 0 && r.Chance(0.06) {
			g := prng.Pick(r, guests)
			role := []policy.NoteRole{policy.Viewer, policy.Commenter, policy.Editor}[r.IntN(3)]
			p.addOverride(overridePlan{Note: n.Index, User: g, Role: role, GrantedBy: n.CreatedBy, At: at})
			if role == policy.Editor {
				n.Editors = append(n.Editors, g)
			}
		}
		if len(members) > 0 && r.Chance(0.04) {
			m := prng.Pick(r, members)
			if m != n.CreatedBy && !containsInt(n.Editors, m) {
				role := []policy.NoteRole{policy.Viewer, policy.Commenter}[r.IntN(2)]
				p.addOverride(overridePlan{Note: n.Index, User: m, Role: role, GrantedBy: n.CreatedBy, At: at})
				if r.Chance(0.5) {
					n.RejectViewer = m
				}
			}
		}
		if r.Chance(0.03) {
			sl := shareLinkPlan{Note: n.Index, CreatedBy: n.CreatedBy, CreatedAt: between(r, n.CreatedAt, ReferenceTime.Add(-24*time.Hour)),
				Role: []policy.NoteRole{policy.Viewer, policy.Viewer, policy.Commenter, policy.Editor}[r.IntN(4)]}
			switch r.IntN(4) {
			case 1:
				sl.ExpiresAt = timePtr(ReferenceTime.Add(time.Duration(1+r.IntN(60)) * 24 * time.Hour))
			case 2:
				sl.ExpiresAt = timePtr(between(r, sl.CreatedAt, ReferenceTime.Add(-time.Hour)))
			case 3:
				sl.RevokedAt = timePtr(between(r, sl.CreatedAt, ReferenceTime.Add(-time.Hour)))
			}
			p.addShareLink(sl)
		}
	}
	// Each removed member had an editor's offline session on up to three
	// notes that is refused when they reconnect after removal.
	removedUsers := make([]int, 0, len(removed))
	for u := range removed {
		removedUsers = append(removedUsers, u)
	}
	sort.Ints(removedUsers)
	for _, u := range removedUsers {
		at := removed[u]
		count := 0
		for _, n := range notes {
			if count == 3 {
				break
			}
			if !n.Trashed && n.CreatedAt.Before(at.Add(-48*time.Hour)) && n.RejectRevoked < 0 && n.CreatedBy != u && r.Chance(0.2) {
				if p.policy.Evaluate(policy.Subject{UserID: p.users[u].ID}, policy.NoteEdit, n.ID, n.CreatedAt.Add(2*time.Hour)).Allowed {
					n.RejectRevoked = u
					count++
				}
			}
		}
	}
	// Agent suggestion edits on about a tenth of notes.
	for _, n := range notes {
		n.Agent = !n.Trashed && r.Chance(0.1)
	}
}

func containsInt(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
