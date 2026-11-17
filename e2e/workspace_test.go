//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

func TestWorkspaceMembersGetTheirRolesPermissions(t *testing.T) {
	t.Parallel()
	owner, member, guest := signUp(t, "owner"), signUp(t, "member"), signUp(t, "guest")
	ws := owner.createWorkspace("Roles " + randomHex(t, 3))
	owner.addMember(ws, member, skeinv1.WorkspaceRole_WORKSPACE_ROLE_MEMBER)
	owner.addMember(ws, guest, skeinv1.WorkspaceRole_WORKSPACE_ROLE_GUEST)

	var members skeinv1.ListMembersResponse
	owner.call(http.MethodGet, "/v1/workspaces/"+ws+"/members", nil, &members)
	roles := map[string]skeinv1.WorkspaceRole{}
	for _, m := range members.GetMembers() {
		roles[m.GetUser().GetId()] = m.GetRole()
	}
	want := map[string]skeinv1.WorkspaceRole{
		owner.id:  skeinv1.WorkspaceRole_WORKSPACE_ROLE_OWNER,
		member.id: skeinv1.WorkspaceRole_WORKSPACE_ROLE_MEMBER,
		guest.id:  skeinv1.WorkspaceRole_WORKSPACE_ROLE_GUEST,
	}
	for id, role := range want {
		if roles[id] != role {
			t.Errorf("member %s has role %v, want %v (all: %v)", id, roles[id], role, roles)
		}
	}

	// A member creates and edits notes; a guest sees nothing until granted.
	note := member.createNote(ws, "Member's plan", "First line.")
	// Documents registers new notes with identity synchronously, so the rest
	// of the workspace can open one immediately, not after the event.
	owner.call(http.MethodGet, "/v1/notes/"+note.GetId(), nil, nil)
	expectError(t, "guest reads an ungranted note", guest.do(http.MethodGet, "/v1/notes/"+note.GetId(), nil),
		http.StatusNotFound, int32(codes.NotFound))
	expectError(t, "guest creates a note",
		guest.do(http.MethodPost, "/v1/workspaces/"+ws+"/notes", &skeinv1.CreateNoteRequest{Title: "Nope"}),
		http.StatusForbidden, int32(codes.PermissionDenied))
	expectError(t, "member manages members",
		member.do(http.MethodPut, "/v1/workspaces/"+ws+"/members/"+guest.id, &skeinv1.SetWorkspaceRoleRequest{Role: skeinv1.WorkspaceRole_WORKSPACE_ROLE_ADMIN}),
		http.StatusForbidden, int32(codes.PermissionDenied))

	owner.call(http.MethodPut, "/v1/notes/"+note.GetId()+"/roles/"+guest.id,
		&skeinv1.SetNoteRoleRequest{Role: skeinv1.NoteRole_NOTE_ROLE_VIEWER}, nil)
	eventually(t, longWait, "the guest's viewer grant takes effect", func() (bool, string) {
		r := guest.do(http.MethodGet, "/v1/notes/"+note.GetId(), nil)
		return r.status == http.StatusOK, string(r.body)
	})
}
