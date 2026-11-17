/**
 * Smaller routes: the notes index (opens the most recent note), drafts kept
 * from rejected edits, the Agent area, the workspace picker for live mode
 * (until the API can list workspaces), and the OAuth callback.
 */

import { useEffect, useState, type FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { AgentPanel } from "../agent/AgentPanel";
import type { AgentRun } from "../agent/run";
import { describeError } from "../api/errors";
import { useServices } from "../app/services";
import { useWorkspace } from "../app/workspace";
import { Header } from "../components/Header";
import type { Draft } from "../crdt/store";
import { relativeTime } from "../lib/format";
import { finishOAuth, redirectUri } from "./oauth";

export function NotesHome() {
  const ws = useWorkspace();
  const first = ws.recent.find((id) => ws.notesById.has(id)) ?? ws.notes[0]?.id;
  if (first) return <Navigate to={`/notes/${first}`} replace />;
  return (
    <div className="app">
      <Header area="notes" here="Notes" />
      <main id="main" className="sk-empty" style={{ minHeight: 400 }}>
        {ws.notesLoading ? <span className="spin" /> : <b>No notes yet. Press N or “New note” to start one.</b>}
      </main>
    </div>
  );
}

export function Drafts() {
  const { store } = useServices();
  const ws = useWorkspace();
  const [drafts, setDrafts] = useState<Draft[] | null>(null);
  useEffect(() => void store.drafts().then(setDrafts), [store]);
  return (
    <div className="app">
      <Header area="notes" here="Drafts" />
      <main id="main" className="ly-page" style={{ maxWidth: 900, margin: "0 auto" }}>
        <div className="ly-head">
          <div className="ly-ttl">
            <h1 className="ly-h1">Drafts on this device</h1>
            <p className="ly-sub">Edits the server refused (for example made offline after your access changed) are kept here until you re-insert or discard them in their note.</p>
          </div>
        </div>
        {drafts?.length === 0 && <span className="cap">No drafts.</span>}
        <div style={{ display: "grid", gap: 10 }}>
          {drafts?.map((d) => (
            <Link key={d.id} to={`/notes/${d.noteId}`} className="card" style={{ padding: 14, gap: 6 }}>
              <b>{ws.notesById.get(d.noteId)?.title ?? "A note"}</b>
              <span className="cap">
                {d.operations} changes · {d.reason} · {relativeTime(new Date(d.at).toISOString())}
              </span>
              <span style={{ color: "var(--mut)", whiteSpace: "pre-wrap" }}>{d.text.slice(0, 200)}</span>
            </Link>
          ))}
        </div>
      </main>
    </div>
  );
}

export function AgentArea() {
  const [runs, setRuns] = useState<AgentRun[]>([]);
  return (
    <div className="app">
      <Header area="agent" here="Agent" />
      <div className="fill">
        <div className="fill-row" style={{ justifyContent: "center" }}>
          <section className="pane c" style={{ flex: "1 1 auto", maxWidth: 420, padding: 24 }} id="main">
            <h1 className="ly-h1" style={{ fontSize: 26 }}>
              Ask across all your notes.
            </h1>
            <p className="ly-sub" style={{ marginTop: 8 }}>
              The agent acts as you: it reads only what you can open, suggests edits for you to accept, and asks before it shares or deletes anything.
            </p>
          </section>
          <AgentPanel runs={runs} setRuns={setRuns} onSuggestion={() => undefined} />
        </div>
      </div>
    </div>
  );
}

export function WorkspacePicker() {
  const ws = useWorkspace();
  const navigate = useNavigate();
  const [id, setId] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!id.trim()) return;
    ws.choose(id.trim());
    navigate("/notes");
  };
  return (
    <main id="main" className="lg-form" style={{ minHeight: "100dvh" }}>
      <form className="lg-card" onSubmit={submit}>
        <h1>
          Choose a <em className="rgb">workspace.</em>
        </h1>
        <p style={{ color: "var(--mut)" }}>The API cannot list your workspaces yet, so enter the workspace ID your admin gave you (or set VITE_WORKSPACE_ID).</p>
        <div className="fld">
          <label htmlFor="ws-id">Workspace ID</label>
          <input id="ws-id" className="inp mono" value={id} onChange={(e) => setId(e.target.value)} placeholder="01936f3e-1a2b-7c3d-…" />
        </div>
        <button className="btn go" type="submit">
          Open
        </button>
      </form>
    </main>
  );
}

export function OAuthCallback() {
  const { api, session } = useServices();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const pending = finishOAuth(params);
    if (!pending) return setError("This sign-in link is stale or was not started here. Try again.");
    api
      .call("IdentityService_SignInWithOAuth", {}, { provider: pending.provider === "github" ? "O_AUTH_PROVIDER_GITHUB" : "O_AUTH_PROVIDER_GOOGLE", code: pending.code, codeVerifier: pending.verifier, redirectUri: redirectUri() }, { anonymous: true })
      .then((res) => {
        session.set(res.session!, res.user!, true);
        navigate("/notes", { replace: true });
      })
      .catch((e: unknown) => setError(describeError(e)));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <main id="main" className="sk-empty" style={{ minHeight: "100dvh" }}>
      {error ? (
        <>
          <b>{error}</b>
          <Link className="btn sm" to="/signin">
            Back to sign in
          </Link>
        </>
      ) : (
        <span className="spin" />
      )}
    </main>
  );
}
