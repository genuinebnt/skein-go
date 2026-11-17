/**
 * OAuth authorization code flow with PKCE (identity.md, SignInWithOAuth):
 * the browser makes a verifier, sends its S256 challenge to the provider,
 * and on return sends the code and verifier to the gateway. The verifier and
 * state live in sessionStorage for the one round trip.
 */

export type Provider = "github" | "google";

const AUTHORIZE: Record<Provider, { url: string; scope: string }> = {
  github: { url: "https://github.com/login/oauth/authorize", scope: "read:user user:email" },
  google: { url: "https://accounts.google.com/o/oauth2/v2/auth", scope: "openid email profile" },
};

const KEY = "skein.oauth";

const base64url = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

export async function pkcePair(random: (n: number) => Uint8Array = (n) => crypto.getRandomValues(new Uint8Array(n))) {
  const verifier = base64url(random(32));
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)));
  return { verifier, challenge: base64url(digest) };
}

export function redirectUri(): string {
  return `${window.location.origin}/auth/callback`;
}

export async function startOAuth(provider: Provider, clientId: string): Promise<void> {
  const { verifier, challenge } = await pkcePair();
  const state = base64url(crypto.getRandomValues(new Uint8Array(16)));
  sessionStorage.setItem(KEY, JSON.stringify({ provider, verifier, state }));
  const params = new URLSearchParams({
    client_id: clientId,
    redirect_uri: redirectUri(),
    response_type: "code",
    scope: AUTHORIZE[provider].scope,
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  window.location.assign(`${AUTHORIZE[provider].url}?${params}`);
}

/** Reads the pending flow back on the callback page; null when the state does not match (a forged redirect). */
export function finishOAuth(search: URLSearchParams): { provider: Provider; verifier: string; code: string } | null {
  const raw = sessionStorage.getItem(KEY);
  sessionStorage.removeItem(KEY);
  if (!raw) return null;
  const pending = JSON.parse(raw) as { provider: Provider; verifier: string; state: string };
  const code = search.get("code");
  if (!code || search.get("state") !== pending.state) return null;
  return { provider: pending.provider, verifier: pending.verifier, code };
}
