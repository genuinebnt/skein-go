//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// sharedNote makes a workspace owned by owner with the others as members,
// and a note in it that every member can already read.
func sharedNote(t *testing.T, owner *user, markdown string, members ...*user) (string, *skeinv1.Note) {
	t.Helper()
	ws := owner.createWorkspace("Shared " + randomHex(t, 3))
	for _, m := range members {
		owner.addMember(ws, m, skeinv1.WorkspaceRole_WORKSPACE_ROLE_MEMBER)
	}
	note := owner.createNote(ws, "Plan "+randomHex(t, 3), markdown)
	for _, m := range members {
		waitReadable(t, m, note.GetId())
	}
	return ws, note
}

// waitReadable waits until u can read the note (identity's projection
// follows documents.events).
func waitReadable(t *testing.T, u *user, noteID string) {
	t.Helper()
	eventually(t, longWait, u.name+" can read the note", func() (bool, string) {
		r := u.do(http.MethodGet, "/v1/notes/"+noteID, nil)
		return r.status == http.StatusOK, string(r.body)
	})
}

func TestTwoEditorsConvergeThroughConcurrentTypingAndAnOfflineSpell(t *testing.T) {
	t.Parallel()
	ada, bob := signUp(t, "ada"), signUp(t, "bob")
	_, note := sharedNote(t, ada, "Agenda", bob)

	a := newReplica(t, ada, note.GetId()).mustConnect()
	b := newReplica(t, bob, note.GetId()).mustConnect()
	defer a.disconnect()
	defer b.disconnect()
	for _, r := range []*replica{a, b} {
		eventually(t, longWait, "a replica catches up with the note", func() (bool, string) {
			return strings.Contains(r.text(), "Agenda"), r.text()
		})
	}
	converge(t, longWait, a, b)

	// Both type at once, at opposite ends, in several frames each.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 5 {
			a.appendText(fmt.Sprintf(" ada%d", i))
			time.Sleep(20 * time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 5 {
			b.insert(1, fmt.Sprintf("bob%d ", i))
			time.Sleep(20 * time.Millisecond)
		}
	}()
	wg.Wait()
	online := converge(t, longWait, a, b)
	for i := range 5 {
		for _, word := range []string{fmt.Sprintf(" ada%d", i), fmt.Sprintf("bob%d ", i)} {
			if !strings.Contains(online, word) {
				t.Errorf("converged text %q lost %q", online, word)
			}
		}
	}

	// Ada goes offline and keeps typing; Bob types meanwhile; Ada comes back.
	a.disconnect()
	a.appendText(" [written offline by ada]")
	b.insert(1, "[bob while ada was away] ")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(b.text(), "offline by ada") {
		t.Fatal("an offline edit reached the other replica")
	}
	a.mustConnect()
	final := converge(t, longWait, a, b)
	for _, piece := range []string{"[written offline by ada]", "[bob while ada was away] ", "Agenda"} {
		if !strings.Contains(final, piece) {
			t.Errorf("final text %q lost %q", final, piece)
		}
	}

	// The server holds the same text: a third replica starting empty
	// catches up to it exactly.
	c := newReplica(t, ada, note.GetId()).mustConnect()
	defer c.disconnect()
	converge(t, longWait, a, b, c)
	eventually(t, longWait, "GetNote returns the merged text", func() (bool, string) {
		md := ada.markdown(note.GetId())
		return strings.Contains(md, "written offline by ada") && strings.Contains(md, "bob while ada was away"), md
	})
}

// Lowering a member to viewer is a revocation (access went down): their
// open socket is closed with 4403 (docs/api/websocket.md). They can still
// read, so they reconnect; an edit they send then comes back as a rejected
// frame, is never applied, and they keep receiving everyone else's edits.
func TestAViewersEditIsRejectedAndNeverApplied(t *testing.T) {
	t.Parallel()
	owner, viewer := signUp(t, "owner"), signUp(t, "viewer")
	_, note := sharedNote(t, owner, "Read only for some", viewer)
	editor := newReplica(t, owner, note.GetId()).mustConnect()
	defer editor.disconnect()
	v := newReplica(t, viewer, note.GetId()).mustConnect()
	defer v.disconnect()
	converge(t, longWait, editor, v)

	owner.call(http.MethodPut, "/v1/notes/"+note.GetId()+"/roles/"+viewer.id,
		&skeinv1.SetNoteRoleRequest{Role: skeinv1.NoteRole_NOTE_ROLE_VIEWER}, nil)
	if code, reason := v.waitClosed(longWait); code != 4403 {
		t.Fatalf("demotion closed the socket with %d %q, want 4403", code, reason)
	}

	// documents drops its cached grant on the same event the gateway acted
	// on, so retry with fresh text until the edit is refused.
	attempt := 0
	eventually(t, longWait, "the viewer's edit is rejected", func() (bool, string) {
		attempt++
		if err := v.connect(); err != nil {
			return false, err.Error()
		}
		v.appendText(fmt.Sprintf(" viewer-edit-%d", attempt))
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && len(v.rejections()) == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		if len(v.rejections()) > 0 {
			return true, ""
		}
		v.disconnect()
		return false, "edit accepted while the old grant was still cached"
	})
	rejected := v.rejections()[0]
	if len(rejected.GetOps()) == 0 || !strings.Contains(rejected.GetReason(), "role") {
		t.Errorf("rejection %v, want the ops and the insufficient-role reason", rejected)
	}
	rejectedEdit := fmt.Sprintf("viewer-edit-%d", attempt)

	editor.appendText(" owner-after")
	eventually(t, longWait, "the owner's later edit reaches the viewer", func() (bool, string) {
		return strings.Contains(v.text(), "owner-after"), v.text()
	})
	if strings.Contains(editor.text(), rejectedEdit) {
		t.Errorf("the rejected edit reached another replica: %q", editor.text())
	}
	if md := owner.markdown(note.GetId()); strings.Contains(md, rejectedEdit) {
		t.Errorf("the rejected edit is in the note: %q", md)
	}
}

func TestRemovingAMemberClosesTheirOpenSyncSocket(t *testing.T) {
	t.Parallel()
	owner, member := signUp(t, "owner"), signUp(t, "member")
	ws, note := sharedNote(t, owner, "Soon private", member)
	r := newReplica(t, member, note.GetId()).mustConnect()
	defer r.disconnect()
	r.appendText(" hello")

	owner.call(http.MethodDelete, "/v1/workspaces/"+ws+"/members/"+member.id, nil, nil)
	code, reason := r.waitClosed(longWait)
	if code != 4403 {
		t.Fatalf("socket closed with %d %q, want 4403", code, reason)
	}
	// Reconnecting is refused with the same code after the handshake
	// (browsers cannot read the HTTP status of a refused upgrade).
	eventually(t, longWait, "a reconnect is refused", func() (bool, string) {
		if err := r.connect(); err != nil {
			return false, err.Error()
		}
		code, reason := r.waitClosed(longWait)
		return code == 4404 || code == 4403, fmt.Sprintf("closed with %d %q", code, reason)
	})
}

// A tab closed right after typing: the edit was sent, then the socket
// closed normally. The edit must still land (the gateway once cancelled the
// documents stream with the socket and lost it every time).
func TestAnEditSentJustBeforeClosingTheSocketIsKept(t *testing.T) {
	t.Parallel()
	u := signUp(t, "closer")
	ws := u.createWorkspace("Close " + randomHex(t, 3))
	for i := range 3 {
		note := u.createNote(ws, fmt.Sprintf("Draft %d", i), "Start.")
		r := newReplica(t, u, note.GetId()).mustConnect()
		eventually(t, longWait, "the replica has the note", func() (bool, string) { return r.length() > 0, r.text() })
		r.appendText(" typed then closed")
		r.disconnect()
		eventually(t, longWait, "the last edit is in the note", func() (bool, string) {
			md := u.markdown(note.GetId())
			return strings.Contains(md, "typed then closed"), md
		})
	}
}
