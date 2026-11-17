/**
 * The share dialog (Share board): give people a role on this note
 * (SetNoteRole), see who has access, and make or revoke share links
 * (CreateShareLink, RevokeShareLink). Listing who has access and which links
 * exist needs routes the API lacks (proposed: GET /v1/notes/{id}/roles and
 * /share-links); without them the dialog shows workspace members and the
 * links made in this session. Inviting by email needs an invite RPC.
 */

import { useEffect, useRef, useState } from "react";
import { describeError } from "../api/errors";
import type { NoteRole, User, WorkspaceRole } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Avatar, NoteRoleBadge, Pending, WorkspaceRoleBadge } from "../components/ui";
import { useAsync } from "../lib/hooks";

type Access = { user: User; workspaceRole: WorkspaceRole; noteRole: NoteRole | null; override: boolean; owner: boolean };
type ShareLink = { id: string; role: NoteRole; expiresAt: string | null; createdAt: string; token?: string };

const ROLES: NoteRole[] = ["NOTE_ROLE_VIEWER", "NOTE_ROLE_COMMENTER", "NOTE_ROLE_EDITOR"];
const EXPIRY = [
  ["1 day", 1],
  ["7 days", 7],
  ["30 days", 30],
  ["never", 0],
] as const;

export function ShareDialog({ noteId, title, onClose }: { noteId: string; title: string; onClose: () => void }) {
  const { api, session } = useServices();
  const myId = session.user?.id;
  const ws = useWorkspace();
  const ui = useUi();
  const dialog = useRef<HTMLDivElement>(null);
  const [invitee, setInvitee] = useState("");
  const [inviteRole, setInviteRole] = useState<NoteRole>("NOTE_ROLE_COMMENTER");
  const [linkRole, setLinkRole] = useState<NoteRole>("NOTE_ROLE_VIEWER");
  const [expiry, setExpiry] = useState(7);
  const [created, setCreated] = useState<ShareLink[]>([]);
  const [overrides, setOverrides] = useState<Record<string, NoteRole | null>>({});

  const access = useAsync(async () => {
    try {
      return { listed: true, rows: (await api.proposed<{ access?: Access[] }>("GET", `/v1/notes/${noteId}/roles`)).access ?? [] };
    } catch {
      // Not in the API yet: fall back to the workspace's members and their workspace-derived role.
      return { listed: false, rows: ws.members.map((m) => ({ user: m.user!, workspaceRole: m.role!, noteRole: m.role === "WORKSPACE_ROLE_GUEST" ? null : ("NOTE_ROLE_EDITOR" as NoteRole), override: false, owner: false })) };
    }
  }, [noteId, ws.members.length]);
  const links = useAsync(async () => {
    try {
      return { listed: true, rows: (await api.proposed<{ links?: ShareLink[] }>("GET", `/v1/notes/${noteId}/share-links`)).links ?? [] };
    } catch {
      return { listed: false, rows: [] as ShareLink[] };
    }
  }, [noteId]);

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    dialog.current?.querySelector<HTMLElement>("input, select, button")?.focus();
    // Escape closes; Tab stays inside the dialog (aria-modal).
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") return onClose();
      if (e.key !== "Tab") return;
      const focusable = [...(dialog.current?.querySelectorAll<HTMLElement>("button:not([disabled]), input, select, a[href]") ?? [])];
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last?.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      previous?.focus();
    };
  }, [onClose]);

  async function setRole(userId: string, role: NoteRole | null) {
    try {
      await api.call("IdentityService_SetNoteRole", { noteId, userId }, role ? { role } : {});
      setOverrides((o) => ({ ...o, [userId]: role }));
      access.reload();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  async function createLink() {
    try {
      const expiresAt = expiry ? new Date(Date.now() + expiry * 86_400_000).toISOString() : undefined;
      const res = await api.call("IdentityService_CreateShareLink", { noteId }, { role: linkRole, expiresAt }, { idempotencyKey: true });
      setCreated((c) => [{ id: res.linkId!, role: linkRole, expiresAt: expiresAt ?? null, createdAt: new Date().toISOString(), token: res.token }, ...c]);
      links.reload();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  async function revoke(id: string) {
    try {
      await api.call("IdentityService_RevokeShareLink", { linkId: id });
      setCreated((c) => c.filter((l) => l.id !== id));
      links.reload();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  const rows = (access.data?.rows ?? []).map((r) => (r.user.id && r.user.id in overrides ? { ...r, noteRole: overrides[r.user.id] ?? r.noteRole, override: true } : r));
  const linkRows = [...created, ...(links.data?.rows ?? []).filter((l) => !created.some((c) => c.id === l.id))];
  const candidates = ws.members.filter((m) => !rows.some((r) => r.user.id === m.user?.id && r.override));

  return (
    <>
      <div className="modal-scrim" onClick={onClose} aria-hidden="true" />
      <div
        ref={dialog}
        className="modal share"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sh-t"
        style={{ left: "50%", top: 24, translate: "-50% 0", width: "min(1000px, 94vw)", display: "grid", gridTemplateColumns: "minmax(0, 1.25fr) minmax(0, 1fr)" }}
      >
        <div style={{ padding: "20px 22px", display: "grid", gap: 16, alignContent: "start", borderRight: "1px solid var(--line2)" }}>
          <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <h2 id="sh-t" style={{ fontSize: 18, fontWeight: 700 }}>
              Share “{title}”
            </h2>
            <span style={{ flex: 1 }} />
            <button className="hd-ib" type="button" aria-label="Close" style={{ width: 30, height: 30 }} onClick={onClose}>
              ×
            </button>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "minmax(0, 1fr) 140px auto", gap: 8 }}>
            <label htmlFor="inv" className="sr">
              Person to add
            </label>
            <select id="inv" className="selbtn" value={invitee} onChange={(e) => setInvitee(e.target.value)} aria-describedby="inv-h">
              <option value="">Choose a workspace member…</option>
              {candidates.map((m) => (
                <option key={m.user?.id} value={m.user?.id}>
                  {m.user?.displayName} · {m.user?.email}
                </option>
              ))}
            </select>
            <select className="selbtn" aria-label="Role for the person" value={inviteRole} onChange={(e) => setInviteRole(e.target.value as NoteRole)}>
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {r.replace("NOTE_ROLE_", "").toLowerCase()}
                </option>
              ))}
            </select>
            <button className="btn sm go" type="button" style={{ height: 36 }} disabled={!invitee} onClick={() => void setRole(invitee, inviteRole).then(() => setInvitee(""))}>
              Invite
            </button>
          </div>
          <span id="inv-h" className="help">
            A note role never reaches past this note. Inviting someone by email who isn't in the workspace needs an invite RPC <Pending />
          </span>
          <div>
            <div className="lab" style={{ marginBottom: 6, display: "flex", gap: 8, alignItems: "center" }}>
              People with access · {rows.filter((r) => r.noteRole).length}
              {!access.data?.listed && <Pending>derived</Pending>}
            </div>
            {rows
              .filter((r) => r.noteRole)
              .map((r) => (
                <div key={r.user.id} style={{ display: "grid", gridTemplateColumns: "34px minmax(0, 1fr) auto auto", gap: 10, alignItems: "center", padding: "8px 0", borderTop: "1px solid var(--line2)" }}>
                  <Avatar id={r.user.id} name={r.user.displayName} size="md" />
                  <span style={{ display: "grid", minWidth: 0, lineHeight: 1.35 }}>
                    <span style={{ fontWeight: 500 }}>
                      {r.user.displayName}
                      {r.user.id === myId ? " (you)" : ""}
                    </span>
                    <span className="cap" style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
                      {r.user.email}
                      {r.owner ? " · created this note" : r.override ? " · role set on this note" : " · from the workspace"}
                    </span>
                  </span>
                  <WorkspaceRoleBadge role={r.workspaceRole} />
                  <select className="selbtn" style={{ height: 30, minWidth: 120 }} aria-label={`Role for ${r.user.displayName}`} value={r.noteRole ?? ""} onChange={(e) => void setRole(r.user.id!, (e.target.value || null) as NoteRole | null)}>
                    {ROLES.map((role) => (
                      <option key={role} value={role}>
                        {role.replace("NOTE_ROLE_", "").toLowerCase()}
                      </option>
                    ))}
                    {r.override && <option value="">workspace default</option>}
                  </select>
                </div>
              ))}
          </div>
        </div>
        <div style={{ padding: "20px 22px", display: "grid", gap: 16, alignContent: "start", background: "color-mix(in oklab, var(--inset) 60%, transparent)", borderRadius: "0 16px 16px 0" }}>
          <div className="lab" style={{ display: "flex", gap: 8, alignItems: "center" }}>
            Share links {!links.data?.listed && <Pending>this session only</Pending>}
          </div>
          {linkRows.map((l) => (
            <div key={l.id} className="card" style={{ padding: "12px 14px", gap: 8 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <b style={{ fontSize: 13 }}>Anyone with the link</b>
                <NoteRoleBadge role={l.role} />
                <span style={{ flex: 1 }} />
                <span className="pill c" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
                  {l.expiresAt ? `expires ${new Date(l.expiresAt).toLocaleDateString()}` : "never expires"}
                </span>
              </div>
              {l.token && <code className="m" style={{ fontSize: 11.5, color: "var(--mut)", background: "var(--bg)", border: "1px solid var(--line2)", borderRadius: 6, padding: "6px 8px", overflowWrap: "anywhere" }}>{`${location.origin}/s/${l.token}`}</code>}
              <div style={{ display: "flex", gap: 6 }}>
                <button className="btn xs" type="button" disabled={!l.token} title={l.token ? undefined : "The token is shown only when the link is made"} onClick={() => void navigator.clipboard?.writeText(`${location.origin}/s/${l.token}`)}>
                  Copy link
                </button>
                <button className="btn xs bad" type="button" onClick={() => void revoke(l.id)}>
                  Revoke
                </button>
              </div>
            </div>
          ))}
          <div className="card" style={{ padding: "12px 14px", gap: 10 }}>
            <b style={{ fontSize: 13 }}>New link</b>
            <div className="fld">
              <span className="lb">Role</span>
              <div className="seg" role="group" aria-label="Link role" style={{ width: "100%" }}>
                {(["NOTE_ROLE_VIEWER", "NOTE_ROLE_COMMENTER"] as const).map((r) => (
                  <button key={r} type="button" className={linkRole === r ? "on" : ""} aria-pressed={linkRole === r} onClick={() => setLinkRole(r)}>
                    {r === "NOTE_ROLE_VIEWER" ? "Viewer" : "Commenter"}
                  </button>
                ))}
              </div>
            </div>
            <div className="fld">
              <span className="lb">Expires</span>
              <div className="seg mono" role="group" aria-label="Link expiry" style={{ width: "100%" }}>
                {EXPIRY.map(([label, days]) => (
                  <button key={label} type="button" className={expiry === days ? "on" : ""} aria-pressed={expiry === days} onClick={() => setExpiry(days)}>
                    {label}
                  </button>
                ))}
              </div>
            </div>
            <button className="btn sm go" type="button" onClick={() => void createLink()}>
              Create link
            </button>
            <span className="cap">Links never grant Editor. Require-sign-in and agent access for link visitors need link settings in the API <Pending /></span>
          </div>
          <div className="lab">What each role can do</div>
          <table className="sk-tbl" style={{ fontSize: 12 }}>
            <thead>
              <tr>
                <th />
                <th className="n">Viewer</th>
                <th className="n">Comment.</th>
                <th className="n">Editor</th>
              </tr>
            </thead>
            <tbody>
              {[
                ["Read, search, history", "✓", "✓", "✓"],
                ["Ask the agent", "✓", "✓", "✓"],
                ["Comment, @mention", "—", "✓", "✓"],
                ["Edit, accept agent edits", "—", "—", "✓"],
                ["Share, restore versions", "—", "—", "✓"],
              ].map(([what, ...cells]) => (
                <tr key={what}>
                  <td>{what}</td>
                  {cells.map((c, i) => (
                    <td key={i} className="n">
                      {c}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <span className="cap">Deleting the note needs its owner or a workspace admin. The agent never has more access than the person using it.</span>
          <div style={{ display: "flex" }}>
            <span style={{ flex: 1 }} />
            <button className="btn sm go" type="button" onClick={onClose}>
              Done
            </button>
          </div>
        </div>
      </div>
    </>
  );
}
