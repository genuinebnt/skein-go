/**
 * The editor's pure logic: how a textarea change becomes CRDT edits, which
 * Markdown typed at a block's start turns it into another type, the slash
 * menu's catalogue, and where decorations (comments, suggestions) sit in a
 * block. No React, no DOM.
 */

import type { BlockAttrs, BlockType } from "../crdt/wasm";

const isHigh = (u: number) => u >= 0xd800 && u <= 0xdbff;
const isLow = (u: number) => u >= 0xdc00 && u <= 0xdfff;

export type TextChange = { start: number; deleteCount: number; insert: string };

/**
 * The single splice that turns `prev` into `next`: common prefix and suffix
 * stripped, never splitting a surrogate pair (an edit that touches half an
 * emoji is widened to the whole character). Null when nothing changed.
 * `caret` (the caret after the change) disambiguates repeated characters:
 * typing "a" into "aa" at the end inserts at the end, not the start.
 */
export function diffText(prev: string, next: string, caret?: number): TextChange | null {
  if (prev === next) return null;
  const max = Math.min(prev.length, next.length);
  let start = 0;
  const limit = caret === undefined ? max : Math.min(max, Math.max(0, caret - Math.max(0, next.length - prev.length)));
  while (start < limit && prev.charCodeAt(start) === next.charCodeAt(start)) start++;
  let end = 0;
  while (end < max - start && prev.charCodeAt(prev.length - 1 - end) === next.charCodeAt(next.length - 1 - end)) end++;
  if (start > 0 && isLow(prev.charCodeAt(start)) && isHigh(prev.charCodeAt(start - 1))) start--;
  if (end > 0 && isHigh(prev.charCodeAt(prev.length - end - 1)) && isLow(prev.charCodeAt(prev.length - end))) end--;
  return { start, deleteCount: prev.length - end - start, insert: next.slice(start, next.length - end) };
}

export const DEFAULT_ATTRS: BlockAttrs = { type: "paragraph", indent: 0, checked: false, language: "" };

/** Markdown typed at the start of a paragraph that turns it into another block type. */
export function markdownShortcut(text: string): { attrs: Partial<BlockAttrs>; strip: number } | null {
  const rules: [RegExp, (m: RegExpExecArray) => Partial<BlockAttrs>][] = [
    [/^### /, () => ({ type: "heading3" })],
    [/^## /, () => ({ type: "heading2" })],
    [/^# /, () => ({ type: "heading1" })],
    [/^[-*+] /, () => ({ type: "bulleted" })],
    [/^\d+[.)] /, () => ({ type: "numbered" })],
    [/^\[( |x)\] /i, (m) => ({ type: "todo", checked: m[1]!.toLowerCase() === "x" })],
    [/^> /, () => ({ type: "quote" })],
    [/^```([\w+#.-]*) $/, (m) => ({ type: "code", language: m[1] ?? "" })],
    [/^---$/, () => ({ type: "divider" })],
  ];
  for (const [re, attrs] of rules) {
    const m = re.exec(text);
    if (m) return { attrs: attrs(m), strip: m[0].length };
  }
  return null;
}

export type SlashItem = {
  id: string;
  group: "Text" | "Lists" | "Blocks" | "Agent";
  label: string;
  hint: string;
  icon: string;
  colour?: string;
  key: string;
  aliases: string[];
  attrs?: Partial<BlockAttrs>;
  action?: "agent";
};

export const SLASH_ITEMS: SlashItem[] = [
  { id: "text", group: "Text", label: "Text", hint: "a plain paragraph", icon: "¶", key: "⌘⌥0", aliases: ["p", "paragraph", "text"], attrs: { type: "paragraph" } },
  { id: "h1", group: "Text", label: "Heading 1", hint: "or type # and a space", icon: "H1", colour: "var(--acc)", key: "#", aliases: ["h1", "heading", "title"], attrs: { type: "heading1" } },
  { id: "h2", group: "Text", label: "Heading 2", hint: "or type ## and a space", icon: "H2", colour: "var(--acc)", key: "##", aliases: ["h2", "heading", "subheading"], attrs: { type: "heading2" } },
  { id: "h3", group: "Text", label: "Heading 3", hint: "or type ### and a space", icon: "H3", colour: "var(--acc)", key: "###", aliases: ["h3", "heading"], attrs: { type: "heading3" } },
  { id: "bulleted", group: "Lists", label: "Bulleted list", hint: "- and a space", icon: "•", key: "-", aliases: ["ul", "bullet", "list"], attrs: { type: "bulleted" } },
  { id: "numbered", group: "Lists", label: "Numbered list", hint: "1. and a space", icon: "1.", key: "1.", aliases: ["ol", "number", "list"], attrs: { type: "numbered" } },
  { id: "todo", group: "Lists", label: "To-do", hint: "[ ] and a space", icon: "☐", key: "[ ]", aliases: ["todo", "task", "check"], attrs: { type: "todo", checked: false } },
  { id: "quote", group: "Blocks", label: "Quote", hint: "> and a space", icon: "“", key: ">", aliases: ["quote", "blockquote"], attrs: { type: "quote" } },
  { id: "code", group: "Blocks", label: "Code block", hint: "```rust, ```go, ```sql… language is a field", icon: "{ }", colour: "var(--fnc)", key: "```", aliases: ["code", "snippet", "pre"], attrs: { type: "code" } },
  { id: "divider", group: "Blocks", label: "Divider", hint: "---", icon: "—", key: "---", aliases: ["hr", "divider", "rule"], attrs: { type: "divider" } },
  { id: "agent", group: "Agent", label: "Ask the agent to write…", hint: "arrives as a suggestion you review", icon: "✦", colour: "var(--vio)", key: "⌘J", aliases: ["ai", "agent", "ask", "write"], action: "agent" },
];

/** Items whose label or aliases start with the query; `/code rust` also sets the language. */
export function filterSlash(query: string): { items: SlashItem[]; language: string } {
  const [word = "", language = ""] = query.trim().toLowerCase().split(/\s+/);
  if (!word) return { items: SLASH_ITEMS, language: "" };
  const items = SLASH_ITEMS.filter((i) => i.label.toLowerCase().startsWith(word) || i.aliases.some((a) => a.startsWith(word)) || i.id.startsWith(word));
  return { items, language };
}

/** Where a slash menu opens: a "/" at the block's start or after white space, with the query typed after it. */
export function slashQuery(text: string, caret: number): { from: number; query: string } | null {
  const before = text.slice(0, caret);
  const at = before.lastIndexOf("/");
  if (at < 0) return null;
  if (at > 0 && !/\s/.test(before[at - 1]!)) return null;
  const query = before.slice(at + 1);
  if (/\n/.test(query) || query.length > 24) return null;
  return { from: at, query };
}

export const isOnFirstLine = (value: string, caret: number) => !value.slice(0, caret).includes("\n");
export const isOnLastLine = (value: string, caret: number) => !value.slice(caret).includes("\n");

/** What Enter makes after a block of this type: lists continue, everything else starts a paragraph. */
export function attrsAfterSplit(current: BlockAttrs, currentText: string): { next: BlockAttrs | null; convertCurrent: BlockAttrs | null } {
  const list = current.type === "bulleted" || current.type === "numbered" || current.type === "todo";
  if (list && currentText.trim() === "") return { next: null, convertCurrent: { ...DEFAULT_ATTRS, indent: current.indent } };
  if (list) return { next: { ...current, checked: false }, convertCurrent: null };
  return { next: current.indent > 0 ? { ...DEFAULT_ATTRS, indent: current.indent } : null, convertCurrent: null };
}

export type Range = { start: number; end: number };

/** UTF-16 ranges in `text` where `needle` occurs (non-overlapping). */
export function occurrences(text: string, needle: string): Range[] {
  if (!needle) return [];
  const out: Range[] = [];
  for (let at = text.indexOf(needle); at >= 0; at = text.indexOf(needle, at + needle.length)) out.push({ start: at, end: at + needle.length });
  return out;
}

/** Ordinal numbers for numbered lists: consecutive numbered blocks at the same indent count up. */
export function listNumbers(blocks: { type: BlockType; indent: number }[]): number[] {
  const counters: number[] = [];
  return blocks.map((b) => {
    if (b.type !== "numbered") {
      counters.length = 0;
      return 0;
    }
    counters.length = b.indent + 1;
    counters[b.indent] = (counters[b.indent] ?? 0) + 1;
    return counters[b.indent]!;
  });
}

/** Rows of a Markdown table block, without the separator row. */
export function tableRows(text: string): string[][] {
  return text
    .split("\n")
    .filter((line) => !/^\s*\|?\s*:?-{2,}/.test(line))
    .map((line) =>
      line
        .trim()
        .replace(/^\||\|$/g, "")
        .split("|")
        .map((c) => c.trim()),
    );
}
