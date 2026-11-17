import { beforeEach, describe, expect, it } from "vitest";
import { OpStore, mintReplicaId, verifyCounter } from "./store";
import { describeSync, initialSync, syncReducer, syncStatus, type SyncEvent, type SyncState } from "./syncState";
import { TextIndex, sliceCodePoints, utf16ToCodePoint } from "./utf16";

describe("UTF-16 to code points", () => {
  it("is the identity for text without astral characters", () => {
    const index = new TextIndex("héllo wörld 漢字");
    expect(index.toCodePoint(7)).toBe(7);
    expect(index.toUtf16(7)).toBe(7);
  });

  it("counts a surrogate pair as one code point in both directions", () => {
    const text = "a🙂b🎉c"; // UTF-16: a(0) 🙂(1,2) b(3) 🎉(4,5) c(6)
    const index = new TextIndex(text);
    expect(index.codePointLength).toBe(5);
    expect([0, 1, 3, 4, 6, 7].map((u) => index.toCodePoint(u))).toEqual([0, 1, 2, 3, 4, 5]);
    expect([0, 1, 2, 3, 4, 5].map((c) => index.toUtf16(c))).toEqual([0, 1, 3, 4, 6, 7]);
  });

  it("rounds an offset inside a pair down to the pair's start, so an edit never splits a character", () => {
    expect(utf16ToCodePoint("a🙂b", 2)).toBe(1);
  });

  it("clamps out-of-range offsets", () => {
    const index = new TextIndex("a🙂");
    expect(index.toCodePoint(-3)).toBe(0);
    expect(index.toCodePoint(99)).toBe(2);
    expect(index.toUtf16(99)).toBe(3);
  });

  it("slices by code points, as API ranges are given", () => {
    expect(sliceCodePoints("👋 hi 🙂 there", 2, 4)).toBe("hi");
  });

  it("agrees with Array.from for random strings", () => {
    const alphabet = ["a", "é", "漢", "🙂", "👨‍👩‍👧", "\u001e"];
    for (let trial = 0; trial < 200; trial++) {
      let s = "";
      for (let i = 0; i < 12; i++) s += alphabet[(trial * 7 + i * 13) % alphabet.length];
      const index = new TextIndex(s);
      const points = Array.from(s);
      let utf16 = 0;
      points.forEach((p, cp) => {
        expect(index.toCodePoint(utf16)).toBe(cp);
        expect(index.toUtf16(cp)).toBe(utf16);
        utf16 += p.length;
      });
      expect(index.codePointLength).toBe(points.length);
    }
  });
});

const run = (events: SyncEvent[], from: SyncState = initialSync) => events.reduce(syncReducer, from);

describe("sync state", () => {
  it("is Synced once open and caught up with nothing queued", () => {
    const s = run([{ type: "connection", connection: "open" }, { type: "hello-sent" }, { type: "caught-up", at: 1 }]);
    expect(syncStatus(s)).toBe("synced");
    expect(describeSync(s, "v57")).toMatchObject({ label: "Synced", count: "v57" });
  });

  it("counts local edits as Syncing while open and as Offline pending while disconnected", () => {
    const open = run([{ type: "connection", connection: "open" }, { type: "local-edit" }, { type: "sent", count: 1 }]);
    expect(describeSync(open)).toMatchObject({ status: "syncing", count: "1" });
    const offline = run([{ type: "connection", connection: "offline" }, { type: "local-edit" }, { type: "local-edit" }]);
    expect(describeSync(offline)).toMatchObject({ status: "offline", label: "Offline", count: "2 pending" });
  });

  it("returns in-flight batches to pending when the connection drops, since they may not have arrived", () => {
    const s = run([{ type: "connection", connection: "open" }, { type: "local-edit" }, { type: "sent", count: 1 }, { type: "connection", connection: "waiting" }]);
    expect(s.pending).toBe(1);
    expect(s.inflight).toBe(0);
  });

  it("confirms in-flight batches back to Synced", () => {
    const s = run([{ type: "connection", connection: "open" }, { type: "local-edit" }, { type: "sent", count: 1 }, { type: "confirmed", count: 1, at: 9 }]);
    expect(syncStatus(s)).toBe("synced");
    expect(s.lastSyncedAt).toBe(9);
  });

  it("shows Draft while rejected changes wait, whatever the connection", () => {
    const s = run([{ type: "connection", connection: "open" }, { type: "local-edit" }, { type: "sent", count: 1 }, { type: "rejected", operations: 2, batches: 1 }]);
    expect(describeSync(s)).toMatchObject({ status: "draft", count: "2 rejected" });
    expect(s.inflight + s.pending).toBe(0);
    expect(syncStatus(syncReducer(s, { type: "draft-resolved", operations: 2 }))).toBe("synced");
  });

  it("reads a first connection with nothing queued as syncing, not offline", () => {
    expect(syncStatus(run([{ type: "connection", connection: "connecting" }]))).toBe("syncing");
  });
});

describe("OpStore", () => {
  let store: OpStore;
  let n = 0;
  beforeEach(() => {
    store = new OpStore(indexedDB, `skein-test-${++n}`);
  });

  it("mints a replica ID once per device and keeps it", async () => {
    const first = await store.replicaId(() => "123");
    expect(first).toBe("123");
    expect(await store.replicaId(() => "456")).toBe("123");
    expect(await store.rotateReplica(() => "789")).toBe("789");
    expect(await store.replicaId()).toBe("789");
  });

  it("never mints 0 or the reserved baseline replica", () => {
    const values = [0n, 0xffffffffffffffffn, 42n];
    expect(mintReplicaId((a) => ((a[0] = values.shift()!), a))).toBe("42");
  });

  it("stores a local batch together with the counter after it", async () => {
    await store.appendLocal("n1", "7", "3", { kind: "ops", bytes: Uint8Array.of(1), createdAt: 1 });
    await store.appendLocal("n1", "7", "5", { kind: "ops", bytes: Uint8Array.of(2), createdAt: 2 });
    const { note, pending } = await store.loadNote("n1");
    expect(note?.counter).toBe("5");
    expect(pending.map((p) => [...p.bytes])).toEqual([[1], [2]]);
  });

  it("removes confirmed batches and forgets a note completely", async () => {
    const a = await store.appendLocal("n1", "7", "1", { kind: "ops", bytes: Uint8Array.of(1), createdAt: 1 });
    await store.appendLocal("n2", "7", "1", { kind: "ops", bytes: Uint8Array.of(2), createdAt: 1 });
    await store.markSent([a], 10);
    expect((await store.loadNote("n1")).pending[0]?.sentAt).toBe(10);
    await store.removePending([a]);
    expect((await store.loadNote("n1")).pending).toEqual([]);
    await store.forgetNote("n2");
    expect(await store.loadNote("n2")).toEqual({ note: undefined, pending: [] });
  });

  it("keeps rejected edits as drafts until discarded", async () => {
    const id = await store.addDraft({ noteId: "n1", text: "lost words", reason: "revoked", operations: 10, at: 5 });
    expect((await store.drafts("n1")).map((d) => d.text)).toEqual(["lost words"]);
    await store.removeDraft(id);
    expect(await store.drafts("n1")).toEqual([]);
  });

  it("detects a counter that disagrees with the stored operations", () => {
    expect(verifyCounter("5", [{ replicaId: "7", counter: "5" }], "7")).toBe(true);
    expect(verifyCounter("3", [{ replicaId: "7", counter: "5" }], "7")).toBe(false);
    expect(verifyCounter(undefined, [], "7")).toBe(true);
  });
});
