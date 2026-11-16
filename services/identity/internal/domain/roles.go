package domain

// WorkspaceRole is a role in a workspace, ordered owner > admin > member >
// guest. Values equal the skein.v1.WorkspaceRole enum; 0 means "not a
// member" here and UNSPECIFIED on the wire.
type WorkspaceRole int32

// Workspace roles.
const (
	WorkspaceRoleNone   WorkspaceRole = 0
	WorkspaceRoleGuest  WorkspaceRole = 1
	WorkspaceRoleMember WorkspaceRole = 2
	WorkspaceRoleAdmin  WorkspaceRole = 3
	WorkspaceRoleOwner  WorkspaceRole = 4
)

// Valid reports whether r is one of the four assignable roles.
func (r WorkspaceRole) Valid() bool { return r >= WorkspaceRoleGuest && r <= WorkspaceRoleOwner }

// String returns the name used in the seed files and decision rules.
func (r WorkspaceRole) String() string {
	switch r {
	case WorkspaceRoleGuest:
		return "guest"
	case WorkspaceRoleMember:
		return "member"
	case WorkspaceRoleAdmin:
		return "admin"
	case WorkspaceRoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// ParseWorkspaceRole is the inverse of String; unknown names give
// WorkspaceRoleNone.
func ParseWorkspaceRole(s string) WorkspaceRole {
	for _, r := range []WorkspaceRole{WorkspaceRoleGuest, WorkspaceRoleMember, WorkspaceRoleAdmin, WorkspaceRoleOwner} {
		if r.String() == s {
			return r
		}
	}
	return WorkspaceRoleNone
}

// NoteRole is a role on one note, ordered editor > commenter > viewer.
// Values equal skein.v1.NoteRole; 0 means no access.
type NoteRole int32

// Note roles.
const (
	NoteRoleNone      NoteRole = 0
	NoteRoleViewer    NoteRole = 1
	NoteRoleCommenter NoteRole = 2
	NoteRoleEditor    NoteRole = 3
)

// Valid reports whether r is one of the three assignable note roles.
func (r NoteRole) Valid() bool { return r >= NoteRoleViewer && r <= NoteRoleEditor }

// String returns the name used in the seed files and decision rules.
func (r NoteRole) String() string {
	switch r {
	case NoteRoleViewer:
		return "viewer"
	case NoteRoleCommenter:
		return "commenter"
	case NoteRoleEditor:
		return "editor"
	default:
		return "none"
	}
}

// ParseNoteRole is the inverse of String; unknown names give NoteRoleNone.
func ParseNoteRole(s string) NoteRole {
	for _, r := range []NoteRole{NoteRoleViewer, NoteRoleCommenter, NoteRoleEditor} {
		if r.String() == s {
			return r
		}
	}
	return NoteRoleNone
}

// Action is something a subject can attempt. Values equal skein.v1.Action.
type Action int32

// Actions.
const (
	ActionUnspecified    Action = 0
	ActionNoteRead       Action = 1
	ActionNoteComment    Action = 2
	ActionNoteEdit       Action = 3
	ActionNoteCreate     Action = 4
	ActionNoteDelete     Action = 5
	ActionNoteShare      Action = 6
	ActionManageMembers  Action = 7
	ActionManageSettings Action = 8
	ActionAgentRun       Action = 9
)

// actionNames are the proto enum value names, which the seed matrix uses.
var actionNames = [...]string{
	"ACTION_UNSPECIFIED", "ACTION_NOTE_READ", "ACTION_NOTE_COMMENT", "ACTION_NOTE_EDIT",
	"ACTION_NOTE_CREATE", "ACTION_NOTE_DELETE", "ACTION_NOTE_SHARE",
	"ACTION_WORKSPACE_MANAGE_MEMBERS", "ACTION_WORKSPACE_MANAGE_SETTINGS", "ACTION_AGENT_RUN",
}

// String returns the proto enum value name.
func (a Action) String() string {
	if a < 0 || int(a) >= len(actionNames) {
		return "ACTION_UNKNOWN"
	}
	return actionNames[a]
}

// ParseAction is the inverse of String; unknown names give
// ActionUnspecified.
func ParseAction(s string) Action {
	for i, n := range actionNames {
		if n == s {
			return Action(i)
		}
	}
	return ActionUnspecified
}

// ResourceKind says what an action's resource ID names.
type ResourceKind int

// Resource kinds.
const (
	ResourceUnknown ResourceKind = iota
	ResourceNote
	ResourceWorkspace
)

// Resource returns the kind of resource the action applies to.
func (a Action) Resource() ResourceKind {
	switch a {
	case ActionNoteRead, ActionNoteComment, ActionNoteEdit, ActionNoteDelete, ActionNoteShare:
		return ResourceNote
	case ActionNoteCreate, ActionManageMembers, ActionManageSettings, ActionAgentRun:
		return ResourceWorkspace
	default:
		return ResourceUnknown
	}
}

// AllActions lists every defined action, for exhaustive tests.
var AllActions = []Action{
	ActionNoteRead, ActionNoteComment, ActionNoteEdit, ActionNoteCreate, ActionNoteDelete,
	ActionNoteShare, ActionManageMembers, ActionManageSettings, ActionAgentRun,
}
