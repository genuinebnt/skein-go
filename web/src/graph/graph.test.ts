import { describe, expect, it } from "vitest";
import { initialInteraction, interact, routeEnds } from "./interaction";
import { nextInDirection } from "./keys";
import { LAYOUT_ALPHA_MIN, forcesFor, rings, seedBodies, settle, stepLayout, type LayoutEdge, type LayoutNode } from "./layout";

const node = (id: string, topic = "rust"): LayoutNode => ({ id, topic, rank: 0.1 });

describe("graph layout (ported from genuine.dev)", () => {
  it("seeds the same positions for the same graph", () => {
    const nodes = [node("a"), node("b", "go"), node("c")];
    expect(seedBodies(nodes, 800, 600, 0.1)).toEqual(seedBodies(nodes, 800, 600, 0.1));
  });

  it("settles in a bounded number of steps with finite positions", () => {
    const nodes = Array.from({ length: 30 }, (_, i) => node(`n${i}`, i % 3 ? "rust" : "go"));
    const edges = nodes.slice(1).map((n, i): LayoutEdge => ({ from: nodes[i]!.id, to: n.id, kind: "link" }));
    const bodies = seedBodies(nodes, 1000, 700, 0.1);
    const byId = new Map(bodies.map((b) => [b.id, b]));
    let alpha = 1;
    let steps = 0;
    while (alpha > LAYOUT_ALPHA_MIN) {
      alpha = stepLayout(bodies, edges, byId, forcesFor(1000, 700), { alpha, pinned: null });
      steps++;
    }
    expect(steps).toBeLessThan(500);
    expect(bodies.every((b) => Number.isFinite(b.x) && Number.isFinite(b.y))).toBe(true);
  });

  it("pulls links tighter than similarity edges", () => {
    const rest = (kind: LayoutEdge["kind"]) => {
      const p = settle([node("a"), node("b"), node("c", "go")], [{ from: "a", to: "b", kind }]);
      return Math.hypot(p.get("a")!.x - p.get("b")!.x, p.get("a")!.y - p.get("b")!.y);
    };
    expect(rest("link")).toBeLessThan(rest("similar"));
  });

  it("normalises positions into the unit square", () => {
    const p = settle([node("a"), node("b"), node("c")], []);
    for (const { x, y } of p.values()) {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(1);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(1);
    }
  });

  it("puts blast-radius rings at increasing distance from the source", () => {
    const p = rings("s", new Map([["s", 0], ["a", 1], ["b", 2]]));
    const d = (id: string) => Math.hypot(p.get(id)!.x - 0.5, p.get(id)!.y - 0.5);
    expect(d("s")).toBe(0);
    expect(d("a")).toBeLessThan(d("b"));
  });
});

describe("graph keys and interaction (ported from genuine.dev)", () => {
  it("moves to the nearest note lying in the pressed direction", () => {
    const here = { id: "here", x: 0, y: 0 };
    expect(nextInDirection(here, [{ id: "far", x: 200, y: 0 }, { id: "near", x: 60, y: 10 }, { id: "left", x: -40, y: 0 }], "right")).toBe("near");
    expect(nextInDirection(here, [{ id: "above", x: 20, y: -100 }], "right")).toBeNull();
  });

  it("picks both route ends with two clicks and ignores a repeat of the first", () => {
    let s = interact(initialInteraction("a"), { type: "pick-route" });
    s = interact(s, { type: "select", id: "x" });
    expect(interact(s, { type: "select", id: "x" }).picking).toBe("to");
    s = interact(s, { type: "select", id: "y" });
    expect(routeEnds(s)).toEqual({ from: "x", to: "y" });
  });

  it("abandons a pick on a click on empty canvas", () => {
    expect(interact(interact(initialInteraction(null), { type: "pick-route" }), { type: "select", id: null }).picking).toBeNull();
  });
});
