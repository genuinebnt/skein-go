/**
 * The block editor (Main and EditorStates boards). Blocks render formatted
 * until the cursor enters one; that block then shows its Markdown source in
 * a textarea over a highlighted mirror, so the delimiters appear only around
 * the cursor. Every change goes through NoteSession as CRDT operations:
 * typing is a splice, Enter inserts a block marker, Backspace at a block's
 * start deletes one, and type changes are attribute writes.
 *
 * Positions are UTF-16 within a block here; the WASM wrapper converts to
 * code points (ADR-0003). The caret survives remote edits by being stored as
 * an element ID and resolved again after each change.
 */

import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import type { AgentRun, Suggestion } from "../agent/run";
import type { NoteSession } from "../crdt/noteSession";
import { TextIndex } from "../crdt/utf16";
import type { Block, BlockAttrs, CrdtModule } from "../crdt/wasm";
import { colorFor } from "../lib/format";
import { CheckIcon, CrossIcon, DragIcon, PlusIcon } from "../components/ui";
import { highlightLine } from "./highlight";
import { offsetFromSelection, renderInline, renderMirror, type Decorations, type InlineContext } from "./inline";
import { DEFAULT_ATTRS, attrsAfterSplit, diffText, filterSlash, isOnFirstLine, isOnLastLine, listNumbers, markdownShortcut, occurrences, slashQuery, tableRows, type SlashItem } from "./model";

export type CommentThread = { id: string; blockId: string; start: number; end: number; quote: string; replies: number; authorId: string; resolved: boolean };

export type EditorHandle = {
  focusBlock: (blockId: string, offset?: number) => void;
  /** Mobile toolbar and shortcuts: act on the active block. */
  wrapSelection: (before: string, after?: string) => void;
  insertAtCaret: (text: string) => void;
  openSlash: () => void;
  moveActive: (direction: -1 | 1) => void;
  appendLine: (text: string) => void;
};

type Props = {
  session: NoteSession;
  crdt: CrdtModule;
  readOnly: boolean;
  comments: CommentThread[];
  onOpenComment: (id: string) => void;
  suggestions: Suggestion[];
  onSuggestion: (s: Suggestion, accept: boolean) => void;
  userName: (id: string | undefined) => string;
  inline: InlineContext;
  onAskAgent: (prompt: string) => void;
  onActive?: (blockIndex: number | null) => void;
  onError: (message: string) => void;
  revision: number;
  /** The note was just created here: open it on an empty block, ready to type. */
  startWriting?: boolean;
  onStarted?: () => void;
};

type Active = { blockId: string; caret: number };
type Slash = { blockId: string; from: number; query: string; index: number };

const PLACEHOLDER = "Type / for blocks, [[ to link a note, @ to mention";

export const NoteEditor = forwardRef<EditorHandle, Props>(function NoteEditor(props, ref) {
  const { session, crdt, readOnly, revision } = props;
  const blocks = useMemo(() => session.doc.blocks(), [session, revision]); // eslint-disable-line react-hooks/exhaustive-deps
  const [active, setActive] = useState<Active | null>(null);
  const [slash, setSlash] = useState<Slash | null>(null);
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const textareas = useRef(new Map<string, HTMLTextAreaElement>());
  const numbers = useMemo(() => listNumbers(blocks), [blocks]);

  const activeIndex = active ? blocks.findIndex((b) => b.id === active.blockId) : -1;
  useEffect(() => props.onActive?.(activeIndex >= 0 ? activeIndex : null), [activeIndex]); // eslint-disable-line react-hooks/exhaustive-deps

  const focusBlock = useCallback((blockId: string, offset?: number) => {
    setActive({ blockId, caret: offset ?? Number.MAX_SAFE_INTEGER });
    setMenuFor(null);
  }, []);

  const { startWriting, onStarted } = props;
  const started = useRef(false);
  useEffect(() => {
    if (!startWriting || started.current || readOnly || blocks.length > 0) return;
    started.current = true;
    focusBlock(session.appendBlock(), 0);
    onStarted?.();
  }, [startWriting, onStarted, readOnly, blocks.length, focusBlock, session]);

  const run = useCallback(
    (work: () => Promise<void> | void) => {
      try {
        void Promise.resolve(work()).catch((e: Error) => props.onError(e.message));
      } catch (e) {
        props.onError((e as Error).message);
      }
    },
    [props],
  );

  const setAttrs = useCallback((block: Block, patch: Partial<BlockAttrs>) => run(() => session.setBlockAttrs(block.id, { type: block.type, indent: block.indent, checked: block.checked, language: block.language, ...patch })), [run, session]);

  /** Moves a block up or down: v1 has no move operation, so it is delete and reinsert (ADR-0005). */
  const moveBlock = useCallback(
    (block: Block, direction: -1 | 1) => {
      const index = blocks.findIndex((b) => b.id === block.id);
      const neighbour = blocks[index + direction];
      if (!neighbour || index === 0 && direction === -1) return;
      run(async () => {
        const anchor = direction === 1 ? neighbour : blocks[index - 2];
        if (!anchor) return;
        await session.deleteBlock(block.id);
        const fresh = session.doc.block(anchor.id)!;
        const newId = session.splitBlock(anchor.id, fresh.text.length);
        if (block.text) await session.insertText(newId, 0, block.text);
        await session.setBlockAttrs(newId, { type: block.type, indent: block.indent, checked: block.checked, language: block.language });
        focusBlock(newId);
      });
    },
    [blocks, run, session, focusBlock],
  );

  useImperativeHandle(
    ref,
    () => ({
      focusBlock,
      wrapSelection: (before, after = before) => {
        const ta = active && textareas.current.get(active.blockId);
        if (!ta || !active) return;
        const { selectionStart: s, selectionEnd: e } = ta;
        run(async () => {
          await session.insertText(active.blockId, e, after);
          await session.insertText(active.blockId, s, before);
          setActive({ blockId: active.blockId, caret: e + before.length });
        });
      },
      insertAtCaret: (text) => {
        const ta = active && textareas.current.get(active.blockId);
        if (!ta || !active) return;
        const at = ta.selectionStart;
        run(async () => {
          await session.insertText(active.blockId, at, text);
          setActive({ blockId: active.blockId, caret: at + text.length });
        });
      },
      openSlash: () => {
        const ta = active && textareas.current.get(active.blockId);
        if (!ta || !active) return;
        const at = ta.selectionStart;
        run(async () => {
          await session.insertText(active.blockId, at, "/");
          setActive({ blockId: active.blockId, caret: at + 1 });
          setSlash({ blockId: active.blockId, from: at, query: "", index: 0 });
        });
      },
      moveActive: (direction) => {
        const block = active && session.doc.block(active.blockId);
        if (block) moveBlock(block, direction);
      },
      appendLine: (text) => {
        run(async () => {
          const id = session.appendBlock();
          await session.insertText(id, 0, text);
        });
      },
    }),
    [active, focusBlock, moveBlock, run, session],
  );

  const chooseSlash = useCallback(
    (block: Block, item: SlashItem, language: string) => {
      if (!slash) return;
      const end = slash.from + 1 + slash.query.length;
      setSlash(null);
      run(async () => {
        await session.deleteText(block.id, slash.from, end);
        if (item.action === "agent") {
          props.onAskAgent("");
          return;
        }
        await session.setBlockAttrs(block.id, { ...DEFAULT_ATTRS, indent: block.indent, ...item.attrs, ...(item.id === "code" ? { language } : {}) });
        if (item.id === "divider") {
          const next = session.splitBlock(block.id, session.doc.block(block.id)!.text.length);
          focusBlock(next, 0);
        } else setActive({ blockId: block.id, caret: slash.from });
      });
    },
    [slash, run, session, props, focusBlock],
  );

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>, block: Block, index: number) {
    const ta = e.currentTarget;
    const caret = ta.selectionStart;
    const collapsed = ta.selectionStart === ta.selectionEnd;
    const mod = e.metaKey || e.ctrlKey;
    if (slash && slash.blockId === block.id) {
      const { items, language } = filterSlash(slash.query);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const n = Math.max(1, items.length);
        setSlash({ ...slash, index: (slash.index + (e.key === "ArrowDown" ? 1 : n - 1)) % n });
        return;
      }
      if ((e.key === "Enter" || e.key === "Tab") && items[slash.index]) {
        e.preventDefault();
        chooseSlash(block, items[slash.index]!, language);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setSlash(null); // keep the slash as text, as the board says
        return;
      }
    }
    if (mod && !e.shiftKey && e.key.toLowerCase() === "z") {
      e.preventDefault();
      run(() => session.undo("undo"));
      return;
    }
    if (mod && e.shiftKey && e.key.toLowerCase() === "z") {
      e.preventDefault();
      run(() => session.undo("redo"));
      return;
    }
    if (mod && ["b", "i", "e"].includes(e.key.toLowerCase())) {
      e.preventDefault();
      const marks: Record<string, string> = { b: "**", i: "*", e: "`" };
      const mark = marks[e.key.toLowerCase()]!;
      const { selectionStart: s, selectionEnd: en } = ta;
      run(async () => {
        await session.insertText(block.id, en, mark);
        await session.insertText(block.id, s, mark);
        setActive({ blockId: block.id, caret: en + mark.length });
      });
      return;
    }
    if (e.altKey && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
      e.preventDefault();
      moveBlock(block, e.key === "ArrowUp" ? -1 : 1);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !(block.type === "code" && !mod)) {
      e.preventDefault();
      run(async () => {
        if (!collapsed) await session.deleteText(block.id, ta.selectionStart, ta.selectionEnd);
        const current = session.doc.block(block.id)!;
        const at = block.type === "code" ? current.text.length : Math.min(caret, current.text.length);
        const { next, convertCurrent } = attrsAfterSplit(current, current.text);
        if (convertCurrent) {
          await session.setBlockAttrs(block.id, convertCurrent);
          return;
        }
        const newId = session.splitBlock(block.id, at);
        if (next) await session.setBlockAttrs(newId, next);
        focusBlock(newId, 0);
      });
      return;
    }
    if (e.key === "Backspace" && collapsed && caret === 0) {
      e.preventDefault();
      if (block.type !== "paragraph") return setAttrs(block, { type: "paragraph", checked: false, language: "" });
      if (block.indent > 0) return setAttrs(block, { indent: block.indent - 1 });
      const prev = blocks[index - 1];
      if (!prev || prev.start === 0) return;
      run(async () => {
        if (prev.type === "divider") {
          await session.deleteBlock(prev.id);
          return focusBlock(block.id, 0);
        }
        const joinAt = prev.text.length;
        await session.mergeWithPrevious(block.id);
        focusBlock(prev.id, joinAt);
      });
      return;
    }
    if (e.key === "Delete" && collapsed && caret === ta.value.length && blocks[index + 1]) {
      e.preventDefault();
      run(() => session.mergeWithPrevious(blocks[index + 1]!.id));
      return;
    }
    if (e.key === "Tab" && (block.type === "bulleted" || block.type === "numbered" || block.type === "todo")) {
      e.preventDefault();
      setAttrs(block, { indent: Math.max(0, Math.min(8, block.indent + (e.shiftKey ? -1 : 1))) });
      return;
    }
    if (e.key === "Tab" && block.type === "code" && !e.shiftKey) {
      e.preventDefault();
      run(async () => {
        await session.insertText(block.id, caret, "    ");
        setActive({ blockId: block.id, caret: caret + 4 });
      });
      return;
    }
    if (e.key === "ArrowUp" && collapsed && isOnFirstLine(ta.value, caret) && blocks[index - 1]) {
      e.preventDefault();
      focusBlock(blocks[index - 1]!.id);
      return;
    }
    if (e.key === "ArrowDown" && collapsed && isOnLastLine(ta.value, caret) && blocks[index + 1]) {
      e.preventDefault();
      focusBlock(blocks[index + 1]!.id, 0);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      setActive(null);
      setTimeout(() => document.getElementById(`view-${block.id}`)?.focus(), 0);
    }
  }

  /**
   * Turns a textarea change into CRDT edits. The base is the document, not the
   * rendered block: a keystroke can arrive before the previous one has
   * re-rendered (the session emits after its IndexedDB write), and diffing
   * against stale text would insert again what was already typed. For the
   * same reason every edit is applied to the document before anything is
   * awaited; only the writes to IndexedDB are waited on, together.
   */
  function onInput(block: Block, value: string, caret: number) {
    const base = session.doc.block(block.id)?.text ?? block.text;
    const change = diffText(base, value, caret);
    if (!change) return;
    run(() => {
      const writes: Promise<void>[] = [];
      const settled = () => Promise.all(writes).then(() => undefined);
      if (change.deleteCount > 0) writes.push(session.deleteText(block.id, change.start, change.start + change.deleteCount));
      if (change.insert && block.type !== "code" && change.insert.includes("\n")) {
        // Pasted or typed newlines inside a non-code block become new blocks, as Enter would.
        const lines = change.insert.split("\n");
        let target = block.id;
        let at = change.start;
        lines.forEach((line, i) => {
          if (line) writes.push(session.insertText(target, at, line));
          if (i < lines.length - 1) {
            target = session.splitBlock(target, at + line.length);
            at = 0;
          }
        });
        focusBlock(target, lines[lines.length - 1]!.length);
        return settled();
      }
      if (change.insert) writes.push(session.insertText(block.id, change.start, change.insert));
      const after = session.doc.block(block.id);
      if (!after) return settled();
      const shortcut = after.type === "paragraph" ? markdownShortcut(after.text) : null;
      if (shortcut) {
        writes.push(session.deleteText(block.id, 0, shortcut.strip));
        writes.push(session.setBlockAttrs(block.id, { ...DEFAULT_ATTRS, indent: after.indent, ...shortcut.attrs }));
        if (shortcut.attrs.type === "divider") focusBlock(session.splitBlock(block.id, 0), 0);
        else setActive({ blockId: block.id, caret: Math.max(0, caret - shortcut.strip) });
        return settled();
      }
      const q = slashQuery(after.text, caret);
      setSlash(q ? { blockId: block.id, from: q.from, query: q.query, index: 0 } : null);
      setActive({ blockId: block.id, caret });
      return settled();
    });
  }

  if (blocks.length === 0) {
    return (
      <div className="sk-empty">
        <b>This note is empty.</b>
        {!readOnly && (
          <button type="button" className="btn sm go" onClick={() => focusBlock(session.appendBlock(), 0)}>
            Start writing
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="px" style={{ marginTop: 18 }} role="list" aria-label="Blocks">
      {blocks.map((block, index) => (
        <BlockRow
          key={block.id}
          block={block}
          index={index}
          number={numbers[index] ?? 0}
          active={active?.blockId === block.id ? active : null}
          readOnly={readOnly}
          crdt={crdt}
          props={props}
          textareas={textareas.current}
          onActivate={(offset) => !readOnly && block.type !== "divider" && focusBlock(block.id, offset)}
          onKeyDown={(e) => onKeyDown(e, block, index)}
          onInput={(value, caret) => onInput(block, value, caret)}
          onCaret={(caret) => {
            setActive({ blockId: block.id, caret });
            session.setCaret({ blockId: block.id, offset: caret });
          }}
          onBlur={() => setTimeout(() => setActive((a) => (a?.blockId === block.id && !textareas.current.get(block.id)?.matches(":focus") ? null : a)), 0)}
          slash={slash?.blockId === block.id ? slash : null}
          onSlashChoose={(item, language) => chooseSlash(block, item, language)}
          menuOpen={menuFor === block.id}
          onMenu={(open) => setMenuFor(open ? block.id : null)}
          onAddBelow={() => run(() => focusBlock(session.splitBlock(block.id, block.text.length), 0))}
          onSetAttrs={(patch) => setAttrs(block, patch)}
          onDelete={() => run(() => session.deleteBlock(block.id))}
          onMove={(d) => moveBlock(block, d)}
        />
      ))}
      {!readOnly && (
        <button type="button" className="btn xs ghost" style={{ marginTop: 10 }} onClick={() => focusBlock(session.appendBlock(), 0)}>
          <PlusIcon size={12} /> Add a block
        </button>
      )}
    </div>
  );
});

type RowProps = {
  block: Block;
  index: number;
  number: number;
  active: Active | null;
  readOnly: boolean;
  crdt: CrdtModule;
  props: Props;
  textareas: Map<string, HTMLTextAreaElement>;
  onActivate: (offset?: number) => void;
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => void;
  onInput: (value: string, caret: number) => void;
  onCaret: (caret: number) => void;
  onBlur: () => void;
  slash: Slash | null;
  onSlashChoose: (item: SlashItem, language: string) => void;
  menuOpen: boolean;
  onMenu: (open: boolean) => void;
  onAddBelow: () => void;
  onSetAttrs: (patch: Partial<BlockAttrs>) => void;
  onDelete: () => void;
  onMove: (d: -1 | 1) => void;
};

const TYPE_CLASS: Partial<Record<Block["type"], string>> = { heading1: "blk-h1", heading2: "blk-h2", heading3: "blk-h3", quote: "blk-quote", code: "blk-code" };
const LANGUAGES = ["", "go", "rust", "python", "typescript", "sql", "yaml", "toml", "json", "bash"];

function BlockRow(p: RowProps) {
  const { block, props } = p;
  const viewRef = useRef<HTMLDivElement>(null);
  const isList = block.type === "bulleted" || block.type === "numbered" || block.type === "todo";
  const decorations = useBlockDecorations(block, props);
  const body =
    block.type === "divider" ? (
      <hr className="blk-hr" />
    ) : p.active ? (
      <SourceEditor {...p} />
    ) : (
      <div
        ref={viewRef}
        id={`view-${block.id}`}
        className={block.type === "code" || block.type === "table" ? undefined : `blk-view${block.text ? "" : " empty"}`}
        data-placeholder={p.index === 0 ? PLACEHOLDER : ""}
        role="textbox"
        aria-readonly={p.readOnly}
        aria-multiline="true"
        aria-label={`${block.type} block${p.readOnly ? "" : ", press Enter to edit"}`}
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" && e.target === e.currentTarget) {
            e.preventDefault();
            p.onActivate();
          }
        }}
        onMouseUp={() => {
          if (window.getSelection()?.toString()) return; // a selection to copy, not a click to edit
          p.onActivate(viewRef.current ? (offsetFromSelection(viewRef.current) ?? undefined) : undefined);
        }}
      >
        {block.type === "code" ? <CodeView block={block} /> : block.type === "table" ? <TableView text={block.text} /> : renderInline(block.text, p.crdt.renderInline(block.text), decorations, props.inline)}
      </div>
    );

  return (
    <div
      id={`blk-${block.id}`}
      className={`blk ${TYPE_CLASS[block.type] ?? "blk-p"}${p.menuOpen ? " menu-open" : ""}`}
      role="listitem"
      style={{ marginLeft: block.indent * 24 }}
      data-block-id={block.id}
      aria-label={block.type === "divider" ? "Divider" : undefined}
    >
      {!p.readOnly && (
        <div className="ed-gut">
          <button type="button" aria-label="Add a block below" onClick={p.onAddBelow}>
            <PlusIcon />
          </button>
          <button type="button" aria-label="Block menu: turn into, link, move, comment, delete" aria-haspopup="menu" aria-expanded={p.menuOpen} onClick={() => p.onMenu(!p.menuOpen)} style={{ cursor: "grab" }}>
            <DragIcon />
          </button>
        </div>
      )}
      {isList ? (
        <div className={`blk-li${block.checked ? " checked" : ""}`} style={{ "--c": "var(--acc)" } as React.CSSProperties}>
          {block.type === "todo" ? (
            <button type="button" className={`mk todo cbx${block.checked ? " on" : ""}`} role="checkbox" aria-checked={block.checked} aria-label={block.checked ? "Mark not done" : "Mark done"} disabled={p.readOnly} onClick={() => p.onSetAttrs({ checked: !block.checked })}>
              <i>{block.checked ? "✓" : ""}</i>
            </button>
          ) : (
            <span className="mk" aria-hidden="true">
              {block.type === "numbered" ? `${p.number}.` : "▪"}
            </span>
          )}
          {body}
        </div>
      ) : (
        body
      )}
      {p.menuOpen && <BlockMenu {...p} />}
    </div>
  );
}

function useBlockDecorations(block: Block, props: Props): Decorations {
  const { session, comments, suggestions, onOpenComment, onSuggestion, userName } = props;
  return useMemo(() => {
    const deco: Decorations = { ranges: [], points: [], before: {} };
    const index = new TextIndex(block.text);
    for (const c of comments) {
      if (c.blockId !== block.id || c.resolved) continue;
      deco.ranges.push({ key: `c${c.id}`, start: index.toUtf16(c.start), end: index.toUtf16(c.end), className: "ed-cm", title: `${c.replies + 1} comment${c.replies ? "s" : ""}`, onClick: () => onOpenComment(c.id) });
    }
    for (const s of suggestions) {
      if (s.state !== "pending" || !s.replace) continue;
      const hit = occurrences(block.text, s.replace)[0];
      if (!hit) continue;
      deco.ranges.push({ key: `s${s.id}`, start: hit.start, end: hit.end, className: "ai-ins", title: "Agent suggestion" });
      deco.before![hit.start] = <span className="ai-del">{s.find}</span>;
      deco.points.push({
        key: `sm${s.id}`,
        at: hit.end,
        node: (
          <span className="sk-mini">
            <button type="button" className="ok" aria-label="Accept the agent's edit" onClick={(e) => (e.stopPropagation(), onSuggestion(s, true))} onMouseUp={(e) => e.stopPropagation()}>
              <CheckIcon />
            </button>
            <button type="button" aria-label="Reject the agent's edit" onClick={(e) => (e.stopPropagation(), onSuggestion(s, false))} onMouseUp={(e) => e.stopPropagation()}>
              <CrossIcon />
            </button>
          </span>
        ),
      });
    }
    const now = Date.now();
    for (const peer of session.peers.values()) {
      const caret = session.doc.caretOf(peer.anchor);
      if (!caret || caret.blockId !== block.id) continue;
      const name = userName(peer.userId).split(" ")[0];
      deco.points.push({
        key: `p${peer.replicaId}`,
        at: caret.offset,
        node: (
          <span className="sk-cur" style={{ "--c": colorFor(peer.userId), opacity: now - peer.at > 30_000 ? 0.5 : 1 } as React.CSSProperties} role="img" aria-label={`${name}'s cursor`}>
            <span aria-hidden="true">{name}</span>
          </span>
        ),
      });
    }
    const count = comments.filter((c) => c.blockId === block.id && !c.resolved).reduce((n, c) => n + 1 + c.replies, 0);
    const first = comments.find((c) => c.blockId === block.id && !c.resolved);
    if (count > 0 && first) {
      deco.points.push({
        key: "cc",
        at: block.text.length,
        node: (
          <button className="chip sm" type="button" style={{ marginLeft: 8, verticalAlign: 2, "--c": "var(--warn)" } as React.CSSProperties} aria-label={`${count} comments`} onClick={(e) => (e.stopPropagation(), onOpenComment(first.id))} onMouseUp={(e) => e.stopPropagation()}>
            <span className="dot" />
            {count}
          </button>
        ),
      });
    }
    return deco;
  }, [block, comments, suggestions, session, onOpenComment, onSuggestion, userName, session.revision]); // eslint-disable-line react-hooks/exhaustive-deps
}

function SourceEditor(p: RowProps) {
  const { block, active, crdt } = p;
  const ref = useRef<HTMLTextAreaElement | null>(null);
  const [value, setValue] = useState(block.text);
  const composing = useRef(false);
  const anchor = useRef<string | null>(null);
  const session = p.props.session;

  // Remote edits change block.text under the cursor: adopt them and put the caret back on its element.
  useLayoutEffect(() => {
    const ta = ref.current;
    if (!ta || composing.current || block.text === ta.value) {
      if (block.text !== value && !composing.current) setValue(block.text);
      return;
    }
    const caret = anchor.current ? session.doc.caretOf(anchor.current) : null;
    ta.value = block.text;
    setValue(block.text);
    if (caret && caret.blockId === block.id) ta.setSelectionRange(caret.offset, caret.offset);
  }, [block.text]); // eslint-disable-line react-hooks/exhaustive-deps

  useLayoutEffect(() => {
    const ta = ref.current;
    if (!ta || !active) return;
    if (document.activeElement !== ta) ta.focus({ preventScroll: false });
    const at = Math.min(active.caret, ta.value.length);
    if (ta.selectionStart !== at || ta.selectionEnd !== at) ta.setSelectionRange(at, at);
  }, [active?.caret]); // eslint-disable-line react-hooks/exhaustive-deps

  const remember = () => {
    const ta = ref.current;
    if (!ta) return;
    try {
      anchor.current = session.doc.anchorAt({ blockId: block.id, offset: ta.selectionStart });
    } catch {
      anchor.current = null;
    }
    p.onCaret(ta.selectionStart);
  };

  const mirror: ReactNode =
    block.type === "code"
      ? value.split("\n").map((line, i) => (
          <span key={i} className="ln">
            {highlightLine(line, block.language)}
            {"\n"}
          </span>
        ))
      : renderMirror(value, crdt.renderInline(value));

  return (
    <div className="blk-src">
      {block.type === "code" && <CodeHeader block={block} onLanguage={(language) => p.onSetAttrs({ language })} />}
      <div style={{ position: "relative" }}>
        <div className="blk-mirror" aria-hidden="true">
          {mirror}
          {/* keeps a trailing newline's height */}
          {"​"}
        </div>
        <textarea
          ref={(el) => {
            ref.current = el;
            if (el) p.textareas.set(block.id, el);
            else p.textareas.delete(block.id);
          }}
          defaultValue={block.text}
          aria-label={`Edit ${block.type} block`}
          spellCheck={block.type !== "code"}
          onKeyDown={p.onKeyDown}
          onCompositionStart={() => (composing.current = true)}
          onCompositionEnd={(e) => {
            composing.current = false;
            setValue(e.currentTarget.value);
            p.onInput(e.currentTarget.value, e.currentTarget.selectionStart);
          }}
          onInput={(e) => {
            const ta = e.currentTarget;
            setValue(ta.value);
            if (!composing.current) p.onInput(ta.value, ta.selectionStart);
          }}
          onSelect={remember}
          onKeyUp={remember}
          onBlur={p.onBlur}
        />
      </div>
      {p.slash && <SlashMenu slash={p.slash} onChoose={p.onSlashChoose} />}
    </div>
  );
}

function SlashMenu({ slash, onChoose }: { slash: Slash; onChoose: (item: SlashItem, language: string) => void }) {
  const { items, language } = filterSlash(slash.query);
  let group = "";
  return (
    <div className="ed-pop ed-slash floating" style={{ left: 0, top: "calc(100% + 4px)" }} role="listbox" aria-label="Insert a block" id="slash-menu">
      <div className="q">
        Type to filter · <b>/{slash.query || "code"}</b>
      </div>
      {items.length === 0 && <span className="cap" style={{ padding: 10 }}>No block type matches “{slash.query}”.</span>}
      {items.map((item, i) => {
        const header = item.group !== group ? ((group = item.group), <span className="gh" key={`g${item.group}`}>{item.group}</span>) : null;
        return (
          <div key={item.id} style={{ display: "contents" }}>
            {header}
            <button
              type="button"
              role="option"
              aria-selected={i === slash.index}
              className={`ed-opt${i === slash.index ? " on" : ""}`}
              onMouseDown={(e) => {
                e.preventDefault();
                onChoose(item, language);
              }}
            >
              <span className="ic" style={{ "--c": item.colour ?? "var(--fg)" } as React.CSSProperties}>
                {item.icon}
              </span>
              <span>
                <b>{item.label}</b>
                <small>{item.hint}</small>
              </span>
              <kbd>{item.key}</kbd>
            </button>
          </div>
        );
      })}
      <div className="ed-foot">
        <span>
          <kbd>↑</kbd> <kbd>↓</kbd>
        </span>
        <span>
          <kbd>↵</kbd> insert
        </span>
        <span>
          <kbd>esc</kbd> keep the slash
        </span>
      </div>
    </div>
  );
}

function BlockMenu(p: RowProps) {
  const first = useRef<HTMLButtonElement>(null);
  useEffect(() => first.current?.focus(), []);
  const turnInto: [string, Partial<BlockAttrs>][] = [
    ["Text", { type: "paragraph" }],
    ["H1", { type: "heading1" }],
    ["H2", { type: "heading2" }],
    ["H3", { type: "heading3" }],
    ["List", { type: "bulleted" }],
    ["To-do", { type: "todo" }],
    ["Quote", { type: "quote" }],
    ["Code", { type: "code" }],
  ];
  const copyLink = () => void navigator.clipboard?.writeText(`${location.origin}${location.pathname}#blk-${p.block.id}`);
  return (
    <div
      className="ed-pop floating"
      role="menu"
      tabIndex={-1}
      style={{ left: -60, top: 30, padding: 6, display: "grid", gap: 1, width: 280 }}
      onKeyDown={(e) => {
        if (e.key === "Escape") p.onMenu(false);
        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
          e.preventDefault();
          const items = [...e.currentTarget.querySelectorAll<HTMLButtonElement>("[role=menuitem]")];
          const i = items.indexOf(document.activeElement as HTMLButtonElement);
          items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
        }
      }}
    >
      <span className="lab" style={{ padding: "4px 8px" }}>
        Turn into
      </span>
      <div style={{ display: "flex", flexWrap: "wrap", gap: 4, padding: "0 6px 6px" }}>
        {turnInto.map(([label, attrs], i) => (
          <button key={label} ref={i === 0 ? first : undefined} role="menuitem" type="button" className={`chip sm${p.block.type === attrs.type ? " on" : ""}`} onClick={() => (p.onSetAttrs(attrs), p.onMenu(false))}>
            {label}
          </button>
        ))}
      </div>
      <button role="menuitem" type="button" className="frow" onClick={() => (copyLink(), p.onMenu(false))}>
        <span>Copy link to block</span>
        <span>⌘⇧L</span>
      </button>
      <button role="menuitem" type="button" className="frow" onClick={() => (p.onMove(-1), p.onMenu(false))}>
        <span>Move up</span>
        <span>⌥↑</span>
      </button>
      <button role="menuitem" type="button" className="frow" onClick={() => (p.onMove(1), p.onMenu(false))}>
        <span>Move down</span>
        <span>⌥↓</span>
      </button>
      <button role="menuitem" type="button" className="frow" style={{ color: "var(--bad)" }} onClick={() => (p.onDelete(), p.onMenu(false))}>
        <span>Delete block</span>
        <span>⌫</span>
      </button>
    </div>
  );
}

function CodeHeader({ block, onLanguage }: { block: Block; onLanguage?: (language: string) => void }) {
  return (
    <div className="cb-h">
      <span className="fn">code</span>
      {onLanguage ? (
        <select className="lang" aria-label="Code language" value={block.language} onChange={(e) => onLanguage(e.target.value)} style={{ background: "transparent", border: 0, color: "inherit", font: "inherit" }}>
          {LANGUAGES.map((l) => (
            <option key={l} value={l}>
              {l || "plain"}
            </option>
          ))}
        </select>
      ) : (
        <span className="lang">{block.language || "plain"}</span>
      )}
      <span className="sp" />
      <button className="cb-cp" type="button" onClick={() => void navigator.clipboard?.writeText(block.text)}>
        Copy
      </button>
    </div>
  );
}

function CodeView({ block }: { block: Block }) {
  return (
    <figure className="cb" style={{ margin: "10px 0" }}>
      <CodeHeader block={block} />
      <pre>
        {block.text.split("\n").map((line, i) => (
          <span key={i} className="ln">
            <span className="g">{i + 1}</span>
            {highlightLine(line, block.language)}
          </span>
        ))}
      </pre>
    </figure>
  );
}

function TableView({ text }: { text: string }) {
  const [head, ...rows] = tableRows(text);
  return (
    <div style={{ overflowX: "auto", overscrollBehavior: "contain", margin: "10px 0" }}>
      <table className="sk-tbl">
        <thead>
          <tr>{head?.map((c, i) => <th key={i}>{c}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((c, j) => (
                <td key={j}>{c}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export type { AgentRun };
