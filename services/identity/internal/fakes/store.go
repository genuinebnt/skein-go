// Package fakes holds in-memory implementations of identity's ports for
// tests: a transactional Store, OAuth providers and a settable clock.
//
// # Store semantics
//
// Store serialises transactions with one mutex (as if every transaction
// were SERIALIZABLE and never conflicted) and gives each a copy-on-write
// view: the first write clones the state, and commit swaps the clone in.
// A transaction that returns an error, or panics, leaves no trace, which
// is what the app layer relies on for "the change and its event commit
// together". Published records AccessChanged events in commit order, like
// the outbox relay would publish them.
//
// CheckInvariants verifies the relational rules the Postgres schema
// enforces with keys and constraints, so tests catch a use case that would
// violate one before it reaches a database:
//
//   - S1 memberships, overrides, sessions, share links, replicas and OAuth
//     links reference existing users (and workspaces);
//   - S2 an override's and a share link's workspace is its note's
//     workspace, when the note is in the projection;
//   - S3 every workspace with an active member has an active owner;
//   - S4 every refresh token belongs to an existing session;
//   - S5 emails are unique and normalised;
//   - S6 only active members hold overrides (removing a member deletes
//     theirs). A member promoted to admin keeps theirs: ignored while
//     admin, in force again if demoted, as the seed policy has it.
package fakes

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

type memberKey struct {
	ws   domain.WorkspaceID
	user domain.UserID
}

type overrideKey struct {
	note domain.NoteID
	user domain.UserID
}

type oauthKey struct {
	provider app.OAuthProviderKind
	subject  string
}

type state struct {
	users       map[domain.UserID]domain.User
	oauth       map[oauthKey]app.OAuthIdentity
	sessions    map[domain.SessionID]domain.Session
	refresh     map[string]domain.RefreshToken
	workspaces  map[domain.WorkspaceID]domain.Workspace
	memberships map[memberKey]domain.Membership
	overrides   map[overrideKey]domain.NoteRoleOverride
	links       map[domain.ShareLinkID]domain.ShareLink
	notes       map[domain.NoteID]domain.NoteAccess
	replicas    map[domain.ReplicaID]domain.Replica
	oneTime     map[string]domain.OneTimeToken
	processed   map[string]bool
	outbox      []domain.AccessChange
}

func newState() *state {
	return &state{
		users: map[domain.UserID]domain.User{}, oauth: map[oauthKey]app.OAuthIdentity{},
		sessions: map[domain.SessionID]domain.Session{}, refresh: map[string]domain.RefreshToken{},
		workspaces: map[domain.WorkspaceID]domain.Workspace{}, memberships: map[memberKey]domain.Membership{},
		overrides: map[overrideKey]domain.NoteRoleOverride{}, links: map[domain.ShareLinkID]domain.ShareLink{},
		notes: map[domain.NoteID]domain.NoteAccess{}, replicas: map[domain.ReplicaID]domain.Replica{},
		oneTime: map[string]domain.OneTimeToken{}, processed: map[string]bool{},
	}
}

// clone copies every map. Values are structs whose pointer fields
// (*time.Time) are never mutated in place, only replaced, so a shallow
// copy of each value is a full copy of the state.
func (s *state) clone() *state {
	return &state{
		users: maps.Clone(s.users), oauth: maps.Clone(s.oauth), sessions: maps.Clone(s.sessions),
		refresh: maps.Clone(s.refresh), workspaces: maps.Clone(s.workspaces),
		memberships: maps.Clone(s.memberships), overrides: maps.Clone(s.overrides),
		links: maps.Clone(s.links), notes: maps.Clone(s.notes), replicas: maps.Clone(s.replicas),
		oneTime: maps.Clone(s.oneTime), processed: maps.Clone(s.processed),
		outbox: slices.Clone(s.outbox),
	}
}

// Store is an in-memory app.Store.
type Store struct {
	mu        sync.Mutex
	state     *state
	published []domain.AccessChange
	// FailNextCommit, when set, makes the next transaction fail at commit
	// with this error, for testing that nothing leaks from a rolled-back
	// transaction.
	FailNextCommit error
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{state: newState()} }

// InTx implements app.Store.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &tx{base: s.state}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if s.FailNextCommit != nil {
		err, s.FailNextCommit = s.FailNextCommit, nil
		return err
	}
	if tx.w != nil {
		s.published = append(s.published, tx.w.outbox...)
		tx.w.outbox = nil
		s.state = tx.w
	}
	return nil
}

// Published returns the AccessChanged events committed so far, in order.
func (s *Store) Published() []domain.AccessChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.published)
}

// ResetPublished forgets the recorded events.
func (s *Store) ResetPublished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published = nil
}

// Session returns a stored session, for assertions.
func (s *Store) Session(id domain.SessionID) (domain.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.state.sessions[id]
	return v, ok
}

// RefreshTokens returns every stored refresh token, for assertions.
func (s *Store) RefreshTokens() []domain.RefreshToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.state.refresh))
	slices.SortFunc(out, func(a, b domain.RefreshToken) int { return cmp.Compare(a.Hash, b.Hash) })
	return out
}

// ShareLinks returns every stored share link, for assertions.
func (s *Store) ShareLinks() []domain.ShareLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.state.links))
	slices.SortFunc(out, func(a, b domain.ShareLink) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// CheckInvariants verifies S1 to S6.
func (s *Store) CheckInvariants() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	emails := map[string]domain.UserID{}
	for _, u := range sortedValues(st.users, func(u domain.User) string { return string(u.ID) }) {
		if u.Email != strings.ToLower(strings.TrimSpace(u.Email)) {
			return fmt.Errorf("S5: email of %s is not normalised", u.ID)
		}
		if other, dup := emails[u.Email]; dup {
			return fmt.Errorf("S5: %s and %s share an email", other, u.ID)
		}
		emails[u.Email] = u.ID
	}
	activeOwner := map[domain.WorkspaceID]bool{}
	activeAny := map[domain.WorkspaceID]bool{}
	for _, m := range st.memberships {
		if _, ok := st.users[m.UserID]; !ok {
			return fmt.Errorf("S1: membership of unknown user %s", m.UserID)
		}
		if _, ok := st.workspaces[m.WorkspaceID]; !ok {
			return fmt.Errorf("S1: membership of unknown workspace %s", m.WorkspaceID)
		}
		if m.RemovedAt == nil {
			activeAny[m.WorkspaceID] = true
			if m.Role == domain.WorkspaceRoleOwner {
				activeOwner[m.WorkspaceID] = true
			}
		}
	}
	for ws := range activeAny {
		if !activeOwner[ws] {
			return fmt.Errorf("S3: workspace %s has active members but no owner", ws)
		}
	}
	for _, o := range st.overrides {
		if _, ok := st.users[o.UserID]; !ok {
			return fmt.Errorf("S1: override for unknown user %s", o.UserID)
		}
		if n, ok := st.notes[o.NoteID]; ok && n.WorkspaceID != o.WorkspaceID {
			return fmt.Errorf("S2: override on %s says workspace %s, note says %s", o.NoteID, o.WorkspaceID, n.WorkspaceID)
		}
		m, ok := st.memberships[memberKey{o.WorkspaceID, o.UserID}]
		if !ok || m.RemovedAt != nil {
			return fmt.Errorf("S6: %s holds an override on %s without an active membership", o.UserID, o.NoteID)
		}
	}
	for _, l := range st.links {
		if n, ok := st.notes[l.NoteID]; ok && n.WorkspaceID != l.WorkspaceID {
			return fmt.Errorf("S2: share link %s workspace mismatch", l.ID)
		}
		if l.TokenHash == "" || strings.HasPrefix(l.TokenHash, "sl_") {
			return fmt.Errorf("S1: share link %s stores no hash, or a raw token", l.ID)
		}
	}
	for _, sess := range st.sessions {
		if _, ok := st.users[sess.UserID]; !ok {
			return fmt.Errorf("S1: session %s of unknown user", sess.ID)
		}
	}
	for h, t := range st.refresh {
		if _, ok := st.sessions[t.SessionID]; !ok {
			return fmt.Errorf("S4: refresh token %s… of unknown session", h[:8])
		}
	}
	for _, r := range st.replicas {
		if _, ok := st.users[r.UserID]; !ok {
			return fmt.Errorf("S1: replica %s of unknown user", r.ID)
		}
	}
	for _, l := range st.oauth {
		if _, ok := st.users[l.UserID]; !ok {
			return fmt.Errorf("S1: OAuth link of unknown user %s", l.UserID)
		}
	}
	return nil
}

func sortedValues[K comparable, V any](m map[K]V, key func(V) string) []V {
	out := slices.Collect(maps.Values(m))
	slices.SortFunc(out, func(a, b V) int { return cmp.Compare(key(a), key(b)) })
	return out
}

// tx is one transaction: reads come from w once it exists, else base.
type tx struct {
	base *state
	w    *state
}

func (t *tx) r() *state {
	if t.w != nil {
		return t.w
	}
	return t.base
}

func (t *tx) write() *state {
	if t.w == nil {
		t.w = t.base.clone()
	}
	return t.w
}

var _ app.Tx = (*tx)(nil)

func notFound(what string) error { return fmt.Errorf("%w: %s", domain.ErrNotFound, what) }

func (t *tx) InsertUser(_ context.Context, u domain.User) error {
	for _, other := range t.r().users {
		if other.Email == u.Email {
			return fmt.Errorf("%w: email", domain.ErrAlreadyExists)
		}
	}
	if _, ok := t.r().users[u.ID]; ok {
		return fmt.Errorf("%w: user id", domain.ErrAlreadyExists)
	}
	t.write().users[u.ID] = u
	return nil
}

func (t *tx) UserByID(_ context.Context, id domain.UserID) (domain.User, error) {
	u, ok := t.r().users[id]
	if !ok {
		return domain.User{}, notFound("user")
	}
	return u, nil
}

func (t *tx) UserByEmail(_ context.Context, email string) (domain.User, error) {
	for _, u := range t.r().users {
		if u.Email == email {
			return u, nil
		}
	}
	return domain.User{}, notFound("user")
}

func (t *tx) SetPasswordHash(_ context.Context, id domain.UserID, hash string) error {
	u, ok := t.r().users[id]
	if !ok {
		return notFound("user")
	}
	u.PasswordHash = hash
	t.write().users[id] = u
	return nil
}

func (t *tx) MarkEmailVerified(_ context.Context, id domain.UserID, at time.Time) error {
	u, ok := t.r().users[id]
	if !ok {
		return notFound("user")
	}
	if u.EmailVerifiedAt == nil {
		u.EmailVerifiedAt = &at
		t.write().users[id] = u
	}
	return nil
}

func (t *tx) OAuthIdentity(_ context.Context, p app.OAuthProviderKind, subject string) (app.OAuthIdentity, error) {
	l, ok := t.r().oauth[oauthKey{p, subject}]
	if !ok {
		return app.OAuthIdentity{}, notFound("oauth identity")
	}
	return l, nil
}

func (t *tx) InsertOAuthIdentity(_ context.Context, l app.OAuthIdentity) error {
	k := oauthKey{l.Provider, l.Subject}
	if _, ok := t.r().oauth[k]; ok {
		return fmt.Errorf("%w: oauth identity", domain.ErrAlreadyExists)
	}
	t.write().oauth[k] = l
	return nil
}

func (t *tx) InsertSession(_ context.Context, sess domain.Session) error {
	t.write().sessions[sess.ID] = sess
	return nil
}

func (t *tx) SessionByIDForUpdate(_ context.Context, id domain.SessionID) (domain.Session, error) {
	sess, ok := t.r().sessions[id]
	if !ok {
		return domain.Session{}, notFound("session")
	}
	return sess, nil
}

func (t *tx) RevokeSession(_ context.Context, id domain.SessionID, at time.Time, reason domain.RevokeReason) error {
	sess, ok := t.r().sessions[id]
	if !ok {
		return notFound("session")
	}
	if sess.RevokedAt == nil {
		sess.RevokedAt, sess.RevokeReason = &at, reason
		t.write().sessions[id] = sess
	}
	return nil
}

func (t *tx) RevokeUserSessions(_ context.Context, user domain.UserID, at time.Time, reason domain.RevokeReason) (int, error) {
	n := 0
	for id, sess := range t.r().sessions {
		if sess.UserID == user && sess.RevokedAt == nil {
			sess.RevokedAt, sess.RevokeReason = &at, reason
			t.write().sessions[id] = sess
			n++
		}
	}
	return n, nil
}

func (t *tx) InsertRefreshToken(_ context.Context, rt domain.RefreshToken) error {
	if _, ok := t.r().refresh[rt.Hash]; ok {
		return fmt.Errorf("%w: refresh token", domain.ErrAlreadyExists)
	}
	t.write().refresh[rt.Hash] = rt
	return nil
}

func (t *tx) RefreshTokenForUpdate(_ context.Context, hash string) (domain.RefreshToken, error) {
	rt, ok := t.r().refresh[hash]
	if !ok {
		return domain.RefreshToken{}, notFound("refresh token")
	}
	return rt, nil
}

func (t *tx) MarkRefreshTokenRotated(_ context.Context, hash string, at time.Time) error {
	rt, ok := t.r().refresh[hash]
	if !ok {
		return notFound("refresh token")
	}
	rt.RotatedAt = &at
	t.write().refresh[hash] = rt
	return nil
}

func (t *tx) InsertWorkspace(_ context.Context, w domain.Workspace) error {
	if _, ok := t.r().workspaces[w.ID]; ok {
		return fmt.Errorf("%w: workspace", domain.ErrAlreadyExists)
	}
	t.write().workspaces[w.ID] = w
	return nil
}

func (t *tx) Workspace(_ context.Context, id domain.WorkspaceID) (domain.Workspace, error) {
	w, ok := t.r().workspaces[id]
	if !ok {
		return domain.Workspace{}, notFound("workspace")
	}
	return w, nil
}

func (t *tx) Membership(_ context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error) {
	m, ok := t.r().memberships[memberKey{ws, user}]
	if !ok {
		return domain.Membership{}, notFound("membership")
	}
	return m, nil
}

func (t *tx) MembershipForUpdate(ctx context.Context, ws domain.WorkspaceID, user domain.UserID) (domain.Membership, error) {
	return t.Membership(ctx, ws, user)
}

func (t *tx) UpsertMembership(_ context.Context, m domain.Membership) error {
	t.write().memberships[memberKey{m.WorkspaceID, m.UserID}] = m
	return nil
}

func (t *tx) MembershipsOf(_ context.Context, user domain.UserID, wss []domain.WorkspaceID) ([]domain.Membership, error) {
	var out []domain.Membership
	for _, ws := range wss {
		if m, ok := t.r().memberships[memberKey{ws, user}]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (t *tx) ListActiveMembers(_ context.Context, ws domain.WorkspaceID, after domain.UserID, limit int) ([]domain.Member, error) {
	var out []domain.Member
	for k, m := range t.r().memberships {
		if k.ws == ws && m.RemovedAt == nil && k.user > after {
			out = append(out, domain.Member{User: t.r().users[k.user], Role: m.Role})
		}
	}
	slices.SortFunc(out, func(a, b domain.Member) int { return cmp.Compare(a.User.ID, b.User.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *tx) CountActiveOwners(_ context.Context, ws domain.WorkspaceID) (int, error) {
	n := 0
	for k, m := range t.r().memberships {
		if k.ws == ws && m.RemovedAt == nil && m.Role == domain.WorkspaceRoleOwner {
			n++
		}
	}
	return n, nil
}

func (t *tx) ShareActiveWorkspace(_ context.Context, a, b domain.UserID) (bool, error) {
	for k, m := range t.r().memberships {
		if k.user != a || m.RemovedAt != nil {
			continue
		}
		if other, ok := t.r().memberships[memberKey{k.ws, b}]; ok && other.RemovedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

func (t *tx) UpsertNoteRoleOverride(_ context.Context, o domain.NoteRoleOverride) error {
	t.write().overrides[overrideKey{o.NoteID, o.UserID}] = o
	return nil
}

func (t *tx) DeleteNoteRoleOverride(_ context.Context, note domain.NoteID, user domain.UserID) error {
	if _, ok := t.r().overrides[overrideKey{note, user}]; ok {
		delete(t.write().overrides, overrideKey{note, user})
	}
	return nil
}

func (t *tx) DeleteOverridesInWorkspace(_ context.Context, ws domain.WorkspaceID, user domain.UserID) error {
	for k, o := range t.r().overrides {
		if o.WorkspaceID == ws && o.UserID == user {
			delete(t.write().overrides, k)
		}
	}
	return nil
}

func (t *tx) OverridesOf(_ context.Context, user domain.UserID, notes []domain.NoteID) ([]domain.NoteRoleOverride, error) {
	var out []domain.NoteRoleOverride
	for _, n := range notes {
		if o, ok := t.r().overrides[overrideKey{n, user}]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}

func (t *tx) OverridesInWorkspace(_ context.Context, ws domain.WorkspaceID, user domain.UserID) ([]domain.NoteRoleOverride, error) {
	var out []domain.NoteRoleOverride
	for _, o := range t.r().overrides {
		if o.WorkspaceID == ws && o.UserID == user {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b domain.NoteRoleOverride) int { return cmp.Compare(a.NoteID, b.NoteID) })
	return out, nil
}

func (t *tx) InsertShareLink(_ context.Context, l domain.ShareLink) error {
	if _, ok := t.r().links[l.ID]; ok {
		return fmt.Errorf("%w: share link", domain.ErrAlreadyExists)
	}
	t.write().links[l.ID] = l
	return nil
}

func (t *tx) ShareLink(_ context.Context, id domain.ShareLinkID) (domain.ShareLink, error) {
	l, ok := t.r().links[id]
	if !ok {
		return domain.ShareLink{}, notFound("share link")
	}
	return l, nil
}

func (t *tx) ShareLinkByTokenHash(_ context.Context, hash string) (domain.ShareLink, error) {
	for _, l := range t.r().links {
		if l.TokenHash == hash {
			return l, nil
		}
	}
	return domain.ShareLink{}, notFound("share link")
}

func (t *tx) RevokeShareLink(_ context.Context, id domain.ShareLinkID, at time.Time) error {
	l, ok := t.r().links[id]
	if !ok {
		return notFound("share link")
	}
	if l.RevokedAt == nil {
		l.RevokedAt = &at
		t.write().links[id] = l
	}
	return nil
}

func (t *tx) UpsertNote(_ context.Context, n domain.NoteAccess) error {
	t.write().notes[n.ID] = n
	return nil
}

func (t *tx) SetNoteTrashed(_ context.Context, id domain.NoteID, at *time.Time) error {
	n, ok := t.r().notes[id]
	if !ok {
		return notFound("note")
	}
	n.TrashedAt = at
	t.write().notes[id] = n
	return nil
}

func (t *tx) PurgeNote(_ context.Context, id domain.NoteID) error {
	w := t.write()
	delete(w.notes, id)
	for k := range w.overrides {
		if k.note == id {
			delete(w.overrides, k)
		}
	}
	for k, l := range w.links {
		if l.NoteID == id {
			delete(w.links, k)
		}
	}
	return nil
}

func (t *tx) Notes(_ context.Context, ids []domain.NoteID) ([]domain.NoteAccess, error) {
	var out []domain.NoteAccess
	for _, id := range ids {
		if n, ok := t.r().notes[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

func (t *tx) NotesInWorkspace(_ context.Context, ws domain.WorkspaceID) ([]domain.NoteAccess, error) {
	var out []domain.NoteAccess
	for _, n := range t.r().notes {
		if n.WorkspaceID == ws {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b domain.NoteAccess) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (t *tx) MarkEventProcessed(_ context.Context, consumer, eventID string) (bool, error) {
	k := consumer + "/" + eventID
	if t.r().processed[k] {
		return false, nil
	}
	t.write().processed[k] = true
	return true, nil
}

func (t *tx) Replica(_ context.Context, id domain.ReplicaID) (domain.Replica, error) {
	r, ok := t.r().replicas[id]
	if !ok {
		return domain.Replica{}, notFound("replica")
	}
	return r, nil
}

func (t *tx) InsertReplica(_ context.Context, r domain.Replica) error {
	if _, ok := t.r().replicas[r.ID]; ok {
		return fmt.Errorf("%w: replica", domain.ErrAlreadyExists)
	}
	t.write().replicas[r.ID] = r
	return nil
}

func (t *tx) InsertOneTimeToken(_ context.Context, o domain.OneTimeToken) error {
	t.write().oneTime[o.Hash] = o
	return nil
}

func (t *tx) OneTimeTokenForUpdate(_ context.Context, hash string) (domain.OneTimeToken, error) {
	o, ok := t.r().oneTime[hash]
	if !ok {
		return domain.OneTimeToken{}, notFound("token")
	}
	return o, nil
}

func (t *tx) MarkOneTimeTokenUsed(_ context.Context, hash string, at time.Time) error {
	o, ok := t.r().oneTime[hash]
	if !ok {
		return notFound("token")
	}
	o.UsedAt = &at
	t.write().oneTime[hash] = o
	return nil
}

func (t *tx) EnqueueAccessChanged(_ context.Context, c domain.AccessChange) error {
	if c.WorkspaceID == "" {
		return errors.New("fakes: AccessChanged without a workspace")
	}
	w := t.write()
	w.outbox = append(w.outbox, c)
	return nil
}

// Dump returns the whole state as deterministic text, so tests can assert
// that an operation changed nothing (for example a second seed load).
func (s *Store) Dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	var lines []string
	// JSON, not %+v: optional times are pointers, and %+v would print
	// their addresses.
	add := func(kind string, v any) {
		b, _ := json.Marshal(v)
		lines = append(lines, kind+" "+string(b))
	}
	for _, v := range st.users {
		add("user", v)
	}
	for _, v := range st.oauth {
		add("oauth", v)
	}
	for _, v := range st.sessions {
		add("session", v)
	}
	for _, v := range st.refresh {
		add("refresh", v)
	}
	for _, v := range st.workspaces {
		add("workspace", v)
	}
	for _, v := range st.memberships {
		add("membership", v)
	}
	for _, v := range st.overrides {
		add("override", v)
	}
	for _, v := range st.links {
		add("link", v)
	}
	for _, v := range st.notes {
		add("note", v)
	}
	for _, v := range st.replicas {
		add("replica", v)
	}
	for _, v := range st.oneTime {
		add("onetime", v)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}
