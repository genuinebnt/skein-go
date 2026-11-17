import { describe, expect, it } from "vitest";
import type { DiffHunk } from "../api/schema.gen";
import { attribute, blocksFromHunks, summarise } from "./model";

const eq = (text: string): DiffHunk => ({ kind: "KIND_EQUAL", text });
const ins = (text: string): DiffHunk => ({ kind: "KIND_INSERT", text });
const del = (text: string): DiffHunk => ({ kind: "KIND_DELETE", text });

describe("blocksFromHunks", () => {
  it("classifies blocks as same, changed, added and removed", () => {
    const blocks = blocksFromHunks([
      eq("## Commit rule\n\nA leader may only advance commitIndex once a current-term entry is "),
      del("replicated to"),
      ins("stored on"),
      eq(" a majority.\n\n"),
      ins("Why: the term check makes it safe.\n\n"),
      del("TODO: check fsync.\n\n"),
      eq("- [ ] Batch the backoff"),
    ]);
    expect(blocks.map((b) => [b.kind, b.status])).toEqual([
      ["h2", "same"],
      ["p", "chg"],
      ["p", "add"],
      ["p", "rem"],
      ["todo", "same"],
    ]);
    expect(blocks[0]!.segments).toEqual([{ text: "Commit rule", kind: "same" }]);
    expect(blocks[1]!.segments.map((s) => s.kind)).toEqual(["same", "del", "ins", "same"]);
  });

  it("keeps a fenced code block together across its lines", () => {
    const blocks = blocksFromHunks([eq("```rust\nfn a() {\n"), del("  old();\n"), ins("  new();\n"), eq("}\n```\n\nAfter.")]);
    expect(blocks.map((b) => [b.kind, b.status])).toEqual([
      ["code", "chg"],
      ["p", "same"],
    ]);
  });

  it("tracks code-point offsets in the new text for blame", () => {
    const blocks = blocksFromHunks([eq("🙂 one\n\n"), ins("two\n\n"), eq("three")]);
    expect(blocks.map((b) => [b.newStart, b.newEnd])).toEqual([
      [0, 5],
      [7, 10],
      [12, 17],
    ]);
  });
});

describe("attribute", () => {
  it("picks the span that covers most of the block", () => {
    const spans = [
      { start: 0, end: 4, userId: "a" },
      { start: 4, end: 20, userId: "b", agentRunId: "r" },
    ];
    expect(attribute(spans, 2, 18)).toEqual({ userId: "b", agentRunId: "r", at: undefined });
    expect(attribute(spans, 40, 50)).toBeNull();
  });
});

describe("summarise", () => {
  it("counts blocks and words in and out", () => {
    const s = summarise(blocksFromHunks([eq("a b\n\n"), ins("c d e\n\n"), del("f\n\n"), eq("g")]));
    expect(s).toEqual({ added: 1, removed: 1, changed: 0, wordsIn: 3, wordsOut: 1 });
  });
});
