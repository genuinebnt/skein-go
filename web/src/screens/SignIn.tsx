/**
 * Sign in (SignIn board): email and password, Google and GitHub OAuth with
 * PKCE, and the three states the board previews (fresh, wrong password,
 * signing in).
 */

import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { ApiError, describeError } from "../api/errors";
import { useServices } from "../app/services";
import { Spinner } from "../components/ui";
import { startOAuth, type Provider } from "./oauth";

const MOCK_USERS = [
  ["mira@kestrel-lab.example", "owner"],
  ["tobias@kestrel-lab.example", "admin"],
  ["rasmus@kestrel-lab.example", "viewer"],
  ["ifeoma@inkwell-tx.example", "guest"],
] as const;

export function SignIn() {
  const services = useServices();
  const { api, session } = services;
  const navigate = useNavigate();
  const [email, setEmail] = useState(services.mode === "mock" ? MOCK_USERS[0][0] : "");
  const [password, setPassword] = useState("");
  const [keep, setKeep] = useState(true);
  const [state, setState] = useState<{ kind: "fresh" } | { kind: "loading" } | { kind: "error"; message: string; field: "password" | "email" | "form" }>({ kind: "fresh" });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!email.includes("@")) return setState({ kind: "error", message: "That doesn't look like an email.", field: "email" });
    if (!password) return setState({ kind: "error", message: "Enter your password.", field: "password" });
    setState({ kind: "loading" });
    try {
      const res = await api.call("IdentityService_SignIn", {}, { email, password }, { anonymous: true });
      session.set(res.session!, res.user!, keep);
      navigate("/notes", { replace: true });
    } catch (error) {
      if (error instanceof ApiError && error.code === "UNAUTHENTICATED") setState({ kind: "error", message: "That email and password don't match.", field: "password" });
      else setState({ kind: "error", message: describeError(error), field: "form" });
    }
  }

  async function oauth(provider: Provider) {
    const clientId = provider === "github" ? services.oauth.github : services.oauth.google;
    if (services.mode === "mock") {
      // The mock gateway accepts any code; this skips the provider round trip.
      const res = await api.call("IdentityService_SignInWithOAuth", {}, { provider: provider === "github" ? "O_AUTH_PROVIDER_GITHUB" : "O_AUTH_PROVIDER_GOOGLE", code: "mock-code", codeVerifier: "mock-verifier-mock-verifier-mock-verifier-00", redirectUri: `${location.origin}/auth/callback` }, { anonymous: true });
      session.set(res.session!, res.user!, keep);
      navigate("/notes", { replace: true });
      return;
    }
    if (clientId) await startOAuth(provider, clientId);
  }

  const err = state.kind === "error" ? state : null;
  const oauthReady = (p: Provider) => services.mode === "mock" || !!(p === "github" ? services.oauth.github : services.oauth.google);

  return (
    <div className="lgn" style={{ minHeight: "100dvh" }}>
      <section className="lg-art" aria-hidden="true">
        <span className="logo">
          <span className="mk">
            <span style={{ background: "var(--fnc)" }} />
            <span style={{ background: "var(--vio)" }} />
            <span style={{ background: "var(--grn)" }} />
            <span style={{ boxShadow: "inset 0 0 0 1px var(--line)" }} />
          </span>
          <b>skein</b>
        </span>
        <div style={{ display: "grid", gap: 22, maxWidth: 580 }}>
          <div className="eyebrow" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
            <span className="dot" />
            {services.mode === "mock" ? "Mock mode · seed data" : `Self-hosted · ${services.host}`}
          </div>
          <h2 style={{ fontSize: 50, lineHeight: 1, fontWeight: 800, letterSpacing: "-.04em" }}>
            Your notes, where you left them. <em className="rgb">Even offline.</em>
          </h2>
          <div className="lg-term">
            <div className="bar">
              <i />
              <i />
              <i />
              <span>skein · status</span>
            </div>
            <pre>
              <span className="d">$</span> skein status{"\n"}
              <span className="ok">ready</span>   notes sync live and offline{"\n"}
              <span className="d">$</span> skein queue{"\n"}
              <span className="p">IndexedDB</span> keeps changes made on this device{"\n"}
              <span className="d">$</span> skein agent{"\n"}
              <span className="v">ready</span>    acts as you · confirms before deletes{"\n"}
              <span className="d">$</span> <span className="ed-caret" />
            </pre>
          </div>
        </div>
        <div />
      </section>
      <section className="lg-form">
        <form className="lg-card" onSubmit={submit} noValidate>
          <div style={{ display: "grid", gap: 10 }}>
            <h1>
              Sign in to <em className="rgb">Skein.</em>
            </h1>
            <p style={{ fontSize: 15, color: "var(--mut)" }}>Use the account your admin invited.</p>
          </div>
          <div style={{ display: "grid", gap: 8 }}>
            {(["google", "github"] as const).map((p) => (
              <button key={p} className="sk-oauth" type="button" onClick={() => void oauth(p)} disabled={!oauthReady(p)} title={oauthReady(p) ? undefined : `Set VITE_OAUTH_${p.toUpperCase()}_CLIENT_ID to enable`}>
                <span className="m" style={{ width: 18, height: 18, borderRadius: 4, border: "1px solid var(--line)", display: "grid", placeItems: "center", fontSize: 11, fontWeight: 700 }} aria-hidden="true">
                  {p === "google" ? "G" : "gh"}
                </span>
                Continue with {p === "google" ? "Google" : "GitHub"}
                {!oauthReady(p) && <span className="cap">not configured</span>}
              </button>
            ))}
          </div>
          <div className="sk-or">or with email</div>
          <div className="fld">
            <label htmlFor="em">Email</label>
            <input id="em" className={`inp${err?.field === "email" ? " err" : ""}`} type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" aria-invalid={err?.field === "email"} aria-describedby={err?.field === "email" ? "form-err" : undefined} />
          </div>
          <div className="fld">
            <label htmlFor="pw">Password</label>
            <input id="pw" className={`inp${err?.field === "password" ? " err" : ""}`} type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" aria-invalid={err?.field === "password"} aria-describedby={err ? "form-err" : undefined} />
          </div>
          {err && (
            <span id="form-err" className="help err" role="alert">
              {err.message}
            </span>
          )}
          <label className={`cbx${keep ? " on" : ""}`}>
            <input type="checkbox" className="sr" checked={keep} onChange={(e) => setKeep(e.target.checked)} />
            <i aria-hidden="true">{keep ? "✓" : ""}</i>
            <span>
              <b>Keep me signed in on this device</b>
              <br />
              <span className="cap">notes stay readable and editable offline</span>
            </span>
          </label>
          <button className="btn go" type="submit" style={{ height: 46, fontSize: 15 }} disabled={state.kind === "loading"}>
            {state.kind === "loading" && <Spinner />}
            {state.kind === "loading" ? "Signing in…" : "Sign in"}
          </button>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", paddingTop: 12, borderTop: "1px solid var(--line2)" }}>
            <span className="cap">No account? Ask an admin for an invite.</span>
            <span className="cap">data stays on this server</span>
          </div>
          {services.mode === "mock" && (
            <div style={{ display: "grid", gap: 6 }}>
              <span className="cap">Mock mode: any seed user, any password. Sign in as</span>
              <div className="kt-row" style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
                {MOCK_USERS.map(([address, role]) => (
                  <button key={address} type="button" className={`chip sm${email === address ? " on" : ""}`} onClick={() => setEmail(address)}>
                    {role}
                  </button>
                ))}
              </div>
            </div>
          )}
        </form>
      </section>
    </div>
  );
}
