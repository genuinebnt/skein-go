/**
 * The note screen (Main, Viewer, EditorStates and Mobile boards): open-note
 * tabs, the subbar (title, role, presence, sync, comments, history, share,
 * agent), the notes/outline/backlinks pane, the editor, the agent pane, and
 * the sync banners (offline, rejected draft). Viewers get the read-only
 * variant: a banner, Share disabled, and an agent that answers but never
 * suggests edits.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { AgentPanel } from "../agent/AgentPanel";
import { setSuggestion, type AgentRun, type Suggestion } from "../agent/run";
import { ApiError, describeError } from "../api/errors";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Avatar, EyeIcon, NoteRoleBadge, OfflineIcon, Pending, SearchIcon, Spark, SyncBadge, WarnIcon } from "../components/ui";
import { NoteSession } from "../crdt/noteSession";
import { describeSync, syncStatus } from "../crdt/syncState";
import type { CrdtModule } from "../crdt/wasm";
import { NoteEditor, type CommentThread, type EditorHandle } from "../editor/NoteEditor";
import { clockTime, colorFor, plural, relativeTime, wordCount } from "../lib/format";
import { useAsync, useHotkey, useMediaQuery, useRevision } from "../lib/hooks";
import { CommentsPanel, type Comment } from "./CommentsPanel";
import { ShareDialog } from "./ShareDialog";

export function NoteScreen() {
  const { noteId } = useParams();
  const services = useServices();
  const ws = useWorkspace();
  const navigate = useNavigate();
  const location = useLocation();
  const [search, setSearch] = useSearchParams();
  const ui = useUi();
  const [crdt, setCrdt] = useState<CrdtModule | null>(null);
  const [session, setSession] = useState<NoteSession | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [leftTab, setLeftTab] = useState<"notes" | "outline" | "backlinks">("notes");
  const [rightPane, setRightPane] = useState<"agent" | "comments" | null>("agent");
  const [openComment, setOpenComment] = useState<string | null>(null);
  const [activeBlock, setActiveBlock] = useState<number | null>(null);
  const [agentFocus, setAgentFocus] = useState(0);
  const [agentPrefill, setAgentPrefill] = useState<string | undefined>(undefined);
  const editor = useRef<EditorHandle>(null);
  const revision = useRevision(session);
  const narrow = useMediaQuery("(max-width: 760px)");
  const shareOpen = search.get("share") === "1";
  const onStarted = useCallback(() => {
    search.delete("new");
    setSearch(search, { replace: true });
  }, [search, setSearch]);

  useEffect(() => {
    void services.crdt().then(setCrdt).catch((e: Error) => setFailure(`The editor could not load: ${e.message}`));
  }, [services]);

  useEffect(() => {
    if (!crdt || !noteId) return;
    let opened: NoteSession | null = null;
    let cancelled = false;
    setSession(null);
    setFailure(null);
    setRuns([]);
    ws.touchRecent(noteId);
    NoteSession.open(noteId, {
      crdt,
      store: services.store,
      api: services.api,
      wsBase: services.wsBase,
      createSocket: services.createSocket,
      accessToken: () => services.session.accessToken,
      refresh: () => services.session.refresh(services.api),
      userId: services.session.user?.id ?? "",
      attributeSync: services.mode === "mock",
      network: services.network,
    })
      .then((s) => {
        if (cancelled) return void s.close();
        opened = s;
        setSession(s);
      })
      .catch((e: unknown) => !cancelled && setFailure(e instanceof ApiError ? describeError(e) : (e as Error).message));
    return () => {
      cancelled = true;
      ui.setSync(null);
      void opened?.close();
    };
  }, [crdt, noteId]); // eslint-disable-line react-hooks/exhaustive-deps

  const versions = useAsync(async () => (noteId ? (await services.api.listAll("DocumentService_ListVersions", { noteId }, (r) => r.versions, 1000)).length : 0), [noteId, session?.sync.lastSyncedAt]);
  const versionLabel = versions.data ? `v${versions.data}` : undefined;

  useEffect(() => {
    if (session) ui.setSync(describeSync(session.sync, versionLabel));
  }, [session, revision, versionLabel]); // eslint-disable-line react-hooks/exhaustive-deps

  // Jump to a block from a search result or citation (#blk-…).
  useEffect(() => {
    if (!session || !location.hash.startsWith("#blk-")) return;
    const el = document.getElementById(location.hash.slice(1));
    el?.scrollIntoView({ block: "center" });
    el?.classList.add("selb");
    const t = setTimeout(() => el?.classList.remove("selb"), 1600);
    return () => clearTimeout(t);
  }, [session, location.hash]);

  // Discover's "Add [[link]] as a suggestion" lands here.
  useEffect(() => {
    const link = search.get("addLink");
    if (!session || !link || !session.canEdit) return;
    editor.current?.appendLine(`See also [[${link}]]`);
    search.delete("addLink");
    setSearch(search, { replace: true });
  }, [session, search, setSearch]);

  const comments = useAsync(async () => {
    if (!noteId) return { available: false, list: [] as Comment[] };
    try {
      const res = await services.api.proposed<{ comments?: Comment[] }>("GET", `/v1/notes/${noteId}/comments`);
      return { available: true, list: res.comments ?? [] };
    } catch {
      return { available: false, list: [] as Comment[] };
    }
  }, [noteId]);
  const threads: CommentThread[] = useMemo(() => {
    const list = comments.data?.list ?? [];
    return list.filter((c) => !c.parentId).map((c) => ({ id: c.id, blockId: c.blockId, start: c.start, end: c.end, quote: c.quote, authorId: c.authorId, resolved: c.resolved, replies: list.filter((r) => r.parentId === c.id).length }));
  }, [comments.data]);
  const openThreads = threads.filter((t) => !t.resolved).length;

  const suggestions = useMemo(() => runs.flatMap((r) => r.suggestions.filter((s) => s.noteId === noteId)), [runs, noteId]);
  const onSuggestion = useCallback(
    (s: Suggestion, accept: boolean) => {
      if (!session) return;
      const mark = (state: Suggestion["state"]) => setRuns((list) => list.map((r) => (r.suggestions.some((x) => x.id === s.id) ? setSuggestion(r, s.id, state) : r)));
      if (accept) return mark("accepted");
      void session
        .replaceText(s.replace, s.find)
        .then((ok) => (ok ? mark("rejected") : ui.toast("That text changed since the agent suggested it; nothing to undo.", "warn")))
        .catch((e: Error) => ui.toast(e.message, "bad"));
    },
    [session, ui],
  );

  useHotkey("j", () => {
    setRightPane("agent");
    setAgentFocus((n) => n + 1);
  }, { meta: true, allowInInputs: true });
  useHotkey("h", () => noteId && navigate(`/notes/${noteId}/history`), { alt: true });

  const note = session?.note ?? (noteId ? ws.notesById.get(noteId) : undefined);
  const role = session?.role;
  const readOnly = !session || !session.canEdit;
  const viewer = role === "NOTE_ROLE_VIEWER";
  const blocks = session ? session.doc.blocks() : [];
  const text = blocks.map((b) => b.text).join("\n");
  const peers = session ? [...session.peers.values()] : [];
  const sync = session ? describeSync(session.sync, versionLabel) : null;
  const backlinks = useAsync(async () => {
    if (!ws.workspace || !noteId) return [];
    const g = await services.api.call("GraphService_GetLinkGraph", { workspaceId: ws.workspace.id });
    return (g.edges ?? []).filter((e) => e.to === noteId).map((e) => e.from!);
  }, [ws.workspace?.id, noteId]);

  const resolveWikiLink = useCallback(
    (title: string) => {
      const lower = title.trim().toLowerCase();
      return ws.notes.find((n) => n.title?.toLowerCase() === lower)?.id ?? null;
    },
    [ws.notes],
  );

  async function rename(title: string) {
    if (!noteId || !title.trim() || title === note?.title) return;
    try {
      await services.api.call("DocumentService_RenameNote", { noteId }, { title: title.trim() });
      ws.upsertNote({ ...(note ?? {}), id: noteId, title: title.trim() });
      await session?.refreshNote();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  if (failure) {
    return (
      <div className="app">
        <Header area="notes" here="Note" />
        <main id="main" className="sk-empty" style={{ minHeight: 400 }}>
          <b>{failure}</b>
          <Link className="btn sm" to="/notes">
            Back to notes
          </Link>
        </main>
      </div>
    );
  }

  const drafts = session?.drafts ?? [];
  const status = session ? syncStatus(session.sync) : "syncing";

  return (
    <div className="app ed-route">
      <Header area="notes" here={note?.title ?? "Note"} />
      <div className="fill">
        <div className="m-head">
          <Link className="m-ib" to="/notes" aria-label="Back to notes">
            <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true">
              <path d="M10 3 5 8l5 5" />
            </svg>
          </Link>
          <span style={{ flex: 1, minWidth: 0, display: "grid", lineHeight: 1.25 }}>
            <b style={{ fontSize: 14, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>{note?.title}</b>
            <span className="cap">{role ? role.replace("NOTE_ROLE_", "").toLowerCase() : ""}</span>
          </span>
          {sync && <SyncBadge {...sync} onClick={() => session?.retryNow()} />}
          <Link className="m-ib" to={`/notes/${noteId}/history`} aria-label="History">
            ⋯
          </Link>
        </div>
        <OpenTabs current={noteId} />
        <div className="subbar">
          <span className="crumb">
            <span>{ws.workspace?.name}</span>
            <span>/</span>
          </span>
          <TitleField title={note?.title ?? ""} editable={!readOnly} onRename={(t) => void rename(t)} />
          {role && <NoteRoleBadge role={role} />}
          <div className="sbr">
            <div className="avs" aria-label={`${plural(peers.length, "other person", "other people")} here`}>
              {peers.slice(0, 4).map((p) => (
                <Avatar key={p.replicaId} id={p.userId} name={ws.userName(p.userId)} idle={Date.now() - p.at > 30_000} title={`${ws.userName(p.userId)} · ${Date.now() - p.at > 30_000 ? "idle" : "here"}`} />
              ))}
            </div>
            {sync && <SyncBadge {...sync} onClick={() => session?.retryNow()} />}
            <span className="vr" />
            <button className="btn xs" type="button" aria-pressed={rightPane === "comments"} onClick={() => setRightPane((p) => (p === "comments" ? "agent" : "comments"))}>
              Comments{comments.data?.available ? <span className="hd-n">{openThreads}</span> : <Pending>preview</Pending>}
            </button>
            <Link className="btn xs" to={`/notes/${noteId}/history`}>
              History <span className="kb">⌥H</span>
            </Link>
            <button className={`btn xs${readOnly ? " dis" : ""}`} type="button" disabled={readOnly} title={readOnly ? "Only editors can share this note" : undefined} onClick={() => setSearch({ share: "1" })}>
              Share
            </button>
            <button className="btn xs vio" type="button" aria-pressed={rightPane === "agent"} onClick={() => setRightPane((p) => (p === "agent" ? null : "agent"))}>
              <Spark style={{ color: "var(--on-acc)" }} />
              Agent <span className="kb">⌘J</span>
            </button>
          </div>
        </div>
        <div className="fill-row">
          <NotesPane tab={leftTab} setTab={setLeftTab} current={noteId} backlinks={backlinks.data ?? []} blocks={blocks} />
          <section className="pane c" style={{ flex: "999 1 560px", minWidth: 0 }} aria-label="Note">
            <div className="pbody" id="main" style={{ padding: 0, color: "inherit", fontSize: "inherit", lineHeight: "inherit" }}>
              <div style={{ display: "grid", gap: 10, padding: "16px 24px 0", maxWidth: 820, margin: "0 auto" }}>
                {viewer && (
                  <div className="sk-banner" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
                    <EyeIcon />
                    <span>You can read this note and ask the agent about it. Editing and comments are off for viewers.</span>
                    <span className="sp" />
                    <span className="cap">Owner · {ws.userName(note?.createdBy)}</span>
                  </div>
                )}
                {status === "offline" && session && (
                  <div className="sk-banner" style={{ "--c": "var(--warn)" } as React.CSSProperties} role="status">
                    <OfflineIcon />
                    <span>
                      <b>Offline.</b> {session.sync.pending + session.sync.inflight > 0 ? `${plural(session.sync.pending + session.sync.inflight, "change")} are saved on this device and send when you reconnect.` : "Changes you make are saved on this device."}
                    </span>
                    <span className="sp" />
                    <span className="cap">{session.sync.lastSyncedAt ? `last sync ${clockTime(new Date(session.sync.lastSyncedAt).toISOString())}` : ""}</span>
                    <button type="button" className="btn xs" onClick={() => session.retryNow()}>
                      Retry now
                    </button>
                  </div>
                )}
                {drafts.map((d) => (
                  <div key={d.id ?? d.at} className="sk-banner" style={{ "--c": "var(--bad)", display: "grid", gap: 10 } as React.CSSProperties} role="alert">
                    <div style={{ display: "flex", gap: 10, alignItems: "center" }}>
                      <WarnIcon />
                      <span>
                        <b>{plural(d.operations, "change")} weren't applied.</b> The server refused them ({d.reason}). They're kept in a draft.
                      </span>
                    </div>
                    <div className="diff">
                      <div className="cap" style={{ paddingBottom: 4 }}>
                        Draft · {relativeTime(new Date(d.at).toISOString())}
                      </div>
                      {d.text
                        .split("\n")
                        .filter(Boolean)
                        .map((line, i) => (
                          <div key={i} className="a">
                            + {line}
                          </div>
                        ))}
                    </div>
                    <div style={{ display: "flex", gap: 6 }}>
                      <button className="btn xs go" type="button" disabled={readOnly} onClick={() => void session?.reinsertDraft(d)}>
                        Re-insert as a new block
                      </button>
                      <button className="btn xs" type="button" onClick={() => void navigator.clipboard?.writeText(d.text)}>
                        Copy
                      </button>
                      <button className="btn xs ghost" type="button" onClick={() => void session?.discardDraft(d)}>
                        Discard draft
                      </button>
                    </div>
                  </div>
                ))}
              </div>
              <div className="ed-doc" style={viewer ? { paddingTop: 24 } : undefined}>
                <div className="ed-meta">
                  {(note as { tags?: string[] } | undefined)?.tags?.map((t, i) => (
                    <span key={t} className={`chip sm${i === 0 ? " on" : ""}`} style={i === 0 ? ({ "--c": colorFor(t) } as React.CSSProperties) : undefined}>
                      {i === 0 && <span className="dot" />}#{t}
                    </span>
                  ))}
                  {note?.updatedAt && <span className="cap">edited {relativeTime(note.updatedAt)}</span>}
                </div>
                <div className="ed-title">{note?.title}</div>
                {session && crdt ? (
                  <NoteEditor
                    ref={editor}
                    session={session}
                    crdt={crdt}
                    revision={revision}
                    readOnly={readOnly}
                    startWriting={search.get("new") === "1"}
                    onStarted={onStarted}
                    comments={threads}
                    onOpenComment={(id) => {
                      setOpenComment(id);
                      setRightPane("comments");
                    }}
                    suggestions={suggestions}
                    onSuggestion={onSuggestion}
                    userName={ws.userName}
                    inline={{ resolveWikiLink, onOpenNote: (id) => navigate(`/notes/${id}`) }}
                    onAskAgent={(prompt) => {
                      setRightPane("agent");
                      setAgentPrefill(prompt);
                      setAgentFocus((n) => n + 1);
                    }}
                    onActive={setActiveBlock}
                    onError={(m) => ui.toast(m, "bad")}
                  />
                ) : (
                  <div style={{ display: "grid", gap: 12, marginTop: 24 }} aria-busy="true" aria-label="Loading the note">
                    <span className="sk" style={{ width: "70%" }} />
                    <span className="sk" />
                    <span className="sk" style={{ width: "85%" }} />
                  </div>
                )}
                {narrow && suggestions.some((s) => s.state === "pending") && (
                  <div className="ai-card m-sug" style={{ padding: "10px 12px", gap: 8, marginTop: 14 }}>
                    <div className="h">
                      <Spark />
                      Agent suggestion · {suggestions.filter((s) => s.state === "pending").length} pending
                    </div>
                    <p style={{ fontSize: 12.5, color: "var(--mut)" }}>Accepting while offline queues it with your other changes.</p>
                    <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 6 }}>
                      <button className="btn vio" type="button" style={{ height: 44, fontSize: 14 }} onClick={() => onSuggestion(suggestions.find((s) => s.state === "pending")!, true)}>
                        Accept
                      </button>
                      <button className="btn" type="button" style={{ height: 44, fontSize: 14 }} onClick={() => onSuggestion(suggestions.find((s) => s.state === "pending")!, false)}>
                        Reject
                      </button>
                    </div>
                  </div>
                )}
              </div>
            </div>
            <div className="statusb">
              <span>
                <b>{wordCount(text).toLocaleString()}</b> words
              </span>
              <span>
                <b>{backlinks.data?.length ?? 0}</b> backlinks
              </span>
              <span>{activeBlock !== null ? `block ${activeBlock + 1} of ${blocks.length}` : `${blocks.length} blocks`}</span>
              <span className="sp" />
              <span>Markdown shows around the cursor</span>
              <span style={{ color: status === "synced" ? "var(--grn)" : status === "draft" ? "var(--bad)" : "var(--warn)" }}>● {status === "synced" ? "saved locally and on server" : status === "draft" ? "draft kept on this device" : "saved on this device"}</span>
            </div>
            <MobileToolbar editor={editor} onAgent={() => setRightPane("agent")} />
          </section>
          {rightPane === "comments" && noteId && (
            <CommentsPanel noteId={noteId} available={!!comments.data?.available} comments={comments.data?.list ?? []} focus={openComment} canComment={role !== "NOTE_ROLE_VIEWER"} onChanged={comments.reload} onClose={() => setRightPane("agent")} />
          )}
          {rightPane === "agent" && (
            <AgentPanel noteId={noteId} noteTitle={note?.title} role={role} runs={runs} setRuns={setRuns} onSuggestion={onSuggestion} focusRequest={agentFocus} prefill={agentPrefill} />
          )}
        </div>
      </div>
      {shareOpen && noteId && <ShareDialog noteId={noteId} title={note?.title ?? ""} onClose={() => setSearch({})} />}
    </div>
  );
}

function TitleField({ title, editable, onRename }: { title: string; editable: boolean; onRename: (title: string) => void }) {
  const [value, setValue] = useState(title);
  useEffect(() => setValue(title), [title]);
  if (!editable) return <span className="wtitle">{title}</span>;
  return (
    <input
      className="wtitle"
      aria-label="Note title"
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onBlur={() => onRename(value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
        if (e.key === "Escape") {
          setValue(title);
          e.currentTarget.blur();
        }
      }}
      style={{ background: "transparent", border: 0, outline: "none", minWidth: 120, width: `${Math.max(12, value.length + 1)}ch`, maxWidth: 520 }}
    />
  );
}

function OpenTabs({ current }: { current?: string }) {
  const ws = useWorkspace();
  const tabs = ws.recent.filter((id) => ws.notesById.has(id) || id === current).slice(0, 6);
  return (
    <div className="vtabs" role="tablist" aria-label="Open notes">
      <div className="scroll">
        {tabs.map((id) => {
          const note = ws.notesById.get(id);
          return (
            <Link key={id} className={`vtab${id === current ? " on" : ""}`} to={`/notes/${id}`} role="tab" aria-selected={id === current} style={{ "--c": colorFor((note as { tags?: string[] } | undefined)?.tags?.[0] ?? id) } as React.CSSProperties}>
              <span className="k" />
              {note?.title ?? "…"}
              {id !== current && (
                <span
                  className="x"
                  role="button"
                  tabIndex={0}
                  aria-label={`Close ${note?.title}`}
                  onClick={(e) => {
                    e.preventDefault();
                    ws.closeRecent(id);
                  }}
                  onKeyDown={(e) => e.key === "Enter" && ws.closeRecent(id)}
                >
                  ×
                </span>
              )}
            </Link>
          );
        })}
      </div>
      <span style={{ flex: 1 }} />
    </div>
  );
}

function NotesPane({ tab, setTab, current, backlinks, blocks }: { tab: "notes" | "outline" | "backlinks"; setTab: (t: "notes" | "outline" | "backlinks") => void; current?: string; backlinks: string[]; blocks: { id: string; type: string; text: string }[] }) {
  const ws = useWorkspace();
  const [filter, setFilter] = useState("");
  const groups = useMemo(() => {
    const out = new Map<string, typeof ws.notes>();
    for (const n of ws.notes) {
      if (filter && !n.title?.toLowerCase().includes(filter.toLowerCase())) continue;
      const key = n.tags?.[0] ?? "untagged";
      out.set(key, [...(out.get(key) ?? []), n]);
    }
    return [...out.entries()].sort((a, b) => b[1].length - a[1].length);
  }, [ws, filter]);
  return (
    <section className="pane l" style={{ flex: "0 0 248px" }} aria-label="Notes">
      <div className="tabs" role="tablist">
        {(["notes", "outline", "backlinks"] as const).map((t) => (
          <button key={t} type="button" role="tab" aria-selected={tab === t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
            {t === "notes" ? "Notes" : t === "outline" ? "Outline" : "Backlinks"}
            {t === "backlinks" && backlinks.length > 0 && <span className="hd-n">{backlinks.length}</span>}
          </button>
        ))}
      </div>
      <div className="pbody" style={{ padding: 10 }}>
        {tab === "notes" && (
          <>
            <label className="cm-find" style={{ height: 34, marginBottom: 8 }}>
              <SearchIcon />
              <input type="search" placeholder="Filter notes…" aria-label="Filter notes" value={filter} onChange={(e) => setFilter(e.target.value)} />
              <kbd>/</kbd>
            </label>
            <div className="tree">
              {groups.map(([tag, notes]) => (
                <div key={tag}>
                  <div className="dir">
                    {tag} · {notes.length}
                  </div>
                  {notes.slice(0, 40).map((n) => (
                    <Link key={n.id} className={`it${n.id === current ? " on" : ""}`} to={`/notes/${n.id}`} aria-current={n.id === current ? "page" : undefined}>
                      <span className="k" style={{ color: colorFor(tag) }}>
                        ●
                      </span>
                      <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{n.title}</span>
                    </Link>
                  ))}
                </div>
              ))}
            </div>
          </>
        )}
        {tab === "outline" && (
          <div className="tree">
            {blocks.filter((b) => b.type.startsWith("heading")).length === 0 && <span className="cap">No headings yet.</span>}
            {blocks
              .filter((b) => b.type.startsWith("heading"))
              .map((b) => (
                <a key={b.id} className={`it ind${Number(b.type.slice(-1)) - 1}`} href={`#blk-${b.id}`}>
                  {b.text || "Untitled heading"}
                </a>
              ))}
          </div>
        )}
        {tab === "backlinks" && (
          <div className="tree">
            {backlinks.length === 0 && <span className="cap">No note links here yet.</span>}
            {backlinks.map((id) => (
              <Link key={id} className="it" to={`/notes/${id}`}>
                <span className="k">←</span>
                {ws.notesById.get(id)?.title ?? id}
              </Link>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function MobileToolbar({ editor, onAgent }: { editor: React.RefObject<EditorHandle | null>; onAgent: () => void }) {
  const keep = (e: React.MouseEvent) => e.preventDefault(); // keep the textarea focused
  const act = (fn: (h: EditorHandle) => void) => () => editor.current && fn(editor.current);
  return (
    <nav className="m-toolbar" aria-label="Formatting">
      <button type="button" aria-label="Insert block" onMouseDown={keep} onClick={act((h) => h.openSlash())}>
        /
      </button>
      <button type="button" aria-label="Bold" onMouseDown={keep} onClick={act((h) => h.wrapSelection("**"))} style={{ fontWeight: 700 }}>
        B
      </button>
      <button type="button" aria-label="Italic" onMouseDown={keep} onClick={act((h) => h.wrapSelection("*"))} style={{ fontStyle: "italic" }}>
        I
      </button>
      <button type="button" aria-label="Inline code" onMouseDown={keep} onClick={act((h) => h.wrapSelection("`"))}>
        `c`
      </button>
      <button type="button" aria-label="Link a note" onMouseDown={keep} onClick={act((h) => h.wrapSelection("[[", "]]"))} style={{ color: "var(--grn)" }}>
        [[ ]]
      </button>
      <button type="button" aria-label="Mention" onMouseDown={keep} onClick={act((h) => h.insertAtCaret("@"))}>
        @
      </button>
      <button type="button" aria-label="Move block down" onMouseDown={keep} onClick={act((h) => h.moveActive(1))}>
        ⇅
      </button>
      <span style={{ flex: 1 }} />
      <button type="button" className="ag" aria-label="Ask the agent" onClick={onAgent}>
        <Spark />
      </button>
    </nav>
  );
}
