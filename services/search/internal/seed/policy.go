package seed

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
)

// Directory is an app.AccessDirectory answering from a dataset's
// memberships and per-note roles with the READ rules of the seed policy
// (docs/seed-data.md, "Permissions"), at the dataset's reference time:
//
//  1. not a member at t (never joined, or removed at or before t): nothing,
//     reported as NOT_FOUND;
//  2. owner or admin: every note, trashed ones included;
//  3. member: every note that is not trashed (every override role reads);
//  4. guest: the non-trashed notes an override created at or before t
//     grants a role on.
//
// It stands in for identity in tests and in the seed tool's dry run;
// identity owns the real rules. A test cross-checks it against the
// dataset's permission matrix.
type Directory struct {
	at        time.Time
	roles     map[string]string           // user|workspace -> role
	notes     map[string][]seedNoteAccess // workspace -> notes
	overrides map[string]map[string]bool  // user -> note -> granted
}

type seedNoteAccess struct {
	id      string
	trashed bool
}

type membership struct {
	WorkspaceID string     `json:"workspace_id"`
	UserID      string     `json:"user_id"`
	Role        string     `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	RemovedAt   *time.Time `json:"removed_at"`
}

type override struct {
	NoteID    string    `json:"note_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type noteRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Trashed     bool   `json:"trashed"`
}

// NewDirectory reads memberships, acl and notes from dir.
func NewDirectory(dir string) (*Directory, error) {
	manifest, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	if err := manifest.Verify(dir, "memberships.jsonl", "acl.jsonl", "notes.jsonl"); err != nil {
		return nil, err
	}
	members, err := ReadJSONL[membership](filepath.Join(dir, "memberships.jsonl"))
	if err != nil {
		return nil, err
	}
	acl, err := ReadJSONL[override](filepath.Join(dir, "acl.jsonl"))
	if err != nil {
		return nil, err
	}
	notes, err := ReadJSONL[noteRecord](filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		return nil, err
	}
	d := &Directory{at: manifest.ReferenceTime, roles: map[string]string{}, notes: map[string][]seedNoteAccess{}, overrides: map[string]map[string]bool{}}
	for _, m := range members {
		if !m.JoinedAt.After(d.at) && (m.RemovedAt == nil || m.RemovedAt.After(d.at)) {
			d.roles[m.UserID+"|"+m.WorkspaceID] = m.Role
		}
	}
	for _, o := range acl {
		if !o.CreatedAt.After(d.at) {
			if d.overrides[o.UserID] == nil {
				d.overrides[o.UserID] = map[string]bool{}
			}
			d.overrides[o.UserID][o.NoteID] = true
		}
	}
	for _, n := range notes {
		d.notes[n.WorkspaceID] = append(d.notes[n.WorkspaceID], seedNoteAccess{id: n.ID, trashed: n.Trashed})
	}
	return d, nil
}

// ListReadableNotes implements app.AccessDirectory.
func (d *Directory) ListReadableNotes(_ context.Context, subject auth.Subject, workspace string) ([]string, error) {
	role, member := d.roles[subject.UserID+"|"+workspace]
	if !member {
		return nil, &app.NotFoundError{ResourceType: "skein.v1.Workspace", Name: workspace}
	}
	var out []string
	for _, n := range d.notes[workspace] {
		var ok bool
		switch role {
		case "owner", "admin":
			ok = true
		case "member":
			ok = !n.trashed
		default:
			ok = !n.trashed && d.overrides[subject.UserID][n.id]
		}
		if ok {
			out = append(out, n.id)
		}
	}
	slices.Sort(out)
	return out, nil
}
