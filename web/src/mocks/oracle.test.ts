// @vitest-environment node
/**
 * The mock services' algorithms checked against testdata/seed/small's ground
 * truth, so mock mode shows numbers the real services would agree with.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { adjacency, bfs, components, readingPlan, shortestPath, stronglyConnected, type Graph } from "./graph";
import { diffWords, embed } from "./text";

const seedDir = resolve(__dirname, "../../../testdata/seed/small");
const jsonl = <T,>(name: string): T[] =>
  readFileSync(resolve(seedDir, name), "utf8")
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l) as T);

type NoteRow = { id: string; workspace_id: string; trashed: boolean; is_root: boolean };
type LinkRow = { workspace_id: string; source_note_id: string; target_note_id: string | null; removed_at: string | null };

const notes = jsonl<NoteRow>("notes.jsonl");
const links = jsonl<LinkRow>("links.jsonl");
const workspaces = [...new Set(notes.map((n) => n.workspace_id))];

function graphOf(workspace: string): Graph {
  const live = notes.filter((n) => n.workspace_id === workspace && !n.trashed);
  const ids = new Set(live.map((n) => n.id));
  const edges = new Map<string, [string, string]>();
  for (const l of links) {
    if (l.workspace_id !== workspace || l.removed_at || !l.target_note_id) continue;
    if (ids.has(l.source_note_id) && ids.has(l.target_note_id) && l.source_note_id !== l.target_note_id) edges.set(`${l.source_note_id}>${l.target_note_id}`, [l.source_note_id, l.target_note_id]);
  }
  return { nodes: [...ids].sort(), edges: [...edges.values()], roots: new Set(live.filter((n) => n.is_root).map((n) => n.id)) };
}

describe("skein-hash-v1", () => {
  it("reproduces the seed's block embeddings", () => {
    const stopwords = new Set(JSON.parse(readFileSync(resolve(seedDir, "truth/clustering/topics.json"), "utf8")).embedder.stopwords as string[]);
    const blocks = new Map(jsonl<{ block_id: string; note_id: string; text: string }>("blocks.jsonl").map((b) => [`${b.note_id}/${b.block_id}`, b.text]));
    const truth = jsonl<{ block_id: string; note_id: string; vector: number[] }>("truth/clustering/block_embeddings.jsonl").slice(0, 400);
    for (const row of truth) {
      const got = embed(blocks.get(`${row.note_id}/${row.block_id}`)!, stopwords);
      got.forEach((x, i) => expect(Math.abs(x - row.vector[i]!), `${row.block_id}[${i}]`).toBeLessThanOrEqual(1e-4));
    }
  });
});

describe("graph algorithms against truth/graph", () => {
  it("finds exactly the seed's non-trivial strongly connected components", () => {
    const truth = jsonl<{ workspace_id: string; members: string[] }>("truth/graph/sccs.jsonl");
    for (const ws of workspaces) {
      const of = stronglyConnected(graphOf(ws));
      const groups = new Map<number, string[]>();
      for (const [n, c] of of) groups.set(c, [...(groups.get(c) ?? []), n]);
      const nontrivial = [...groups.values()].filter((g) => g.length > 1).map((g) => g.sort().join(",")).sort();
      expect(nontrivial).toEqual(truth.filter((t) => t.workspace_id === ws).map((t) => [...t.members].sort().join(",")).sort());
    }
  });

  it("finds the seed's weak components and which of them are orphans", () => {
    const truth = jsonl<{ workspace_id: string; members: string[]; orphan: boolean }>("truth/graph/components.jsonl");
    for (const ws of workspaces) {
      const g = graphOf(ws);
      const comp = components(g);
      const groups = new Map<number, string[]>();
      for (const [n, c] of comp) groups.set(c, [...(groups.get(c) ?? []), n]);
      const mine = [...groups.values()].map((m) => ({ key: m.sort().join(","), orphan: !m.some((x) => g.roots.has(x)) }));
      const want = truth.filter((t) => t.workspace_id === ws).map((t) => ({ key: [...t.members].sort().join(","), orphan: t.orphan }));
      expect(mine.sort((a, b) => a.key.localeCompare(b.key))).toEqual(want.sort((a, b) => a.key.localeCompare(b.key)));
    }
  });

  it("reaches the same notes at the same hops as truth/graph/blast_radius", () => {
    const truth = jsonl<{ workspace_id: string; note_id: string; direction: string; max_hops: number; hops?: Record<string, number> }>("truth/graph/blast_radius.jsonl");
    let checked = 0;
    for (const t of truth) {
      if (!t.hops) continue;
      const { out, inn } = adjacency(graphOf(t.workspace_id));
      const got = bfs(t.note_id, t.direction === "outbound" ? out : inn, t.max_hops);
      expect(Object.fromEntries([...got].sort())).toEqual(Object.fromEntries(Object.entries(t.hops).sort()));
      checked++;
    }
    expect(checked).toBeGreaterThan(10);
  });

  it("agrees with truth/graph/shortest_paths on distance and reachability", () => {
    const truth = jsonl<{ workspace_id: string; from: string; to: string; distance: number | null; reachable?: boolean }>("truth/graph/shortest_paths.jsonl");
    for (const t of truth) {
      const got = shortestPath(graphOf(t.workspace_id), t.from, t.to);
      if (t.distance === null || t.distance === undefined || t.distance < 0) expect(got).toBeNull();
      else expect(got?.cost).toBe(t.distance);
    }
  });
});

describe("reading plan", () => {
  it("never leaves out a prerequisite and stays within the budget", () => {
    const plan = readingPlan(
      [
        { noteId: "a", minutes: 10, value: 0.9, needs: ["b"] },
        { noteId: "b", minutes: 5, value: 0.2, needs: [] },
        { noteId: "c", minutes: 12, value: 0.8, needs: [] },
      ],
      16,
    );
    expect(plan.chosen.map((c) => c.noteId)).toEqual(["b", "a"]);
    expect(plan.totalMinutes).toBe(15);
  });
});

describe("word diff", () => {
  it("produces hunks that rebuild both texts", () => {
    const a = "Snapshots bound the log.\n\nOld paragraph here.";
    const b = "Snapshots bound the log and let slow followers catch up.\n\nNew paragraph here.";
    const hunks = diffWords(a, b);
    expect(hunks.filter((h) => h.kind !== "KIND_INSERT").map((h) => h.text).join("")).toBe(a);
    expect(hunks.filter((h) => h.kind !== "KIND_DELETE").map((h) => h.text).join("")).toBe(b);
  });
});
