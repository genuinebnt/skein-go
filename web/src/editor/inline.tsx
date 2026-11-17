/**
 * Inline Markdown in two forms:
 *
 * - `renderInline`: the formatted view of a block that does not hold the
 *   cursor. Delimiters are hidden, wiki links resolve to notes, and
 *   decorations (comment ranges, agent suggestions, other people's cursors)
 *   are laid over the text. Every text run keeps its source offset in
 *   `data-s`, so a click maps back to a caret position in the source.
 * - `renderMirror`: the block holding the cursor shows its source, with
 *   delimiters dimmed around their content ("Markdown shows only around the
 *   cursor"). It sits behind a transparent textarea, so it must render
 *   every source character with the same metrics: emphasis is drawn with
 *   an oblique synthetic and strong with a hairline stroke, neither of which
 *   changes advance widths.
 *
 * Offsets are UTF-16 within the block (the WASM wrapper converts).
 */

import type { ReactNode } from "react";
import type { InlineNode } from "../crdt/wasm";

export type RangeDecoration = { start: number; end: number; className: string; title?: string; onClick?: () => void; key: string };
export type PointDecoration = { at: number; node: ReactNode; key: string };
export type Decorations = { ranges: RangeDecoration[]; points: PointDecoration[]; before?: Record<number, ReactNode> };

export type InlineContext = {
  resolveWikiLink: (title: string) => string | null;
  onOpenNote?: (noteId: string) => void;
};

/** Splits [start, end) of the source at decoration boundaries and wraps each piece. */
function decoratedText(text: string, start: number, end: number, deco: Decorations, keyBase: string): ReactNode[] {
  const cuts = new Set([start, end]);
  for (const r of deco.ranges) {
    if (r.start > start && r.start < end) cuts.add(r.start);
    if (r.end > start && r.end < end) cuts.add(r.end);
  }
  for (const p of deco.points) if (p.at > start && p.at < end) cuts.add(p.at);
  for (const k of Object.keys(deco.before ?? {})) {
    const at = Number(k);
    if (at > start && at < end) cuts.add(at);
  }
  const points = [...cuts].sort((a, b) => a - b);
  const out: ReactNode[] = [];
  for (let i = 0; i < points.length - 1; i++) {
    const s = points[i]!;
    const e = points[i + 1]!;
    if (deco.before?.[s] !== undefined && s !== end) out.push(<span key={`${keyBase}b${s}`}>{deco.before[s]}</span>);
    for (const p of deco.points) if (p.at === s && s !== end) out.push(<span key={`${keyBase}p${p.key}`}>{p.node}</span>);
    let node: ReactNode = (
      <span key={`${keyBase}t${s}`} data-s={s}>
        {text.slice(s, e)}
      </span>
    );
    for (const r of deco.ranges) {
      if (r.start <= s && r.end >= e) {
        node = r.onClick ? (
          <span key={`${keyBase}r${r.key}${s}`} className={r.className} title={r.title} role="button" tabIndex={0} onClick={r.onClick} onKeyDown={(ev) => (ev.key === "Enter" || ev.key === " ") && r.onClick?.()}>
            {node}
          </span>
        ) : (
          <span key={`${keyBase}r${r.key}${s}`} className={r.className} title={r.title}>
            {node}
          </span>
        );
      }
    }
    out.push(node);
  }
  return out;
}

export function renderInline(text: string, nodes: InlineNode[], deco: Decorations, ctx: InlineContext, keyBase = "n"): ReactNode[] {
  const out = nodes.map((n, i): ReactNode => {
    const key = `${keyBase}-${i}`;
    const inner = () => decoratedText(text, n.contentStart, n.contentEnd, deco, key);
    switch (n.kind) {
      case "text":
        return <span key={key}>{inner()}</span>;
      case "escape":
        return (
          <span key={key} data-s={n.contentStart}>
            {n.value}
          </span>
        );
      case "code":
        return <code key={key}>{inner()}</code>;
      case "emphasis":
        return <em key={key}>{renderInline(text, n.children, deco, ctx, key)}</em>;
      case "strong":
        return <strong key={key}>{renderInline(text, n.children, deco, ctx, key)}</strong>;
      case "link":
        return (
          <a key={key} href={n.value} target="_blank" rel="noopener noreferrer">
            {renderInline(text, n.children, deco, ctx, key)}
          </a>
        );
      case "wikilink": {
        const target = ctx.resolveWikiLink(n.value);
        return target ? (
          <a
            key={key}
            className="wl"
            href={`/notes/${target}`}
            onClick={(e) => {
              if (!ctx.onOpenNote) return;
              e.preventDefault();
              e.stopPropagation();
              ctx.onOpenNote(target);
            }}
          >
            {inner()}
          </a>
        ) : (
          <span key={key} className="wl dangling" title="No note has this title yet" style={{ borderBottom: "1px dashed var(--warn)" }}>
            {inner()}
          </span>
        );
      }
      case "mention":
        return (
          <span key={key} className="mn">
            @{inner()}
          </span>
        );
    }
  });
  // A cursor at the very end of the block has no character after it.
  for (const p of deco.points) if (p.at >= text.length && keyBase === "n") out.push(<span key={`end${p.key}`}>{p.node}</span>);
  return out;
}

const KIND_CLASS: Partial<Record<InlineNode["kind"], string>> = { strong: "strong", emphasis: "em", code: "code", wikilink: "wl", mention: "mention", link: "link" };

/** The source of a block with dimmed delimiters, for the mirror behind the textarea. */
export function renderMirror(text: string, nodes: InlineNode[], keyBase = "m"): ReactNode[] {
  const out: ReactNode[] = [];
  nodes.forEach((n, i) => {
    const key = `${keyBase}-${i}`;
    const cls = KIND_CLASS[n.kind];
    if (n.start < n.contentStart) {
      out.push(
        <span key={`${key}o`} className="sk-md">
          {text.slice(n.start, n.contentStart)}
        </span>,
      );
    }
    const content = n.children.length > 0 ? renderMirror(text, n.children, key) : text.slice(n.contentStart, n.contentEnd);
    out.push(
      <span key={key} className={cls}>
        {content}
      </span>,
    );
    if (n.contentEnd < n.end) {
      out.push(
        <span key={`${key}c`} className="sk-md">
          {text.slice(n.contentEnd, n.end)}
        </span>,
      );
    }
  });
  return out;
}

/** The source offset of a click inside a rendered block, from the `data-s` runs. */
export function offsetFromSelection(container: HTMLElement): number | null {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0) return null;
  const node = sel.anchorNode;
  if (!node || !container.contains(node)) return null;
  const run = (node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as Element))?.closest("[data-s]");
  if (!run) return null;
  return Number(run.getAttribute("data-s")) + (node.nodeType === Node.TEXT_NODE ? sel.anchorOffset : 0);
}
