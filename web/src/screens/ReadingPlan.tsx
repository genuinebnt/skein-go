/**
 * Reading plan (ReadingPlan board): pick a topic and a time budget, get the
 * most valuable set of notes that fits, every note's prerequisites first
 * (GraphService.ReadingPlan, a knapsack with dependencies). The API returns
 * the chosen notes only, so the board's "left out" list needs the server to
 * also return the candidates it considered.
 */

import { useState } from "react";
import { Link } from "react-router";
import { useServices } from "../app/services";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import { Pending } from "../components/ui";
import { useAsync } from "../lib/hooks";

const BUDGETS = [20, 30, 45, 60];

export function ReadingPlan() {
  const { api } = useServices();
  const ws = useWorkspace();
  const [topic, setTopic] = useState("raft consensus and replication");
  const [draft, setDraft] = useState(topic);
  const [budget, setBudget] = useState(45);
  const plan = useAsync(() => (ws.workspace && topic ? api.call("GraphService_ReadingPlan", { workspaceId: ws.workspace.id, topic, minutes: budget }) : Promise.resolve(null)), [ws.workspace?.id, topic, budget]);
  const notes = plan.data?.notes ?? [];
  const cumulative = notes.reduce<number[]>((acc, n) => [...acc, (acc[acc.length - 1] ?? 0) + (n.minutes ?? 0)], []);

  return (
    <div className="app">
      <Header area="graph" here="Reading plan" />
      <main id="main" className="ly-page">
        <div className="ly-head">
          <div className="ly-ttl">
            <div className="ly-crumb" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
              <span>Graph</span>
              <i>/</i>
              <span>Reading plan</span>
            </div>
            <h1 className="ly-h1">
              I have <em className="rgb">{budget} minutes.</em>
            </h1>
          </div>
          <div className="ly-tiles">
            <div className="ly-tl">
              <em>Minutes used</em>
              <strong>
                {plan.data?.totalMinutes ?? 0}
                <span> / {budget}</span>
              </strong>
            </div>
            <div className="ly-tl hot" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
              <em>Relevance</em>
              <strong>{(plan.data?.totalValue ?? 0).toFixed(2)}</strong>
              <small>sum over the plan</small>
            </div>
            <div className="ly-tl">
              <em>Notes</em>
              <strong>{notes.length}</strong>
            </div>
          </div>
        </div>
        <div className="ly-lay tworight">
          <div className="ly-cp">
            <form
              className="kbar"
              style={{ padding: "10px 12px" }}
              onSubmit={(e) => {
                e.preventDefault();
                setTopic(draft.trim());
              }}
            >
              <label htmlFor="topic" className="lab" style={{ flex: "none" }}>
                Topic
              </label>
              <input id="topic" className="inp" value={draft} onChange={(e) => setDraft(e.target.value)} onBlur={() => setTopic(draft.trim())} style={{ flex: 1, minWidth: 260 }} />
              <span className="lab">Budget</span>
              <div className="seg mono" role="group" aria-label="Minutes">
                {BUDGETS.map((m) => (
                  <button key={m} type="button" className={m === budget ? "on" : ""} aria-pressed={m === budget} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setBudget(m)}>
                    {m} min
                  </button>
                ))}
              </div>
            </form>
            <section className="ly-pn" style={{ padding: "16px 18px" }} aria-busy={plan.loading}>
              <h5 className="ly-pnh">
                <span>Read in this order</span>
              </h5>
              {plan.error && <span className="help err">{plan.error}</span>}
              {!plan.loading && notes.length === 0 && <span className="cap">Nothing on this topic fits {budget} minutes. Try a longer budget or another topic.</span>}
              <ol className="ly-path" style={{ "--c": "var(--fnc)" } as React.CSSProperties}>
                {notes.map((n, i) => {
                  return (
                    <li key={n.noteId}>
                      <Link className="ly-ps" to={`/notes/${n.noteId}`} style={{ display: "grid", gridTemplateColumns: "20px minmax(0, 1fr) 64px 70px", gap: 14 }}>
                        <i />
                        <div>
                          <b>
                            {i + 1} · {ws.notesById.get(n.noteId!)?.title ?? n.noteId}
                          </b>
                          <span>
                            relevance {(n.value ?? 0).toFixed(2)} · {n.reason}
                          </span>
                        </div>
                        <span className="m" style={{ textAlign: "right", color: "var(--fg)", fontSize: 13 }}>
                          {n.minutes} min
                        </span>
                        <span className="m" style={{ textAlign: "right", fontSize: 12 }}>
                          ⟶ {cumulative[i]}
                        </span>
                      </Link>
                    </li>
                  );
                })}
              </ol>
            </section>
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>Left out</span>
                <Pending />
              </h5>
              <span className="cap">The API returns only the chosen notes. Listing the candidates it left out, and why, needs ReadingPlanResponse to include them.</span>
              <Link className="btn xs" to="/graph" style={{ justifySelf: "start" }}>
                See the notes on the graph
              </Link>
            </section>
          </div>
          <aside className="ly-rp">
            <section className="ly-pn">
              <h5 className="ly-pnh">
                <span>How it was chosen</span>
              </h5>
              <p>Each note has a reading time and a relevance to the topic. A note can only be picked with everything it builds on. The plan is the exact best total relevance that fits the budget.</p>
              <div className="ly-kv" style={{ fontSize: 12.5 }}>
                <div>
                  <span>Method</span>
                  <b>0/1 knapsack, prerequisite-closed</b>
                </div>
                <div>
                  <span>Solved by</span>
                  <b>dynamic programming</b>
                </div>
                <div>
                  <span>Budget</span>
                  <b>{budget} min</b>
                </div>
              </div>
            </section>
            {notes[0] && (
              <Link className="btn sm go" to={`/notes/${notes[0].noteId}`}>
                Start with {ws.notesById.get(notes[0].noteId!)?.title}
              </Link>
            )}
          </aside>
        </div>
      </main>
    </div>
  );
}
