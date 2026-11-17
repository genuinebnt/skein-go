import { describe, expect, it } from "vitest";
import { attrsAfterSplit, diffText, filterSlash, listNumbers, markdownShortcut, slashQuery, tableRows, DEFAULT_ATTRS } from "./model";

describe("diffText", () => {
  it("finds a single insertion", () => {
    expect(diffText("hello world", "hello brave world")).toEqual({ start: 6, deleteCount: 0, insert: "brave " });
  });

  it("uses the caret to place a repeated character where it was typed", () => {
    expect(diffText("aa", "aaa", 3)).toEqual({ start: 2, deleteCount: 0, insert: "a" });
    expect(diffText("aa", "aaa", 1)).toEqual({ start: 0, deleteCount: 0, insert: "a" });
  });

  it("finds a replacement of a selection", () => {
    expect(diffText("the quick fox", "the slow fox")).toEqual({ start: 4, deleteCount: 5, insert: "slow" });
  });

  it("never splits a surrogate pair", () => {
    // 🙂 is 🙂 and 🙃 is 🙃: they share a high surrogate.
    expect(diffText("a🙂b", "a🙃b")).toEqual({ start: 1, deleteCount: 2, insert: "🙃" });
    expect(diffText("x🙂", "x")).toEqual({ start: 1, deleteCount: 2, insert: "" });
  });

  it("returns null for no change", () => {
    expect(diffText("same", "same")).toBeNull();
  });
});

describe("markdown shortcuts", () => {
  it.each([
    ["# ", { type: "heading1" }, 2],
    ["## ", { type: "heading2" }, 3],
    ["- ", { type: "bulleted" }, 2],
    ["1. ", { type: "numbered" }, 3],
    ["[x] ", { type: "todo", checked: true }, 4],
    ["> ", { type: "quote" }, 2],
    ["```rust ", { type: "code", language: "rust" }, 8],
    ["---", { type: "divider" }, 3],
  ])("%j turns the block into %o", (text, attrs, strip) => {
    expect(markdownShortcut(text)).toEqual({ attrs, strip });
  });

  it("ignores Markdown that is not at the start", () => {
    expect(markdownShortcut("see # notes")).toBeNull();
  });
});

describe("slash menu", () => {
  it("opens on a slash at the start or after a space, and reads the query", () => {
    expect(slashQuery("/co", 3)).toEqual({ from: 0, query: "co" });
    expect(slashQuery("text /h2", 8)).toEqual({ from: 5, query: "h2" });
    expect(slashQuery("a/b", 3)).toBeNull();
  });

  it("filters by label or alias and reads a language after /code", () => {
    expect(filterSlash("h2").items.map((i) => i.id)).toEqual(["h2"]);
    expect(filterSlash("code rust")).toMatchObject({ language: "rust" });
    expect(filterSlash("list").items.map((i) => i.id)).toEqual(["bulleted", "numbered"]);
  });
});

describe("Enter in lists", () => {
  it("continues a list item, and leaves the list from an empty one", () => {
    const item = { ...DEFAULT_ATTRS, type: "todo" as const, checked: true };
    expect(attrsAfterSplit(item, "do it").next).toEqual({ ...item, checked: false });
    expect(attrsAfterSplit(item, "  ").convertCurrent?.type).toBe("paragraph");
    expect(attrsAfterSplit({ ...DEFAULT_ATTRS, type: "heading2" }, "Title").next).toBeNull();
  });
});

describe("rendering helpers", () => {
  it("numbers consecutive numbered items per indent and restarts after other blocks", () => {
    const n = (indent: number) => ({ type: "numbered" as const, indent });
    expect(listNumbers([n(0), n(0), n(1), n(1), n(0), { type: "paragraph", indent: 0 }, n(0)])).toEqual([1, 2, 1, 2, 3, 0, 1]);
  });

  it("splits a Markdown table into cells without the separator row", () => {
    expect(tableRows("| a | b |\n|---|---|\n| 1 | 2 |")).toEqual([
      ["a", "b"],
      ["1", "2"],
    ]);
  });
});
