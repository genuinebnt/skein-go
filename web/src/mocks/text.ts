/**
 * Text machinery for the mock services: the seed's tokenizer and its
 * skein-hash-v1 embedder (normative in docs/seed-data.md, so mock semantic
 * search, discovery and clustering behave like the real seed data), word
 * diffs for history, edit distance for typo-tolerant search, and small
 * k-means variants for the clustering benchmark.
 */

const isLetterLike = (ch: string) => /[\p{L}\p{N}\p{M}]/u.test(ch);
const isCjk = (ch: string) => /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}]/u.test(ch);

/** The seed tokenizer: runs of letters, digits and marks, each Han/kana character its own token. */
export function tokenize(text: string): string[] {
  const out: string[] = [];
  let run = "";
  for (const ch of text.toLowerCase()) {
    if (isCjk(ch)) {
      if (run) out.push(run);
      run = "";
      out.push(ch);
    } else if (isLetterLike(ch)) {
      run += ch;
    } else {
      if (run) out.push(run);
      run = "";
    }
  }
  if (run) out.push(run);
  return out;
}

function fnv1a32(text: string): number {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(text)) {
    h ^= b;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

export const EMBED_DIM = 64;

const compareBytes = (a: string, b: string) => {
  const x = new TextEncoder().encode(a);
  const y = new TextEncoder().encode(b);
  for (let i = 0; i < Math.min(x.length, y.length); i++) if (x[i] !== y[i]) return x[i]! - y[i]!;
  return x.length - y.length;
};

/** skein-hash-v1: feature hashing of tokens into 64 dimensions, L2-normalised, rounded to 4 decimals. */
export function embed(text: string, stopwords: Set<string>): number[] {
  const tf = new Map<string, number>();
  for (const t of tokenize(text)) {
    if ((t.length === 1 && t.charCodeAt(0) < 0x80) || stopwords.has(t)) continue;
    tf.set(t, (tf.get(t) ?? 0) + 1);
  }
  const v = new Array<number>(EMBED_DIM).fill(0);
  for (const t of [...tf.keys()].sort(compareBytes)) {
    const h = fnv1a32(t);
    const weight = 1 + Math.log(tf.get(t)!);
    v[h % EMBED_DIM]! += h & 0x80000000 ? -weight : weight;
  }
  const norm = Math.hypot(...v);
  return norm === 0 ? v : v.map((x) => Math.round((x / norm) * 1e4) / 1e4);
}

export function cosine(a: number[], b: number[]): number {
  let dot = 0;
  let na = 0;
  let nb = 0;
  for (let i = 0; i < a.length; i++) {
    dot += a[i]! * b[i]!;
    na += a[i]! * a[i]!;
    nb += b[i]! * b[i]!;
  }
  return na === 0 || nb === 0 ? 0 : dot / Math.sqrt(na * nb);
}

/** Levenshtein distance in code points, stopping early past `limit`. */
export function editDistance(a: string, b: string, limit = Infinity): number {
  const x = [...a];
  const y = [...b];
  let prev = Array.from({ length: y.length + 1 }, (_, i) => i);
  for (let i = 1; i <= x.length; i++) {
    const cur = [i];
    let rowMin = i;
    for (let j = 1; j <= y.length; j++) {
      cur[j] = Math.min(prev[j]! + 1, cur[j - 1]! + 1, prev[j - 1]! + (x[i - 1] === y[j - 1] ? 0 : 1));
      rowMin = Math.min(rowMin, cur[j]!);
    }
    if (rowMin > limit) return rowMin;
    prev = cur;
  }
  return prev[y.length]!;
}

export type Hunk = { kind: "KIND_EQUAL" | "KIND_INSERT" | "KIND_DELETE"; text: string };

/**
 * A word-level diff (LCS over words and the whitespace between them), merged
 * into hunks that concatenate to the old text (equal + delete) and the new
 * text (equal + insert), as DiffResponse requires.
 */
export function diffWords(oldText: string, newText: string): Hunk[] {
  const split = (s: string) => s.match(/\s+|[^\s]+/g) ?? [];
  const a = split(oldText);
  const b = split(newText);
  // Trim the common prefix and suffix first; notes change in small places.
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;
  const am = a.slice(pre, a.length - suf);
  const bm = b.slice(pre, b.length - suf);
  const out: Hunk[] = [];
  const push = (kind: Hunk["kind"], text: string) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && last.kind === kind) last.text += text;
    else out.push({ kind, text });
  };
  push("KIND_EQUAL", a.slice(0, pre).join(""));
  if (am.length * bm.length > 4_000_000) {
    push("KIND_DELETE", am.join(""));
    push("KIND_INSERT", bm.join(""));
  } else {
    const lcs: Uint32Array[] = Array.from({ length: am.length + 1 }, () => new Uint32Array(bm.length + 1));
    for (let i = am.length - 1; i >= 0; i--) {
      for (let j = bm.length - 1; j >= 0; j--) lcs[i]![j] = am[i] === bm[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
    let i = 0;
    let j = 0;
    while (i < am.length || j < bm.length) {
      if (i < am.length && j < bm.length && am[i] === bm[j]) {
        push("KIND_EQUAL", am[i++]!);
        j++;
      } else if (j < bm.length && (i === am.length || lcs[i]![j + 1]! >= lcs[i + 1]![j]!)) push("KIND_INSERT", bm[j++]!);
      else push("KIND_DELETE", am[i++]!);
    }
  }
  push("KIND_EQUAL", a.slice(a.length - suf).join(""));
  return out;
}

// ── clustering ───────────────────────────────────────────────────────────────

const sqDist = (a: number[], b: number[]) => a.reduce((s, x, i) => s + (x - b[i]!) ** 2, 0);

export function seededRandom(seed: number): () => number {
  let s = seed % 2147483647 || 1;
  return () => {
    s = (s * 16807) % 2147483647;
    return s / 2147483647;
  };
}

export type ClusterRun = { centroids: number[][]; assign: number[]; iterations: number; distanceComputations: number; epochs: number };

function nearest(p: number[], centroids: number[][]): number {
  let best = 0;
  let bestD = Infinity;
  centroids.forEach((c, i) => {
    const d = sqDist(p, c);
    if (d < bestD) {
      bestD = d;
      best = i;
    }
  });
  return best;
}

function seedCentroids(points: number[][], k: number, random: () => number, plusPlus: boolean): number[][] {
  const centroids = [points[Math.floor(random() * points.length)]!.slice()];
  while (centroids.length < k) {
    if (!plusPlus) {
      centroids.push(points[Math.floor(random() * points.length)]!.slice());
      continue;
    }
    const d = points.map((p) => Math.min(...centroids.map((c) => sqDist(p, c))));
    const total = d.reduce((a, b) => a + b, 0);
    let r = random() * total;
    let i = 0;
    while (i < d.length - 1 && (r -= d[i]!) > 0) i++;
    centroids.push(points[i]!.slice());
  }
  return centroids;
}

/** Lloyd's k-means (k-means++ seeding unless `plusPlus` is false). */
export function lloyd(points: number[][], k: number, seed: number, plusPlus = true, maxIter = 25): ClusterRun {
  const random = seededRandom(seed);
  let centroids = seedCentroids(points, k, random, plusPlus);
  let assign = points.map(() => 0);
  let iterations = 0;
  for (; iterations < maxIter; iterations++) {
    const next = points.map((p) => nearest(p, centroids));
    const changed = next.some((a, i) => a !== assign[i]);
    assign = next;
    centroids = centroids.map((c, ci) => {
      const members = points.filter((_, i) => assign[i] === ci);
      return members.length ? c.map((_, d) => members.reduce((s, m) => s + m[d]!, 0) / members.length) : c;
    });
    if (!changed && iterations > 0) break;
  }
  return { centroids, assign, iterations: iterations + 1, distanceComputations: (iterations + 1) * points.length * k, epochs: iterations + 1 };
}

/**
 * Mini-batch k-means. With `ema` the per-cluster sums and counts are an
 * exponential moving average (the e-means recipe of docs/research/e-means.md,
 * simplified), otherwise MacQueen's running means.
 */
export function miniBatch(points: number[][], k: number, seed: number, batch: number, epochs: number, ema: boolean): ClusterRun {
  const random = seededRandom(seed);
  const centroids = seedCentroids(points, k, random, true);
  const sums = centroids.map((c) => c.slice());
  const counts = centroids.map(() => 1);
  const steps = Math.max(1, Math.round((epochs * points.length) / batch));
  for (let s = 0; s < steps; s++) {
    const alpha = ema ? 0.3 * (0.1 + 0.9 * 0.5 * (1 + Math.cos((Math.PI * s) / steps))) : 0;
    const picks = Array.from({ length: batch }, () => points[Math.floor(random() * points.length)]!);
    const batchSums = centroids.map((c) => c.map(() => 0));
    const batchCounts = centroids.map(() => 0);
    for (const p of picks) {
      const c = nearest(p, centroids);
      batchCounts[c]!++;
      p.forEach((x, d) => (batchSums[c]![d]! += x));
    }
    centroids.forEach((_, ci) => {
      if (ema) {
        sums[ci] = sums[ci]!.map((x, d) => (1 - alpha) * x + alpha * batchSums[ci]![d]!);
        counts[ci] = (1 - alpha) * counts[ci]! + alpha * batchCounts[ci]!;
      } else {
        sums[ci] = sums[ci]!.map((x, d) => x + batchSums[ci]![d]!);
        counts[ci] = counts[ci]! + batchCounts[ci]!;
      }
      if (counts[ci]! > 1e-9) centroids[ci] = sums[ci]!.map((x) => x / counts[ci]!);
    });
  }
  return { centroids, assign: points.map((p) => nearest(p, centroids)), iterations: steps, distanceComputations: steps * batch * k + points.length * k, epochs };
}

export function objective(points: number[][], centroids: number[][]): number {
  return points.reduce((s, p) => s + Math.min(...centroids.map((c) => sqDist(p, c))), 0) / Math.max(1, points.length);
}

/** Mean silhouette over Euclidean distance. */
export function silhouette(points: number[][], assign: number[]): number {
  const n = points.length;
  if (n < 3) return 0;
  let total = 0;
  for (let i = 0; i < n; i++) {
    const sums = new Map<number, { s: number; c: number }>();
    for (let j = 0; j < n; j++) {
      if (i === j) continue;
      const e = sums.get(assign[j]!) ?? { s: 0, c: 0 };
      e.s += Math.sqrt(sqDist(points[i]!, points[j]!));
      e.c++;
      sums.set(assign[j]!, e);
    }
    const own = sums.get(assign[i]!);
    const a = own && own.c > 0 ? own.s / own.c : 0;
    let b = Infinity;
    for (const [c, e] of sums) if (c !== assign[i]) b = Math.min(b, e.s / e.c);
    if (!Number.isFinite(b)) continue;
    total += (b - a) / Math.max(a, b) || 0;
  }
  return total / n;
}
