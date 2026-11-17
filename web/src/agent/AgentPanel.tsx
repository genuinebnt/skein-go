/**
 * The agent panel (Main, Viewer and EditorStates boards): a chat anchored to
 * the open note over /v1/chats/{chatId}/messages, with each run's steps,
 * answer, citations, confirmations and suggested edits.
 *
 * The agent acts as the user (ADR-0009): the role chip says so, viewers get
 * answers but no suggestions, and every destructive action waits for a
 * confirmation card. Suggestions are the agent's apply_edit change groups;
 * accept keeps them, reject puts the original text back (see run.ts).
 */

import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { describeError } from "../api/errors";
import type { NoteRole, SendMessageResponse } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { NoteRoleBadge, Spark, Spinner } from "../components/ui";
import { useHotkey } from "../lib/hooks";
import { AgentChannel } from "../ws/channels";
import { applyEvent, describeStep, newRun, setConfirmation, stepsSummary, type AgentRun, type Suggestion } from "./run";

type Props = {
  noteId?: string;
  noteTitle?: string;
  role?: NoteRole;
  runs: AgentRun[];
  setRuns: (update: (runs: AgentRun[]) => AgentRun[]) => void;
  onSuggestion: (s: Suggestion, accept: boolean) => void;
  focusRequest?: number;
  prefill?: string;
};

export function AgentPanel({ noteId, noteTitle, role, runs, setRuns, onSuggestion, focusRequest, prefill }: Props) {
  const services = useServices();
  const { api, session } = services;
  const ws = useWorkspace();
  const ui = useUi();
  const navigate = useNavigate();
  const [tab, setTab] = useState<"chat" | "tasks">("chat");
  const [text, setText] = useState(prefill ?? "");
  const [stepsOpen, setStepsOpen] = useState<Record<string, boolean>>({});
  const [cursor, setCursor] = useState(0);
  const channel = useRef<AgentChannel | null>(null);
  const chatId = useRef<string | null>(null);
  const pendingText = useRef<string[]>([]);
  const input = useRef<HTMLTextAreaElement>(null);
  const viewer = role === "NOTE_ROLE_VIEWER";

  useEffect(() => {
    if (focusRequest) input.current?.focus();
  }, [focusRequest]);
  useEffect(() => {
    if (prefill !== undefined) setText(prefill);
  }, [prefill]);
  useHotkey("j", () => input.current?.focus(), { meta: true, allowInInputs: true });

  useEffect(
    () => () => {
      channel.current?.close();
      channel.current = null;
      chatId.current = null;
    },
    [noteId],
  );

  const onEvent = useCallback(
    (event: SendMessageResponse) => {
      const runId = event.runId ?? "";
      setRuns((list) => {
        const existing = list.find((r) => r.runId === runId);
        if (existing) return list.map((r) => (r.runId === runId ? applyEvent(r, event) : r));
        // The first event of a run belongs to the oldest message still waiting for one.
        const placeholder = list.find((r) => r.runId.startsWith("pending:"));
        const fresh = applyEvent(placeholder ? { ...placeholder, runId } : newRun(runId, pendingText.current.shift()), event);
        return placeholder ? list.map((r) => (r === placeholder ? fresh : r)) : [...list, fresh];
      });
    },
    [setRuns],
  );

  async function send(e?: FormEvent) {
    e?.preventDefault();
    const message = text.trim();
    if (!message || !ws.workspace) return;
    setText("");
    try {
      if (!chatId.current) {
        const res = await api.call("AgentService_CreateChat", { workspaceId: ws.workspace.id }, { noteId }, { idempotencyKey: true });
        chatId.current = res.chatId ?? null;
        if (!chatId.current) throw new Error("no chat");
        channel.current = new AgentChannel(
          chatId.current,
          { wsBase: services.wsBase, createSocket: services.createSocket, accessToken: () => session.accessToken, refresh: () => session.refresh(api), network: services.network },
          onEvent,
          undefined,
          () => {
            // Runs keep going while disconnected; read what we missed (websocket.md).
            for (const r of runsRef.current) {
              if (!r.finished && !r.runId.startsWith("pending:")) void api.call("AgentService_GetRun", { runId: r.runId }).then((res) => res.events?.forEach(onEvent));
            }
          },
        );
      }
      setRuns((list) => [...list, newRun(`pending:${crypto.randomUUID()}`, message)]);
      channel.current!.send(message);
    } catch (error) {
      ui.toast(describeError(error), "bad");
      setText(message);
    }
  }

  const runsRef = useRef(runs);
  runsRef.current = runs;

  async function answer(run: AgentRun, confirmationId: string, approved: boolean) {
    try {
      await api.call("AgentService_Confirm", { runId: run.runId }, { confirmationId, approved });
      setRuns((list) => list.map((r) => (r.runId === run.runId ? setConfirmation(r, confirmationId, approved ? "approved" : "declined") : r)));
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  async function cancel(run: AgentRun) {
    await api.call("AgentService_CancelRun", { runId: run.runId }, {}).catch((error: unknown) => ui.toast(describeError(error), "bad"));
  }

  async function draftInMyNotes(run: AgentRun) {
    if (!ws.workspace) return;
    try {
      const res = await api.call("DocumentService_CreateNote", { workspaceId: ws.workspace.id }, { title: `Notes on ${noteTitle ?? "a shared note"}`, markdown: run.text }, { idempotencyKey: true });
      if (res.note) {
        ws.upsertNote(res.note);
        navigate(`/notes/${res.note.id}`);
      }
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  const suggestions = runs.flatMap((r) => r.suggestions.filter((s) => !noteId || s.noteId === noteId));
  const pending = suggestions.filter((s) => s.state === "pending");
  useHotkey("Enter", () => pending.forEach((s) => onSuggestion(s, true)), { meta: true });
  const step = (by: number) => {
    if (suggestions.length === 0) return;
    const next = Math.max(0, Math.min(suggestions.length - 1, cursor + by));
    setCursor(next);
    document.getElementById(`sug-${next}`)?.focus();
  };
  useHotkey("j", () => step(1));
  useHotkey("k", () => step(-1));

  return (
    <section className="pane r" aria-label="Agent" style={{ flex: "1 1 340px", maxWidth: 400, minWidth: 300 }}>
      <div className="tabs" role="tablist" style={{ "--c": "var(--vio)" } as React.CSSProperties}>
        <button type="button" role="tab" aria-selected={tab === "chat"} className={tab === "chat" ? "on" : ""} onClick={() => setTab("chat")}>
          Chat
        </button>
        <button type="button" role="tab" aria-selected={tab === "tasks"} className={tab === "tasks" ? "on" : ""} onClick={() => setTab("tasks")}>
          Tasks
        </button>
        <span style={{ flex: 1 }} />
        {role && (
          <span style={{ alignSelf: "center" }} title="The agent can do what you can do in this note">
            <NoteRoleBadge role={role} suffix={`acts as you · ${role.replace("NOTE_ROLE_", "").toLowerCase()}`} />
          </span>
        )}
      </div>
      {tab === "tasks" ? (
        <ChatList />
      ) : (
        <div className="pbody" style={{ display: "grid", gap: 12, alignContent: "start" }} aria-live="polite">
          {runs.length === 0 && (
            <p className="cap" style={{ lineHeight: 1.6 }}>
              Ask about {noteTitle ? `“${noteTitle}”` : "your notes"}, or tell the agent what to change. It reads only notes you can open, and asks before it shares or deletes anything.
            </p>
          )}
          <div className="chat">
            {runs.map((run) => (
              <RunView
                key={run.runId}
                run={run}
                open={stepsOpen[run.runId] ?? !run.finished}
                onToggle={() => setStepsOpen((s) => ({ ...s, [run.runId]: !(s[run.runId] ?? !run.finished) }))}
                onAnswer={(id, ok) => void answer(run, id, ok)}
                onCancel={() => void cancel(run)}
                viewer={viewer}
                onDraft={() => void draftInMyNotes(run)}
              />
            ))}
          </div>
          {suggestions.length > 0 && (
            <section className="ai-card" aria-label="Suggested edits">
              <div className="h">
                <Spark />
                {pending.length === 0 ? "All suggestions resolved" : pending.length === 1 ? "1 suggested edit" : `${pending.length} suggested edits`}
                <span className="sp" />
                <span className="cap" style={{ letterSpacing: 0, textTransform: "none" }}>
                  in this note
                </span>
              </div>
              <div className="gr-nb" role="list" aria-label="Suggestions; J and K move between them">
                {suggestions.map((s, i) => (
                  <div key={s.id} role="listitem" className="r" style={{ display: "grid", gridTemplateColumns: "18px minmax(0,1fr) auto", gap: 8, alignItems: "center", padding: "5px 4px", borderRadius: 7, background: i === cursor ? "var(--raise)" : undefined }}>
                    <span className="m" style={{ color: s.find ? "var(--warn)" : "var(--grn)", fontWeight: 700 }}>
                      {s.find ? "~" : "+"}
                    </span>
                    <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                      {s.find ? `${s.find} → ${s.replace}` : `Insert · ${s.replace}`}
                    </span>
                    {s.state === "pending" ? (
                      <span className="ai-acts">
                        <button id={`sug-${i}`} type="button" className="go" onClick={() => onSuggestion(s, true)} onFocus={() => setCursor(i)} style={{ height: 24 }}>
                          Accept
                        </button>
                        <button type="button" onClick={() => onSuggestion(s, false)} style={{ height: 24 }}>
                          Reject
                        </button>
                      </span>
                    ) : (
                      <em className="cap">{s.state}</em>
                    )}
                  </div>
                ))}
              </div>
              <div className="ai-acts">
                {pending.length > 0 ? (
                  <>
                    <button type="button" className="go" onClick={() => pending.forEach((s) => onSuggestion(s, true))}>
                      Accept all <kbd style={{ background: "none", borderColor: "currentColor", color: "inherit" }}>⌘↵</kbd>
                    </button>
                    <button type="button" onClick={() => pending.forEach((s) => onSuggestion(s, false))}>
                      Reject all
                    </button>
                  </>
                ) : null}
                <span className="cap" style={{ marginLeft: "auto", alignSelf: "center" }}>
                  J / K to step
                </span>
              </div>
            </section>
          )}
        </div>
      )}
      {viewer && (
        <div style={{ padding: "0 14px 6px" }} className="cap">
          Answers use only notes you can open.
        </div>
      )}
      <form className="ask" style={{ margin: "10px 14px 14px" }} onSubmit={(e) => void send(e)}>
        <label htmlFor="agent-in" className="sr">
          Message the agent
        </label>
        <textarea
          id="agent-in"
          ref={input}
          className="inp"
          rows={2}
          placeholder={viewer ? "Ask about this note…" : "Ask, or tell the agent what to change…"}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <button className="btn" type="submit" disabled={!text.trim()}>
          Send
        </button>
      </form>
    </section>
  );
}

function RunView({ run, open, onToggle, onAnswer, onCancel, viewer, onDraft }: { run: AgentRun; open: boolean; onToggle: () => void; onAnswer: (id: string, ok: boolean) => void; onCancel: () => void; viewer: boolean; onDraft: () => void }) {
  const ws = useWorkspace();
  const working = !run.finished;
  const failed = run.finished && run.finished.outcome !== "OUTCOME_COMPLETED";
  return (
    <>
      {run.userText && <div className="msg me">{run.userText}</div>}
      {(run.steps.length > 0 || working) && (
        <div className="sk-steps">
          {working ? (
            <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "8px 10px", color: "var(--mut)" }} role="status">
              <Spinner />
              <b className="ai-shim">Working</b>
              step {run.steps.length + 1}
              <button type="button" className="btn xs ghost" style={{ marginLeft: "auto", height: 22 }} onClick={onCancel}>
                Stop
              </button>
            </div>
          ) : (
            <button type="button" onClick={onToggle} aria-expanded={open}>
              <span style={{ color: failed ? "var(--warn)" : "var(--grn)" }}>●</span>
              <b>{run.steps.length} steps</b>
              {stepsSummary(run)}
              <span style={{ marginLeft: "auto" }}>{open ? "hide" : "show"}</span>
            </button>
          )}
          {(open || working) && run.steps.length > 0 && (
            <ol>
              {run.steps.map((s) => (
                <li key={s.callId}>
                  <i className={s.isError ? "err" : s.done ? "" : "run"} />
                  <span>{describeStep(s)}</span>
                  <em>{s.summary ?? "…"}</em>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
      {run.text && <div className="msg ai">{withCitationMarks(run.text, run.citations.length)}</div>}
      {run.confirmations.map((c) => (
        <div key={c.confirmationId} className="sk-confirm" role="group" aria-label="Confirmation">
          <div className="h">
            <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
              <rect x="3.5" y="7" width="9" height="7" rx="1.5" />
              <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
            </svg>
            Confirm · {c.tool.replace(/_/g, " ")}
          </div>
          <p>{c.description}</p>
          {c.state === "waiting" ? (
            <div className="ai-acts">
              <button type="button" className="bad" onClick={() => onAnswer(c.confirmationId, true)}>
                Confirm
              </button>
              <button type="button" onClick={() => onAnswer(c.confirmationId, false)}>
                Cancel
              </button>
              <span className="cap" style={{ marginLeft: "auto", alignSelf: "center" }}>
                nothing changes until you confirm
              </span>
            </div>
          ) : (
            <span className="done">{c.state === "approved" ? "Confirmed" : "Not done"}</span>
          )}
        </div>
      ))}
      {viewer && run.finished && run.text && (
        <div className="ai-acts">
          <button type="button" className="go" onClick={onDraft}>
            Draft in my notes
          </button>
          <button type="button" onClick={() => void navigator.clipboard?.writeText(run.text)}>
            Copy answer
          </button>
        </div>
      )}
      {run.citations.map((c, i) => {
        const note = c.location.noteId ? ws.notesById.get(c.location.noteId) : undefined;
        return (
          <Link key={i} className="sk-cite" to={`/notes/${c.location.noteId}#blk-${c.location.blockId}`}>
            <span>{i + 1}</span>
            <span>
              <b>{note?.title ?? "A note"}</b> › block {c.location.blockId?.split(":")[1]}
              <small>“{c.quote}”</small>
            </span>
          </Link>
        );
      })}
    </>
  );
}

/** Turns the agent's [1]-style citation labels into superscripts. */
function withCitationMarks(text: string, count: number): ReactNode[] {
  return text.split(/(\[\d+\])/g).map((part, i) => {
    const m = /^\[(\d+)\]$/.exec(part);
    if (m && Number(m[1]) <= count) return <sup key={i}>{m[1]}</sup>;
    return <span key={i}>{part}</span>;
  });
}

function ChatList() {
  const { api } = useServices();
  const ws = useWorkspace();
  const [chats, setChats] = useState<{ chatId?: string; title?: string; updatedAt?: string }[] | null>(null);
  useEffect(() => {
    if (!ws.workspace) return;
    api
      .call("AgentService_ListChats", { workspaceId: ws.workspace.id, "page.pageSize": 30 })
      .then((r) => setChats(r.chats ?? []))
      .catch(() => setChats([]));
  }, [api, ws.workspace]);
  return (
    <div className="pbody" style={{ display: "grid", gap: 6, alignContent: "start" }}>
      {chats === null && <span className="sk" style={{ width: "70%" }} />}
      {chats?.length === 0 && <span className="cap">No chats yet.</span>}
      {chats?.map((c) => (
        <div key={c.chatId} className="frow" style={{ height: "auto", padding: "8px 10px" }}>
          <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{c.title || "Untitled chat"}</span>
          <span>{c.updatedAt ? new Date(c.updatedAt).toLocaleDateString() : ""}</span>
        </div>
      ))}
      <span className="cap" style={{ paddingTop: 8 }}>
        Earlier messages need a list-messages RPC; runs you start here show in Chat.
      </span>
    </div>
  );
}
