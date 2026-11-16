// Package domain holds identity's entities and rules: users, workspaces,
// memberships, per-note role overrides, share links, sessions and the
// access-control policy (ADR-0008). It is pure: no I/O, no goroutines, no
// clocks (callers pass "now"), and no dependence on map iteration order.
//
// # Access policy
//
// Decide is the single authorization function. It must agree exactly with
// the policy the seed data's ground truth was generated with
// (tools/seed/internal/policy, docs/seed-data.md "Permissions"), so every
// row of testdata/seed/small/truth/permissions/matrix.jsonl is a test case
// here, rule string included. In order:
//
//  1. Not a member of the note's workspace at time t (never joined, or
//     removed at or before t): no access. Overrides of a removed member are
//     void.
//  2. Owner or admin: editor on every note; overrides are ignored.
//  3. Member: editor, unless a per-note override created at or before t
//     says otherwise. Overrides can lower a member, never raise one, because
//     editor is already the highest note role.
//  4. Guest: no access unless an override grants a role.
//  5. A share link grants its role on its own note while
//     created_at <= t < expires_at and it is not revoked at or before t.
//     Its holder can only read, comment or edit, never manage.
//  6. From trashed_at, only owners and admins can read or delete the note.
//  7. READ needs viewer, COMMENT commenter, EDIT editor; DELETE needs owner
//     or admin, or the note's creator while an editor; SHARE needs editor
//     and at least member. Workspace actions: NOTE_CREATE and AGENT_RUN need
//     member, MANAGE_MEMBERS admin, MANAGE_SETTINGS owner. An agent run is
//     decided exactly as its user (ADR-0009).
//
// One deliberate addition: share links never grant editor. New links are
// refused with that role (NewShareLinkExpiry), and Decide clamps any stored
// editor link to commenter, so data loaded from elsewhere cannot widen a
// link. The seed's only editor links are revoked before the reference time,
// so the clamp never changes a matrix row.
//
// Every decision carries the rule that decided it ("workspace_role:member",
// "share_link:expired", ...), spelled as in the seed matrix, so a mismatch
// names the rule the service disagrees with.
//
// # Facts
//
// Decide reads a Facts value: the notes, workspaces, memberships, overrides
// and share links that a batch of checks can touch, keyed for lookup only.
// The app layer loads exactly those rows; Decide never sees anything else,
// which keeps it a total function of its inputs and trivially testable.
//
// # Other rules here
//
//   - Membership changes (CheckRoleChange, CheckRemoval): only admins and
//     owners manage members, only an owner grants or takes away owner, and a
//     workspace always keeps at least one owner.
//   - Refresh token rotation (DecideRefresh): a token can be exchanged once;
//     presenting an already-rotated token means it was copied, so the whole
//     session (every token descended from the same sign-in) is revoked.
//   - One-time tokens for email verification and password reset
//     (DecideRedeem): single use, short expiry, bound to one purpose.
//
// # Porting notes
//
// Sum types (WorkspaceRole, NoteRole, Action, SubjectKind, RefreshOutcome)
// are integer kinds switched exhaustively; in Rust they become enums. IDs
// are string newtypes holding canonical lowercase UUIDs; in Rust,
// `struct UserId(uuid::Uuid)`. Sentinel errors become one thiserror enum.
// Optional times are *time.Time here and Option<DateTime<Utc>> there.
package domain
