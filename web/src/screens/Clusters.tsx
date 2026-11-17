/**
 * Topic clusters (Clusters board): the workspace's notes grouped by topic
 * (MLService.ListClusters) with labels, top terms and a 2-D map, and a
 * clustering benchmark (BenchmarkClustering) comparing algorithms on
 * held-out loss, time and distance computations. The map places notes
 * around their cluster's centre: the API returns no coordinates, so the
 * picture shows membership, not distances.
 */

import { useMemo, useState } from "react";
import { Link } from "react-router";
import { describeError } from "../api/errors";
import type { BenchmarkClusteringResponse } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Spinner } from "../components/ui";
import { useAsync } from "../lib/hooks";

const COLOURS = ["var(--acc)", "var(--vio)", "var(--grn)", "var(--fnc)", "var(--warn)", "var(--pink)"];
const CENTRES: [number, number][] = [
  [22, 30],
  [46, 24],
  [72, 28],
  [76, 70],
  [46, 72],
  [22, 74],
];
const ALGORITHMS = [
  ["CLUSTERING_ALGORITHM_E_MEANS", "e-means"],
  ["CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS", "Lloyd ++"],
  ["CLUSTERING_ALGORITHM_KMEANS_ELKAN", "Elkan"],
  ["CLUSTERING_ALGORITHM_KMEANS_RANDOM", "Lloyd random"],
  ["CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH", "Mini-batch"],
  ["CLUSTERING_ALGORITHM_GMM_EM", "GMM"],
] as const;

function seeded(seed: number) {
  let s = seed;
  return () => ((s = (s * 16807) % 2147483647), s / 2147483647);
}

export function Clusters() {
  const { api } = useServices();
  const ws = useWorkspace();
  const ui = useUi();
  const [algo, setAlgo] = useState<string>(ALGORITHMS[0][0]);
  const [bench, setBench] = useState<BenchmarkClusteringResponse | null>(null);
  const [running, setRunning] = useState(false);
  const clusters = useAsync(() => (ws.workspace ? api.call("MLService_ListClusters", { workspaceId: ws.workspace.id }) : Promise.resolve({ clusters: [] })), [ws.workspace?.id]);
  const list = useMemo(() => clusters.data?.clusters ?? [], [clusters.data]);
  const total = list.reduce((n, c) => n + (c.noteIds?.length ?? 0), 0);
  const points = useMemo(() => {
    const rnd = seeded(7);
    return list.flatMap((c, i) => {
      const [cx, cy] = CENTRES[i % CENTRES.length]!;
      return (c.noteIds ?? []).map((id) => {
        const a = rnd() * Math.PI * 2;
        const r = Math.sqrt(rnd()) * 11;
        return { id, i, x: cx + r * Math.cos(a), y: cy + r * 1.6 * Math.sin(a) };
      });
    });
  }, [list]);
  const run = bench?.results?.find((r) => r.algorithm === algo);
  const maxSeconds = Math.max(...(bench?.results ?? []).map((r) => r.meanSeconds ?? 0), 1e-9);

  async function benchmark() {
    if (!ws.workspace) return;
    setRunning(true);
    try {
      setBench(await api.call("MLService_BenchmarkClustering", { workspaceId: ws.workspace.id }, { algorithms: ALGORITHMS.map(([a]) => a), k: Math.max(2, list.length), runs: 3, seed: "7" }));
    } catch (error) {
      ui.toast(describeError(error), "bad");
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="app">
      <Header area="discover" here="Clusters" />
      <main id="main" className="ly-page">
        <div className="ly-head">
          <div className="ly-ttl">
            <div className="ly-crumb" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
              <span>Discover</span>
              <i>/</i>
              <span>Clusters</span>
            </div>
            <h1 className="ly-h1">
              {list.length} topics, found from <em className="rgb">the text alone.</em>
            </h1>
          </div>
          <div className="ly-tiles">
            <div className="ly-tl">
              <em>Notes</em>
              <strong>{total}</strong>
            </div>
            <div className="ly-tl hot" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
              <em>k</em>
              <strong>{list.length}</strong>
            </div>
            <div className="ly-tl">
              <em>Silhouette</em>
              <strong>{run?.silhouette?.toFixed(2) ?? "—"}</strong>
              <small>{run ? ALGORITHMS.find(([a]) => a === algo)?.[1] : "run the benchmark"}</small>
            </div>
          </div>
        </div>
        <div className="ly-lay tworight">
          <div className="ly-cp">
            <div className="kbar">
              <span className="lab">Algorithm</span>
              <div className="seg sm" role="group" aria-label="Clustering algorithm">
                {ALGORITHMS.map(([id, label]) => (
                  <button key={id} type="button" className={algo === id ? "on" : ""} aria-pressed={algo === id} style={{ "--c": "var(--grn)" } as React.CSSProperties} onClick={() => setAlgo(id)}>
                    {label}
                  </button>
                ))}
              </div>
              <span style={{ flex: 1 }} />
              <span className="cap">{run ? `${run.meanIterations} iterations · ${((run.meanSeconds ?? 0) * 1000).toFixed(1)} ms · ${run.implementation}` : "clusters from the most recent run"}</span>
            </div>
            <div style={{ position: "relative", height: 300, border: "1px solid var(--line2)", borderRadius: 16, overflow: "hidden", background: "radial-gradient(circle at 50% 50%, var(--panel), var(--bg))" }} role="img" aria-label={`${total} notes coloured by cluster`}>
              {list.map((c, i) => {
                const [cx, cy] = CENTRES[i % CENTRES.length]!;
                return (
                  <span key={`b${i}`}>
                    <span style={{ position: "absolute", borderRadius: "50%", filter: "blur(30px)", opacity: 0.5, background: `color-mix(in oklab, ${COLOURS[i % 6]} 30%, transparent)`, width: 220, height: 150, left: `calc(${cx}% - 110px)`, top: `calc(${cy}% - 75px)` }} />
                    <span style={{ position: "absolute", font: "700 11px var(--mono)", letterSpacing: ".14em", textTransform: "uppercase", color: COLOURS[i % 6], left: `calc(${cx}% - 70px)`, top: `calc(${cy}% - 72px)`, whiteSpace: "nowrap" }}>{c.label}</span>
                  </span>
                );
              })}
              {points.map((p) => (
                <Link key={p.id} to={`/notes/${p.id}`} aria-label={ws.notesById.get(p.id)?.title} title={ws.notesById.get(p.id)?.title} style={{ position: "absolute", width: 8, height: 8, margin: -4, borderRadius: "50%", background: COLOURS[p.i % 6], boxShadow: "0 0 0 2px var(--bg)", left: `${p.x}%`, top: `${p.y}%` }} />
              ))}
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))", gap: 12 }}>
              {list.map((c, i) => (
                <section key={i} className="ly-pn" style={{ "--c": COLOURS[i % 6] } as React.CSSProperties}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <span className="tp" style={{ "--c": COLOURS[i % 6] } as React.CSSProperties}>
                      auto-label
                    </span>
                    <span style={{ flex: 1 }} />
                    <span className="m" style={{ fontSize: 12, color: "var(--fg)" }}>
                      {c.noteIds?.length} notes
                    </span>
                  </div>
                  <h4 className="gr-h">{c.label}</h4>
                  <div className="chips" style={{ gap: 5 }}>
                    {(c.topTerms ?? []).map((t) => (
                      <span key={t} className="chip sm">
                        {t}
                      </span>
                    ))}
                  </div>
                  <span className="cap">e.g. {ws.notesById.get(c.noteIds?.[0] ?? "")?.title ?? "—"}</span>
                </section>
              ))}
            </div>
            <section className="ly-pn" style={{ padding: "16px 18px" }}>
              <h5 className="ly-pnh">
                <span>Benchmark · note embeddings, k {Math.max(2, list.length)}, 3 runs each</span>
                <button type="button" onClick={() => void benchmark()} disabled={running}>
                  {running ? <Spinner /> : null} {bench ? "re-run" : "run benchmark"}
                </button>
              </h5>
              {!bench && <span className="cap">The benchmark is expensive (it counts against the agent budget), so it runs when you ask.</span>}
              {bench && (
                <>
                  <div style={{ overflowX: "auto", overscrollBehavior: "contain" }}>
                    <table className="sk-tbl" style={{ fontSize: 13, minWidth: 680 }}>
                      <thead>
                        <tr>
                          <th>Variant</th>
                          <th className="n">Epochs</th>
                          <th className="n">Wall time</th>
                          <th className="n">Distance comps</th>
                          <th className="n">Held-out loss</th>
                          <th className="n">Silhouette</th>
                        </tr>
                      </thead>
                      <tbody>
                        {(bench.results ?? []).map((r) => (
                          <tr key={`${r.algorithm}${r.implementation}`} style={r.algorithm === algo ? { background: "var(--grn-bg)" } : undefined}>
                            <td>
                              <b>{ALGORITHMS.find(([a]) => a === r.algorithm)?.[1] ?? r.algorithm}</b>
                              <br />
                              <span className="cap">{r.implementation}</span>
                            </td>
                            <td className="n">{r.epochs}</td>
                            <td className="n">{((r.meanSeconds ?? 0) * 1000).toFixed(1)} ms</td>
                            <td className="n">{Number(r.distanceComputations ?? 0).toLocaleString()}</td>
                            <td className="n">{r.testObjective?.toFixed(4)}</td>
                            <td className="n">{r.silhouette?.toFixed(3)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  <div style={{ display: "grid", gap: 6 }}>
                    {(bench.results ?? []).map((r) => (
                      <div key={`bar${r.algorithm}`} className="rdbar" style={{ "--c": r.algorithm === algo ? "var(--grn)" : "var(--dim)" } as React.CSSProperties}>
                        <div className="t">
                          <span>{ALGORITHMS.find(([a]) => a === r.algorithm)?.[1]}</span>
                          <b>{((r.meanSeconds ?? 0) * 1000).toFixed(1)} ms</b>
                        </div>
                        <i>
                          <u style={{ width: `${Math.max(2, Math.round(((r.meanSeconds ?? 0) / maxSeconds) * 100))}%` }} />
                        </i>
                      </div>
                    ))}
                  </div>
                  <p>Compare algorithms on held-out loss (mean squared distance from held-out points to their nearest centroid), not training objective.</p>
                </>
              )}
            </section>
          </div>
          <aside className="ly-rp">
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>e-means</span>
              </h5>
              <p>An exponential moving average of each cluster's sum and count, with batch size and step from scaling laws. It reaches Lloyd's held-out loss in a fraction of the epochs and revives empty clusters.</p>
            </section>
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>Sources</span>
              </h5>
              <p>Clusters and the benchmark come from the ml service (MLService.ListClusters and BenchmarkClustering).</p>
            </section>
          </aside>
        </div>
      </main>
    </div>
  );
}
