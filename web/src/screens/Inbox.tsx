/**
 * The notifications inbox (Inbox board): ListInbox with live additions from
 * /v1/notifications/live, filters by kind, mark read (one, or all), and the
 * preferences table (GetPreferences / UpdatePreferences). Keyboard: J / K
 * move, E marks done, Enter opens.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { describeError } from "../api/errors";
import type { Notification, NotificationKind, Preference } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Spark } from "../components/ui";
import { useHotkey } from "../lib/hooks";
import { relativeTime } from "../lib/format";

const KINDS: [NotificationKind, string, string, string][] = [
  ["NOTIFICATION_KIND_MENTION", "Mentions", "var(--grn)", "@"],
  ["NOTIFICATION_KIND_COMMENT", "Comments", "var(--warn)", "✎"],
  ["NOTIFICATION_KIND_SHARE", "Shares", "var(--fnc)", "↗"],
  ["NOTIFICATION_KIND_AGENT_RUN_FINISHED", "Agent tasks", "var(--vio)", "✓"],
  ["NOTIFICATION_KIND_ACCESS_CHANGED", "Access", "var(--bad)", "!"],
];
const meta = (kind: NotificationKind | undefined) => KINDS.find(([k]) => k === kind) ?? KINDS[0]!;

export function Inbox() {
  const { api } = useServices();
  const ws = useWorkspace();
  const ui = useUi();
  const navigate = useNavigate();
  const [items, setItems] = useState<Notification[]>([]);
  const [token, setToken] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [kind, setKind] = useState<NotificationKind | "all">("all");
  const [prefs, setPrefs] = useState<Preference[]>([]);
  const [cursor, setCursor] = useState(0);
  const list = useRef<HTMLDivElement>(null);

  async function load(more = false) {
    setLoading(true);
    try {
      const res = await api.call("NotificationService_ListInbox", { "page.pageSize": 50, "page.pageToken": more ? token : undefined });
      setItems((cur) => (more ? [...cur, ...(res.notifications ?? [])] : (res.notifications ?? [])));
      setToken(res.page?.nextPageToken || undefined);
      ws.setUnread(res.unread ?? 0);
    } catch (error) {
      ui.toast(describeError(error), "bad");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    void api.call("NotificationService_GetPreferences", {}).then((r) => setPrefs(r.preferences ?? []));
    return ws.onNotification((n) => setItems((cur) => [n, ...cur]));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  async function markRead(ids: string[]) {
    if (ids.length === 0) return;
    setItems((cur) => cur.map((n) => (ids.includes(n.id!) ? { ...n, read: true } : n)));
    ws.setUnread((u) => Math.max(0, u - ids.length));
    await api.call("NotificationService_MarkRead", {}, { ids }).catch((e: unknown) => ui.toast(describeError(e), "bad"));
  }

  async function setPref(k: NotificationKind, field: "inApp" | "email", value: boolean) {
    const current = prefs.find((p) => p.kind === k) ?? { kind: k };
    const next = { ...current, [field]: value };
    setPrefs((ps) => [...ps.filter((p) => p.kind !== k), next]);
    await api.call("NotificationService_UpdatePreferences", {}, { preferences: [next] }).catch((e: unknown) => ui.toast(describeError(e), "bad"));
  }

  const shown = useMemo(() => items.filter((n) => kind === "all" || n.kind === kind), [items, kind]);
  const unread = shown.filter((n) => !n.read);
  const earlier = shown.filter((n) => n.read);
  const ordered = [...unread, ...earlier];

  function open(n: Notification) {
    void markRead(n.read ? [] : [n.id!]);
    if (n.location?.noteId) navigate(`/notes/${n.location.noteId}${n.location.blockId ? `#blk-${n.location.blockId}` : ""}`);
  }

  const move = (by: number) => {
    const next = Math.max(0, Math.min(ordered.length - 1, cursor + by));
    setCursor(next);
    list.current?.querySelector<HTMLElement>(`[data-i="${next}"]`)?.focus();
  };
  useHotkey("j", () => move(1));
  useHotkey("k", () => move(-1));
  useHotkey("e", () => ordered[cursor] && void markRead([ordered[cursor]!.id!]));
  useHotkey("o", () => ordered[cursor] && open(ordered[cursor]!));

  const row = (n: Notification, i: number) => {
    const [, , colour, icon] = meta(n.kind);
    return (
      <div key={n.id} className="nt-item" style={{ "--t": colour } as React.CSSProperties}>
        <div className={`nt-in${n.read ? "" : " unread"}`} data-i={i} role="article" tabIndex={-1} aria-label={`${n.read ? "" : "Unread: "}${n.title}`}>
          <span className="nt-ico2" aria-hidden="true">
            {n.kind === "NOTIFICATION_KIND_AGENT_RUN_FINISHED" ? <Spark style={{ color: "inherit" }} /> : icon}
          </span>
          <span className="nt-tx">
            <span>
              <b>{n.title}</b>
            </span>
            {n.body && <small>“{n.body}”</small>}
            {n.actorId && <small>from {ws.userName(n.actorId)}</small>}
          </span>
          <div className="ai-acts">
            {n.location?.noteId && (
              <button type="button" onClick={() => open(n)}>
                Open
              </button>
            )}
            {!n.read && (
              <button type="button" onClick={() => void markRead([n.id!])}>
                Mark read
              </button>
            )}
          </div>
          <span className="when">{relativeTime(n.createdAt)}</span>
          {!n.read && <span className="nt-dot" />}
        </div>
      </div>
    );
  };

  return (
    <div className="app">
      <Header area="inbox" here="Inbox" />
      <main id="main" className="ly-page">
        <div className="ly-head">
          <div className="ly-ttl">
            <div className="ly-crumb">
              <span>Inbox</span>
              <i>/</i>
              <span>{kind === "all" ? "All" : meta(kind)[1]}</span>
            </div>
            <h1 className="ly-h1">{ws.unread === 0 ? "You're all caught up." : `${ws.unread} thing${ws.unread === 1 ? "" : "s"} need${ws.unread === 1 ? "s" : ""} you.`}</h1>
          </div>
          <div className="ly-tiles">
            <div className="ly-tl hot" style={{ "--c": "var(--acc)" } as React.CSSProperties}>
              <em>Unread</em>
              <strong>{ws.unread}</strong>
            </div>
            <div className="ly-tl">
              <em>Loaded</em>
              <strong>{items.length}</strong>
            </div>
          </div>
        </div>
        <div className="ly-lay">
          <aside className="ly-lp" aria-label="Filter">
            <div className="lab" style={{ padding: "0 10px" }}>
              Show
            </div>
            <button type="button" className={`frow${kind === "all" ? " on" : ""}`} aria-pressed={kind === "all"} onClick={() => setKind("all")}>
              <span>
                <span className="dot" style={{ "--c": "var(--acc)" } as React.CSSProperties} />
                All
              </span>
              <span>{items.length}</span>
            </button>
            {KINDS.map(([k, label, colour]) => (
              <button key={k} type="button" className={`frow${kind === k ? " on" : ""}`} aria-pressed={kind === k} onClick={() => setKind(k)}>
                <span>
                  <span className="dot" style={{ "--c": colour } as React.CSSProperties} />
                  {label}
                </span>
                <span>{items.filter((n) => n.kind === k).length}</span>
              </button>
            ))}
            <span className="cap" style={{ padding: "8px 10px" }}>
              E marks done · J / K moves · O opens
            </span>
          </aside>
          <div className="ly-cp" ref={list}>
            <div className="sech">
              <span className="t">Needs you</span>
              <span className="rule" />
              <button className="c" type="button" onClick={() => void markRead(unread.map((n) => n.id!))} disabled={unread.length === 0}>
                Mark all read
              </button>
            </div>
            <div style={{ display: "grid", gap: 8 }} aria-live="polite">
              {unread.length === 0 && !loading && <span className="cap">Nothing unread.</span>}
              {unread.map((n, i) => row(n, i))}
            </div>
            <div className="sech" style={{ marginTop: 10 }}>
              <span className="t">Earlier</span>
              <span className="rule" />
            </div>
            <div style={{ display: "grid", gap: 8 }}>{earlier.map((n, i) => row(n, unread.length + i))}</div>
            {token && (
              <button type="button" className="btn sm" onClick={() => void load(true)} disabled={loading}>
                Load more
              </button>
            )}
          </div>
          <aside className="ly-rp">
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>Tell me about</span>
              </h5>
              <table className="sk-tbl" style={{ fontSize: 12.5 }}>
                <thead>
                  <tr>
                    <th />
                    <th className="n">In app</th>
                    <th className="n">Email</th>
                  </tr>
                </thead>
                <tbody>
                  {KINDS.map(([k, label]) => {
                    const p = prefs.find((x) => x.kind === k);
                    return (
                      <tr key={k}>
                        <td>{label}</td>
                        {(["inApp", "email"] as const).map((field) => (
                          <td key={field} className="n">
                            <input type="checkbox" aria-label={`${label}: ${field === "inApp" ? "in app" : "email"}`} checked={!!p?.[field]} onChange={(e) => void setPref(k, field, e.target.checked)} />
                          </td>
                        ))}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </section>
          </aside>
        </div>
      </main>
    </div>
  );
}
