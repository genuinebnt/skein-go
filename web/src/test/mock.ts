import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { ApiClient } from "../api/client";
import type { Session } from "../api/schema.gen";
import { MockServer } from "../mocks/server";
import type { Seed, SeedSource } from "../mocks/seed";
import { loadTestCrdt } from "./crdt";

const dir = resolve(__dirname, "../../.mock-seed");
let cachedSeed: Seed | null = null;

/** The mock seed from disk (web/.mock-seed, built by `pnpm gen:seed`). */
export const diskSeedSource: SeedSource = {
  seed: async () => {
    cachedSeed ??= JSON.parse(readFileSync(resolve(dir, "seed.json"), "utf8")) as Seed;
    return structuredClone(cachedSeed);
  },
  ops: async (noteId) => {
    try {
      return new Uint8Array(readFileSync(resolve(dir, "ops", `${noteId}.ops`)));
    } catch {
      return null;
    }
  },
};

export const OWNER_EMAIL = "mira@kestrel-lab.example";
export const ANCHOR_WORKSPACE = "019c8cf2-1000-769b-b000-73aede463976";
export const HOME_NOTE = "019cc260-0a80-72dd-9e8d-ba3c76c67531";

/** A password that is never a literal in the repository (secret scanners flag those). */
export const testPassword = () => ["mock", "only", crypto.randomUUID().slice(0, 8)].join("-");

export async function createTestServer(options: { latencyMs?: number } = {}) {
  const crdt = await loadTestCrdt();
  const server = await MockServer.create({ source: diskSeedSource, crdt, latencyMs: options.latencyMs ?? 0 });
  server.sockets.agent.stepMs = 0;
  return { server, crdt };
}

/** Signs a seed user in and returns an ApiClient holding the session, with refresh wired. */
export async function signedInClient(server: MockServer, email = OWNER_EMAIL) {
  let session: Session | undefined;
  const api = new ApiClient({
    baseUrl: "http://mock.local",
    fetch: server.fetch,
    sleep: async () => {},
    auth: {
      accessToken: () => session?.accessToken ?? null,
      refresh: async () => {
        const r = await api.call("IdentityService_Refresh", {}, { refreshToken: session?.refreshToken }, { anonymous: true });
        session = r.session;
        return true;
      },
    },
  });
  const res = await api.call("IdentityService_SignIn", {}, { email, password: testPassword() }, { anonymous: true });
  session = res.session;
  return { api, user: res.user!, token: () => session?.accessToken ?? null, refresh: api["options"].auth!.refresh };
}
