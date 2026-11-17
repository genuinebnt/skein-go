/**
 * The explorer's force layout, ported from genuine.dev's graph workspace
 * (frontend/lib/graphLayout.ts, itself from anneal's engine) so both move
 * the same way: written links pull close and hard, similarity edges hold
 * loosely at a distance, neighbours repel in proportion to their size, a
 * weak pull keeps everything near the middle, and velocity is damped and
 * capped. Pure functions over plain bodies, deterministic for a given graph.
 *
 * Changes from genuine.dev: nodes are notes (id, topic, rank) and the edge
 * kinds are Skein's `link` ([[links]]) and `similar` (close in meaning, not
 * linked); `settle` runs the simulation to rest in one call, because the
 * explorer draws the settled layout rather than animating every frame.
 */

export type LayoutNode = { id: string; topic: string; rank: number };
export type LayoutEdge = { from: string; to: string; kind: "link" | "similar" };
export type Body = LayoutNode & { x: number; y: number; vx: number; vy: number; s: number };
export type Forces = { repulsion: number };

const CENTERING = 0.0016;
const DAMPING = 0.86;
const MAX_SPEED = 14;
const ALPHA_DECAY = 0.985;
/** Below this the layout counts as settled. */
export const LAYOUT_ALPHA_MIN = 0.02;
/** Pairs farther apart than this (squared) do not repel; it keeps far clusters from drifting off. */
const REPULSION_REACH_SQUARED = 90000;
const MIN_DISTANCE_SQUARED = 144;
const SEED = 20260808;

const SPRINGS: Record<LayoutEdge["kind"], { length: number; stiffness: number }> = {
  link: { length: 34, stiffness: 0.03 },
  similar: { length: 170, stiffness: 0.004 },
};

/** Repulsion scales with the stage, so a wide screen spreads the graph instead of only adding margins. */
export function forcesFor(width: number, height: number): Forces {
  return { repulsion: Math.min(width, height) * 1.6 };
}

function seededRandom(seed: number): () => number {
  let state = seed;
  return () => {
    state = (state * 1103515245 + 12345) & 0x7fffffff;
    return state / 0x7fffffff;
  };
}

/** Radius in px: PageRank sets it, between a small dot and a hub. */
export const nodeRadius = (rank: number, peakRank: number) => 5 + Math.sqrt(rank / Math.max(peakRank, Number.MIN_VALUE)) * 9;

/** Seeds positions on a ring by topic, so the layout starts as clusters. Positions are relative to the centre. */
export function seedBodies(nodes: LayoutNode[], width: number, height: number, peakRank: number): Body[] {
  const random = seededRandom(SEED);
  const topics = [...new Set(nodes.map((n) => n.topic || "untagged"))];
  const ring = Math.min(width, height) * 0.36;
  const jitter = 90;
  return nodes.map((node) => {
    const index = topics.indexOf(node.topic || "untagged");
    const angle = (index / Math.max(1, topics.length)) * Math.PI * 2 - Math.PI / 2;
    return {
      ...node,
      x: Math.cos(angle) * ring + (random() - 0.5) * jitter,
      y: Math.sin(angle) * ring + (random() - 0.5) * jitter,
      vx: 0,
      vy: 0,
      s: nodeRadius(node.rank, peakRank) * 2,
    };
  });
}

/** Advances the simulation one step and returns the cooled alpha. `pinned` is a body the pointer holds. */
export function stepLayout(bodies: Body[], edges: LayoutEdge[], byId: Map<string, Body>, { repulsion }: Forces, { alpha, pinned }: { alpha: number; pinned: Body | null }): number {
  for (let i = 0; i < bodies.length; i += 1) {
    const a = bodies[i]!;
    for (let j = i + 1; j < bodies.length; j += 1) {
      const b = bodies[j]!;
      const dx = a.x - b.x || 0.1;
      const dy = a.y - b.y || 0.1;
      const d2 = Math.max(dx * dx + dy * dy, MIN_DISTANCE_SQUARED);
      if (d2 > REPULSION_REACH_SQUARED) continue;
      const d = Math.sqrt(d2);
      const f = (repulsion * ((a.s + b.s) / 2)) / 14 / d2;
      a.vx += (dx / d) * f;
      a.vy += (dy / d) * f;
      b.vx -= (dx / d) * f;
      b.vy -= (dy / d) * f;
    }
  }
  for (const edge of edges) {
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    if (!from || !to) continue;
    const { length, stiffness } = SPRINGS[edge.kind];
    const dx = to.x - from.x;
    const dy = to.y - from.y;
    const d = Math.hypot(dx, dy) || 0.01;
    const f = (d - length) * stiffness;
    from.vx += (dx / d) * f;
    from.vy += (dy / d) * f;
    to.vx -= (dx / d) * f;
    to.vy -= (dy / d) * f;
  }
  for (const body of bodies) {
    body.vx -= body.x * CENTERING;
    body.vy -= body.y * CENTERING;
    if (body === pinned) {
      body.vx = 0;
      body.vy = 0;
      continue;
    }
    body.vx = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, body.vx * DAMPING));
    body.vy = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, body.vy * DAMPING));
    body.x += body.vx * alpha;
    body.y += body.vy * alpha;
  }
  return alpha * ALPHA_DECAY;
}

/** Runs the layout to rest and returns positions normalised to [0, 1] with a margin. */
export function settle(nodes: LayoutNode[], edges: LayoutEdge[], width = 1000, height = 700): Map<string, { x: number; y: number }> {
  const peak = Math.max(...nodes.map((n) => n.rank), Number.MIN_VALUE);
  const bodies = seedBodies(nodes, width, height, peak);
  const byId = new Map(bodies.map((b) => [b.id, b]));
  const forces = forcesFor(width, height);
  let alpha = 1;
  for (let step = 0; alpha > LAYOUT_ALPHA_MIN && step < 600; step++) alpha = stepLayout(bodies, edges, byId, forces, { alpha, pinned: null });
  const xs = bodies.map((b) => b.x);
  const ys = bodies.map((b) => b.y);
  const [minX, maxX, minY, maxY] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
  const margin = 0.07;
  const scale = (v: number, lo: number, hi: number) => margin + ((v - lo) / Math.max(hi - lo, 1)) * (1 - 2 * margin);
  return new Map(bodies.map((b) => [b.id, { x: scale(b.x, minX, maxX), y: scale(b.y, minY, maxY) }]));
}

/** Concentric rings around a source by hop distance (blast radius), in [0, 1]. */
export function rings(source: string, hops: Map<string, number>): Map<string, { x: number; y: number }> {
  const out = new Map<string, { x: number; y: number }>([[source, { x: 0.5, y: 0.5 }]]);
  const byRing = new Map<number, string[]>();
  for (const [id, d] of hops) if (id !== source) byRing.set(d, [...(byRing.get(d) ?? []), id]);
  const deepest = Math.max(1, ...byRing.keys());
  for (const [d, ids] of byRing) {
    ids.sort();
    const radius = (0.42 * d) / deepest;
    ids.forEach((id, i) => {
      const angle = -Math.PI / 2 + ((i + 0.5) * 2 * Math.PI) / ids.length + d * 0.35;
      out.set(id, { x: 0.5 + radius * Math.cos(angle), y: 0.5 + radius * Math.sin(angle) });
    });
  }
  return out;
}

/** The radius in [0, 1] units of ring `d` when `deepest` rings are drawn. */
export const ringRadius = (d: number, deepest: number) => (0.42 * d) / Math.max(1, deepest);
