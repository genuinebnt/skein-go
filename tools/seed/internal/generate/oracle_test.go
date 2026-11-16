package generate_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// oraclePolicy re-implements the access rules of docs/seed-data.md from the
// raw files, independently of internal/policy.
type oraclePolicy struct {
	members   map[string][]oracleMember // ws/user
	overrides map[string]oracleOverride // note/user
	notes     map[string]note
	links     map[string]oracleLink
	ws        map[string]bool
}

type oracleMember struct {
	Role      string  `json:"role"`
	JoinedAt  string  `json:"joined_at"`
	RemovedAt *string `json:"removed_at"`
}

type oracleOverride struct {
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type oracleLink struct {
	ID        string  `json:"id"`
	NoteID    string  `json:"note_id"`
	Role      string  `json:"role"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}

func loadPolicy(t testing.TB, dir string) *oraclePolicy {
	t.Helper()
	p := &oraclePolicy{members: map[string][]oracleMember{}, overrides: map[string]oracleOverride{}, notes: map[string]note{}, links: map[string]oracleLink{}, ws: map[string]bool{}}
	type m struct {
		WorkspaceID string `json:"workspace_id"`
		UserID      string `json:"user_id"`
		oracleMember
	}
	for _, r := range lines[m](t, dir, "memberships.jsonl") {
		p.members[r.WorkspaceID+"/"+r.UserID] = append(p.members[r.WorkspaceID+"/"+r.UserID], r.oracleMember)
		p.ws[r.WorkspaceID] = true
	}
	type o struct {
		NoteID string `json:"note_id"`
		UserID string `json:"user_id"`
		oracleOverride
	}
	for _, r := range lines[o](t, dir, "acl.jsonl") {
		p.overrides[r.NoteID+"/"+r.UserID] = r.oracleOverride
	}
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		p.notes[n.ID] = n
	}
	for _, l := range lines[oracleLink](t, dir, "share_links.jsonl") {
		p.links[l.ID] = l
	}
	return p
}

var wsRank = map[string]int{"guest": 1, "member": 2, "admin": 3, "owner": 4}
var noteRank = map[string]int{"viewer": 1, "commenter": 2, "editor": 3}

func before(t testing.TB, a string, b time.Time) bool { return !mustParseTime(t, a).After(b) }

func (p *oraclePolicy) wsRole(t testing.TB, ws, user string, at time.Time) int {
	for _, m := range p.members[ws+"/"+user] {
		if before(t, m.JoinedAt, at) && (m.RemovedAt == nil || !before(t, *m.RemovedAt, at)) {
			return wsRank[m.Role]
		}
	}
	return 0
}

func (p *oraclePolicy) removedBy(ws, user string, at time.Time) bool {
	for _, m := range p.members[ws+"/"+user] {
		if m.RemovedAt != nil && !mustParseTime(nil, *m.RemovedAt).After(at) {
			return true
		}
	}
	return false
}

func (p *oraclePolicy) trashed(t testing.TB, n note, at time.Time) bool {
	return n.TrashedAt != nil && before(t, *n.TrashedAt, at)
}

// can decides a user (or agent run for the user) check.
func (p *oraclePolicy) can(user, action, resource string, at time.Time) bool {
	var t testing.TB
	if p.ws[resource] {
		r := p.wsRole(t, resource, user, at)
		need := map[string]int{"ACTION_NOTE_CREATE": 2, "ACTION_WORKSPACE_MANAGE_MEMBERS": 3, "ACTION_WORKSPACE_MANAGE_SETTINGS": 4, "ACTION_AGENT_RUN": 2}[action]
		return r > 0 && r >= need
	}
	n, ok := p.notes[resource]
	if !ok {
		return false
	}
	ws := p.wsRole(t, n.WorkspaceID, user, at)
	if ws == 0 {
		return false
	}
	if p.trashed(t, n, at) {
		return ws >= 3 && (action == "ACTION_NOTE_READ" || action == "ACTION_NOTE_DELETE")
	}
	role := 0
	switch {
	case ws >= 3:
		role = 3
	default:
		if o, ok := p.overrides[n.ID+"/"+user]; ok && before(t, o.CreatedAt, at) {
			role = noteRank[o.Role]
		} else if ws == 2 {
			role = 3
		}
	}
	switch action {
	case "ACTION_NOTE_READ":
		return role >= 1
	case "ACTION_NOTE_COMMENT":
		return role >= 2
	case "ACTION_NOTE_EDIT":
		return role == 3
	case "ACTION_NOTE_DELETE":
		return ws >= 3 || role == 3 && n.CreatedBy == user
	case "ACTION_NOTE_SHARE":
		return role == 3 && ws >= 2
	}
	return false
}

func (p *oraclePolicy) canLink(linkID, action, resource string, at time.Time) bool {
	var t testing.TB
	l, ok := p.links[linkID]
	n, isNote := p.notes[resource]
	if !ok || !isNote || l.NoteID != resource || !before(t, l.CreatedAt, at) || p.trashed(t, n, at) {
		return false
	}
	if l.RevokedAt != nil && before(t, *l.RevokedAt, at) || l.ExpiresAt != nil && before(t, *l.ExpiresAt, at) {
		return false
	}
	need := map[string]int{"ACTION_NOTE_READ": 1, "ACTION_NOTE_COMMENT": 2, "ACTION_NOTE_EDIT": 3}[action]
	return need > 0 && noteRank[l.Role] >= need
}

func TestPermissionMatrixMatchesAnIndependentEvaluator(t *testing.T) {
	dir := smallDir(t)
	pol := loadPolicy(t, dir)
	type row struct {
		Subject struct {
			UserID      *string `json:"user_id"`
			AgentRunID  *string `json:"agent_run_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string `json:"action"`
		ResourceID string `json:"resource_id"`
		At         string `json:"at"`
		Expected   string `json:"expected"`
		Rule       string `json:"rule"`
	}
	rules := map[string]int{}
	allow := 0
	rows := lines[row](t, dir, "truth/permissions/matrix.jsonl")
	for i, r := range rows {
		at := mustParseTime(t, r.At)
		var got bool
		if r.Subject.ShareLinkID != nil {
			got = pol.canLink(*r.Subject.ShareLinkID, r.Action, r.ResourceID, at)
		} else {
			got = pol.can(*r.Subject.UserID, r.Action, r.ResourceID, at)
		}
		if got != (r.Expected == "allow") {
			t.Fatalf("row %d: %s on %s by %+v: oracle says %v, truth %s (%s)", i, r.Action, r.ResourceID, r.Subject, got, r.Expected, r.Rule)
		}
		rules[strings.SplitN(r.Rule, ":", 2)[0]+":"+r.Expected]++
		if got {
			allow++
		}
	}
	for _, need := range []string{"workspace_role:allow", "workspace_role:deny", "note_override:allow", "note_override:deny", "share_link:allow",
		"share_link:deny", "membership_removed:deny", "not_member:deny", "guest_without_grant:deny"} {
		if rules[need] == 0 {
			t.Errorf("matrix has no row decided by %s; got %v", need, rules)
		}
	}
	if allow == 0 || allow == len(rows) {
		t.Fatalf("%d of %d rows allowed; the matrix must mix both", allow, len(rows))
	}
}

// --- schema validation -----------------------------------------------------

func schemaFor(rel string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(rel, ".jsonl"), ".json")
	return strings.ReplaceAll(name, "/", ".") + ".schema.json"
}

func TestEverySmallFileMatchesItsSchema(t *testing.T) {
	root := repoRoot(t)
	schemaDir := filepath.Join(root, "testdata", "seed", "schema")
	c := jsonschema.NewCompiler()
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		f, err := os.Open(filepath.Join(schemaDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if err := c.AddResource(e.Name(), doc); err != nil {
			t.Fatal(err)
		}
	}
	dir := smallDir(t)
	m := readManifest(t, dir)
	check := func(rel string, schemaName string) {
		sch, err := c.Compile(schemaName)
		if err != nil {
			t.Fatalf("compile %s: %v", schemaName, err)
		}
		f, err := os.Open(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if strings.HasSuffix(rel, ".json") {
			doc, err := jsonschema.UnmarshalJSON(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(doc); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
			return
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for n := 1; sc.Scan(); n++ {
			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(sc.Text()))
			if err != nil {
				t.Fatalf("%s line %d: %v", rel, n, err)
			}
			if err := sch.Validate(doc); err != nil {
				t.Fatalf("%s line %d: %v", rel, n, err)
			}
		}
	}
	for _, f := range m.Files {
		check(f.Path, schemaFor(f.Path))
	}
	check("manifest.json", "manifest.schema.json")
	// Every schema describes a file that exists, so the two cannot drift.
	used := map[string]bool{"common.schema.json": true, "manifest.schema.json": true}
	for _, f := range m.Files {
		used[schemaFor(f.Path)] = true
	}
	for _, e := range entries {
		if !used[e.Name()] {
			t.Errorf("schema %s describes no generated file", e.Name())
		}
	}
}

// TestSchemasRejectAMalformedRecord shows the schemas are strict enough to
// catch drift: a block with a marker in its text and an unknown key fails.
func TestSchemasRejectAMalformedRecord(t *testing.T) {
	root := repoRoot(t)
	c := jsonschema.NewCompiler()
	for _, name := range []string{"common.schema.json", "blocks.schema.json"} {
		f, err := os.Open(filepath.Join(root, "testdata", "seed", "schema", name))
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := jsonschema.UnmarshalJSON(f)
		f.Close()
		_ = c.AddResource(name, doc)
	}
	sch, err := c.Compile("blocks.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"note_id": "019cc260-0a80-72dd-9e8d-ba3c76c67531", "block_id": "4294967297:1", "position": 0, "type": "paragraph",
		"indent": 0, "checked": nil, "language": nil, "text": "hello", "author_id": "019cc260-0a80-72dd-9e8d-ba3c76c67531", "created_at": "2026-03-06T09:00:00.000Z"}
	roundTrip := func(v map[string]any) any {
		b, _ := json.Marshal(v)
		doc, _ := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
		return doc
	}
	if err := sch.Validate(roundTrip(good)); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"marker in text":   func(m map[string]any) { m["text"] = "a\u001eb" },
		"unknown key":      func(m map[string]any) { m["extra"] = 1 },
		"todo without box": func(m map[string]any) { m["type"] = "todo" },
		"bad block id":     func(m map[string]any) { m["block_id"] = "0:1" },
	} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		mutate(bad)
		if err := sch.Validate(roundTrip(bad)); err == nil {
			t.Errorf("%s: schema accepted it", name)
		}
	}
}
