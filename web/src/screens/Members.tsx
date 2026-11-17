/**
 * Members admin (Members board): the workspace's people and roles
 * (ListMembers), changing a role (SetWorkspaceRole, which also adds a person
 * by user ID) and removing someone (RemoveMember). Sign-in method, activity,
 * agent use and the workspace policies on the board need admin routes the
 * API does not have yet; they are shown, marked, and not editable.
 */

import { useMemo, useState } from "react";
import { describeError } from "../api/errors";
import type { WorkspaceRole } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Avatar, Pending, SearchIcon, WorkspaceRoleBadge } from "../components/ui";
import { useAsync } from "../lib/hooks";

const ROLES: WorkspaceRole[] = ["WORKSPACE_ROLE_OWNER", "WORKSPACE_ROLE_ADMIN", "WORKSPACE_ROLE_MEMBER", "WORKSPACE_ROLE_GUEST"];
const name = (r: WorkspaceRole | undefined) => (r ?? "").replace("WORKSPACE_ROLE_", "").toLowerCase();

export function Members() {
  const { api, session } = useServices();
  const ws = useWorkspace();
  const ui = useUi();
  const [filter, setFilter] = useState<WorkspaceRole | "all">("all");
  const [query, setQuery] = useState("");
  const [addId, setAddId] = useState("");
  const [addRole, setAddRole] = useState<WorkspaceRole>("WORKSPACE_ROLE_MEMBER");
  const members = useAsync(() => (ws.workspace ? api.listAll("IdentityService_ListMembers", { workspaceId: ws.workspace.id }, (r) => r.members) : Promise.resolve([])), [ws.workspace?.id]);
  const list = useMemo(() => members.data ?? [], [members.data]);
  const me = session.user?.id;
  const myRole = list.find((m) => m.user?.id === me)?.role;
  const admin = myRole === "WORKSPACE_ROLE_OWNER" || myRole === "WORKSPACE_ROLE_ADMIN";
  const counts = useMemo(() => Object.fromEntries(ROLES.map((r) => [r, list.filter((m) => m.role === r).length])), [list]);
  const shown = list.filter((m) => (filter === "all" || m.role === filter) && (!query || `${m.user?.displayName} ${m.user?.email}`.toLowerCase().includes(query.toLowerCase())));

  async function setRole(userId: string, role: WorkspaceRole) {
    if (!ws.workspace) return;
    try {
      await api.call("IdentityService_SetWorkspaceRole", { workspaceId: ws.workspace.id, userId }, { role });
      members.reload();
      ui.toast("Role updated.", "ok");
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  async function remove(userId: string, who: string) {
    if (!ws.workspace || !window.confirm(`Remove ${who} from ${ws.workspace.name}? Their open notes close and their share links stop working.`)) return;
    try {
      await api.call("IdentityService_RemoveMember", { workspaceId: ws.workspace.id, userId });
      members.reload();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  return (
    <div className="app">
      <Header area="admin" here="Members" />
      <main id="main" className="ly-page">
        <div className="ly-head">
          <div className="ly-ttl">
            <div className="ly-crumb" style={{ "--c": "var(--vio)" } as React.CSSProperties}>
              <span>Workspace</span>
              <i>/</i>
              <span>Members</span>
            </div>
            <h1 className="ly-h1">
              {list.length} people in {ws.workspace?.name}.
            </h1>
          </div>
          <div className="ly-tiles">
            <div className="ly-tl">
              <em>Owner</em>
              <strong>{counts.WORKSPACE_ROLE_OWNER}</strong>
            </div>
            <div className="ly-tl">
              <em>Admins</em>
              <strong>{counts.WORKSPACE_ROLE_ADMIN}</strong>
            </div>
            <div className="ly-tl">
              <em>Members</em>
              <strong>{counts.WORKSPACE_ROLE_MEMBER}</strong>
            </div>
            <div className="ly-tl hot" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
              <em>Guests</em>
              <strong>{counts.WORKSPACE_ROLE_GUEST}</strong>
            </div>
          </div>
        </div>
        <div className="ly-lay tworight">
          <div className="ly-cp">
            <div className="kbar">
              <label className="cm-find" style={{ flex: 1, minWidth: 220 }}>
                <SearchIcon />
                <input type="search" placeholder="Find a person…" aria-label="Find a person" value={query} onChange={(e) => setQuery(e.target.value)} />
              </label>
              <div className="seg sm" role="group" aria-label="Filter by role">
                <button type="button" className={filter === "all" ? "on" : ""} aria-pressed={filter === "all"} onClick={() => setFilter("all")}>
                  All {list.length}
                </button>
                {ROLES.map((r) => (
                  <button key={r} type="button" className={filter === r ? "on" : ""} aria-pressed={filter === r} onClick={() => setFilter(r)}>
                    {name(r)} {counts[r]}
                  </button>
                ))}
              </div>
            </div>
            {admin && (
              <form
                className="kbar"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (addId.trim()) void setRole(addId.trim(), addRole).then(() => setAddId(""));
                }}
              >
                <label htmlFor="add-id" className="lab">
                  Add by user ID
                </label>
                <input id="add-id" className="inp mono" style={{ flex: 1, minWidth: 220, height: 34 }} value={addId} onChange={(e) => setAddId(e.target.value)} placeholder="01936f3e-…" />
                <select className="selbtn" aria-label="Role" value={addRole} onChange={(e) => setAddRole(e.target.value as WorkspaceRole)} style={{ width: 130 }}>
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {name(r)}
                    </option>
                  ))}
                </select>
                <button className="btn sm go" type="submit" style={{ height: 34 }}>
                  Add
                </button>
                <span className="cap">
                  Inviting by email needs an invite RPC <Pending />
                </span>
              </form>
            )}
            <div style={{ border: "1px solid var(--line2)", borderRadius: 12, background: "var(--panel)", overflowX: "auto", overscrollBehavior: "contain" }}>
              <table className="sk-tbl" style={{ fontSize: 13, minWidth: 720 }}>
                <thead>
                  <tr>
                    <th style={{ paddingLeft: 16 }}>Person</th>
                    <th>Workspace role</th>
                    <th>
                      Signs in with <Pending>admin API</Pending>
                    </th>
                    <th>Last active</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {members.loading && (
                    <tr>
                      <td colSpan={5}>
                        <span className="sk" />
                      </td>
                    </tr>
                  )}
                  {shown.map((m) => {
                    const id = m.user?.id ?? "";
                    const self = id === me;
                    return (
                      <tr key={id}>
                        <td style={{ paddingLeft: 16 }}>
                          <div style={{ display: "flex", gap: 10, alignItems: "center" }}>
                            <Avatar id={id} name={m.user?.displayName} />
                            <span style={{ display: "grid", lineHeight: 1.35 }}>
                              <b>
                                {m.user?.displayName}
                                {self ? " (you)" : ""}
                              </b>
                              <span className="cap">{m.user?.email}</span>
                            </span>
                          </div>
                        </td>
                        <td>
                          {admin && !self ? (
                            <select className="selbtn" style={{ height: 28, width: 120 }} aria-label={`Workspace role for ${m.user?.displayName}`} value={m.role} onChange={(e) => void setRole(id, e.target.value as WorkspaceRole)}>
                              {ROLES.map((r) => (
                                <option key={r} value={r} disabled={r === "WORKSPACE_ROLE_OWNER" && myRole !== "WORKSPACE_ROLE_OWNER"}>
                                  {name(r)}
                                </option>
                              ))}
                            </select>
                          ) : (
                            <WorkspaceRoleBadge role={m.role} />
                          )}
                        </td>
                        <td>
                          <span className="cap">—</span>
                        </td>
                        <td>
                          <span className="cap">joined {m.user?.createdAt ? new Date(m.user.createdAt).toLocaleDateString() : "—"}</span>
                        </td>
                        <td>
                          {admin && !self && (
                            <button className="btn xs ghost" type="button" aria-label={`Remove ${m.user?.displayName}`} onClick={() => void remove(id, m.user?.displayName ?? "this person")}>
                              Remove
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <span className="cap">Removing someone ends their open connections to this workspace's notes and their share links stop working. Nothing is deleted.</span>
          </div>
          <aside className="ly-rp">
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>Workspace roles</span>
              </h5>
              <div className="ly-kv" style={{ fontSize: 12.5 }}>
                {(
                  [
                    ["WORKSPACE_ROLE_OWNER", "billing, delete workspace"],
                    ["WORKSPACE_ROLE_ADMIN", "members, links, audit"],
                    ["WORKSPACE_ROLE_MEMBER", "create notes, see shared ones"],
                    ["WORKSPACE_ROLE_GUEST", "only notes shared with them"],
                  ] as const
                ).map(([r, what]) => (
                  <div key={r}>
                    <WorkspaceRoleBadge role={r} />
                    <b style={{ fontWeight: 500, fontFamily: "var(--sans)", color: "var(--mut)" }}>{what}</b>
                  </div>
                ))}
              </div>
              <p>A note's own role (Editor, Commenter, Viewer) is set in its share dialog and never reaches past that note.</p>
            </section>
            {[
              ["Agent", ["Members can use the agent", "Guests can use the agent", "Ask before deleting or re-sharing"]],
              ["Discovery · suggested links", ["Close in meaning at cosine ≥ 0.70", "Not connected counts as far"]],
              ["Sign-in", ["Email + password", "Google", "GitHub"]],
            ].map(([head, rows]) => (
              <section key={head as string} className="ly-pn">
                <h5 className="ly-pnh">
                  <span>{head}</span>
                  <Pending>policy API</Pending>
                </h5>
                {(rows as string[]).map((r) => (
                  <span key={r} className="sw dis">
                    <span className="tr0" />
                    {r}
                  </span>
                ))}
              </section>
            ))}
          </aside>
        </div>
      </main>
    </div>
  );
}
