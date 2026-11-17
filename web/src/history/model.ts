/**
 * Version history's view model (History board): Diff hunks become blocks
 * with an inline word diff and a block status (added, removed, changed,
 * same), and Blame spans give each block its main author.
 *
 * Hunks cover the exported Markdown (docs/spec/blocks.md section 6): blocks
 * are separated by blank lines, list items by single newlines, and fenced
 * code keeps its newlines. Offsets in the new text count code points, as
 * Blame's spans do (ADR-0003).
 */

import type { BlameSpan, DiffHunk } from "../api/schema.gen";

export type SegmentKind = "same" | "ins" | "del";
export type Segment = { text: string; kind: SegmentKind };
export type BlockKind = "h1" | "h2" | "h3" | "p" | "li" | "todo" | "quote" | "code";
export type BlockStatus = "same" | "add" | "rem" | "chg";
export type HistoryBlock = { kind: BlockKind; status: BlockStatus; segments: Segment[]; newStart: number; newEnd: number };

type Line = { segments: Segment[]; newStart: number; newEnd: number };

const codePoints = (s: string) => [...s].length;

/** Splits hunks into lines, each a list of segments, tracking code-point offsets in the new text. */
function lines(hunks: DiffHunk[]): Line[] {
  const out: Line[] = [];
  let current: Line = { segments: [], newStart: 0, newEnd: 0 };
  let pos = 0;
  const flush = (kind: SegmentKind) => {
    // A newline that only one side has still ends the line on that side; both sides' lines are shown.
    current.newEnd = pos;
    out.push(current);
    if (kind !== "del") pos += 1;
    current = { segments: [], newStart: pos, newEnd: pos };
  };
  for (const h of hunks) {
    const kind: SegmentKind = h.kind === "KIND_INSERT" ? "ins" : h.kind === "KIND_DELETE" ? "del" : "same";
    const parts = (h.text ?? "").split("\n");
    parts.forEach((part, i) => {
      if (part) {
        const last = current.segments[current.segments.length - 1];
        if (last && last.kind === kind) last.text += part;
        else current.segments.push({ text: part, kind });
        if (kind !== "del") pos += codePoints(part);
      }
      if (i < parts.length - 1) flush(kind);
    });
  }
  current.newEnd = pos;
  out.push(current);
  return out;
}

const textOf = (line: Line, side: "old" | "new") => line.segments.filter((s) => s.kind === "same" || s.kind === (side === "new" ? "ins" : "del")).map((s) => s.text).join("");

function kindOf(text: string): { kind: BlockKind; strip: number } {
  const rules: [RegExp, BlockKind][] = [
    [/^### /, "h3"],
    [/^## /, "h2"],
    [/^# /, "h1"],
    [/^[-*+] \[[ xX]\] /, "todo"],
    [/^(?:[-*+]|\d+[.)]) /, "li"],
    [/^> /, "quote"],
    [/^```/, "code"],
  ];
  for (const [re, kind] of rules) {
    const m = re.exec(text);
    if (m) return { kind, strip: kind === "code" ? 0 : m[0].length };
  }
  return { kind: "p", strip: 0 };
}

function statusOf(segments: Segment[]): BlockStatus {
  const visible = segments.filter((s) => s.text.trim() !== "");
  if (visible.length === 0) return "same";
  if (visible.every((s) => s.kind === "same")) return "same";
  if (visible.every((s) => s.kind === "ins")) return "add";
  if (visible.every((s) => s.kind === "del")) return "rem";
  return "chg";
}

/** Removes the first `n` characters of a block's Markdown prefix from its segments, for display. */
function stripPrefix(segments: Segment[], n: number): Segment[] {
  let left = n;
  const out: Segment[] = [];
  for (const s of segments) {
    if (left <= 0) out.push(s);
    else if (s.text.length <= left) left -= s.text.length;
    else {
      out.push({ ...s, text: s.text.slice(left) });
      left = 0;
    }
  }
  return out;
}

export function blocksFromHunks(hunks: DiffHunk[]): HistoryBlock[] {
  const out: HistoryBlock[] = [];
  let code: Line[] | null = null;
  const push = (group: Line[], kind: BlockKind, strip: number) => {
    const segments: Segment[] = [];
    group.forEach((l, i) => {
      if (i > 0) segments.push({ text: "\n", kind: "same" });
      segments.push(...l.segments);
    });
    const shown = kind === "code" ? segments.filter((s) => !/^```/.test(s.text)) : stripPrefix(segments, strip);
    out.push({ kind, status: statusOf(segments), segments: shown, newStart: group[0]!.newStart, newEnd: group[group.length - 1]!.newEnd });
  };
  for (const line of lines(hunks)) {
    const visible = textOf(line, "new") || textOf(line, "old");
    if (code) {
      code.push(line);
      if (/^```\s*$/.test(visible) && code.length > 1) {
        push(code, "code", 0);
        code = null;
      }
      continue;
    }
    if (visible.trim() === "") continue;
    const { kind, strip } = kindOf(visible);
    if (kind === "code") {
      code = [line];
      continue;
    }
    push([line], kind, strip);
  }
  if (code) push(code, "code", 0);
  return out;
}

export type Attribution = { userId?: string; agentRunId?: string; at?: string };

/** The span covering most of [start, end) of the new text, the block's main author. */
export function attribute(spans: BlameSpan[], start: number, end: number): Attribution | null {
  let best: BlameSpan | null = null;
  let bestOverlap = 0;
  for (const s of spans) {
    const overlap = Math.min(end, s.end ?? 0) - Math.max(start, s.start ?? 0);
    if (overlap > bestOverlap) {
      best = s;
      bestOverlap = overlap;
    }
  }
  return best ? { userId: best.userId, agentRunId: best.agentRunId, at: best.at } : null;
}

export function summarise(blocks: HistoryBlock[]): { added: number; removed: number; changed: number; wordsIn: number; wordsOut: number } {
  const words = (s: string) => s.split(/\s+/).filter(Boolean).length;
  let wordsIn = 0;
  let wordsOut = 0;
  for (const b of blocks) {
    for (const s of b.segments) {
      if (s.kind === "ins") wordsIn += words(s.text);
      if (s.kind === "del") wordsOut += words(s.text);
    }
  }
  return {
    added: blocks.filter((b) => b.status === "add").length,
    removed: blocks.filter((b) => b.status === "rem").length,
    changed: blocks.filter((b) => b.status === "chg").length,
    wordsIn,
    wordsOut,
  };
}
