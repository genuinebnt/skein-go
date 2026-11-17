/**
 * Version history (History board): the timeline (ListVersions), a block diff
 * between two versions (Diff) with inline word changes, blame by author
 * (Blame), and restore as a new version. Click picks the version to show;
 * shift-click picks the base. Agent edits show as "Agent for <user>".
 */

import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { describeError } from "../api/errors";
import type { NoteVersion } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Avatar, Pending, Spinner } from "../components/ui";
import { NoteSession } from "../crdt/noteSession";
import { attribute, blocksFromHunks, summarise, type HistoryBlock } from "../history/model";
import { clockTime, colorFor, initials, relativeTime } from "../lib/format";
import { useAsync } from "../lib/hooks";

type View = "diff" | "blame" | "both";

const dayOf = (iso?: string) => {
  if (!iso) return "Earlier";
  const days = Math.floor((Date.now() - Date.parse(iso)) / 86_400_000);
  return days < 1 ? "Today" : days < 2 ? "Yesterday" : days < 7 ? "This week" : "Earlier";
};

export function History() {
  const { noteId = "" } = useParams();
  const services = useServices();
  const { api } = services;
  const ws = useWorkspace();
  const ui = useUi();
  const navigate = useNavigate();
  const [view, setView] = useState<View>("both");
  const [toIndex, setToIndex] = useState(0);
  const [fromIndex, setFromIndex] = useState(1);
  const [restoring, setRestoring] = useState(false);
  const note = ws.notesById.get(noteId);

  const versions = useAsync(() => api.listAll("DocumentService_ListVersions", { noteId }, (r) => r.versions, 1000), [noteId]);
  const list = useMemo(() => versions.data ?? [], [versions.data]);
  const label = (i: number) => `v${list.length - i}`;
  const from = list[Math.min(fromIndex, list.length - 1)];
  const to = list[toIndex];
  const me = useAsync(() => api.call("DocumentService_GetNote", { noteId }) as Promise<{ callerRole?: string }>, [noteId]);
  const canRestore = (me.data?.callerRole ?? "NOTE_ROLE_EDITOR") === "NOTE_ROLE_EDITOR";

  const diff = useAsync(async () => {
    if (!from?.version || !to?.version) return null;
    const res = await api.call("DocumentService_Diff", { noteId }, { from: from.version, to: to.version });
    return blocksFromHunks(res.hunks ?? []);
  }, [noteId, from, to]);
  const blame = useAsync(() => api.call("DocumentService_Blame", { noteId }), [noteId, list.length]);
  const blocks = diff.data ?? [];
  const totals = summarise(blocks);
  // Blame describes the current text; it lines up with the diff only when "to" is the newest version.
  const blameUsable = toIndex === 0;

  useEffect(() => {
    if (list.length > 0 && fromIndex >= list.length) setFromIndex(list.length - 1);
  }, [list.length, fromIndex]);

  const authors = useMemo(() => {
    const ids = new Map<string, number>();
    for (let i = toIndex; i < Math.min(fromIndex, list.length); i++) for (const a of list[i]?.authorIds ?? []) ids.set(a, (ids.get(a) ?? 0) + 1);
    return [...ids];
  }, [list, toIndex, fromIndex]);

  async function textAt(version: NoteVersion) {
    return (await api.call("DocumentService_GetTextAt", { noteId }, { version: version.version })).markdown ?? "";
  }

  async function restore() {
    if (!from) return;
    setRestoring(true);
    let session: NoteSession | null = null;
    try {
      const markdown = await textAt(from);
      session = await NoteSession.open(noteId, {
        crdt: await services.crdt(),
        store: services.store,
        api,
        wsBase: services.wsBase,
        createSocket: services.createSocket,
        accessToken: () => services.session.accessToken,
        refresh: () => services.session.refresh(api),
        userId: services.session.user?.id ?? "",
        attributeSync: services.mode === "mock",
        network: services.network,
      });
      await waitFor(() => session!.sync.catchingUp === false && session!.sync.connection === "open", 8000);
      const { ops, attrs } = session.doc.replaceWithMarkdown(markdown);
      await session.commit(ops);
      await session.commit(attrs, "attrs");
      await waitFor(() => session!.sync.inflight === 0 && session!.sync.pending === 0, 8000);
      ui.toast(`Restored ${label(fromIndex)} as a new version.`, "ok");
      versions.reload();
      setToIndex(0);
    } catch (error) {
      ui.toast(describeError(error), "bad");
    } finally {
      await session?.close();
      setRestoring(false);
    }
  }

  async function copyToNewNote() {
    if (!from || !ws.workspace) return;
    try {
      const res = await api.call("DocumentService_CreateNote", { workspaceId: ws.workspace.id }, { title: `${note?.title ?? "Note"} (${label(fromIndex)})`, markdown: await textAt(from) }, { idempotencyKey: true });
      if (res.note) {
        ws.upsertNote(res.note);
        navigate(`/notes/${res.note.id}`);
      }
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  let lastDay = "";
  return (
    <div className="app">
      <Header area="notes" here={`${note?.title ?? "Note"} · history`} />
      <div className="fill">
        <div className="subbar">
          <Link className="crumb" to={`/notes/${noteId}`}>
            <span>← Back to note</span>
          </Link>
          <span className="wtitle">{note?.title}</span>
          {list.length > 1 && (
            <span className="pill c" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
              comparing {label(Math.min(fromIndex, list.length - 1))} → {label(toIndex)}
            </span>
          )}
          <div className="sbr">
            <div className="seg mono sm" role="group" aria-label="View">
              {(["diff", "blame", "both"] as const).map((v) => (
                <button key={v} type="button" className={view === v ? "on" : ""} aria-pressed={view === v} onClick={() => setView(v)} style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
                  {v[0]!.toUpperCase() + v.slice(1)}
                </button>
              ))}
            </div>
            <span className="vr" />
            <button className="btn xs dis" type="button" disabled title="Named versions need a NameVersion RPC">
              Name this version
            </button>
            <button className="btn xs go" type="button" disabled={!canRestore || restoring || !from} onClick={() => void restore()}>
              {restoring && <Spinner />}Restore {label(Math.min(fromIndex, list.length - 1))}…
            </button>
          </div>
        </div>
        <div className="fill-row">
          <section className="pane l" style={{ flex: "0 0 290px" }} aria-label="Versions">
            <div className="tabs">
              <button type="button" className="on">
                Versions
              </button>
              <button type="button" disabled title="Named versions need a NameVersion RPC">
                Named <Pending>soon</Pending>
              </button>
            </div>
            <div className="pbody" style={{ display: "grid", gap: 2, alignContent: "start" }} role="listbox" aria-label="Versions; click to show, shift-click to pick the base">
              {versions.loading && <span className="sk" />}
              {list.map((v, i) => {
                const day = dayOf(v.at);
                const header = day !== lastDay ? ((lastDay = day), <div key={`d${day}`} className="lab" style={{ padding: "10px 6px 4px" }}>{day}</div>) : null;
                const agent = (v as { agent?: boolean }).agent;
                const who = (v.authorIds ?? []).map((a) => ws.userName(a)).join(", ");
                return (
                  <div key={i} style={{ display: "contents" }}>
                    {header}
                    <button
                      type="button"
                      role="option"
                      aria-selected={i === toIndex}
                      className={`ed-ver${i === toIndex ? " on" : ""}${i === fromIndex ? " cmp" : ""}`}
                      onClick={(e) => {
                        if (e.shiftKey) setFromIndex(i);
                        else {
                          setToIndex(i);
                          if (fromIndex <= i) setFromIndex(Math.min(i + 1, list.length - 1));
                        }
                      }}
                    >
                      <i style={agent ? { borderColor: "var(--vio)" } : undefined} />
                      <span>
                        <b>
                          {label(i)} · {agent ? `Agent for ${who}` : who}
                        </b>
                        <small>
                          {clockTime(v.at)} · {relativeTime(v.at)}
                        </small>
                      </span>
                      <small>{i === toIndex ? "to" : i === fromIndex ? "from" : ""}</small>
                    </button>
                  </div>
                );
              })}
              <span className="cap" style={{ padding: "10px 6px" }}>
                Click a version to show it. Shift-click picks the base.
              </span>
            </div>
          </section>
          <section className="pane c" id="main" style={{ flex: "999 1 600px", minWidth: 0 }} aria-label="Changes">
            <div className="pbody" style={{ padding: "24px 32px", color: "inherit", fontSize: 15, lineHeight: 1.65 }}>
              <div style={{ maxWidth: 860, margin: "0 auto", display: "grid", gap: 6 }}>
                {diff.loading && <span className="sk" style={{ width: "60%" }} />}
                {diff.error && <span className="help err">{diff.error}</span>}
                {list.length === 1 && <span className="cap">This note has one version so far.</span>}
                {blocks
                  .filter((b) => view !== "blame" || b.status !== "rem")
                  .map((b, i) => (
                    <BlockRow key={i} block={b} view={view} attribution={blameUsable && b.status !== "rem" ? attribute(blame.data?.spans ?? [], b.newStart, b.newEnd) : null} removedIn={b.status === "rem" ? label(toIndex) : undefined} />
                  ))}
              </div>
            </div>
            <div className="statusb">
              <span style={{ color: "var(--grn)" }}>+{totals.added} blocks</span>
              <span style={{ color: "var(--bad)" }}>−{totals.removed} block{totals.removed === 1 ? "" : "s"}</span>
              <span style={{ color: "var(--warn)" }}>~{totals.changed} changed</span>
              <span className="sp" />
              <span>
                words:{" "}
                <b>
                  +{totals.wordsIn} −{totals.wordsOut}
                </b>
              </span>
              <span>block-level diff · inline word diff</span>
            </div>
          </section>
          <section className="pane r" style={{ flex: "1 1 320px", maxWidth: 360 }} aria-label="Summary">
            <div className="tabs">
              <button type="button" className="on">
                Summary
              </button>
            </div>
            <div className="pbody" style={{ display: "grid", gap: 14, alignContent: "start" }}>
              <div className="gr-ird">
                <div>
                  <span>Added</span>
                  <b style={{ color: "var(--grn)" }}>{totals.added}</b>
                </div>
                <div>
                  <span>Removed</span>
                  <b style={{ color: "var(--bad)" }}>{totals.removed}</b>
                </div>
                <div>
                  <span>Changed</span>
                  <b style={{ color: "var(--warn)" }}>{totals.changed}</b>
                </div>
              </div>
              <div className="lab">
                By author · {label(Math.min(fromIndex, list.length - 1))} → {label(toIndex)}
              </div>
              <table className="sk-tbl">
                <tbody>
                  {authors.map(([id, n]) => (
                    <tr key={id}>
                      <td>
                        <Avatar id={id} name={ws.userName(id)} size="xs" />
                      </td>
                      <td>{ws.userName(id)}</td>
                      <td className="n">
                        {n} version{n === 1 ? "" : "s"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!blameUsable && view !== "diff" && <span className="cap">Blame describes the newest version; pick it as “to” to see authors per block.</span>}
              <div className="callout info">
                <div className="ctitle">Restoring is a new version</div>
                <p>
                  Restore {label(Math.min(fromIndex, list.length - 1))} writes a new version with its content. Later versions stay in history, and people editing now see the change live.
                </p>
              </div>
              <div style={{ display: "grid", gap: 6 }}>
                <button className="btn sm go" type="button" disabled={!canRestore || restoring || !from} onClick={() => void restore()}>
                  Restore {label(Math.min(fromIndex, list.length - 1))} as v{list.length + 1}
                </button>
                <button className="btn sm ghost" type="button" disabled={!from} onClick={() => void copyToNewNote()}>
                  Copy {label(Math.min(fromIndex, list.length - 1))} to a new note
                </button>
              </div>
              <span className="cap">Viewers see history but can't restore.</span>
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}

function BlockRow({ block, view, attribution, removedIn }: { block: HistoryBlock; view: View; attribution: ReturnType<typeof attribute>; removedIn?: string }) {
  const ws = useWorkspace();
  const showDiff = view !== "blame";
  const showBlame = view !== "diff";
  const style: React.CSSProperties = { padding: "4px 10px", borderRadius: 6, whiteSpace: block.kind === "code" ? "pre-wrap" : undefined };
  if (block.kind === "h1" || block.kind === "h2") Object.assign(style, { fontSize: block.kind === "h1" ? 26 : 21, fontWeight: 700, letterSpacing: "-.02em", marginTop: 14 });
  if (block.kind === "h3") Object.assign(style, { fontSize: 18, fontWeight: 700, marginTop: 10 });
  if (block.kind === "code") Object.assign(style, { font: "400 13px/1.7 var(--code)", background: "var(--inset)", border: "1px solid var(--line2)" });
  if (["p", "li", "todo", "quote"].includes(block.kind)) style.color = "var(--mut)";
  if (showDiff && block.status === "add") Object.assign(style, { background: "var(--grn-bg)", boxShadow: "inset 3px 0 0 var(--grn)", color: "var(--fg)" });
  if (showDiff && block.status === "rem") Object.assign(style, { background: "var(--bad-bg)", boxShadow: "inset 3px 0 0 var(--bad)", textDecoration: "line-through" });
  if (showDiff && block.status === "chg") style.boxShadow = "inset 3px 0 0 var(--warn)";
  const name = attribution ? (attribution.agentRunId ? `Agent for ${ws.userName(attribution.userId)}` : ws.userName(attribution.userId)) : "";
  return (
    <div style={{ display: "grid", gridTemplateColumns: showBlame ? "170px minmax(0,1fr)" : "minmax(0,1fr)", gap: 14, alignItems: "start" }}>
      {showBlame && (
        <div style={{ display: "flex", gap: 8, alignItems: "flex-start", paddingTop: 3, minWidth: 0 }}>
          {attribution ? (
            <>
              <span className="av xs" style={{ "--c": attribution.agentRunId ? "var(--vio)" : colorFor(attribution.userId) } as React.CSSProperties}>
                {attribution.agentRunId ? "✦" : initials(ws.userName(attribution.userId))}
              </span>
              <span style={{ display: "grid", lineHeight: 1.3, minWidth: 0 }}>
                <span style={{ fontSize: 12, color: "var(--fg)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>{name}</span>
                <span className="cap" style={{ fontSize: 10.5 }}>
                  {relativeTime(attribution.at)}
                </span>
              </span>
            </>
          ) : (
            <span className="cap">{removedIn ? `removed in ${removedIn}` : ""}</span>
          )}
        </div>
      )}
      <div style={style}>
        {block.kind === "li" && "• "}
        {block.kind === "todo" && "☐ "}
        {block.segments
          .filter((s) => showDiff || s.kind !== "del")
          .map((s, i) => (
            <span key={i} className={showDiff ? (s.kind === "del" ? "ai-del" : s.kind === "ins" ? "ai-ins" : "") : ""}>
              {s.text}
            </span>
          ))}
      </div>
    </div>
  );
}

function waitFor(condition: () => boolean, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const tick = () => {
      if (condition()) return resolve();
      if (Date.now() - started > timeoutMs) return reject(new Error("timed out waiting for the server"));
      setTimeout(tick, 50);
    };
    tick();
  });
}
