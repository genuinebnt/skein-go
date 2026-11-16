// Package seed loads identity's share of a seed dataset (docs/seed-data.md,
// "Loader contract") through identity's own repositories:
//
//   - users, workspaces, memberships (inserted, then members with
//     removed_at are removed, which deletes their overrides as RemoveMember
//     does), per-note role overrides (acl.jsonl), share links (the token's
//     SHA-256 only, after checking it matches the token) and replicas;
//   - the note-access projection from notes.jsonl. Notes belong to
//     documents, and in the full stack identity's projection is filled from
//     documents' events; loading it here lets identity run, and be tested
//     against the permission matrix, on its own. It is the same projection
//     the events write, keyed by note ID, so loading it and later
//     consuming documents' events for the same notes agree.
//
// The loader checks the manifest first (format version and the SHA-256 of
// every file it reads) and refuses on any mismatch. It never reads truth/.
// Loading is one transaction and idempotent: rows are keyed by the
// dataset's IDs, existing ones are left as they are (or upserted to the
// same value), so loading twice changes nothing. No AccessChanged events are
// published: a freshly loaded dataset has no caches or streams to drop, and
// a second load would only publish duplicates.
//
// Seed users have no passwords. Options.PasswordHash, when set, is given
// to every loaded user that has none, so a local stack can sign in as the
// anchor cast.
package seed

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// FormatVersion is the dataset format this loader reads.
const FormatVersion = 1

// ErrManifest means the dataset's manifest is missing, of another format
// version, or disagrees with a file's contents.
var ErrManifest = errors.New("seed: manifest check failed")

// Files is every dataset file the loader reads.
var Files = []string{
	"users.jsonl", "workspaces.jsonl", "notes.jsonl", "memberships.jsonl",
	"acl.jsonl", "share_links.jsonl", "replicas.jsonl",
}

// Manifest is the part of manifest.json the loader uses.
type Manifest struct {
	FormatVersion int       `json:"format_version"`
	ReferenceTime time.Time `json:"reference_time"`
	Files         []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// ReadManifest reads and checks dir/manifest.json and the hashes of Files.
func ReadManifest(dir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if m.FormatVersion != FormatVersion {
		return Manifest{}, fmt.Errorf("%w: format_version %d, this loader reads %d", ErrManifest, m.FormatVersion, FormatVersion)
	}
	want := map[string]string{}
	for _, f := range m.Files {
		want[f.Path] = f.SHA256
	}
	for _, name := range Files {
		expected, ok := want[name]
		if !ok {
			return Manifest{}, fmt.Errorf("%w: %s is not in the manifest", ErrManifest, name)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != expected {
			return Manifest{}, fmt.Errorf("%w: %s has sha256 %s, manifest says %s", ErrManifest, name, got, expected)
		}
	}
	return m, nil
}

// Options configure Load.
type Options struct {
	Dir string
	// PasswordHash (a PHC string) is set on loaded users without one.
	PasswordHash string
}

// Report counts what one load wrote or found already present.
type Report struct {
	Users, Workspaces, Notes, Memberships, Removed, Overrides, ShareLinks, Replicas int
	ReferenceTime                                                                   time.Time
}

type userRecord struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

type workspaceRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
}

type noteRecord struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	TrashedAt   *time.Time `json:"trashed_at"`
}

type membershipRecord struct {
	WorkspaceID string     `json:"workspace_id"`
	UserID      string     `json:"user_id"`
	Role        string     `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	RemovedAt   *time.Time `json:"removed_at"`
	InvitedBy   *string    `json:"invited_by"`
}

type aclRecord struct {
	NoteID    string    `json:"note_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	GrantedBy string    `json:"granted_by"`
	CreatedAt time.Time `json:"created_at"`
}

type shareLinkRecord struct {
	ID          string     `json:"id"`
	NoteID      string     `json:"note_id"`
	Role        string     `json:"role"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	Token       string     `json:"token"`
	TokenSHA256 string     `json:"token_sha256"`
}

type replicaRecord struct {
	ReplicaID    string    `json:"replica_id"`
	UserID       string    `json:"user_id"`
	AgentRunID   *string   `json:"agent_run_id"`
	RegisteredAt time.Time `json:"registered_at"`
}

// ReadJSONL decodes every non-empty line of dir/name into T and fails on
// the first malformed line, naming it.
func ReadJSONL[T any](dir, name string) ([]T, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var out []T
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("seed: %s line %d: %w", name, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

type dataset struct {
	users       []userRecord
	workspaces  []workspaceRecord
	notes       []noteRecord
	memberships []membershipRecord
	acl         []aclRecord
	links       []shareLinkRecord
	replicas    []replicaRecord
}

func readDataset(dir string) (dataset, error) {
	var d dataset
	var err error
	if d.users, err = ReadJSONL[userRecord](dir, "users.jsonl"); err != nil {
		return d, err
	}
	if d.workspaces, err = ReadJSONL[workspaceRecord](dir, "workspaces.jsonl"); err != nil {
		return d, err
	}
	if d.notes, err = ReadJSONL[noteRecord](dir, "notes.jsonl"); err != nil {
		return d, err
	}
	if d.memberships, err = ReadJSONL[membershipRecord](dir, "memberships.jsonl"); err != nil {
		return d, err
	}
	if d.acl, err = ReadJSONL[aclRecord](dir, "acl.jsonl"); err != nil {
		return d, err
	}
	if d.links, err = ReadJSONL[shareLinkRecord](dir, "share_links.jsonl"); err != nil {
		return d, err
	}
	d.replicas, err = ReadJSONL[replicaRecord](dir, "replicas.jsonl")
	return d, err
}

// Load checks the manifest and loads the dataset into store.
func Load(ctx context.Context, store app.Store, opts Options) (Report, error) {
	m, err := ReadManifest(opts.Dir)
	if err != nil {
		return Report{}, err
	}
	d, err := readDataset(opts.Dir)
	if err != nil {
		return Report{}, err
	}
	if opts.PasswordHash != "" {
		if _, err := credential.ParsePasswordHash(opts.PasswordHash); err != nil {
			return Report{}, fmt.Errorf("seed: password hash: %w", err)
		}
	}
	rep := Report{ReferenceTime: m.ReferenceTime}
	err = store.InTx(ctx, func(ctx context.Context, tx app.Tx) error {
		rep = Report{ReferenceTime: m.ReferenceTime}
		return load(ctx, tx, d, opts, &rep)
	})
	return rep, err
}

func load(ctx context.Context, tx app.Tx, d dataset, opts Options, rep *Report) error {
	for _, u := range d.users {
		if err := loadUser(ctx, tx, u, opts.PasswordHash); err != nil {
			return fmt.Errorf("user %s: %w", u.ID, err)
		}
		rep.Users++
	}
	for _, w := range d.workspaces {
		if err := loadWorkspace(ctx, tx, w); err != nil {
			return fmt.Errorf("workspace %s: %w", w.ID, err)
		}
		rep.Workspaces++
	}
	noteWorkspace := map[domain.NoteID]domain.WorkspaceID{}
	for _, n := range d.notes {
		na := domain.NoteAccess{ID: domain.NoteID(n.ID), WorkspaceID: domain.WorkspaceID(n.WorkspaceID),
			CreatedBy: domain.UserID(n.CreatedBy), CreatedAt: n.CreatedAt.UTC(), TrashedAt: utcPtr(n.TrashedAt)}
		if err := tx.UpsertNote(ctx, na); err != nil {
			return fmt.Errorf("note %s: %w", n.ID, err)
		}
		noteWorkspace[na.ID] = na.WorkspaceID
		rep.Notes++
	}
	for _, r := range d.memberships {
		role := domain.ParseWorkspaceRole(r.Role)
		if !role.Valid() {
			return fmt.Errorf("membership %s/%s: unknown role %q", r.WorkspaceID, r.UserID, r.Role)
		}
		m := domain.Membership{WorkspaceID: domain.WorkspaceID(r.WorkspaceID), UserID: domain.UserID(r.UserID),
			Role: role, JoinedAt: r.JoinedAt.UTC()}
		if r.InvitedBy != nil {
			m.InvitedBy = domain.UserID(*r.InvitedBy)
		}
		if err := tx.UpsertMembership(ctx, m); err != nil {
			return err
		}
		rep.Memberships++
	}
	for _, a := range d.acl {
		ws, ok := noteWorkspace[domain.NoteID(a.NoteID)]
		if !ok {
			return fmt.Errorf("acl: note %s is not in notes.jsonl", a.NoteID)
		}
		role := domain.ParseNoteRole(a.Role)
		if !role.Valid() {
			return fmt.Errorf("acl %s/%s: unknown role %q", a.NoteID, a.UserID, a.Role)
		}
		o := domain.NoteRoleOverride{NoteID: domain.NoteID(a.NoteID), WorkspaceID: ws, UserID: domain.UserID(a.UserID),
			Role: role, GrantedBy: domain.UserID(a.GrantedBy), CreatedAt: a.CreatedAt.UTC()}
		if err := tx.UpsertNoteRoleOverride(ctx, o); err != nil {
			return err
		}
		rep.Overrides++
	}
	// Remove members last, through the same two steps RemoveMember takes,
	// so their overrides go with them.
	for _, r := range d.memberships {
		if r.RemovedAt == nil {
			continue
		}
		ws, user := domain.WorkspaceID(r.WorkspaceID), domain.UserID(r.UserID)
		m, err := tx.Membership(ctx, ws, user)
		if err != nil {
			return err
		}
		m.RemovedAt = utcPtr(r.RemovedAt)
		if err := tx.UpsertMembership(ctx, m); err != nil {
			return err
		}
		if err := tx.DeleteOverridesInWorkspace(ctx, ws, user); err != nil {
			return err
		}
		rep.Removed++
	}
	for _, l := range d.links {
		if err := loadShareLink(ctx, tx, l, noteWorkspace); err != nil {
			return fmt.Errorf("share link %s: %w", l.ID, err)
		}
		rep.ShareLinks++
	}
	for _, r := range d.replicas {
		if err := loadReplica(ctx, tx, r); err != nil {
			return fmt.Errorf("replica %s: %w", r.ReplicaID, err)
		}
		rep.Replicas++
	}
	return nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func loadUser(ctx context.Context, tx app.Tx, r userRecord, passwordHash string) error {
	existing, err := tx.UserByID(ctx, domain.UserID(r.ID))
	if err == nil {
		if existing.PasswordHash == "" && passwordHash != "" {
			return tx.SetPasswordHash(ctx, existing.ID, passwordHash)
		}
		return nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	email, err := domain.NormalizeEmail(r.Email)
	if err != nil {
		return err
	}
	created := r.CreatedAt.UTC()
	// The cast and generated people are fixtures, not sign-ups: their
	// addresses count as verified.
	return tx.InsertUser(ctx, domain.User{ID: domain.UserID(r.ID), Email: email, DisplayName: r.DisplayName,
		PasswordHash: passwordHash, EmailVerifiedAt: &created, CreatedAt: created})
}

func loadWorkspace(ctx context.Context, tx app.Tx, r workspaceRecord) error {
	if _, err := tx.Workspace(ctx, domain.WorkspaceID(r.ID)); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return tx.InsertWorkspace(ctx, domain.Workspace{ID: domain.WorkspaceID(r.ID), Name: r.Name,
		OwnerID: domain.UserID(r.OwnerID), CreatedAt: r.CreatedAt.UTC()})
}

func loadShareLink(ctx context.Context, tx app.Tx, r shareLinkRecord, noteWorkspace map[domain.NoteID]domain.WorkspaceID) error {
	if credential.HashToken(r.Token) != r.TokenSHA256 {
		return errors.New("token_sha256 does not match the token")
	}
	if _, err := tx.ShareLink(ctx, domain.ShareLinkID(r.ID)); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	ws, ok := noteWorkspace[domain.NoteID(r.NoteID)]
	if !ok {
		return fmt.Errorf("note %s is not in notes.jsonl", r.NoteID)
	}
	role := domain.ParseNoteRole(r.Role)
	if !role.Valid() {
		return fmt.Errorf("unknown role %q", r.Role)
	}
	return tx.InsertShareLink(ctx, domain.ShareLink{
		ID: domain.ShareLinkID(r.ID), NoteID: domain.NoteID(r.NoteID), WorkspaceID: ws, Role: role,
		TokenHash: r.TokenSHA256, CreatedBy: domain.UserID(r.CreatedBy), CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: utcPtr(r.ExpiresAt), RevokedAt: utcPtr(r.RevokedAt),
	})
}

func loadReplica(ctx context.Context, tx app.Tx, r replicaRecord) error {
	v, err := strconv.ParseUint(r.ReplicaID, 10, 64)
	if err != nil {
		return err
	}
	id, err := domain.ParseReplicaID("replica_id", v)
	if err != nil {
		return err
	}
	existing, err := tx.Replica(ctx, id)
	if err == nil {
		if existing.UserID != domain.UserID(r.UserID) {
			return fmt.Errorf("%w: replica is registered to %s", domain.ErrAlreadyExists, existing.UserID)
		}
		return nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	rep := domain.Replica{ID: id, UserID: domain.UserID(r.UserID), RegisteredAt: r.RegisteredAt.UTC()}
	if r.AgentRunID != nil {
		rep.AgentRunID = domain.AgentRunID(*r.AgentRunID)
	}
	return tx.InsertReplica(ctx, rep)
}
