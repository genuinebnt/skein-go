/**
 * The offline-first sync path end to end, in-process: NoteSession (WASM CRDT,
 * IndexedDB via fake-indexeddb, ReconnectingSocket) against the mock gateway
 * serving the seed's real op logs.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { ApiError } from "../api/errors";
import { NoteSession, type SessionDeps } from "../crdt/noteSession";
import { OpStore } from "../crdt/store";
import { syncStatus } from "../crdt/syncState";
import { HOME_NOTE, createTestServer, signedInClient, testPassword } from "../test/mock";
import type { MockServer } from "./server";

const finalText = new Map(
  readFileSync(resolve(__dirname, "../../../testdata/seed/small/truth/crdt/final.jsonl"), "utf8")
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l) as { note_id: string; text: string })
    .map((r) => [r.note_id, r.text]),
);

let dbCounter = 0;

async function openSession(server: MockServer, crdt: SessionDeps["crdt"], options: { store?: OpStore; email?: string } = {}) {
  const client = await signedInClient(server, options.email);
  const store = options.store ?? new OpStore(indexedDB, `sync-test-${++dbCounter}`);
  const session = await NoteSession.open(HOME_NOTE, {
    crdt,
    store,
    api: client.api,
    wsBase: "ws://mock.local",
    createSocket: server.sockets.create,
    accessToken: client.token,
    refresh: client.refresh,
    userId: client.user.id!,
    attributeSync: true,
    network: server.network,
  });
  return { session, store, client };
}

const settled = (session: NoteSession) => expect.poll(() => syncStatus(session.sync), { timeout: 5000 }).toBe("synced");

describe("sync against the mock gateway", () => {
  it("catches up to the seed's final text and recovers block attributes", async () => {
    const { server, crdt } = await createTestServer();
    const { session } = await openSession(server, crdt);
    await settled(session);
    expect(session.doc.text()).toBe(finalText.get(HOME_NOTE));
    expect(session.doc.blocks().some((b) => b.type === "heading2")).toBe(true);
    session.doc.checkInvariants();
    await session.close();
  });

  it("delivers one client's edit to another and shows its presence", async () => {
    const { server, crdt } = await createTestServer();
    const a = await openSession(server, crdt);
    const b = await openSession(server, crdt);
    await settled(a.session);
    await settled(b.session);
    const block = a.session.doc.blocks()[0]!;
    await a.session.insertText(block.id, 0, "Edited 🙂 ");
    await expect.poll(() => b.session.doc.text(), { timeout: 5000 }).toContain("Edited 🙂 Welcome");
    a.session.setCaret({ blockId: block.id, offset: 3 });
    await expect.poll(() => b.session.peers.size, { timeout: 5000 }).toBe(1);
    await a.session.close();
    await b.session.close();
  });

  it("queues edits offline, counts them as pending, and sends them on reconnect", async () => {
    const { server, crdt } = await createTestServer();
    const { session } = await openSession(server, crdt);
    await settled(session);
    server.network.setOffline(true);
    await expect.poll(() => syncStatus(session.sync)).toBe("offline");
    const block = session.doc.blocks()[1]!;
    await session.insertText(block.id, 0, "offline one ");
    await session.insertText(block.id, 0, "offline two ");
    expect(session.sync.pending).toBe(2);
    server.network.setOffline(false);
    await settled(session);
    const nd = await server.loadDoc(HOME_NOTE);
    expect(nd.doc.text()).toContain("offline two offline one ");
    await session.close();
  });

  it("keeps local edits across a reload while offline (IndexedDB)", async () => {
    const { server, crdt } = await createTestServer();
    const first = await openSession(server, crdt);
    await settled(first.session);
    server.network.setOffline(true);
    const block = first.session.doc.blocks()[2]!;
    await first.session.insertText(block.id, 0, "survives reload ");
    await first.session.close();
    const again = await NoteSession.open(HOME_NOTE, {
      crdt,
      store: first.store,
      api: first.client.api,
      wsBase: "ws://mock.local",
      createSocket: server.sockets.create,
      accessToken: first.client.token,
      refresh: first.client.refresh,
      userId: first.client.user.id!,
      network: server.network,
    });
    expect(again.doc.text()).toContain("survives reload ");
    expect(again.sync.pending).toBe(1);
    server.network.setOffline(false);
    await settled(again);
    expect((await server.loadDoc(HOME_NOTE)).doc.text()).toContain("survives reload ");
    await again.close();
  });

  it("turns a rejected batch into a draft and resyncs without it", async () => {
    const { server, crdt } = await createTestServer();
    const { session, client } = await openSession(server, crdt);
    await settled(session);
    const replicaBefore = session.replica;
    server.revokeEdit(HOME_NOTE, client.user.id!);
    const block = session.doc.blocks()[0]!;
    await session.insertText(block.id, 0, "never accepted ");
    await expect.poll(() => syncStatus(session.sync), { timeout: 5000 }).toBe("draft");
    expect(session.drafts[0]?.text).toBe("never accepted ");
    await expect.poll(() => session.doc.text(), { timeout: 5000 }).toBe(finalText.get(HOME_NOTE));
    expect(session.replica).not.toBe(replicaBefore);
    expect(session.role).toBe("NOTE_ROLE_VIEWER");
    await session.discardDraft(session.drafts[0]!);
    expect(syncStatus(session.sync)).not.toBe("draft");
    await session.close();
  });
});

describe("REST conventions in the mock gateway", () => {
  it("answers an unreadable note with NOT_FOUND, not PERMISSION_DENIED", async () => {
    const { server } = await createTestServer();
    const { api } = await signedInClient(server, "ifeoma@inkwell-tx.example");
    await expect(api.call("DocumentService_GetNote", { noteId: HOME_NOTE })).rejects.toMatchObject({ code: "NOT_FOUND" });
  });

  it("replays a create with the same Idempotency-Key instead of creating twice", async () => {
    const { server } = await createTestServer();
    const { api } = await signedInClient(server);
    const key = crypto.randomUUID();
    const first = await api.callWithMeta("DocumentService_CreateNote", { workspaceId: "019c8cf2-1000-769b-b000-73aede463976" }, { title: "Once" }, { idempotencyKey: key });
    const second = await api.callWithMeta("DocumentService_CreateNote", { workspaceId: "019c8cf2-1000-769b-b000-73aede463976" }, { title: "Once" }, { idempotencyKey: key });
    expect(second.body.note?.id).toBe(first.body.note?.id);
    expect(second.meta.replayed).toBe(true);
  });

  it("refreshes an expired token transparently and treats a reused refresh token as theft", async () => {
    const { server } = await createTestServer();
    const { api } = await signedInClient(server);
    server.expireTokens();
    await expect(api.call("NotificationService_GetPreferences", {})).resolves.toHaveProperty("preferences");
    const signIn = await api.call("IdentityService_SignIn", {}, { email: "tobias@kestrel-lab.example", password: testPassword() }, { anonymous: true });
    const refreshToken = signIn.session!.refreshToken!;
    await api.call("IdentityService_Refresh", {}, { refreshToken }, { anonymous: true });
    await expect(api.call("IdentityService_Refresh", {}, { refreshToken }, { anonymous: true })).rejects.toBeInstanceOf(ApiError);
  });

  it("keeps at least one owner", async () => {
    const { server } = await createTestServer();
    const { api, user } = await signedInClient(server);
    await expect(api.call("IdentityService_SetWorkspaceRole", { workspaceId: "019c8cf2-1000-769b-b000-73aede463976", userId: user.id! }, { role: "WORKSPACE_ROLE_MEMBER" })).rejects.toMatchObject({
      code: "FAILED_PRECONDITION",
    });
  });

  it("runs a regex code search with a language filter", async () => {
    const { server } = await createTestServer();
    const { api } = await signedInClient(server);
    const res = await api.call("SearchService_Search", { workspaceId: "019c8cf2-1000-769b-b000-73aede463976", query: "func \\w+\\(", mode: "SEARCH_MODE_REGEX", languages: ["go"] });
    expect(res.hits?.length).toBeGreaterThan(0);
    expect(res.hits!.every((h) => h.language === "go")).toBe(true);
    await expect(api.call("SearchService_Search", { workspaceId: "019c8cf2-1000-769b-b000-73aede463976", query: "(", mode: "SEARCH_MODE_REGEX" })).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
  });
});
