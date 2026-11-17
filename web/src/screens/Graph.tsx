/**
 * The graph explorer (GraphExplorer board), extending genuine.dev's graph
 * workspace (glass side panels over a full-bleed canvas, force layout,
 * legend, controls, minimap) with an algorithm toolbar and an inspector that
 * shows the numbers behind each result:
 *
 *   Blast radius (BlastRadius, either direction, depth), Shortest path
 *   (hops or semantic), Reading path; Strongly connected, Cycle, Components
 *   and orphans, Topological order; Bridges (betweenness), PageRank,
 *   Communities (AnalyzeGraph); and the Start / Like / Path modes.
 *
 * Notes are buttons: Tab reaches the canvas, arrow keys move to the nearest
 * note in that direction, Enter selects. The toolbar is a toolbar with
 * roving focus (←/→), and 1 / 2 / 3 switch modes.
 */

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Link, useNavigate } from "react-router";
import type { AnalyzeGraphResponse, GetLinkGraphResponse } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { SearchIcon } from "../components/ui";
import { initialInteraction, interact, routeEnds } from "../graph/interaction";
import { ARROW_DIRECTION, nextInDirection } from "../graph/keys";
import { ringRadius, rings, settle } from "../graph/layout";
import { downloadCsv } from "../lib/format";
import { useAsync, useHotkey } from "../lib/hooks";

type Algo = "blast" | "path" | "read" | "scc" | "cycle" | "comps" | "topo" | "between" | "pr" | "comm" | "start" | "like";

const GROUPS: [Algo, string][][] = [
  [
    ["blast", "Blast radius"],
    ["path", "Shortest path"],
    ["read", "Reading path"],
  ],
  [
    ["scc", "Strongly connected"],
    ["cycle", "Cycle"],
    ["comps", "Components"],
    ["topo", "Topological order"],
  ],
  [
    ["between", "Bridges"],
    ["pr", "PageRank"],
    ["comm", "Communities"],
  ],
];

const TITLE: Record<Algo, [string, string, string]> = {
  blast: ["Blast radius", "Who is affected if this note changes?", "reverse BFS · O(V+E)"],
  path: ["Shortest path", "How are two notes connected?", "BFS or Dijkstra"],
  read: ["Reading path", "What should I read first, in order?", "condense loops, then Kahn"],
  scc: ["Strongly connected", "Which notes cite each other in a loop?", "Tarjan · O(V+E)"],
  cycle: ["Cycle", "Show me one loop.", "3-colour DFS · O(V+E)"],
  comps: ["Components and orphans", "What is cut off from the rest?", "flood fill + BFS from roots"],
  topo: ["Topological order", "Is there a reading order for everything?", "Kahn · O(V+E)"],
  between: ["Bridges", "Which notes hold the graph together?", "Brandes · O(VE)"],
  pr: ["PageRank", "Which notes are foundations?", "power iteration"],
  comm: ["Communities", "What groups form from the links alone?", "Louvain"],
  start: ["Where to start", "Most-cited notes pulse.", "PageRank top 3"],
  like: ["Like this note", "Nearest by meaning, with tags and hops beside it.", "Discover"],
};

const PALETTE = ["var(--grn)", "var(--fnc)", "var(--acc)", "var(--vio)", "var(--warn)", "var(--pink)"];
const pal = (i: number) => PALETTE[((i % PALETTE.length) + PALETTE.length) % PALETTE.length]!;

type NodeLook = { colour: string; size: number; dim?: boolean; hide?: boolean; sel?: boolean; mark?: boolean; orph?: boolean; start?: boolean; badge?: string };
type EdgeLook = "" | "dim" | "hi" | "pth" | "scc" | "cyc";

export function GraphExplorer() {
  const { api } = useServices();
  const ws = useWorkspace();
  const navigate = useNavigate();
  const workspaceId = ws.workspace?.id;
  const [algo, setAlgo] = useState<Algo>("start");
  const [state, setState] = useState(initialInteraction(null));
  const [dir, setDir] = useState<"in" | "out">("in");
  const [depth, setDepth] = useState(4);
  const [semantic, setSemantic] = useState(false);
  const [labels, setLabels] = useState(true);
  const [panels, setPanels] = useState({ left: true, right: true });
  const [view, setView] = useState({ k: 1, x: 0, y: 0 });
  const [find, setFind] = useState("");
  const [timing, setTiming] = useState<Record<string, number>>({});
  const canvas = useRef<HTMLDivElement>(null);
  const drag = useRef<{ x: number; y: number; vx: number; vy: number } | null>(null);

  const timed = <T,>(key: string, p: Promise<T>) => {
    const t0 = performance.now();
    return p.then((v) => (setTiming((t) => ({ ...t, [key]: Math.round((performance.now() - t0) * 10) / 10 })), v));
  };
  const graph = useAsync<GetLinkGraphResponse>(() => (workspaceId ? timed("graph", api.call("GraphService_GetLinkGraph", { workspaceId })) : Promise.resolve({})), [workspaceId]);
  const analysis = useAsync<AnalyzeGraphResponse>(() => (workspaceId ? timed("analysis", api.call("GraphService_AnalyzeGraph", { workspaceId })) : Promise.resolve({})), [workspaceId]);
  const nodes = useMemo(() => graph.data?.nodes ?? [], [graph.data]);
  const edges = useMemo(() => (graph.data?.edges ?? []).map((e) => [e.from!, e.to!] as [string, string]), [graph.data]);
  const title = (id: string | undefined) => nodes.find((n) => n.noteId === id)?.title ?? ws.notesById.get(id ?? "")?.title ?? "…";
  const a = analysis.data ?? {};
  const pr = useMemo(() => a.pagerank ?? {}, [a.pagerank]);
  const ranked = useMemo(() => [...nodes].sort((x, y) => (pr[y.noteId!] ?? 0) - (pr[x.noteId!] ?? 0)), [nodes, pr]);
  const selected = state.selected ?? ranked[0]?.noteId ?? null;

  const degree = useMemo(() => {
    const d: Record<string, { in: number; out: number }> = {};
    for (const n of nodes) d[n.noteId!] = { in: 0, out: 0 };
    for (const [f, t] of edges) {
      if (d[f]) d[f].out++;
      if (d[t]) d[t].in++;
    }
    return d;
  }, [nodes, edges]);

  const forceLayout = useMemo(
    () => settle(nodes.map((n) => ({ id: n.noteId!, topic: String(a.communityOf?.[n.noteId!] ?? 0), rank: pr[n.noteId!] ?? 0.01 })), edges.map(([from, to]) => ({ from, to, kind: "link" as const }))),
    [nodes, edges, a.communityOf, pr],
  );

  const blast = useAsync(async () => {
    if (algo !== "blast" || !selected) return null;
    const call = (direction: "BLAST_DIRECTION_CITED_BY" | "BLAST_DIRECTION_CITES") => api.call("GraphService_BlastRadius", { noteId: selected, direction, maxHops: depth >= 4 ? 0 : depth });
    const [cited, cites] = await timed("blast", Promise.all([call("BLAST_DIRECTION_CITED_BY"), call("BLAST_DIRECTION_CITES")]));
    return { cited, cites };
  }, [algo, selected, depth]);
  const route = routeEnds(state);
  const path = useAsync(async () => {
    if (algo !== "path" || !route) return null;
    const [chosen, other] = await timed(
      "path",
      Promise.all([
        api.call("GraphService_ShortestPath", { from: route.from, to: route.to, metric: semantic ? "PATH_METRIC_SEMANTIC" : "PATH_METRIC_HOPS" }),
        api.call("GraphService_ShortestPath", { from: route.from, to: route.to, metric: semantic ? "PATH_METRIC_HOPS" : "PATH_METRIC_SEMANTIC" }),
      ]),
    );
    return { chosen, other };
  }, [algo, route?.from, route?.to, semantic]);
  const reading = useAsync(() => (algo === "read" && selected ? timed("read", api.call("GraphService_ReadingPath", { noteId: selected })) : Promise.resolve(null)), [algo, selected]);
  const like = useAsync(() => (algo === "like" && selected ? timed("like", api.call("MLService_Discover", { noteId: selected, k: 6 })) : Promise.resolve(null)), [algo, selected]);

  useEffect(() => {
    if (algo === "path" && !state.pathFrom && !state.pathTo && ranked.length > 1) setState((s) => interact(s, { type: "set-route", from: ranked[0]!.noteId!, to: ranked[Math.min(5, ranked.length - 1)]!.noteId! }));
  }, [algo, ranked, state.pathFrom, state.pathTo]);

  useHotkey("1", () => setAlgo("start"));
  useHotkey("2", () => setAlgo("like"));
  useHotkey("3", () => setAlgo("path"));
  useHotkey("b", () => setPanels((p) => ({ ...p, left: !p.left })), { meta: true });

  // ── what each algorithm draws ──────────────────────────────────────────────
  const blastHops = dir === "in" ? blast.data?.cited.hops : blast.data?.cites.hops;
  const positions = algo === "blast" && selected && blastHops ? new Map([...forceLayout, ...rings(selected, new Map(Object.entries(blastHops)))]) : forceLayout;
  const sccSize = (id: string) => (a.sccSizes ?? [])[a.sccOf?.[id] ?? -1] ?? 1;
  const orphanSet = new Set((a.orphanComponents ?? []).map(Number));
  const cycle = a.cycle ?? [];
  const topo = a.topologicalOrder ?? [];
  const pathNodes = path.data?.chosen.found ? (path.data.chosen.path ?? []) : [];
  const onPath = (x: string, y: string) => {
    const i = pathNodes.indexOf(x);
    const j = pathNodes.indexOf(y);
    return i >= 0 && j >= 0 && Math.abs(i - j) === 1;
  };
  const readSteps = reading.data?.steps ?? [];
  const likeIds = (like.data?.related ?? []).map((r) => r.noteId!);
  const startIds = ranked.slice(0, 3).map((n) => n.noteId!);
  const baseSize = (id: string) => 8 + Math.min(14, (degree[id]?.in ?? 0) + (degree[id]?.out ?? 0)) * 1.2;
  const topicColour = (id: string) => pal(a.communityOf?.[id] ?? 0);

  function look(id: string): NodeLook {
    const base: NodeLook = { colour: topicColour(id), size: baseSize(id) };
    switch (algo) {
      case "blast": {
        const h = blastHops?.[id];
        return { ...base, sel: id === selected, hide: h === undefined, badge: h ? `${h} hop${h > 1 ? "s" : ""}` : "" };
      }
      case "path":
        return { ...base, mark: pathNodes.includes(id), dim: pathNodes.length > 0 && !pathNodes.includes(id), sel: id === state.pathFrom || id === state.pathTo, badge: pathNodes.includes(id) ? String(pathNodes.indexOf(id)) : "" };
      case "read": {
        const i = readSteps.findIndex((s) => s.noteId === id);
        return { ...base, sel: id === selected, dim: i < 0 && id !== selected, badge: id === selected ? "target" : i >= 0 ? String(i + 1) : "" };
      }
      case "scc":
        return sccSize(id) > 1 ? { colour: pal(a.sccOf?.[id] ?? 0), size: 14 } : { colour: "var(--dim)", size: 8, dim: true };
      case "cycle":
        return cycle.includes(id) ? { colour: "var(--warn)", size: 14, sel: true } : { ...base, dim: true };
      case "comps": {
        const c = a.componentOf?.[id] ?? 0;
        const orph = orphanSet.has(c);
        return { colour: orph ? pal(c + 4) : "var(--fnc)", size: baseSize(id), orph, badge: orph ? "orphan" : nodes.find((n) => n.noteId === id)?.isRoot ? "root" : "" };
      }
      case "topo": {
        const i = topo.indexOf(id);
        return i >= 0 ? { ...base, badge: `#${i + 1}` } : { colour: "var(--dim)", size: 8, orph: true };
      }
      case "between": {
        const b = a.betweenness?.[id] ?? 0;
        return { ...base, size: 7 + b * 60, dim: b === 0, badge: b >= 0.1 ? b.toFixed(3) : "" };
      }
      case "pr": {
        const r = pr[id] ?? 0;
        return { ...base, size: 7 + r * 220, badge: startIds.includes(id) ? r.toFixed(3) : "" };
      }
      case "comm":
        return { colour: pal(a.communityOf?.[id] ?? 0), size: baseSize(id) };
      case "start":
        return { ...base, start: startIds.includes(id), sel: id === selected };
      case "like":
        return { ...base, sel: id === selected, mark: likeIds.includes(id), dim: id !== selected && !likeIds.includes(id) };
    }
  }

  function edgeLook(f: string, t: string): EdgeLook {
    switch (algo) {
      case "blast":
        return blastHops?.[f] !== undefined && blastHops?.[t] !== undefined ? "hi" : "dim";
      case "path":
        return onPath(f, t) ? "pth" : "dim";
      case "scc":
        return a.sccOf?.[f] === a.sccOf?.[t] && sccSize(f) > 1 ? "scc" : "dim";
      case "cycle": {
        const i = cycle.indexOf(f);
        return i >= 0 && cycle[i + 1] === t ? "cyc" : "dim";
      }
      case "topo":
        return "dim";
      case "comm":
        return a.communityOf?.[f] === a.communityOf?.[t] ? "" : "dim";
      case "read":
        return (readSteps.some((s) => s.noteId === f) || f === selected) && (readSteps.some((s) => s.noteId === t) || t === selected) ? "hi" : "dim";
      case "start":
      case "like":
        return f === selected || t === selected ? "hi" : "dim";
      default:
        return "";
    }
  }

  const focusNode = (id: string) => (canvas.current?.querySelector(`[data-node="${CSS.escape(id)}"]`) as HTMLElement | null)?.focus();
  function onNodeKey(e: KeyboardEvent<HTMLButtonElement>, id: string) {
    const direction = ARROW_DIRECTION[e.key];
    if (!direction) return;
    e.preventDefault();
    const here = positions.get(id);
    if (!here) return;
    const next = nextInDirection({ id, ...here }, [...positions].map(([pid, p]) => ({ id: pid, ...p })), direction);
    if (next) focusNode(next);
  }

  function onToolbarKey(e: KeyboardEvent<HTMLElement>) {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const buttons = [...e.currentTarget.querySelectorAll<HTMLButtonElement>("button")];
    const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "Home" ? 0 : e.key === "End" ? buttons.length - 1 : (i + (e.key === "ArrowRight" ? 1 : buttons.length - 1)) % buttons.length;
    buttons[next]?.focus();
  }

  const fit = () => setView({ k: 1, x: 0, y: 0 });
  const matches = find ? nodes.filter((n) => n.title?.toLowerCase().includes(find.toLowerCase())).slice(0, 6) : [];
  const [heading, ask, cost] = TITLE[algo];
  const blastRows = (() => {
    const h = blastHops ?? {};
    const byRing = new Map<number, string[]>();
    for (const [id, d] of Object.entries(h)) if (d > 0) byRing.set(d, [...(byRing.get(d) ?? []), id]);
    return [...byRing].sort((x, y) => x[0] - y[0]);
  })();
  const reached = blastRows.reduce((n, [, ids]) => n + ids.length, 0);
  const deepest = blastRows.length;

  return (
    <div className="app">
      <Header area="graph" here={`Graph · ${ws.workspace?.name ?? ""}`} />
      <div className="gfull" id="main">
        <aside className={`gside l${panels.left ? "" : " gone"}`} aria-label="Inputs">
          <div className="gr-modes" role="group" aria-label="Mode">
            {(
              [
                ["start", "Start", "1"],
                ["like", "Like", "2"],
                ["path", "Path", "3"],
              ] as const
            ).map(([id, label, k]) => (
              <button key={id} type="button" className={algo === id ? "on" : ""} aria-pressed={algo === id} onClick={() => setAlgo(id)}>
                {label}
                <kbd>{k}</kbd>
              </button>
            ))}
          </div>
          <section className="ly-pn">
            <h4 className="gr-h">{heading}</h4>
            <p>{ask}</p>
            {(algo === "blast" || algo === "read" || algo === "like" || algo === "start") && (
              <div className="fld">
                <label htmlFor="g-src">{algo === "read" ? "Before reading" : algo === "blast" ? "Source" : "Note"}</label>
                <NotePick id="g-src" value={selected ?? ""} nodes={nodes} onChange={(id) => setState((s) => interact(s, { type: "select", id }))} />
              </div>
            )}
            {algo === "blast" && (
              <>
                <div className="fld">
                  <span className="lb">Follow links</span>
                  <div className="seg" style={{ width: "100%" }} role="group" aria-label="Direction">
                    <button type="button" className={dir === "in" ? "on" : ""} aria-pressed={dir === "in"} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setDir("in")}>
                      Cited by · {Math.max(0, Object.keys(blast.data?.cited.hops ?? {}).length - 1)}
                    </button>
                    <button type="button" className={dir === "out" ? "on" : ""} aria-pressed={dir === "out"} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setDir("out")}>
                      Cites · {Math.max(0, Object.keys(blast.data?.cites.hops ?? {}).length - 1)}
                    </button>
                  </div>
                </div>
                <div className="fld">
                  <label htmlFor="depth">Depth · {depth >= 4 ? "all" : `${depth} hops`}</label>
                  <input id="depth" type="range" min={1} max={4} step={1} value={depth} onChange={(e) => setDepth(Number(e.target.value))} style={{ width: "100%", accentColor: "var(--fnc)" }} />
                  <div className="cap" style={{ display: "flex", justifyContent: "space-between" }}>
                    <span>1</span>
                    <span>2</span>
                    <span>3</span>
                    <span>4 · all</span>
                  </div>
                </div>
              </>
            )}
            {algo === "path" && (
              <>
                <div className="fld">
                  <label htmlFor="g-from">From</label>
                  <NotePick id="g-from" value={state.pathFrom} nodes={nodes} onChange={(id) => setState((s) => interact(s, { type: "set-route", from: id, to: s.pathTo }))} />
                </div>
                <div className="fld">
                  <label htmlFor="g-to">To</label>
                  <NotePick id="g-to" value={state.pathTo} nodes={nodes} onChange={(id) => setState((s) => interact(s, { type: "set-route", from: s.pathFrom, to: id }))} />
                </div>
                <button type="button" className="btn xs" onClick={() => setState((s) => interact(s, { type: "pick-route" }))}>
                  {state.picking ? `Click the ${state.picking === "from" ? "start" : "end"} note on the graph…` : "Pick both ends on the graph"}
                </button>
                <div className="fld">
                  <span className="lb">Distance</span>
                  <div className="seg" style={{ width: "100%" }} role="group" aria-label="Metric">
                    <button type="button" className={!semantic ? "on" : ""} aria-pressed={!semantic} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setSemantic(false)}>
                      Link hops
                    </button>
                    <button type="button" className={semantic ? "on" : ""} aria-pressed={semantic} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setSemantic(true)}>
                      Semantic
                    </button>
                  </div>
                </div>
                <span className="cap">Hops: every link costs 1, direction ignored. Semantic: cost 1 − cosine along links.</span>
              </>
            )}
            {["scc", "cycle", "comps", "topo", "between", "pr", "comm"].includes(algo) && (
              <div className="ly-kv" style={{ fontSize: 12.5 }}>
                <div>
                  <span>Runs on</span>
                  <b>the whole workspace</b>
                </div>
                <div>
                  <span>Scope</span>
                  <b>{nodes.length} notes</b>
                </div>
                <div>
                  <span>{algo === "pr" ? "Damping" : "Edges used"}</span>
                  <b>{algo === "pr" ? "0.85" : "[[links]] only"}</b>
                </div>
              </div>
            )}
          </section>
          <section className="ly-pn">
            <h5 className="ly-pnh">
              <span>Scope</span>
            </h5>
            <span className="cap">Only notes you can open are drawn. {ws.notes.length} in the workspace.</span>
          </section>
        </aside>

        <div
          className="gcanvas"
          ref={canvas}
          onWheel={(e) => setView((v) => ({ ...v, k: Math.max(0.4, Math.min(3, v.k * (e.deltaY < 0 ? 1.1 : 0.9))) }))}
          onPointerDown={(e) => {
            if ((e.target as HTMLElement).closest("button, input, select, a, .gside, .gr-legend, .gr-ctl, .sk-algos, .gtop")) return;
            drag.current = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
            (e.target as HTMLElement).setPointerCapture?.(e.pointerId);
          }}
          onPointerMove={(e) => drag.current && setView((v) => ({ ...v, x: drag.current!.vx + e.clientX - drag.current!.x, y: drag.current!.vy + e.clientY - drag.current!.y }))}
          onPointerUp={(e) => {
            const d = drag.current;
            drag.current = null;
            if (d && Math.hypot(e.clientX - d.x, e.clientY - d.y) < 4 && state.picking) setState((s) => interact(s, { type: "select", id: null }));
          }}
        >
          <div className="gtop">
            <div className="lay">
              <button type="button" className={panels.left ? "on" : ""} aria-pressed={panels.left} aria-label="Toggle left panel" title="Toggle left panel ⌘B" onClick={() => setPanels((p) => ({ ...p, left: !p.left }))}>
                ◧
              </button>
            </div>
            <div className="hd-srch" style={{ position: "relative" }}>
              <SearchIcon />
              <input aria-label="Find a note on the graph" placeholder="Find a note on the graph…" value={find} onChange={(e) => setFind(e.target.value)} style={{ flex: 1, border: 0, background: "none", outline: "none", color: "var(--fg)", font: "inherit" }} />
              {matches.length > 0 && (
                <div className="ed-pop" style={{ top: 40, left: 0, right: 0, padding: 4, display: "grid" }}>
                  {matches.map((m) => (
                    <button
                      key={m.noteId}
                      type="button"
                      className="frow"
                      onClick={() => {
                        setState((s) => interact(s, { type: "select", id: m.noteId! }));
                        setFind("");
                        setTimeout(() => focusNode(m.noteId!), 0);
                      }}
                    >
                      <span>{m.title}</span>
                      <span />
                    </button>
                  ))}
                </div>
              )}
            </div>
            <div className="lay">
              <button type="button" className={panels.right ? "on" : ""} aria-pressed={panels.right} aria-label="Toggle inspector" onClick={() => setPanels((p) => ({ ...p, right: !p.right }))}>
                ◨
              </button>
            </div>
          </div>
          <div className="sk-algos" role="toolbar" aria-label="Algorithms" aria-orientation="horizontal" onKeyDown={onToolbarKey}>
            {GROUPS.map((group, gi) => (
              <span key={gi} style={{ display: "contents" }}>
                {gi > 0 && <i aria-hidden="true" />}
                {group.map(([id, label]) => (
                  <button key={id} type="button" tabIndex={id === algo || (!GROUPS.flat().some(([x]) => x === algo) && id === "blast") ? 0 : -1} className={algo === id ? "on" : ""} aria-pressed={algo === id} onClick={() => setAlgo(id)}>
                    {label}
                  </button>
                ))}
              </span>
            ))}
          </div>
          <div className="gstage" style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})`, transformOrigin: "50% 50%" }}>
            {algo === "blast" &&
              dir === "in" &&
              blastRows.map(([d, ids]) => (
                <span key={d} className="gring" style={{ left: "50%", top: "50%", width: `${ringRadius(d, deepest) * 200}%`, height: `${ringRadius(d, deepest) * 200}%` }}>
                  <b>
                    {d} hop{d > 1 ? "s" : ""} · {ids.length}
                  </b>
                </span>
              ))}
            <svg className="gln" viewBox="0 0 1000 1000" preserveAspectRatio="none" aria-hidden="true">
              <defs>
                <marker id="arr" viewBox="0 0 10 10" refX="10" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
                  <path d="M0 0 L10 5 L0 10z" fill="color-mix(in oklab, var(--mut) 50%, transparent)" />
                </marker>
              </defs>
              {edges.map(([f, t]) => {
                const p = positions.get(f);
                const q = positions.get(t);
                const cls = edgeLook(f, t);
                if (!p || !q || (algo === "blast" && cls === "dim" && (blastHops?.[f] === undefined || blastHops?.[t] === undefined))) return null;
                return <line key={`${f}>${t}`} className={cls} style={cls === "scc" ? ({ "--c": pal(a.sccOf?.[f] ?? 0) } as React.CSSProperties) : undefined} x1={p.x * 1000} y1={p.y * 1000} x2={q.x * 1000} y2={q.y * 1000} />;
              })}
            </svg>
            {nodes.map((n) => {
              const id = n.noteId!;
              const p = positions.get(id);
              if (!p) return null;
              const l = look(id);
              const cls = ["gn", n.isRoot ? "root" : "", l.size >= 16 ? "big" : "", l.sel ? "sel" : "", l.mark ? "pth" : "", l.dim ? "dim" : "", l.orph ? "orph" : "", l.start ? "start" : "", l.hide ? "hidden" : ""].filter(Boolean).join(" ");
              return (
                <button
                  key={id}
                  type="button"
                  data-node={id}
                  className={cls}
                  tabIndex={l.hide ? -1 : 0}
                  aria-label={`${n.title}${l.badge ? `, ${l.badge}` : ""}`}
                  aria-pressed={id === selected}
                  style={{ left: `${p.x * 100}%`, top: `${p.y * 100}%`, "--c": l.colour, "--s": `${l.size.toFixed(1)}px` } as React.CSSProperties}
                  onClick={() => setState((s) => interact(s, { type: "select", id }))}
                  onDoubleClick={() => navigate(`/notes/${id}`)}
                  onKeyDown={(e) => onNodeKey(e, id)}
                >
                  <span className="dt" />
                  {(labels || l.sel || l.badge) && !l.hide && (
                    <span className="lb">
                      {n.title}
                      {l.badge ? <b>{l.badge}</b> : null}
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          <Legend algo={algo} a={a} nodes={nodes.length} edges={edges.length} />
          <div className="gr-ctl">
            <span className="cap" style={{ padding: "0 8px" }}>
              <b style={{ color: "var(--fg)" }}>{nodes.length}</b> notes · <b style={{ color: "var(--fg)" }}>{edges.length}</b> links
            </span>
            <i />
            <button type="button" aria-label="Zoom out" onClick={() => setView((v) => ({ ...v, k: Math.max(0.4, v.k / 1.2) }))}>
              −
            </button>
            <button type="button" onClick={fit}>
              {Math.round(view.k * 100)}%
            </button>
            <button type="button" aria-label="Zoom in" onClick={() => setView((v) => ({ ...v, k: Math.min(3, v.k * 1.2) }))}>
              +
            </button>
            <i />
            <button type="button" onClick={fit}>
              Fit
            </button>
            <button type="button" className={labels ? "on" : ""} aria-pressed={labels} onClick={() => setLabels((l) => !l)}>
              Labels
            </button>
            <button
              type="button"
              onClick={() =>
                downloadCsv("skein-graph.csv", [
                  ["note_id", "title", "in", "out", "pagerank", "betweenness", "scc", "component", "community"],
                  ...nodes.map((n) => [n.noteId!, n.title ?? "", degree[n.noteId!]?.in ?? 0, degree[n.noteId!]?.out ?? 0, pr[n.noteId!] ?? 0, a.betweenness?.[n.noteId!] ?? 0, a.sccOf?.[n.noteId!] ?? 0, a.componentOf?.[n.noteId!] ?? 0, a.communityOf?.[n.noteId!] ?? 0]),
                ])
              }
            >
              Export CSV
            </button>
          </div>
          <div className="gmini" aria-hidden="true">
            {[...positions].map(([id, p]) => (
              <i key={id} style={{ left: `${p.x * 100}%`, top: `${p.y * 100}%`, "--c": look(id).colour } as React.CSSProperties} />
            ))}
            <b style={{ "--vx": `${50 - 50 / view.k - (view.x / (canvas.current?.clientWidth ?? 1000)) * 100 / view.k}%`, "--vy": `${50 - 50 / view.k - (view.y / (canvas.current?.clientHeight ?? 700)) * 100 / view.k}%`, "--vw": `${100 / view.k}%`, "--vh": `${100 / view.k}%` } as React.CSSProperties} />
          </div>
          {(graph.loading || analysis.loading) && (
            <div className="sk-empty" style={{ position: "absolute", inset: 0 }} aria-busy="true">
              <span className="spin" />
              Laying out the graph…
            </div>
          )}
        </div>

        <aside className={`gside r${panels.right ? "" : " gone"}`} aria-label="Inspector" aria-live="polite">
          <section className="ly-pn">
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <span className="pill c" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
                {heading}
              </span>
              <span className="cap">{cost.split(" · ")[0]}</span>
            </div>
            <Inspector
              algo={algo}
              a={a}
              title={title}
              selected={selected}
              degree={degree}
              dir={dir}
              depth={depth}
              blastRows={blastRows}
              reached={reached}
              total={nodes.length}
              path={path.data}
              semantic={semantic}
              reading={readSteps}
              like={like.data?.related ?? []}
              ranked={ranked.map((n) => n.noteId!)}
              edges={edges}
            />
          </section>
          <section className="ly-pn">
            <h5 className="ly-pnh">
              <span>Check it</span>
            </h5>
            <div className="sk-cost">
              <span>{cost}</span>
              <b>{timing[algo] ?? timing.analysis ?? "…"} ms</b>
            </div>
            <span className="cap">Computed on the server from the same link table the editor writes; the time is the round trip. The agent and the API return these numbers too.</span>
          </section>
        </aside>
      </div>
    </div>
  );
}

function NotePick({ id, value, nodes, onChange }: { id: string; value: string; nodes: GetLinkGraphResponse["nodes"]; onChange: (id: string) => void }) {
  return (
    <select id={id} className="selbtn" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="" disabled>
        Choose a note…
      </option>
      {[...(nodes ?? [])]
        .sort((x, y) => (x.title ?? "").localeCompare(y.title ?? ""))
        .map((n) => (
          <option key={n.noteId} value={n.noteId}>
            {n.title}
          </option>
        ))}
    </select>
  );
}

function Legend({ algo, a, nodes, edges }: { algo: Algo; a: AnalyzeGraphResponse; nodes: number; edges: number }) {
  const count = (map: Record<string, number> | undefined) => {
    const out = new Map<number, number>();
    for (const v of Object.values(map ?? {})) out.set(v, (out.get(v) ?? 0) + 1);
    return [...out].sort((x, y) => y[1] - x[1]).slice(0, 6);
  };
  let head = "Communities";
  let rows: [string, string, number][] = count(a.communityOf).map(([c, n]) => [pal(c), `group ${c + 1}`, n]);
  if (algo === "scc") {
    head = "Loops";
    rows = (a.sccSizes ?? []).map((s, i) => [pal(i), `loop ${i + 1}`, s] as [string, string, number]).filter((r) => r[2] > 1).slice(0, 6);
    rows.push(["var(--dim)", "not in a loop", (a.sccSizes ?? []).filter((s) => s === 1).length]);
  }
  if (algo === "comps") {
    head = "Components";
    const orphans = new Set((a.orphanComponents ?? []).map(Number));
    rows = count(a.componentOf).map(([c, n]) => [orphans.has(c) ? pal(c + 4) : "var(--fnc)", orphans.has(c) ? `orphan component ${c}` : "joined to a root", n]);
  }
  return (
    <div className="gr-legend">
      <span className="gr-lh">{head}</span>
      {rows.map(([c, label, n], i) => (
        <span key={i} className="gr-li is-key">
          <span className="gr-sw" style={{ background: c }} />
          {label}
          <em>{n}</em>
        </span>
      ))}
      <span className="gr-lh">Edges</span>
      <span className="gr-li is-key">
        <span className="gr-esw" />
        [[link]] · direction = who cites<em>{edges}</em>
      </span>
      <span className="gr-li is-key">
        <span className="gr-sw" />
        notes<em>{nodes}</em>
      </span>
    </div>
  );
}

type InspectorProps = {
  algo: Algo;
  a: AnalyzeGraphResponse;
  title: (id: string | undefined) => string;
  selected: string | null;
  degree: Record<string, { in: number; out: number }>;
  dir: "in" | "out";
  depth: number;
  blastRows: [number, string[]][];
  reached: number;
  total: number;
  path: { chosen: { found?: boolean; path?: string[]; cost?: number }; other: { found?: boolean; cost?: number; path?: string[] } } | null | undefined;
  semantic: boolean;
  reading: { noteId?: string; reason?: string; depth?: number }[];
  like: { noteId?: string; cosine?: number; sharedTags?: number; tagJaccard?: number; hops?: number }[];
  ranked: string[];
  edges: [string, string][];
};

function Inspector(p: InspectorProps) {
  const { a, title } = p;
  const groupsOf = (map: Record<string, number> | undefined) => {
    const g = new Map<number, string[]>();
    for (const [id, c] of Object.entries(map ?? {})) g.set(c, [...(g.get(c) ?? []), id]);
    return [...g].sort((x, y) => y[1].length - x[1].length);
  };
  const names = (ids: string[], n = 3) => ids.slice(0, n).map(title).join(", ") + (ids.length > n ? ` and ${ids.length - n} more` : "");

  switch (p.algo) {
    case "blast":
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {p.dir === "in" ? (p.reached ? `If ${title(p.selected ?? "")} changes, ${p.reached} notes cite it${p.depth < 4 ? ` within ${p.depth} hops` : " directly or indirectly"}.` : `Nothing cites ${title(p.selected ?? "")}.`) : p.reached ? `${title(p.selected ?? "")} depends on ${p.reached} notes.` : `${title(p.selected ?? "")} cites nothing.`}
          </h4>
          <p>{p.dir === "in" ? "Everything that links to it, then everything that links to those, ring by ring: the notes that may go stale when it is edited." : "Following its outbound links: what it builds on."}</p>
          <div className="gr-ird">
            <div>
              <span>Reached</span>
              <b>{p.reached}</b>
            </div>
            <div>
              <span>Of scope</span>
              <b>{p.total ? Math.round((p.reached / p.total) * 100) : 0}%</b>
            </div>
            <div>
              <span>Deepest</span>
              <b>{p.blastRows.length ? `${p.blastRows.length} hops` : "—"}</b>
            </div>
          </div>
          <table className="sk-tbl">
            <thead>
              <tr>
                <th>Hop</th>
                <th>Notes</th>
                <th className="n">Count</th>
              </tr>
            </thead>
            <tbody>
              {p.blastRows.map(([d, ids]) => (
                <tr key={d}>
                  <td className="n" style={{ textAlign: "left" }}>
                    {d}
                  </td>
                  <td>{names(ids)}</td>
                  <td className="n">{ids.length}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      );
    case "path": {
      const c = p.path?.chosen;
      const o = p.path?.other;
      if (!p.path) return <p>Pick two notes.</p>;
      if (!c?.found) return <h4 className="gr-h">No path connects these notes.</h4>;
      const steps = (c.path ?? []).length - 1;
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {title(c.path?.[0])} is {steps} {p.semantic ? "steps by meaning" : "link hops"} from {title(c.path?.[steps])}.
          </h4>
          <p>{p.semantic ? "Dijkstra on 1 − cosine along links." : "Breadth-first over links in either direction."}</p>
          <div className="gr-ird">
            <div>
              <span>Steps</span>
              <b>{steps}</b>
            </div>
            <div>
              <span>Cost</span>
              <b>{p.semantic ? (c.cost ?? 0).toFixed(2) : `${steps} hops`}</b>
            </div>
            <div>
              <span>Other metric</span>
              <b>{o?.found ? (p.semantic ? `${(o.path ?? []).length - 1} hops` : (o.cost ?? 0).toFixed(2)) : "none"}</b>
            </div>
          </div>
          <ol className="ly-path" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
            {(c.path ?? []).map((id, i) => (
              <li key={id}>
                <Link className="ly-ps done" to={`/notes/${id}`}>
                  <i />
                  <div>
                    <b>{title(id)}</b>
                    <span>{i === 0 ? "start" : i === steps ? "end" : `step ${i}`}</span>
                  </div>
                </Link>
              </li>
            ))}
          </ol>
        </>
      );
    }
    case "read":
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {p.reading.length} notes to read before {title(p.selected ?? "")}.
          </h4>
          <p>Everything it links to, directly or not, ordered so each note comes after what it builds on. Notes in a loop are read together.</p>
          <ol className="ly-path" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
            {p.reading.map((s, i) => (
              <li key={s.noteId}>
                <Link className="ly-ps" to={`/notes/${s.noteId}`}>
                  <i />
                  <div>
                    <b>
                      {i + 1} · {title(s.noteId)}
                    </b>
                    <span>
                      {s.reason} · depth {s.depth}
                    </span>
                  </div>
                </Link>
              </li>
            ))}
            <li>
              <span className="ly-ps cur">
                <i />
                <div>
                  <b>{title(p.selected ?? "")}</b>
                </div>
                <em>target</em>
              </span>
            </li>
          </ol>
          <Link className="btn xs" to="/graph/reading-plan" style={{ justifySelf: "start" }}>
            Fit it into a time budget →
          </Link>
        </>
      );
    case "scc": {
      const loops = groupsOf(a.sccOf).filter(([, ids]) => ids.length > 1);
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {loops.length} groups of notes cite each other in a loop.
          </h4>
          <p>Strongly connected: from any note in a group you can follow links to any other and back. Direction matters, unlike Components.</p>
          <div className="gr-ird">
            <div>
              <span>Components</span>
              <b>{(a.sccSizes ?? []).length}</b>
            </div>
            <div>
              <span>Size &gt; 1</span>
              <b>{loops.length}</b>
            </div>
            <div>
              <span>In a loop</span>
              <b>{loops.reduce((n, [, ids]) => n + ids.length, 0)}</b>
            </div>
          </div>
          <table className="sk-tbl">
            <thead>
              <tr>
                <th />
                <th>Notes that cite each other</th>
                <th className="n">Size</th>
              </tr>
            </thead>
            <tbody>
              {loops.slice(0, 8).map(([c, ids]) => (
                <tr key={c}>
                  <td>
                    <span className="dt" style={{ "--c": pal(c) } as React.CSSProperties} />
                  </td>
                  <td>{names(ids)}</td>
                  <td className="n">{ids.length}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      );
    }
    case "cycle": {
      const cycle = a.cycle ?? [];
      return cycle.length === 0 ? (
        <h4 className="gr-h">The graph has no cycles.</h4>
      ) : (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {cycle.slice(0, -1).map(title).join(" → ")} → back.
          </h4>
          <p>One example loop, drawn in link direction, found by three-colour DFS (a plain visited set would also flag diamonds).</p>
          <ol className="ly-path" style={{ "--c": "var(--warn)" } as React.CSSProperties}>
            {cycle.slice(0, -1).map((id, i) => (
              <li key={id}>
                <span className="ly-ps done">
                  <i />
                  <div>
                    <b>{title(id)}</b>
                    <span>{i === cycle.length - 2 ? `links back to ${title(cycle[0])}` : "links to ↓"}</span>
                  </div>
                </span>
              </li>
            ))}
          </ol>
        </>
      );
    }
    case "comps": {
      const comps = groupsOf(a.componentOf);
      const orphans = new Set((a.orphanComponents ?? []).map(Number));
      const orphanNotes = comps.filter(([c]) => orphans.has(c)).flatMap(([, ids]) => ids);
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {comps.length} components; {orphanNotes.length} notes no path from a root reaches.
          </h4>
          <p>Components ignore direction. An orphan component contains no root note.</p>
          <div className="gr-ird">
            <div>
              <span>Components</span>
              <b>{comps.length}</b>
            </div>
            <div>
              <span>Reachable</span>
              <b>
                {p.total - orphanNotes.length} / {p.total}
              </b>
            </div>
            <div>
              <span>Orphans</span>
              <b>{orphanNotes.length}</b>
            </div>
          </div>
          <table className="sk-tbl">
            <thead>
              <tr>
                <th />
                <th>Component</th>
                <th className="n">Size</th>
              </tr>
            </thead>
            <tbody>
              {comps.slice(0, 8).map(([c, ids]) => (
                <tr key={c}>
                  <td>
                    <span className="dt" style={{ "--c": orphans.has(c) ? pal(c + 4) : "var(--fnc)" } as React.CSSProperties} />
                  </td>
                  <td>{orphans.has(c) ? names(ids, 2) : `Everything joined to a root (${names(ids, 1)})`}</td>
                  <td className="n">{ids.length}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      );
    }
    case "topo": {
      const order = a.topologicalOrder ?? [];
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {order.length} notes in reading order; {(a.unplaced ?? []).length} left out by loops.
          </h4>
          <p>Every note comes after the notes it links to. Ties break on note ID, so the order is stable.</p>
          <ol className="ly-path" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
            {order.slice(0, 12).map((id, i) => (
              <li key={id}>
                <Link className="ly-ps" to={`/notes/${id}`}>
                  <i />
                  <div>
                    <b>
                      #{i + 1} · {title(id)}
                    </b>
                  </div>
                </Link>
              </li>
            ))}
          </ol>
          {(a.unplaced ?? []).length > 0 && (
            <div className="callout">
              <div className="ctitle">{(a.unplaced ?? []).length} can't be ordered</div>
              <p>They sit in a loop or link into one. Break one link per loop to order them (see Strongly connected).</p>
            </div>
          )}
        </>
      );
    }
    case "between":
    case "pr": {
      const metric = p.algo === "pr" ? (a.pagerank ?? {}) : (a.betweenness ?? {});
      const top = Object.entries(metric)
        .sort((x, y) => y[1] - x[1])
        .slice(0, 8);
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {title(top[0]?.[0])} {p.algo === "pr" ? "is the most-cited foundation." : "is the main bridge."}
          </h4>
          <p>{p.algo === "pr" ? "PageRank over link direction: a note scores high when well-cited notes cite it. Scores sum to 1." : "Betweenness: the share of shortest paths between other notes that pass through this one. High means removing it splits the graph."}</p>
          <table className="sk-tbl">
            <thead>
              <tr>
                <th>#</th>
                <th>Note</th>
                <th className="n">Cited by</th>
                <th className="n">{p.algo === "pr" ? "PageRank" : "Betweenness"}</th>
              </tr>
            </thead>
            <tbody>
              {top.map(([id, v], i) => (
                <tr key={id}>
                  <td>{i + 1}</td>
                  <td>{i < 2 ? <b>{title(id)}</b> : title(id)}</td>
                  <td className="n">{p.degree[id]?.in ?? 0}</td>
                  <td className="n">{v.toFixed(3)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      );
    }
    case "comm": {
      const groups = groupsOf(a.communityOf);
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {groups.length} communities, modularity {(a.modularity ?? 0).toFixed(3)}.
          </h4>
          <p>Groups with many links inside and few between. Colour here is the group, not a tag.</p>
          <div className="gr-ird">
            <div>
              <span>Communities</span>
              <b>{groups.length}</b>
            </div>
            <div>
              <span>Modularity</span>
              <b>{(a.modularity ?? 0).toFixed(3)}</b>
            </div>
            <div>
              <span>Largest</span>
              <b>{groups[0]?.[1].length ?? 0}</b>
            </div>
          </div>
          <table className="sk-tbl">
            <tbody>
              {groups.slice(0, 8).map(([c, ids]) => (
                <tr key={c}>
                  <td>
                    <span className="dt" style={{ "--c": pal(c) } as React.CSSProperties} />
                  </td>
                  <td>{names(ids, 2)}</td>
                  <td className="n">{ids.length}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <span className="cap">Diameter {a.diameter ?? 0} hops.</span>
        </>
      );
    }
    case "start": {
      const id = p.selected ?? "";
      const neighbours = p.edges.filter(([f, t]) => f === id || t === id).map(([f, t]) => (f === id ? { id: t, how: "links to" } : { id: f, how: "cited by" }));
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            {title(id)}
          </h4>
          <p>Where to start: the three pulsing notes have the highest PageRank. Click any note to focus its links; double-click opens it.</p>
          <div className="gr-ird">
            <div>
              <span>Links in</span>
              <b>{p.degree[id]?.in ?? 0}</b>
            </div>
            <div>
              <span>Links out</span>
              <b>{p.degree[id]?.out ?? 0}</b>
            </div>
            <div>
              <span>PageRank</span>
              <b>{(a.pagerank?.[id] ?? 0).toFixed(3)}</b>
            </div>
          </div>
          <div className="lab">Connected · {neighbours.length}</div>
          <ul className="gr-nb">
            {neighbours.slice(0, 10).map((n) => (
              <li key={`${n.how}${n.id}`}>
                <Link to={`/notes/${n.id}`}>
                  <i style={{ background: "var(--fnc)" }} />
                  <span>{title(n.id)}</span>
                  <em>{n.how}</em>
                </Link>
              </li>
            ))}
          </ul>
          <div className="gr-acts">
            <Link className="btn go xs" to={`/notes/${id}`}>
              Open
            </Link>
            <Link className="btn xs" to={`/discover/${id}`}>
              Similar by meaning
            </Link>
          </div>
        </>
      );
    }
    case "like":
      return (
        <>
          <h4 className="gr-h" style={{ fontSize: 18 }}>
            Like {title(p.selected ?? "")}
          </h4>
          <p>Three signals, kept apart: meaning, shared tags, link distance.</p>
          <table className="sk-tbl">
            <thead>
              <tr>
                <th>Note</th>
                <th className="n">Cosine</th>
                <th className="n">Tags</th>
                <th className="n">Hops</th>
              </tr>
            </thead>
            <tbody>
              {p.like.map((r) => (
                <tr key={r.noteId}>
                  <td>
                    <b>{title(r.noteId)}</b>
                  </td>
                  <td className="n">{(r.cosine ?? 0).toFixed(2)}</td>
                  <td className="n">
                    {r.sharedTags ?? 0} · {(r.tagJaccard ?? 0).toFixed(2)}
                  </td>
                  <td className="n">{r.hops !== undefined && r.hops >= 0 ? r.hops : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <Link className="btn xs" to={`/discover/${p.selected}`} style={{ justifySelf: "start" }}>
            All signals in Discover →
          </Link>
        </>
      );
  }
}
