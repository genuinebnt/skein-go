/**
 * Everything the screens need from the outside world, built once at startup
 * and passed down through React context. Live and mock mode differ only
 * here: in mock mode `fetch`, `createSocket` and `network` come from the
 * in-process fake gateway (src/mocks); everything else is the same code.
 */

import { createContext, useContext } from "react";
import { ApiClient } from "../api/client";
import { OpStore } from "../crdt/store";
import { loadCrdt, type CrdtModule } from "../crdt/wasm";
import type { WebSocketFactory } from "../ws/socket";
import type { MockServer } from "../mocks/server";
import { SessionStore } from "./session";

export type Network = Pick<EventTarget, "addEventListener" | "removeEventListener"> & { onLine: boolean };

export type Services = {
  mode: "live" | "mock";
  api: ApiClient;
  session: SessionStore;
  /** ws:// or wss:// origin of the gateway. */
  wsBase: string;
  createSocket: WebSocketFactory;
  network: Network;
  store: OpStore;
  crdt: () => Promise<CrdtModule>;
  /** The host shown in the header's tier-two chip. */
  host: string;
  /** Mock mode only: simulation controls (offline, token expiry, revoked access). */
  mock?: MockServer;
  /** Configured workspace for live mode until the API can list them (VITE_WORKSPACE_ID). */
  defaultWorkspaceId?: string;
  oauth: { github?: string; google?: string };
};

const browserNetwork: Network = {
  addEventListener: (...args: Parameters<Window["addEventListener"]>) => window.addEventListener(...args),
  removeEventListener: (...args: Parameters<Window["removeEventListener"]>) => window.removeEventListener(...args),
  get onLine() {
    return navigator.onLine;
  },
};

function build(mode: Services["mode"], parts: { fetch?: typeof fetch; createSocket: WebSocketFactory; network: Network; baseUrl: string; wsBase: string; host: string; mock?: MockServer; crdt?: CrdtModule }): Services {
  const session = new SessionStore();
  const api: ApiClient = new ApiClient({
    baseUrl: parts.baseUrl,
    fetch: parts.fetch,
    auth: { accessToken: () => session.accessToken, refresh: (): Promise<boolean> => session.refresh(api) },
  });
  const env = import.meta.env;
  const loaded = parts.crdt;
  return {
    mode,
    api,
    session,
    wsBase: parts.wsBase,
    createSocket: parts.createSocket,
    network: parts.network,
    store: new OpStore(),
    crdt: loaded ? () => Promise.resolve(loaded) : () => loadCrdt(),
    host: parts.host,
    mock: parts.mock,
    defaultWorkspaceId: env.VITE_WORKSPACE_ID || undefined,
    oauth: { github: env.VITE_OAUTH_GITHUB_CLIENT_ID || undefined, google: env.VITE_OAUTH_GOOGLE_CLIENT_ID || undefined },
  };
}

/** Live mode: the gateway at VITE_API_BASE, or this origin (the dev server proxies /v1). */
export function liveServices(): Services {
  const base = (import.meta.env.VITE_API_BASE as string | undefined) || window.location.origin;
  return build("live", {
    createSocket: (url, protocols) => new WebSocket(url, protocols),
    network: browserNetwork,
    baseUrl: base,
    wsBase: base.replace(/^http/, "ws"),
    host: new URL(base).host,
  });
}

/** Mock mode: the fake gateway over testdata/seed/small, loaded lazily so live builds never ship it. */
export async function mockServices(): Promise<Services> {
  const [{ MockServer }, { httpSeedSource }] = await Promise.all([import("../mocks/server"), import("../mocks/seed")]);
  const crdt = await loadCrdt();
  const server = await MockServer.create({ source: httpSeedSource(), crdt, latencyMs: 40, ghosts: true });
  return mockServicesFor(server, crdt);
}

/** Services wired to an existing fake gateway; tests build theirs from the seed on disk. */
export function mockServicesFor(server: MockServer, crdt: CrdtModule): Services {
  return build("mock", {
    crdt,
    fetch: server.fetch,
    createSocket: server.sockets.create,
    network: server.network,
    baseUrl: "http://mock.local",
    wsBase: "ws://mock.local",
    host: "seed small",
    mock: server,
  });
}

export const ServicesContext = createContext<Services | null>(null);

export function useServices(): Services {
  const services = useContext(ServicesContext);
  if (!services) throw new Error("useServices outside ServicesContext");
  return services;
}
