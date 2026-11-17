/**
 * Graph algorithms for the mock GraphService, over a directed graph of note
 * IDs (an edge from → to means note `from` links to note `to`). They follow
 * docs/api/graph.md's definitions closely enough to drive the explorer and
 * are checked against testdata/seed/small/truth/graph; the real ones live in
 * libs/graph. Ties are broken by note ID so results are deterministic.
 */

export type Graph = { nodes: string[]; edges: [string, string][]; roots: Set<string> };

export function adjacency(g: Graph) {
  const out = new Map<string, string[]>(g.nodes.map((n) => [n, []]));
  const inn = new Map<string, string[]>(g.nodes.map((n) => [n, []]));
  for (const [a, b] of g.edges) {
    if (!out.has(a) || !out.has(b) || a === b) continue;
    out.get(a)!.push(b);
    inn.get(b)!.push(a);
  }
  for (const list of [...out.values(), ...inn.values()]) list.sort();
  const undirected = new Map<string, string[]>(g.nodes.map((n) => [n, [...new Set([...out.get(n)!, ...inn.get(n)!])].sort()]));
  return { out, inn, undirected };
}

/** Hops from `source` following `next`, up to maxHops (0 = no limit). */
export function bfs(source: string, next: Map<string, string[]>, maxHops = 0): Map<string, number> {
  const dist = new Map([[source, 0]]);
  let frontier = [source];
  while (frontier.length > 0) {
    const d = dist.get(frontier[0]!)!;
    if (maxHops > 0 && d >= maxHops) break;
    const following: string[] = [];
    for (const n of frontier) {
      for (const m of next.get(n) ?? []) {
        if (!dist.has(m)) {
          dist.set(m, d + 1);
          following.push(m);
        }
      }
    }
    frontier = following;
  }
  return dist;
}

export function components(g: Graph): Map<string, number> {
  const { undirected } = adjacency(g);
  const comp = new Map<string, number>();
  let id = 0;
  for (const n of [...g.nodes].sort()) {
    if (comp.has(n)) continue;
    for (const m of bfs(n, undirected).keys()) comp.set(m, id);
    id++;
  }
  return comp;
}

/** Tarjan's SCC, iterative; components numbered so the one with the smallest note ID is 0, and so on. */
export function stronglyConnected(g: Graph): Map<string, number> {
  const { out } = adjacency(g);
  const index = new Map<string, number>();
  const low = new Map<string, number>();
  const onStack = new Set<string>();
  const stack: string[] = [];
  const groups: string[][] = [];
  let counter = 0;
  for (const start of [...g.nodes].sort()) {
    if (index.has(start)) continue;
    const work: [string, number][] = [[start, 0]];
    while (work.length > 0) {
      const frame = work[work.length - 1]!;
      const [v, i] = frame;
      if (i === 0) {
        index.set(v, counter);
        low.set(v, counter);
        counter++;
        stack.push(v);
        onStack.add(v);
      }
      const succ = out.get(v)!;
      if (i < succ.length) {
        frame[1] = i + 1;
        const w = succ[i]!;
        if (!index.has(w)) work.push([w, 0]);
        else if (onStack.has(w)) low.set(v, Math.min(low.get(v)!, index.get(w)!));
        continue;
      }
      work.pop();
      if (work.length > 0) {
        const parent = work[work.length - 1]![0];
        low.set(parent, Math.min(low.get(parent)!, low.get(v)!));
      }
      if (low.get(v) === index.get(v)) {
        const group: string[] = [];
        let w: string;
        do {
          w = stack.pop()!;
          onStack.delete(w);
          group.push(w);
        } while (w !== v);
        groups.push(group.sort());
      }
    }
  }
  groups.sort((a, b) => (a[0]! < b[0]! ? -1 : 1));
  const of = new Map<string, number>();
  groups.forEach((group, i) => group.forEach((n) => of.set(n, i)));
  return of;
}

/** One example cycle (first equals last), found by three-colour DFS; empty when acyclic. */
export function findCycle(g: Graph): string[] {
  const { out } = adjacency(g);
  const colour = new Map<string, 0 | 1 | 2>();
  const parent = new Map<string, string>();
  for (const start of [...g.nodes].sort()) {
    if (colour.get(start)) continue;
    const work: [string, number][] = [[start, 0]];
    colour.set(start, 1);
    while (work.length > 0) {
      const frame = work[work.length - 1]!;
      const succ = out.get(frame[0])!;
      if (frame[1] < succ.length) {
        const w = succ[frame[1]++]!;
        if (colour.get(w) === 1) {
          const cycle = [w];
          for (let v = frame[0]; v !== w; v = parent.get(v)!) cycle.push(v);
          cycle.push(w);
          return cycle.reverse();
        }
        if (!colour.get(w)) {
          colour.set(w, 1);
          parent.set(w, frame[0]);
          work.push([w, 0]);
        }
      } else {
        colour.set(frame[0], 2);
        work.pop();
      }
    }
  }
  return [];
}

/** Kahn's algorithm: every note after the notes it links to; notes in or behind cycles are unplaced. */
export function topologicalOrder(g: Graph): { order: string[]; unplaced: string[] } {
  const { out, inn } = adjacency(g);
  const remaining = new Map(g.nodes.map((n) => [n, out.get(n)!.length]));
  const ready = g.nodes.filter((n) => remaining.get(n) === 0).sort();
  const order: string[] = [];
  while (ready.length > 0) {
    const n = ready.shift()!;
    order.push(n);
    for (const p of inn.get(n)!) {
      remaining.set(p, remaining.get(p)! - 1);
      if (remaining.get(p) === 0) {
        ready.push(p);
        ready.sort();
      }
    }
  }
  const placed = new Set(order);
  return { order, unplaced: g.nodes.filter((n) => !placed.has(n)).sort() };
}

export function pagerank(g: Graph, damping = 0.85, iterations = 60): Map<string, number> {
  const { out } = adjacency(g);
  const n = g.nodes.length || 1;
  let rank = new Map(g.nodes.map((v) => [v, 1 / n]));
  for (let it = 0; it < iterations; it++) {
    const next = new Map(g.nodes.map((v) => [v, (1 - damping) / n]));
    let dangling = 0;
    for (const v of g.nodes) {
      const succ = out.get(v)!;
      if (succ.length === 0) dangling += rank.get(v)!;
      else for (const w of succ) next.set(w, next.get(w)! + (damping * rank.get(v)!) / succ.length);
    }
    for (const v of g.nodes) next.set(v, next.get(v)! + (damping * dangling) / n);
    rank = next;
  }
  return rank;
}

/** Brandes' betweenness over undirected hops, normalised to [0, 1]. */
export function betweenness(g: Graph): Map<string, number> {
  const { undirected } = adjacency(g);
  const cb = new Map(g.nodes.map((v) => [v, 0]));
  for (const s of g.nodes) {
    const stack: string[] = [];
    const pred = new Map<string, string[]>(g.nodes.map((v) => [v, []]));
    const sigma = new Map(g.nodes.map((v) => [v, 0]));
    const dist = new Map<string, number>();
    sigma.set(s, 1);
    dist.set(s, 0);
    const queue = [s];
    while (queue.length > 0) {
      const v = queue.shift()!;
      stack.push(v);
      for (const w of undirected.get(v)!) {
        if (!dist.has(w)) {
          dist.set(w, dist.get(v)! + 1);
          queue.push(w);
        }
        if (dist.get(w) === dist.get(v)! + 1) {
          sigma.set(w, sigma.get(w)! + sigma.get(v)!);
          pred.get(w)!.push(v);
        }
      }
    }
    const delta = new Map(g.nodes.map((v) => [v, 0]));
    while (stack.length > 0) {
      const w = stack.pop()!;
      for (const v of pred.get(w)!) delta.set(v, delta.get(v)! + (sigma.get(v)! / sigma.get(w)!) * (1 + delta.get(w)!));
      if (w !== s) cb.set(w, cb.get(w)! + delta.get(w)!);
    }
  }
  const n = g.nodes.length;
  const scale = n > 2 ? 1 / ((n - 1) * (n - 2)) : 1;
  return new Map([...cb].map(([v, c]) => [v, c * scale]));
}

/** One-level Louvain (local moving until no gain), deterministic by note ID, with the partition's modularity. */
export function communities(g: Graph): { communityOf: Map<string, number>; modularity: number } {
  const { undirected } = adjacency(g);
  const nodes = [...g.nodes].sort();
  const degree = new Map(nodes.map((v) => [v, undirected.get(v)!.length]));
  const m2 = [...degree.values()].reduce((a, b) => a + b, 0) || 1;
  const comm = new Map(nodes.map((v, i) => [v, i]));
  const tot = new Map(nodes.map((v, i) => [i, degree.get(v)!]));
  let moved = true;
  for (let pass = 0; moved && pass < 20; pass++) {
    moved = false;
    for (const v of nodes) {
      const current = comm.get(v)!;
      const kv = degree.get(v)!;
      tot.set(current, tot.get(current)! - kv);
      const links = new Map<number, number>();
      for (const w of undirected.get(v)!) links.set(comm.get(w)!, (links.get(comm.get(w)!) ?? 0) + 1);
      let best = current;
      let bestGain = (links.get(current) ?? 0) - (tot.get(current)! * kv) / m2;
      for (const [c, k] of [...links].sort((a, b) => a[0] - b[0])) {
        const gain = k - (tot.get(c)! * kv) / m2;
        if (gain > bestGain + 1e-12) {
          best = c;
          bestGain = gain;
        }
      }
      tot.set(best, tot.get(best)! + kv);
      if (best !== current) {
        comm.set(v, best);
        moved = true;
      }
    }
  }
  // Renumber by first appearance in ID order.
  const renumber = new Map<number, number>();
  const communityOf = new Map<string, number>();
  for (const v of nodes) {
    const c = comm.get(v)!;
    if (!renumber.has(c)) renumber.set(c, renumber.size);
    communityOf.set(v, renumber.get(c)!);
  }
  let q = 0;
  for (const v of nodes) {
    for (const w of undirected.get(v)!) if (communityOf.get(v) === communityOf.get(w)) q += 1;
  }
  const sums = new Map<number, number>();
  for (const v of nodes) sums.set(communityOf.get(v)!, (sums.get(communityOf.get(v)!) ?? 0) + degree.get(v)!);
  let expected = 0;
  for (const s of sums.values()) expected += (s / m2) ** 2;
  return { communityOf, modularity: q / m2 - expected };
}

export function diameter(g: Graph): number {
  const { undirected } = adjacency(g);
  let best = 0;
  for (const v of g.nodes) for (const d of bfs(v, undirected).values()) best = Math.max(best, d);
  return best;
}

/** Shortest path ignoring direction: BFS for hops, Dijkstra for a weighted metric. */
export function shortestPath(g: Graph, from: string, to: string, weight?: (a: string, b: string) => number): { path: string[]; cost: number } | null {
  const { undirected } = adjacency(g);
  if (!undirected.has(from) || !undirected.has(to)) return null;
  const dist = new Map<string, number>([[from, 0]]);
  const prev = new Map<string, string>();
  const done = new Set<string>();
  const w = weight ?? (() => 1);
  for (;;) {
    let v: string | null = null;
    for (const [n, d] of dist) if (!done.has(n) && (v === null || d < dist.get(v)! || (d === dist.get(v)! && n < v))) v = n;
    if (v === null) return null;
    if (v === to) break;
    done.add(v);
    for (const u of undirected.get(v)!) {
      const nd = dist.get(v)! + w(v, u);
      if (!dist.has(u) || nd < dist.get(u)! - 1e-12) {
        dist.set(u, nd);
        prev.set(u, v);
      }
    }
  }
  const path = [to];
  while (path[0] !== from) path.unshift(prev.get(path[0]!)!);
  return { path, cost: dist.get(to)! };
}

/**
 * What to read before a note: everything it links to, transitively, ordered
 * so each note comes after the notes it links to (cycles kept together by
 * condensing them), with the shortest link distance as depth.
 */
export function readingPath(g: Graph, target: string, title: (id: string) => string): { noteId: string; reason: string; depth: number }[] {
  const { out, inn } = adjacency(g);
  const depth = bfs(target, out);
  depth.delete(target);
  const sub: Graph = { nodes: [...depth.keys()], edges: g.edges.filter(([a, b]) => depth.has(a) && depth.has(b)), roots: new Set() };
  const scc = stronglyConnected(sub);
  // Order condensed groups topologically; inside a loop, by depth then ID.
  const groupEdges: [string, string][] = sub.edges.filter(([a, b]) => scc.get(a) !== scc.get(b)).map(([a, b]) => [String(scc.get(a)), String(scc.get(b))]);
  const groups = [...new Set(scc.values())].map(String);
  const { order } = topologicalOrder({ nodes: groups, edges: groupEdges, roots: new Set() });
  const members = (gid: string) => sub.nodes.filter((n) => String(scc.get(n)) === gid).sort((a, b) => depth.get(a)! - depth.get(b)! || (a < b ? -1 : 1));
  return order.flatMap(members).map((noteId) => {
    const from = inn.get(noteId)!.filter((p) => p === target || depth.has(p));
    return { noteId, depth: depth.get(noteId)!, reason: `linked from ${from.slice(0, 2).map(title).join(" and ")}${from.length > 2 ? ` and ${from.length - 2} more` : ""}` };
  });
}

export type PlanCandidate = { noteId: string; minutes: number; value: number; needs: string[] };

/**
 * The most valuable prerequisite-closed set of candidates that fits the
 * budget, by exhaustive search over closed sets (fine for the dozen
 * candidates the mock considers), returned in reading order.
 */
export function readingPlan(candidates: PlanCandidate[], minutes: number): { chosen: PlanCandidate[]; totalMinutes: number; totalValue: number } {
  const ids = candidates.map((c) => c.noteId);
  const byId = new Map(candidates.map((c) => [c.noteId, c]));
  const closure = (id: string, seen = new Set<string>()): Set<string> => {
    if (seen.has(id)) return seen;
    seen.add(id);
    for (const need of byId.get(id)?.needs ?? []) if (byId.has(need)) closure(need, seen);
    return seen;
  };
  let best = { set: new Set<string>(), minutes: 0, value: 0 };
  const limit = Math.min(ids.length, 14);
  for (let mask = 1; mask < 1 << limit; mask++) {
    const set = new Set<string>();
    for (let i = 0; i < limit; i++) if (mask & (1 << i)) for (const x of closure(ids[i]!)) set.add(x);
    let total = 0;
    let value = 0;
    for (const x of set) {
      total += byId.get(x)!.minutes;
      value += byId.get(x)!.value;
    }
    if (total <= minutes && (value > best.value + 1e-9 || (Math.abs(value - best.value) < 1e-9 && total < best.minutes))) best = { set, minutes: total, value };
  }
  const chosenIds = [...best.set];
  const order = topologicalOrder({
    nodes: chosenIds,
    edges: chosenIds.flatMap((id) => byId.get(id)!.needs.filter((n) => best.set.has(n)).map((n) => [id, n] as [string, string])),
    roots: new Set(),
  });
  const ordered = [...order.order, ...order.unplaced];
  return { chosen: ordered.map((id) => byId.get(id)!), totalMinutes: best.minutes, totalValue: best.value };
}
