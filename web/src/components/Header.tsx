/**
 * The two-tier header every screen imports (Header board): areas, search,
 * sync, theme, inbox, new note and account on tier one; breadcrumb, the
 * area's tabs and status chips on tier two. In mock mode a "Simulate" chip
 * opens the network and access controls the demo states need.
 */

import { useEffect, useRef, useState } from "react";
import { Link, NavLink, useLocation, useNavigate } from "react-router";
import { describeError } from "../api/errors";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { useHotkey } from "../lib/hooks";
import { initials } from "../lib/format";
import { PlusIcon, SearchIcon, SyncBadge } from "./ui";

export type Area = "notes" | "discover" | "graph" | "agent" | "admin" | "inbox";

const AREAS: { id: Area; label: string; colour: string; to: string }[] = [
  { id: "notes", label: "Notes", colour: "var(--acc)", to: "/notes" },
  { id: "discover", label: "Discover", colour: "var(--grn)", to: "/discover" },
  { id: "graph", label: "Graph", colour: "var(--fnc)", to: "/graph" },
  { id: "agent", label: "Agent", colour: "var(--vio)", to: "/agent" },
];

const TABS: Record<Area, { label: string; to: string; end?: boolean }[]> = {
  notes: [
    { label: "All notes", to: "/notes", end: true },
    { label: "Drafts", to: "/notes/drafts" },
  ],
  discover: [
    { label: "Related", to: "/discover", end: true },
    { label: "Clusters", to: "/discover/clusters" },
  ],
  graph: [
    { label: "Explorer", to: "/graph", end: true },
    { label: "Reading plan", to: "/graph/reading-plan" },
  ],
  agent: [{ label: "Chats", to: "/agent", end: true }],
  admin: [{ label: "Members", to: "/admin/members" }],
  inbox: [{ label: "All", to: "/inbox", end: true }],
};

export function Header({ area, here }: { area: Area; here: string }) {
  const services = useServices();
  const { api, session } = services;
  const ws = useWorkspace();
  const ui = useUi();
  const navigate = useNavigate();
  const me = session.user;
  const colour = AREAS.find((a) => a.id === area)?.colour ?? "var(--acc)";
  const [pendingCount, setPendingCount] = useState(0);

  useHotkey("k", () => ui.openPalette(), { meta: true, allowInInputs: true });
  useHotkey("t", ws.toggleTheme);
  useHotkey("n", () => void newNote());

  useEffect(() => {
    if (ui.sync) return;
    const tick = () => void services.store.pendingCount().then(setPendingCount).catch(() => undefined);
    tick();
    const timer = setInterval(tick, 4000);
    return () => clearInterval(timer);
  }, [services.store, ui.sync]);

  async function newNote() {
    if (!ws.workspace) return;
    try {
      const res = await api.call("DocumentService_CreateNote", { workspaceId: ws.workspace.id }, { title: "Untitled" }, { idempotencyKey: true });
      if (res.note) {
        ws.upsertNote(res.note);
        navigate(`/notes/${res.note.id}?new=1`);
      }
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  const sync = ui.sync ?? (pendingCount > 0 ? { status: "offline" as const, label: "Offline", count: `${pendingCount} pending`, title: "Saved on this device; sends when you reconnect" } : { status: "synced" as const, label: services.network.onLine ? "Synced" : "Offline", count: "", title: "Every change is on the server" });

  return (
    <header className="hdr">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <div className="hd-t1">
        <div className="hd-l">
          <Link className="logo" to="/notes" aria-label="Skein home">
            <span className="mk" aria-hidden="true">
              <span style={{ background: "var(--fnc)" }} />
              <span style={{ background: "var(--vio)" }} />
              <span style={{ background: "var(--grn)" }} />
              <span style={{ boxShadow: "inset 0 0 0 1px var(--line)" }} />
            </span>
            <b>
              skein<i> · {ws.workspace?.name ?? "…"}</i>
            </b>
          </Link>
          <nav className="hd-areas" aria-label="Areas">
            {AREAS.map((a) => (
              <NavLink key={a.id} to={a.to} className={a.id === area ? "on" : ""} style={{ "--c": a.colour } as React.CSSProperties} aria-current={a.id === area ? "page" : undefined}>
                {a.label}
                {a.id === "notes" && ws.notes.length > 0 ? <small>{ws.notes.length}</small> : null}
              </NavLink>
            ))}
          </nav>
        </div>
        <button className="hd-srch" type="button" onClick={() => ui.openPalette()} aria-label="Search" aria-keyshortcuts="Meta+K Control+K">
          <SearchIcon />
          <span>Search text, meaning or code…</span>
          <kbd>⌘K</kbd>
        </button>
        <div className="hdr-r">
          <SyncBadge {...sync} />
          <span className="hd-vsep" aria-hidden="true" />
          <button className="hd-ib" type="button" title="Light or dark · T" aria-label={`Switch to ${ws.theme === "dark" ? "light" : "dark"} theme`} onClick={ws.toggleTheme}>
            <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
              <path d="M13.5 9.6A6 6 0 0 1 6.4 2.5a6 6 0 1 0 7.1 7.1Z" />
            </svg>
          </button>
          <Link className="hd-ib" to="/inbox" title="Inbox" aria-label={`Inbox, ${ws.unread} unread`}>
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M4 11.5V7a4 4 0 0 1 8 0v4.5l1 1.5H3z" />
              <path d="M6.5 14.5h3" />
            </svg>
            {ws.unread > 0 && <span className="n">{ws.unread > 99 ? "99+" : ws.unread}</span>}
          </Link>
          <button className="hd-cta go" type="button" onClick={() => void newNote()} disabled={ws.myRole === "WORKSPACE_ROLE_GUEST"}>
            <PlusIcon />
            New note<small>N</small>
          </button>
          <AccountMenu name={me?.displayName} email={me?.email} />
        </div>
      </div>
      <div className="hd-t2">
        <div className="hd-crumb">
          <span>{ws.workspace?.name}</span>
          <i>/</i>
          <b>{here}</b>
        </div>
        <nav className="hd-tabs" style={{ "--c": colour } as React.CSSProperties} aria-label={`${area} sections`}>
          {TABS[area].map((t) => (
            <NavLink key={t.to} to={t.to} end={t.end} className={({ isActive }) => (isActive ? "on" : "")}>
              {t.label}
            </NavLink>
          ))}
        </nav>
        <span className="hd-sp" style={{ flex: 1 }} />
        <div className="hd-chips" style={{ display: "flex", gap: 8 }}>
          {services.mock && <MockControls />}
          <span className="hd-chip" style={{ "--c": "var(--vio)" } as React.CSSProperties}>
            <span className="dot" />
            agent <b>ready</b>
          </span>
          <span className="hd-chip" style={{ "--c": services.mode === "mock" ? "var(--warn)" : "var(--dim)" } as React.CSSProperties}>
            <span className="dot" />
            {services.mode === "mock" ? "mock" : "self-hosted"} <b>{services.host}</b>
          </span>
        </div>
      </div>
    </header>
  );
}

function AccountMenu({ name, email }: { name?: string; email?: string }) {
  const { api, session } = useServices();
  const ws = useWorkspace();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button type="button" className="av" aria-haspopup="menu" aria-expanded={open} title={`${name ?? ""} · ${email ?? ""}`} onClick={() => setOpen((o) => !o)} onKeyDown={(e) => e.key === "Escape" && setOpen(false)}>
        {initials(name)}
      </button>
      {open && (
        <div className="ed-pop" role="menu" tabIndex={-1} style={{ right: 0, top: 42, width: 260, padding: 6, display: "grid", gap: 1 }}>
          <div style={{ padding: "8px 10px" }}>
            <b style={{ color: "var(--fg)" }}>{name}</b>
            <div className="cap">{email}</div>
          </div>
          {ws.workspaces.length > 1 &&
            ws.workspaces.map((w) => (
              <button key={w.id} role="menuitem" type="button" className={`frow${w.id === ws.workspace?.id ? " on" : ""}`} onClick={() => (ws.choose(w.id), setOpen(false), navigate("/notes"))}>
                <span>{w.name}</span>
                <span>{w.notes ?? ""}</span>
              </button>
            ))}
          <button role="menuitem" type="button" className="frow" onClick={() => (setOpen(false), navigate("/admin/members"))}>
            <span>Members</span>
            <span>admin</span>
          </button>
          <button role="menuitem" type="button" className="frow" onClick={() => void session.signOut(api).then(() => navigate("/signin"))}>
            <span>Sign out</span>
            <span />
          </button>
        </div>
      )}
    </div>
  );
}

/** Mock mode only: the network and access conditions the design's states need. */
function MockControls() {
  const { mock } = useServices();
  const ui = useUi();
  const location = useLocation();
  const [open, setOpen] = useState(false);
  const [offline, setOffline] = useState(mock!.network.simulatedOffline);
  const noteId = /^\/notes\/([^/]+)/.exec(location.pathname)?.[1];
  const me = useServices().session.user;
  if (!mock) return null;
  // Choosing an item closes the menu, as the ARIA menu pattern expects.
  const choose = (action: () => void) => () => {
    action();
    setOpen(false);
  };
  return (
    <div style={{ position: "relative" }}>
      <button type="button" className="hd-chip" style={{ "--c": "var(--warn)" } as React.CSSProperties} aria-expanded={open} aria-haspopup="menu" onClick={() => setOpen((o) => !o)}>
        <span className="dot" />
        simulate
      </button>
      {open && (
        <div className="ed-pop" role="menu" tabIndex={-1} style={{ right: 0, top: 32, width: 280, padding: 6, display: "grid", gap: 1 }} onKeyDown={(e) => e.key === "Escape" && setOpen(false)}>
          <span className="lab" style={{ padding: "6px 8px" }}>
            Mock gateway
          </span>
          <button
            role="menuitem"
            type="button"
            className="frow"
            onClick={choose(() => {
              mock.network.setOffline(!offline);
              setOffline(!offline);
            })}
          >
            <span>{offline ? "Go back online" : "Go offline"}</span>
            <span>network</span>
          </button>
          <button role="menuitem" type="button" className="frow" onClick={choose(() => (mock.expireTokens(), ui.toast("Access tokens expired; the next call refreshes.")))}>
            <span>Expire my access token</span>
            <span>4401</span>
          </button>
          <button
            role="menuitem"
            type="button"
            className="frow"
            disabled={!noteId}
            onClick={choose(() => {
              if (!noteId || !me?.id) return;
              mock.revokeEdit(noteId, me.id);
              ui.toast("Your edit access to this note was revoked; your next edit will be rejected.", "warn");
            })}
          >
            <span>Revoke my edit access here</span>
            <span>rejected</span>
          </button>
        </div>
      )}
    </div>
  );
}
