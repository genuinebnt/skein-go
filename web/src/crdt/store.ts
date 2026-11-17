/**
 * The device's copy of its notes in IndexedDB (ADR-0007): each note's
 * operations and attribute writes, the queue of local batches the server
 * has not confirmed, rejected edits kept as drafts, and the device's
 * replica ID.
 *
 * Invariants:
 *   - A local batch and the replica's counter after it are written in the
 *     same transaction (ADR-0004), so the stored counter always equals the
 *     highest counter among stored operations from this replica.
 *   - When that does not hold on load (state lost, a crash between stores,
 *     storage cleared by the browser), the caller mints a new replica ID
 *     rather than risk reusing a counter (`verifyCounter`).
 *   - Pending batches are removed only when confirmed or rejected; resending
 *     one is always safe because operations are idempotent.
 */

const DB_NAME = "skein";
const DB_VERSION = 1;

export type NoteRecord = {
  noteId: string;
  replicaId: string;
  /** Highest counter of this replica's operations stored for the note, decimal. */
  counter: string;
  ops: Uint8Array;
  attrs: Uint8Array;
  savedAt: number;
};

export type PendingBatch = {
  seq?: number;
  noteId: string;
  kind: "ops" | "attrs";
  bytes: Uint8Array;
  createdAt: number;
  /** When it last went out on an open connection. */
  sentAt?: number;
};

export type Draft = {
  id?: number;
  noteId: string;
  text: string;
  reason: string;
  operations: number;
  at: number;
};

function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function done(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error ?? new Error("transaction aborted"));
  });
}

/** A random non-zero 64-bit replica ID (ADR-0004). The all-ones value is reserved for baseline writes. */
export function mintReplicaId(random: (array: BigUint64Array) => BigUint64Array = (a) => crypto.getRandomValues(a)): string {
  for (;;) {
    const id = random(new BigUint64Array(1))[0]!;
    if (id !== 0n && id !== 0xffffffffffffffffn) return id.toString();
  }
}

export class OpStore {
  private db: Promise<IDBDatabase> | null = null;

  constructor(
    private readonly factory: IDBFactory = globalThis.indexedDB,
    private readonly name = DB_NAME,
  ) {}

  private open(): Promise<IDBDatabase> {
    this.db ??= new Promise((resolve, reject) => {
      const req = this.factory.open(this.name, DB_VERSION);
      req.onupgradeneeded = () => {
        const db = req.result;
        db.createObjectStore("notes", { keyPath: "noteId" });
        db.createObjectStore("pending", { keyPath: "seq", autoIncrement: true }).createIndex("noteId", "noteId");
        db.createObjectStore("drafts", { keyPath: "id", autoIncrement: true }).createIndex("noteId", "noteId");
        db.createObjectStore("device");
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    return this.db;
  }

  async close(): Promise<void> {
    if (this.db) (await this.db).close();
    this.db = null;
  }

  /** The device's replica ID, minting and storing one on first use. */
  async replicaId(mint: () => string = mintReplicaId): Promise<string> {
    const db = await this.open();
    const tx = db.transaction("device", "readwrite");
    const store = tx.objectStore("device");
    let id = (await request(store.get("replica"))) as string | undefined;
    if (!id) {
      id = mint();
      store.put(id, "replica");
    }
    await done(tx);
    return id;
  }

  /** Replaces the device's replica ID, after an inconsistency or a rejection. */
  async rotateReplica(mint: () => string = mintReplicaId): Promise<string> {
    const db = await this.open();
    const tx = db.transaction("device", "readwrite");
    const id = mint();
    tx.objectStore("device").put(id, "replica");
    await done(tx);
    return id;
  }

  async loadNote(noteId: string): Promise<{ note: NoteRecord | undefined; pending: PendingBatch[] }> {
    const db = await this.open();
    const tx = db.transaction(["notes", "pending"], "readonly");
    const note = (await request(tx.objectStore("notes").get(noteId))) as NoteRecord | undefined;
    const pending = (await request(tx.objectStore("pending").index("noteId").getAll(noteId))) as PendingBatch[];
    pending.sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0));
    return { note, pending };
  }

  /** Saves a full snapshot of the note (after remote changes). */
  async saveSnapshot(record: NoteRecord): Promise<void> {
    const db = await this.open();
    const tx = db.transaction("notes", "readwrite");
    tx.objectStore("notes").put(record);
    await done(tx);
  }

  /**
   * Queues a local batch and records the replica's counter after it, in one
   * transaction (ADR-0004). Returns the batch's queue sequence number.
   */
  async appendLocal(noteId: string, replicaId: string, counter: string, batch: Omit<PendingBatch, "noteId" | "seq">): Promise<number> {
    const db = await this.open();
    const tx = db.transaction(["notes", "pending"], "readwrite");
    const notes = tx.objectStore("notes");
    const existing = (await request(notes.get(noteId))) as NoteRecord | undefined;
    notes.put({
      noteId,
      replicaId,
      counter,
      ops: existing?.ops ?? new Uint8Array(),
      attrs: existing?.attrs ?? new Uint8Array(),
      savedAt: Date.now(),
    } satisfies NoteRecord);
    const seq = (await request(tx.objectStore("pending").add({ ...batch, noteId }))) as number;
    await done(tx);
    return seq;
  }

  async markSent(seqs: number[], at: number): Promise<void> {
    if (seqs.length === 0) return;
    const db = await this.open();
    const tx = db.transaction("pending", "readwrite");
    const store = tx.objectStore("pending");
    for (const seq of seqs) {
      const batch = (await request(store.get(seq))) as PendingBatch | undefined;
      if (batch) store.put({ ...batch, sentAt: at });
    }
    await done(tx);
  }

  async removePending(seqs: number[]): Promise<void> {
    if (seqs.length === 0) return;
    const db = await this.open();
    const tx = db.transaction("pending", "readwrite");
    for (const seq of seqs) tx.objectStore("pending").delete(seq);
    await done(tx);
  }

  /** Drops everything stored for a note (after a rejection: reload from the server). */
  async forgetNote(noteId: string): Promise<void> {
    const db = await this.open();
    const tx = db.transaction(["notes", "pending"], "readwrite");
    tx.objectStore("notes").delete(noteId);
    const keys = await request(tx.objectStore("pending").index("noteId").getAllKeys(noteId));
    for (const key of keys) tx.objectStore("pending").delete(key);
    await done(tx);
  }

  async addDraft(draft: Draft): Promise<number> {
    const db = await this.open();
    const tx = db.transaction("drafts", "readwrite");
    const id = (await request(tx.objectStore("drafts").add(draft))) as number;
    await done(tx);
    return id;
  }

  async drafts(noteId?: string): Promise<Draft[]> {
    const db = await this.open();
    const tx = db.transaction("drafts", "readonly");
    const store = tx.objectStore("drafts");
    const all = (await request(noteId ? store.index("noteId").getAll(noteId) : store.getAll())) as Draft[];
    return all.sort((a, b) => b.at - a.at);
  }

  async removeDraft(id: number): Promise<void> {
    const db = await this.open();
    const tx = db.transaction("drafts", "readwrite");
    tx.objectStore("drafts").delete(id);
    await done(tx);
  }

  /** Every pending batch on the device, for the header's offline count. */
  async pendingCount(): Promise<number> {
    const db = await this.open();
    const tx = db.transaction("pending", "readonly");
    return request(tx.objectStore("pending").count());
  }
}

/**
 * True when the stored counter matches the highest counter of this replica
 * in the restored note, the consistency ADR-0004 requires before reusing the
 * replica for new operations.
 */
export function verifyCounter(stored: string | undefined, restoredVersion: { replicaId: string; counter: string }[], replicaId: string): boolean {
  const actual = restoredVersion.find((e) => e.replicaId === replicaId)?.counter ?? "0";
  return (stored ?? "0") === actual;
}
