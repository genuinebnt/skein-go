/**
 * Discover (Discover board): notes related to the one picked, with the three
 * signals of MLService.Discover side by side and never added up: meaning
 * (cosine), tags (shared count and Jaccard) and link distance (hops, or not
 * connected). Rows where meaning is close but links are far are suggested
 * links; adding one appends a [[link]] to the source note.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import type { RelatedNote } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Pending, SearchIcon } from "../components/ui";
import { colorFor } from "../lib/format";
import { useAsync } from "../lib/hooks";

type Filter = "all" | "disagree" | "linked";
type Sort = "meaning" | "tags" | "hops";

export function Discover() {
  const { noteId: routeNote } = useParams();
  const { api } = useServices();
  const ws = useWorkspace();
  const navigate = useNavigate();
  const [filter, setFilter] = useState<Filter>("all");
  const [sort, setSort] = useState<Sort>("meaning");
  const [pick, setPick] = useState("");
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const noteId = routeNote ?? ws.recent.find((id) => ws.notesById.has(id)) ?? ws.notes[0]?.id;
  const source = noteId ? ws.notesById.get(noteId) : undefined;

  const related = useAsync(async () => {
    if (!noteId) return { rows: [] as RelatedNote[], ms: 0 };
    const t0 = performance.now();
    const res = await api.call("MLService_Discover", { noteId, k: 12 });
    return { rows: res.related ?? [], ms: Math.round(performance.now() - t0) };
  }, [noteId]);
  const clusters = useAsync(() => (ws.workspace ? api.call("MLService_ListClusters", { workspaceId: ws.workspace.id }) : Promise.resolve({ clusters: [] })), [ws.workspace?.id]);
  const clusterOf = useMemo(() => {
    const map = new Map<string, { label: string; topTerms: string[]; size: number }>();
    for (const c of clusters.data?.clusters ?? []) for (const id of c.noteIds ?? []) map.set(id, { label: c.label ?? "", topTerms: c.topTerms ?? [], size: c.noteIds?.length ?? 0 });
    return map;
  }, [clusters.data]);

  const rows = (related.data?.rows ?? []).filter((r) => !hidden.has(r.noteId!));
  const disagree = rows.filter((r) => r.suggestLink);
  const linked = rows.filter((r) => r.hops === 1);
  const shown = (filter === "disagree" ? disagree : filter === "linked" ? linked : rows).slice().sort((a, b) => {
    if (sort === "tags") return (b.tagJaccard ?? 0) - (a.tagJaccard ?? 0);
    if (sort === "hops") return (a.hops ?? -1) < 0 ? 1 : (b.hops ?? -1) < 0 ? -1 : (a.hops ?? 0) - (b.hops ?? 0);
    return (b.cosine ?? 0) - (a.cosine ?? 0);
  });
  const rail = ws.notes.filter((n) => !pick || n.title?.toLowerCase().includes(pick.toLowerCase())).slice(0, 40);
  const cluster = noteId ? clusterOf.get(noteId) : undefined;

  return (
    <div className="app">
      <Header area="discover" here="Discover" />
      <main id="main" className="ly-page">
        <div className="ly-head">
          <div className="ly-ttl">
            <div className="ly-crumb" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
              <span>Discover</span>
              <i>/</i>
              <span>Related</span>
            </div>
            <h1 className="ly-h1">
              Near by meaning, tags and links, <em className="rgb">kept apart.</em>
            </h1>
          </div>
          <div className="ly-tiles">
            <div className="ly-tl">
              <em>Compared</em>
              <strong>{rows.length}</strong>
              <small>nearest of {ws.notes.length}</small>
            </div>
            <div className="ly-tl hot" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
              <em>Suggested links</em>
              <strong>{disagree.length}</strong>
              <small>meaning near, links far</small>
            </div>
            <div className="ly-tl">
              <em>Query</em>
              <strong>
                {related.data?.ms ?? "…"}
                <span> ms</span>
              </strong>
            </div>
          </div>
        </div>
        <div className="ly-lay">
          <aside className="ly-lp" aria-label="Discover from">
            <div className="lab">Discover from</div>
            <label className="cm-find">
              <SearchIcon />
              <input type="search" placeholder="Pick a note…" aria-label="Pick a note" value={pick} onChange={(e) => setPick(e.target.value)} />
            </label>
            <div style={{ display: "grid", gap: 4, maxHeight: "60vh", overflow: "auto", overscrollBehavior: "contain", minHeight: 0 }}>
              {rail.map((n) => (
                <button key={n.id} type="button" className={`rail-row${n.id === noteId ? " on" : ""}`} style={{ "--c": colorFor(n.tags?.[0] ?? n.id) } as React.CSSProperties} aria-current={n.id === noteId ? "true" : undefined} onClick={() => navigate(`/discover/${n.id}`)}>
                  <span className="rail-top">
                    <span className="rail-t">{n.title}</span>
                  </span>
                  <span className="rail-tags">
                    <span className="rail-tp">{clusterOf.get(n.id!)?.label ?? n.tags?.[0] ?? "untagged"}</span>
                    <span className="rail-k">{n.tags?.length ? `${n.tags.length} tags` : ""}</span>
                  </span>
                </button>
              ))}
            </div>
          </aside>
          <div className="ly-cp">
            <div className="kbar">
              <div className="seg" role="group" aria-label="Show">
                {(
                  [
                    ["all", `All ${rows.length}`],
                    ["disagree", `Signals disagree ${disagree.length}`],
                    ["linked", `Already linked ${linked.length}`],
                  ] as const
                ).map(([id, label]) => (
                  <button key={id} type="button" className={filter === id ? "on" : ""} aria-pressed={filter === id} style={{ "--c": "var(--grn)" } as React.CSSProperties} onClick={() => setFilter(id)}>
                    {label}
                  </button>
                ))}
              </div>
              <span style={{ flex: 1 }} />
              <span className="lab">Sort by one signal</span>
              <div className="seg mono sm" role="group" aria-label="Sort by">
                {(["meaning", "tags", "hops"] as const).map((s) => (
                  <button key={s} type="button" className={sort === s ? "on" : ""} aria-pressed={sort === s} style={{ "--c": "var(--grn)" } as React.CSSProperties} onClick={() => setSort(s)}>
                    {s}
                  </button>
                ))}
              </div>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "minmax(0, 1fr) 120px 128px 112px", gap: 14, padding: "0 18px" }} className="lab">
              <span>Related to {source?.title ?? "…"}</span>
              <span>Meaning</span>
              <span>Shared tags</span>
              <span>Link distance</span>
            </div>
            {related.loading && <span className="sk" style={{ height: 60 }} />}
            {related.error && <span className="help err">{related.error}</span>}
            <div style={{ display: "grid", gap: 8 }} role="list">
              {shown.map((r) => {
                const note = ws.notesById.get(r.noteId!);
                const sug = !!r.suggestLink;
                return (
                  <article
                    key={r.noteId}
                    role="listitem"
                    style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) 120px 128px 112px", gap: 14, padding: "14px 18px", borderRadius: 14, border: `1px solid ${sug ? "color-mix(in oklab,var(--grn) 45%,var(--line2))" : "var(--line2)"}`, background: sug ? "color-mix(in oklab,var(--grn) 6%,var(--panel))" : "var(--panel)" }}
                  >
                    <div style={{ minWidth: 0, display: "grid", gap: 6 }}>
                      <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
                        <span className="tp" style={{ "--c": colorFor(clusterOf.get(r.noteId!)?.label ?? "") } as React.CSSProperties}>
                          {clusterOf.get(r.noteId!)?.label ?? note?.tags?.[0] ?? "note"}
                        </span>
                        <Link to={`/notes/${r.noteId}`} style={{ fontSize: 14.5, fontWeight: 700 }}>
                          {note?.title ?? r.noteId}
                        </Link>
                        {sug && (
                          <span className="pill c" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
                            suggested link
                          </span>
                        )}
                      </div>
                      {note?.tags && note.tags.length > 0 && <span className="cap">{note.tags.map((t) => `#${t}`).join(" ")}</span>}
                      {sug && (
                        <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                          <button className="btn xs go" type="button" onClick={() => navigate(`/notes/${noteId}?addLink=${encodeURIComponent(note?.title ?? "")}`)}>
                            Add [[link]] to {source?.title}
                          </button>
                          <button className="btn xs ghost" type="button" onClick={() => setHidden((h) => new Set(h).add(r.noteId!))} title="Hides it here; recording feedback needs a backend route">
                            Not related
                          </button>
                          <span className="cap">
                            close in meaning ({r.cosine?.toFixed(2)}), {r.hops !== undefined && r.hops >= 0 ? `${r.hops} hops by links` : "no link path"}, {r.sharedTags ?? 0} shared tag{r.sharedTags === 1 ? "" : "s"}
                          </span>
                        </div>
                      )}
                    </div>
                    <div style={{ display: "grid", gap: 5, alignContent: "start" }}>
                      <b className="m" style={{ fontSize: 18 }}>
                        {(r.cosine ?? 0).toFixed(2)}
                      </b>
                      <span className="sk-meter" style={{ "--w": `${Math.round(Math.max(0, r.cosine ?? 0) * 100)}%`, "--c": "var(--grn)" } as React.CSSProperties}>
                        <u />
                      </span>
                      <span className="cap">cosine</span>
                    </div>
                    <div style={{ display: "grid", gap: 5, alignContent: "start" }}>
                      <b className="m" style={{ fontSize: 18 }}>
                        {r.sharedTags ?? 0}
                        <span className="cap" style={{ fontSize: 12 }}>
                          {" "}
                          · J {(r.tagJaccard ?? 0).toFixed(2)}
                        </span>
                      </b>
                      <span className="cap">shared · Jaccard</span>
                    </div>
                    <div style={{ display: "grid", gap: 5, alignContent: "start" }}>
                      {r.hops !== undefined && r.hops >= 0 ? (
                        <>
                          <b className="m" style={{ fontSize: 18 }}>
                            {r.hops} hop{r.hops === 1 ? "" : "s"}
                          </b>
                          <span className="cap">{r.hops === 1 ? "already linked" : `via ${r.hops - 1} other note${r.hops > 2 ? "s" : ""}`}</span>
                        </>
                      ) : (
                        <>
                          <span className="pill c" style={{ "--c": "var(--warn)", justifySelf: "start" } as React.CSSProperties}>
                            not connected
                          </span>
                          <span className="cap">no path at all</span>
                        </>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          </div>
          <aside className="ly-rp">
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>How these were found</span>
              </h5>
              <div className="ly-kv" style={{ fontSize: 12.5 }}>
                <div>
                  <span>Meaning</span>
                  <b>embedding cosine</b>
                </div>
                <div>
                  <span>Tags</span>
                  <b>count · Jaccard</b>
                </div>
                <div>
                  <span>Hops</span>
                  <b>BFS, either direction</b>
                </div>
                <div>
                  <span>Round trip</span>
                  <b>{related.data?.ms ?? "…"} ms</b>
                </div>
              </div>
              <p>The three are never added up. A note can be close in meaning and still have no path to this one; that gap is the finding.</p>
            </section>
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>Suggested links · this note</span>
                <Link to="/graph" style={{ color: "var(--grn)" }}>
                  graph →
                </Link>
              </h5>
              <ul className="gr-nb">
                {disagree.length === 0 && <span className="cap">None: every close note is already near by links.</span>}
                {disagree.map((r) => (
                  <li key={r.noteId}>
                    <Link to={`/notes/${r.noteId}`}>
                      <i style={{ background: "var(--grn)" }} />
                      <span>{ws.notesById.get(r.noteId!)?.title}</span>
                      <em>
                        {r.cosine?.toFixed(2)} · {r.hops !== undefined && r.hops >= 0 ? r.hops : "none"}
                      </em>
                    </Link>
                  </li>
                ))}
              </ul>
              <span className="cap">
                Workspace-wide suggestions and the threshold setting need an admin route <Pending />
              </span>
            </section>
            {cluster && (
              <section className="ly-pn">
                <h5 className="ly-pnh">
                  <span>Its topic cluster</span>
                  <Link to="/discover/clusters" style={{ color: "var(--grn)" }}>
                    clusters →
                  </Link>
                </h5>
                <div className="frow on" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
                  <span>
                    <span className="dot" />
                    {cluster.label}
                  </span>
                  <span>{cluster.size} notes</span>
                </div>
                <div className="chips">
                  {cluster.topTerms.map((t) => (
                    <span key={t} className="chip sm">
                      {t}
                    </span>
                  ))}
                </div>
              </section>
            )}
          </aside>
        </div>
      </main>
    </div>
  );
}
