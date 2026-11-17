/**
 * Comment threads on a note (EditorStates board: quote, replies, reply box,
 * resolve, J/K between threads). The API has no comment RPCs yet: this uses
 * the proposed routes (GET/POST /v1/notes/{id}/comments, POST
 * /v1/comments/{id}:resolve), which mock mode serves from the seed's
 * comments. Live, the panel says the feature is waiting for the backend.
 */

import { useEffect, useRef, useState } from "react";
import { useHotkey } from "../lib/hooks";
import { describeError } from "../api/errors";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Avatar, Pending } from "../components/ui";
import { relativeTime } from "../lib/format";

export type Comment = { id: string; noteId: string; blockId: string; start: number; end: number; quote: string; parentId: string | null; authorId: string; body: string; createdAt: string; resolved: boolean };

type Props = { noteId: string; available: boolean; comments: Comment[]; focus: string | null; canComment: boolean; onChanged: () => void; onClose: () => void };

export function CommentsPanel({ noteId, available, comments, focus, canComment, onChanged, onClose }: Props) {
  const { api } = useServices();
  const ws = useWorkspace();
  const ui = useUi();
  const [showResolved, setShowResolved] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const refs = useRef(new Map<string, HTMLDivElement>());
  const roots = comments.filter((c) => !c.parentId && (showResolved || !c.resolved));

  useEffect(() => {
    if (focus) refs.current.get(focus)?.scrollIntoView({ block: "nearest" });
  }, [focus]);
  const move = (by: number) => {
    const items = [...refs.current.values()];
    const i = items.findIndex((el) => el.contains(document.activeElement));
    items[Math.max(0, Math.min(items.length - 1, i + by))]?.focus();
  };
  useHotkey("j", () => move(1));
  useHotkey("k", () => move(-1));

  async function reply(root: Comment) {
    const body = drafts[root.id]?.trim();
    if (!body) return;
    try {
      await api.proposed("POST", `/v1/notes/${noteId}/comments`, { parentId: root.id, blockId: root.blockId, start: root.start, end: root.end, quote: root.quote, body });
      setDrafts((d) => ({ ...d, [root.id]: "" }));
      onChanged();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  async function resolve(root: Comment) {
    try {
      await api.proposed("POST", `/v1/comments/${root.id}:resolve`, {});
      onChanged();
    } catch (error) {
      ui.toast(describeError(error), "bad");
    }
  }

  return (
    <section className="pane r" aria-label="Comments" style={{ flex: "1 1 340px", maxWidth: 400, minWidth: 300 }}>
      <div className="tabs" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
        <button type="button" className="on">
          Comments
        </button>
        <button type="button" aria-pressed={showResolved} className={showResolved ? "on" : ""} onClick={() => setShowResolved((s) => !s)}>
          Resolved
        </button>
        <span style={{ flex: 1 }} />
        <button type="button" onClick={onClose} aria-label="Close comments">
          ×
        </button>
      </div>
      <div className="pbody comment-pane" role="feed" aria-label="Comment threads">
        {!available && (
          <div className="sk-banner" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
            <span>Comments are designed but the API has no comment routes yet. In mock mode they come from the seed.</span>
            <Pending />
          </div>
        )}
        {available && roots.length === 0 && <span className="cap">No open comments. Select text in the note to start one.</span>}
        {roots.map((root, i) => {
          const replies = comments.filter((c) => c.parentId === root.id);
          return (
            <div key={root.id} ref={(el) => void (el ? refs.current.set(root.id, el) : refs.current.delete(root.id))} className={`comment-thread${root.resolved ? " resolved" : ""}`} role="article" tabIndex={-1} aria-label={`Thread ${i + 1} of ${roots.length}`} style={focus === root.id ? { borderColor: "var(--warn)" } : undefined}>
              <div className="q">“{root.quote}”</div>
              {[root, ...replies].map((c) => (
                <div key={c.id} className="comment">
                  <Avatar id={c.authorId} name={ws.userName(c.authorId)} />
                  <div style={{ display: "grid", gap: 2 }}>
                    <span>
                      <b>{ws.userName(c.authorId)}</b> <span className="cap">· {relativeTime(c.createdAt)}</span>
                    </span>
                    <span>{c.body}</span>
                  </div>
                </div>
              ))}
              {canComment && !root.resolved && (
                <>
                  <label className="sr" htmlFor={`r-${root.id}`}>
                    Reply
                  </label>
                  <input id={`r-${root.id}`} className="inp" placeholder="Reply, @ to mention…" style={{ height: 34 }} value={drafts[root.id] ?? ""} onChange={(e) => setDrafts((d) => ({ ...d, [root.id]: e.target.value }))} onKeyDown={(e) => e.key === "Enter" && void reply(root)} />
                  <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
                    <button className="btn xs go" type="button" onClick={() => void reply(root)}>
                      Reply
                    </button>
                    <button className="btn xs" type="button" onClick={() => void resolve(root)}>
                      Resolve
                    </button>
                    <span className="cap" style={{ marginLeft: "auto" }}>
                      {i + 1} of {roots.length} · J / K
                    </span>
                  </div>
                </>
              )}
            </div>
          );
        })}
      </div>
    </section>
  );
}
