//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

func TestShareLinksAreCreatedAndRevoked(t *testing.T) {
	t.Parallel()
	owner, member := signUp(t, "sharer"), signUp(t, "member")
	ws, note := sharedNote(t, owner, "Public draft", member)
	_ = ws

	var viewer, commenter skeinv1.CreateShareLinkResponse
	owner.call(http.MethodPost, "/v1/notes/"+note.GetId()+"/share-links",
		&skeinv1.CreateShareLinkRequest{Role: skeinv1.NoteRole_NOTE_ROLE_VIEWER}, &viewer)
	owner.call(http.MethodPost, "/v1/notes/"+note.GetId()+"/share-links",
		&skeinv1.CreateShareLinkRequest{Role: skeinv1.NoteRole_NOTE_ROLE_COMMENTER}, &commenter)
	if viewer.GetLinkId() == "" || viewer.GetToken() == "" || viewer.GetToken() == commenter.GetToken() {
		t.Fatalf("links %v and %v, want two distinct tokens", &viewer, &commenter)
	}
	// Share links never grant editing (services/identity/README.md).
	expectError(t, "an editor link",
		owner.do(http.MethodPost, "/v1/notes/"+note.GetId()+"/share-links", &skeinv1.CreateShareLinkRequest{Role: skeinv1.NoteRole_NOTE_ROLE_EDITOR}),
		http.StatusBadRequest, int32(codes.InvalidArgument))
	// Sharing needs editor: lower the member to viewer, then they cannot.
	owner.call(http.MethodPut, "/v1/notes/"+note.GetId()+"/roles/"+member.id,
		&skeinv1.SetNoteRoleRequest{Role: skeinv1.NoteRole_NOTE_ROLE_VIEWER}, nil)
	expectError(t, "a viewer shares",
		member.do(http.MethodPost, "/v1/notes/"+note.GetId()+"/share-links", &skeinv1.CreateShareLinkRequest{Role: skeinv1.NoteRole_NOTE_ROLE_VIEWER}),
		http.StatusForbidden, int32(codes.PermissionDenied))
	expectError(t, "a viewer revokes", member.do(http.MethodDelete, "/v1/share-links/"+viewer.GetLinkId(), nil),
		http.StatusForbidden, int32(codes.PermissionDenied))

	owner.call(http.MethodDelete, "/v1/share-links/"+viewer.GetLinkId(), nil, nil)
	again := owner.do(http.MethodDelete, "/v1/share-links/"+viewer.GetLinkId(), nil)
	if again.status != http.StatusOK && again.status != http.StatusNotFound {
		t.Errorf("revoking twice: HTTP %d %s", again.status, again.body)
	}
	owner.call(http.MethodDelete, "/v1/share-links/"+commenter.GetLinkId(), nil, nil)
}

// Not parallel: it spends the search budget of one user (burst 30 per
// user, 60 per IP) and every other test shares the IP.
func TestRateLimitingAnswers429WithTheDocumentedHeaders(t *testing.T) {
	u := signUp(t, "hammer")
	ws := u.createWorkspace("Limits " + randomHex(t, 3))
	q := "/v1/workspaces/" + ws + "/search?" + url.Values{"query": {"anything"}}.Encode()

	first := u.doOnce(http.MethodGet, q, nil)
	limit, _ := strconv.Atoi(first.header.Get("RateLimit-Limit"))
	if first.status != http.StatusOK || limit == 0 || first.header.Get("RateLimit-Remaining") == "" || first.header.Get("RateLimit-Reset") == "" {
		t.Fatalf("first search: HTTP %d, headers %v", first.status, first.header)
	}
	var limited response
	for i := range 200 {
		r := u.doOnce(http.MethodGet, q, nil)
		if r.status == http.StatusTooManyRequests {
			limited = r
			break
		}
		if r.status != http.StatusOK {
			t.Fatalf("search %d: HTTP %d %s", i, r.status, r.body)
		}
	}
	if limited.status == 0 {
		t.Fatal("200 searches in a burst were never limited")
	}
	if wait, err := strconv.Atoi(limited.header.Get("Retry-After")); err != nil || wait < 1 {
		t.Errorf("Retry-After %q", limited.header.Get("Retry-After"))
	}
	if limited.header.Get("RateLimit-Remaining") != "0" {
		t.Errorf("RateLimit-Remaining %q on a 429", limited.header.Get("RateLimit-Remaining"))
	}
	st := limited.rpcStatus(t)
	if st.GetCode() != int32(codes.ResourceExhausted) {
		t.Errorf("429 body %v", st)
	}
	var quota, retry bool
	for _, d := range st.GetDetails() {
		m, err := d.UnmarshalNew()
		if err != nil {
			continue
		}
		switch m.(type) {
		case *errdetails.QuotaFailure:
			quota = true
		case *errdetails.RetryInfo:
			retry = true
		}
	}
	if !quota || !retry {
		t.Errorf("429 details lack QuotaFailure (%v) or RetryInfo (%v): %s", quota, retry, limited.body)
	}
	// Other budgets are untouched: the same user can still read notes.
	u.call(http.MethodGet, "/v1/workspaces/"+ws+"/notes", nil, nil)
}

func TestIdempotencyKeyReplaysTheFirstResponse(t *testing.T) {
	t.Parallel()
	u := signUp(t, "retrier")
	ws := u.createWorkspace("Retries " + randomHex(t, 3))
	key := "e2e-" + randomHex(t, 16)
	body := &skeinv1.CreateNoteRequest{Title: "Created once", Markdown: "Exactly once."}

	var first, second skeinv1.CreateNoteResponse
	r1 := u.call(http.MethodPost, "/v1/workspaces/"+ws+"/notes", body, &first, withHeader("Idempotency-Key", key))
	r2 := u.call(http.MethodPost, "/v1/workspaces/"+ws+"/notes", body, &second, withHeader("Idempotency-Key", key))
	if first.GetNote().GetId() != second.GetNote().GetId() {
		t.Fatalf("the retry created a second note: %s and %s", first.GetNote().GetId(), second.GetNote().GetId())
	}
	if r1.header.Get("Idempotent-Replayed") != "" || r2.header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("Idempotent-Replayed: first %q, retry %q", r1.header.Get("Idempotent-Replayed"), r2.header.Get("Idempotent-Replayed"))
	}
	var list skeinv1.ListNotesResponse
	u.call(http.MethodGet, "/v1/workspaces/"+ws+"/notes", nil, &list)
	if len(list.GetNotes()) != 1 {
		t.Errorf("workspace holds %d notes, want 1", len(list.GetNotes()))
	}
	other := u.do(http.MethodPost, "/v1/workspaces/"+ws+"/notes",
		&skeinv1.CreateNoteRequest{Title: "Something else"}, withHeader("Idempotency-Key", key))
	expectError(t, "the same key with another body", other, http.StatusBadRequest, int32(codes.InvalidArgument))
	// Another user's identical key is their own.
	v := signUp(t, "other")
	vws := v.createWorkspace("Theirs " + randomHex(t, 3))
	var theirs skeinv1.CreateNoteResponse
	v.call(http.MethodPost, "/v1/workspaces/"+vws+"/notes", body, &theirs, withHeader("Idempotency-Key", key))
	if theirs.GetNote().GetId() == first.GetNote().GetId() {
		t.Error("an idempotency key leaked across users")
	}
}

func TestVersionHistoryDiffAndUndo(t *testing.T) {
	t.Parallel()
	u := signUp(t, "historian")
	ws := u.createWorkspace("History " + randomHex(t, 3))
	note := u.createNote(ws, "Changelog", "Version one.")
	r := newReplica(t, u, note.GetId()).mustConnect()
	defer r.disconnect()
	eventually(t, longWait, "the replica has the note", func() (bool, string) {
		return strings.Contains(r.text(), "Version one."), r.text()
	})

	r.appendText(" First edit.")
	eventually(t, longWait, "the first edit is stored", func() (bool, string) {
		md := u.markdown(note.GetId())
		return strings.Contains(md, "First edit."), md
	})
	before := versionProto(r.version())
	// Edits more than UndoWindow (2 s) apart are separate undo groups.
	time.Sleep(2500 * time.Millisecond)
	r.appendText(" Second edit.")
	eventually(t, longWait, "the second edit is stored", func() (bool, string) {
		md := u.markdown(note.GetId())
		return strings.Contains(md, "Second edit."), md
	})
	after := versionProto(r.version())

	var versions skeinv1.ListVersionsResponse
	u.call(http.MethodGet, "/v1/notes/"+note.GetId()+"/versions", nil, &versions)
	if len(versions.GetVersions()) == 0 {
		t.Fatal("no versions listed")
	}
	authored := false
	for _, v := range versions.GetVersions() {
		for _, a := range v.GetAuthorIds() {
			authored = authored || a == u.id
		}
	}
	if !authored {
		t.Errorf("no version names the editor: %v", versions.GetVersions())
	}

	var then skeinv1.GetTextAtResponse
	u.call(http.MethodPost, "/v1/notes/"+note.GetId()+":textAt", &skeinv1.GetTextAtRequest{Version: before}, &then)
	if !strings.Contains(then.GetMarkdown(), "First edit.") || strings.Contains(then.GetMarkdown(), "Second edit.") {
		t.Errorf("text at the earlier version %q", then.GetMarkdown())
	}
	var diff skeinv1.DiffResponse
	u.call(http.MethodPost, "/v1/notes/"+note.GetId()+":diff", &skeinv1.DiffRequest{From: before, To: after}, &diff)
	var inserted strings.Builder
	for _, h := range diff.GetHunks() {
		if h.GetKind() == skeinv1.DiffHunk_KIND_INSERT {
			inserted.WriteString(h.GetText())
		}
		if h.GetKind() == skeinv1.DiffHunk_KIND_DELETE {
			t.Errorf("diff deletes %q", h.GetText())
		}
	}
	if strings.TrimSpace(inserted.String()) != "Second edit." {
		t.Errorf("diff inserts %q, want the second edit", inserted.String())
	}

	// Undo takes back the replica's last group only; redo brings it back.
	var undo skeinv1.UndoResponse
	u.call(http.MethodPost, "/v1/notes/"+note.GetId()+":undo", &skeinv1.UndoRequest{ReplicaId: r.id}, &undo,
		withHeader("Idempotency-Key", "undo-"+randomHex(t, 8)))
	if len(undo.GetOps()) == 0 {
		t.Error("undo returned no operations")
	}
	eventually(t, longWait, "undo removes the second edit everywhere", func() (bool, string) {
		md := u.markdown(note.GetId())
		return !strings.Contains(md, "Second edit.") && strings.Contains(md, "First edit.") &&
			!strings.Contains(r.text(), "Second edit."), fmt.Sprintf("note %q, replica %q", md, r.text())
	})
	u.call(http.MethodPost, "/v1/notes/"+note.GetId()+":redo", &skeinv1.RedoRequest{ReplicaId: r.id}, nil,
		withHeader("Idempotency-Key", "redo-"+randomHex(t, 8)))
	eventually(t, longWait, "redo restores it", func() (bool, string) {
		md := u.markdown(note.GetId())
		return strings.Contains(md, "Second edit."), md
	})
}
