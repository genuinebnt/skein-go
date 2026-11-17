/**
 * One open note: the CRDT document, its IndexedDB copy, and the live sync
 * connection (docs/api/websocket.md#documentservicesync), kept consistent.
 *
 * Lifecycle:
 *   1. Restore from IndexedDB (operations, attributes, unconfirmed local
 *      batches). If the stored replica counter disagrees with the restored
 *      operations, rotate to a new replica ID (ADR-0004).
 *   2. Register the replica (idempotent), read the note over REST for its
 *      title, Markdown and version, and connect.
 *   3. On open, send the hello with our version vector, then resend every
 *      unconfirmed local batch (operations are idempotent).
 *   4. Apply remote batches through the CRDT's causal buffer; once our
 *      version covers the version GetNote reported, we have caught up and
 *      recover block attributes from its Markdown (see web/wasm/bridge).
 *   5. A `rejected` frame drops the refused batches, keeps their text as a
 *      draft, forgets the local copy and starts over from the server with a
 *      fresh replica ID, as ADR-0007 prescribes.
 *
 * Offline, edits keep going into IndexedDB and the badge counts them.
 */

import type { ApiClient } from "../api/client";
import { ApiError } from "../api/errors";
import type { GetNoteResponse, Note, NoteRole } from "../api/schema.gen";
import { ReconnectingSocket, type Timers, type WebSocketFactory } from "../ws/socket";
import type { SocketState } from "../ws/policy";
import { decodeResponse, encodeRequest, fromBase64, toBase64, type Presence, type Subprotocol, type VersionEntry } from "../ws/syncCodec";
import { OpStore, verifyCounter, type Draft, type PendingBatch } from "./store";
import { ACK_GRACE_MS, initialSync, syncReducer, type Connection, type SyncEvent, type SyncState } from "./syncState";
import type { BlockAttrs, Caret, CrdtDocument, CrdtModule } from "./wasm";

export type PeerPresence = Presence & { at: number };

export type SessionDeps = {
  crdt: CrdtModule;
  store: OpStore;
  api: ApiClient;
  /** ws:// or wss:// base of the gateway, without a trailing slash. */
  wsBase: string;
  createSocket: WebSocketFactory;
  accessToken: () => string | null;
  refresh: () => Promise<boolean>;
  userId: string;
  subprotocol?: Subprotocol;
  /** Mock mode carries attribute writes on sync (a proposed field); the live protocol cannot yet. */
  attributeSync?: boolean;
  network?: SocketOptionsNetwork;
  timers?: Timers;
  now?: () => number;
};

type SocketOptionsNetwork = Pick<EventTarget, "addEventListener" | "removeEventListener"> & { onLine?: boolean };

const PRESENCE_THROTTLE_MS = 120;
const HEARTBEAT_MS = 25_000;
const SNAPSHOT_DEBOUNCE_MS = 400;
/** Frames are at most 1 MiB (websocket.md); leave room for the envelope. */
const MAX_FRAME_BYTES = 1_000_000;

/** Whether `have` includes everything in `want`. */
export function covers(have: VersionEntry[], want: VersionEntry[]): boolean {
  const map = new Map(have.map((e) => [e.replicaId, BigInt(e.counter)]));
  return want.every((e) => (map.get(e.replicaId) ?? 0n) >= BigInt(e.counter));
}

const connectionOf = (state: SocketState): Connection => (state.status === "stopped" ? "stopped" : state.status);

export class NoteSession {
  doc: CrdtDocument;
  note: Note | null = null;
  /** The caller's role on the note: `callerRole` when the server sends it (proposed), else assumed editor until refused. */
  role: NoteRole = "NOTE_ROLE_EDITOR";
  sync: SyncState = initialSync;
  peers = new Map<string, PeerPresence>();
  drafts: Draft[] = [];
  /** Bumped on every change, for useSyncExternalStore. */
  revision = 0;
  error: string | null = null;

  private socket: ReconnectingSocket | null = null;
  private listeners = new Set<() => void>();
  private serverVersion: VersionEntry[] | null = null;
  private markdown: string | null = null;
  private attrsSeeded = false;
  private inflight: number[] = [];
  private ackTimer: unknown = null;
  private snapshotTimer: unknown = null;
  private presenceTimer: unknown = null;
  private lastCaret: Caret | null = null;
  private closed = false;
  private readonly subprotocol: Subprotocol;
  private readonly now: () => number;
  private readonly timers: Pick<Timers, "setTimeout" | "clearTimeout">;

  private constructor(
    readonly noteId: string,
    private replicaId: string,
    private readonly deps: SessionDeps,
  ) {
    this.doc = deps.crdt.open(replicaId);
    this.subprotocol = deps.subprotocol ?? "skein.v1.proto";
    this.now = deps.now ?? (() => Date.now());
    this.timers = deps.timers ?? {
      setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
      clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
    };
  }

  static async open(noteId: string, deps: SessionDeps): Promise<NoteSession> {
    let replicaId = await deps.store.replicaId();
    const { note, pending } = await deps.store.loadNote(noteId);
    let session = new NoteSession(noteId, replicaId, deps);
    session.restore(note?.ops, note?.attrs, pending);
    if (note && !verifyCounter(note.counter, session.doc.version(), replicaId)) {
      // ADR-0004: never risk reusing a counter. The stored operations stay valid
      // under their old replica; new ones get a new one.
      session.doc.free();
      replicaId = await deps.store.rotateReplica();
      session = new NoteSession(noteId, replicaId, deps);
      session.restore(note.ops, note.attrs, pending);
    }
    session.drafts = await deps.store.drafts(noteId);
    session.dispatch({
      type: "restored",
      pending: pending.filter((p) => p.kind === "ops" || deps.attributeSync).length,
      rejected: session.drafts.reduce((n, d) => n + d.operations, 0),
    });
    await session.start();
    return session;
  }

  private restore(ops: Uint8Array | undefined, attrs: Uint8Array | undefined, pending: PendingBatch[]): void {
    if (ops && ops.length > 0) this.doc.applyRemote(ops);
    if (attrs && attrs.length > 0) this.doc.applyRemoteAttrs(attrs);
    for (const batch of pending) {
      if (batch.kind === "ops") this.doc.applyRemote(batch.bytes);
      else this.doc.applyRemoteAttrs(batch.bytes);
    }
  }

  private async start(): Promise<void> {
    await this.registerReplica();
    await this.refreshNote();
    this.connect();
  }

  private async registerReplica(): Promise<void> {
    try {
      await this.deps.api.call("IdentityService_RegisterReplica", {}, { replicaId: this.replicaId });
    } catch (error) {
      if (error instanceof ApiError && error.code === "ALREADY_EXISTS") {
        // Someone else owns this ID (vanishingly rare): mint another and start over.
        this.replicaId = await this.deps.store.rotateReplica();
        this.rebuildDoc(this.doc.allOps(), this.doc.allAttrs());
        await this.deps.api.call("IdentityService_RegisterReplica", {}, { replicaId: this.replicaId });
      } else if (!(error instanceof ApiError)) {
        // Offline: the replica registers on a later open; edits stay local meanwhile.
      } else {
        throw error;
      }
    }
  }

  /** Re-reads the note's metadata, Markdown and version (also after a rejection). */
  async refreshNote(): Promise<void> {
    try {
      const response: GetNoteResponse & { callerRole?: NoteRole } = await this.deps.api.call("DocumentService_GetNote", {
        noteId: this.noteId,
        includeMarkdown: true,
      });
      this.note = response.note ?? null;
      this.markdown = response.markdown ?? "";
      this.serverVersion = response.version?.entries?.map((e) => ({ replicaId: e.replicaId ?? "0", counter: e.counter ?? "0" })) ?? [];
      if (response.callerRole) this.role = response.callerRole;
      this.trySeedAttributes();
      this.emit();
    } catch (error) {
      if (error instanceof ApiError) {
        this.error = error.code === "NOT_FOUND" ? "This note doesn't exist, or you can't open it." : error.message;
        if (error.code === "NOT_FOUND" || error.code === "PERMISSION_DENIED") throw error;
      }
      // Offline: work from the local copy.
    }
  }

  private connect(): void {
    const url = `${this.deps.wsBase}/v1/notes/${encodeURIComponent(this.noteId)}/sync`;
    this.socket = new ReconnectingSocket({
      url,
      subprotocol: this.subprotocol,
      accessToken: this.deps.accessToken,
      refresh: this.deps.refresh,
      create: this.deps.createSocket,
      network: this.deps.network,
      onState: (state) => {
        this.dispatch({ type: "connection", connection: connectionOf(state), stopReason: state.status === "stopped" ? state.reason : undefined });
        if (state.status !== "open") this.inflight = [];
        if (state.status === "stopped" && state.reason === "forbidden") void this.handleForbidden();
      },
      onOpen: () => void this.handleOpen(),
      onMessage: (data) => this.handleFrame(data),
      heartbeat: { intervalMs: HEARTBEAT_MS, beat: () => this.sendPresence() },
    });
    this.socket.start();
  }

  private async handleOpen(): Promise<void> {
    this.sendFrame(encodeRequest({ kind: "hello", noteId: this.noteId, replicaId: this.replicaId, version: this.doc.version() }, this.subprotocol));
    this.dispatch({ type: "hello-sent" });
    const { pending } = await this.deps.store.loadNote(this.noteId);
    const sent: number[] = [];
    for (const batch of pending) {
      if (this.sendBatch(batch)) sent.push(batch.seq!);
    }
    await this.afterSend(sent);
    // GetNote told us the server's version. If it lacks operations we hold
    // (confirmed earlier but lost by a server restored from backup, or a fresh
    // mock gateway after a reload), send them: applying them twice is a no-op.
    if (this.serverVersion && !covers(this.serverVersion, this.doc.version())) {
      const missing = this.doc.opsSince(this.serverVersion);
      if (missing.length > 4 && missing.length < MAX_FRAME_BYTES) this.sendFrame(encodeRequest({ kind: "ops", ops: missing }, this.subprotocol));
    }
    this.checkCaughtUp();
    this.sendPresence();
  }

  private sendBatch(batch: Pick<PendingBatch, "kind" | "bytes">): boolean {
    if (batch.kind === "attrs") {
      if (!this.deps.attributeSync) return false;
      // Proposed SyncRequest field `attrs` (see the PR). Until it exists in the
      // proto it travels as a JSON text frame, which only the mock server reads.
      return this.sendFrame(JSON.stringify({ attrs: toBase64(batch.bytes) }));
    }
    return this.sendFrame(encodeRequest({ kind: "ops", ops: batch.bytes }, this.subprotocol));
  }

  private sendFrame(frame: string | Uint8Array): boolean {
    return this.socket?.send(frame) ?? false;
  }

  private async afterSend(seqs: number[]): Promise<void> {
    if (seqs.length === 0) return;
    this.inflight.push(...seqs);
    this.dispatch({ type: "sent", count: seqs.length });
    await this.deps.store.markSent(seqs, this.now());
    if (this.ackTimer !== null) this.timers.clearTimeout(this.ackTimer);
    this.ackTimer = this.timers.setTimeout(() => void this.confirmInflight(), ACK_GRACE_MS);
  }

  private async confirmInflight(): Promise<void> {
    this.ackTimer = null;
    if (!this.socket?.isOpen || this.inflight.length === 0) return;
    const seqs = this.inflight;
    this.inflight = [];
    await this.deps.store.removePending(seqs);
    this.dispatch({ type: "confirmed", count: seqs.length, at: this.now() });
  }

  private handleFrame(data: string | ArrayBuffer): void {
    let frame;
    try {
      if (typeof data === "string" && this.deps.attributeSync) {
        const ext = JSON.parse(data) as { attrs?: string };
        if (ext.attrs) {
          this.doc.applyRemoteAttrs(fromBase64(ext.attrs));
          this.scheduleSnapshot();
          this.emit();
          return;
        }
      }
      frame = decodeResponse(data, typeof data === "string" ? "skein.v1.json" : this.subprotocol);
    } catch {
      return; // a frame we cannot read; the next hello resynchronises
    }
    switch (frame.kind) {
      case "ops":
        try {
          this.doc.applyRemote(frame.ops);
        } catch (error) {
          this.error = `A change from the server could not be applied: ${(error as Error).message}`;
        }
        this.scheduleSnapshot();
        this.checkCaughtUp();
        this.emit();
        return;
      case "presence":
        if (frame.presence.replicaId === this.replicaId) return;
        this.peers.set(frame.presence.replicaId, { ...frame.presence, at: this.now() });
        this.emit();
        return;
      case "rejected":
        void this.handleRejected(frame.ops, frame.reason);
        return;
      default:
        return;
    }
  }

  private checkCaughtUp(): void {
    if (!this.sync.catchingUp) return;
    if (this.serverVersion === null || covers(this.doc.version(), this.serverVersion)) {
      this.dispatch({ type: "caught-up", at: this.now() });
      this.trySeedAttributes();
    }
  }

  private trySeedAttributes(): void {
    if (this.attrsSeeded || this.markdown === null || this.serverVersion === null) return;
    if (!covers(this.doc.version(), this.serverVersion)) return;
    this.doc.seedAttrsFromMarkdown(this.markdown);
    this.attrsSeeded = true;
    this.scheduleSnapshot();
    this.emit();
  }

  private async handleRejected(ops: Uint8Array, reason: string): Promise<void> {
    const summary = this.deps.crdt.describeOps(ops);
    const draft: Draft = { noteId: this.noteId, text: summary.inserted, reason, operations: summary.inserts + summary.deletes, at: this.now() };
    draft.id = await this.deps.store.addDraft(draft);
    this.drafts = [draft, ...this.drafts];
    const { pending } = await this.deps.store.loadNote(this.noteId);
    this.dispatch({ type: "rejected", operations: draft.operations, batches: pending.length });
    // Drop the refused edits: forget the local copy, take a new replica, and resync from scratch.
    this.socket?.stop();
    this.inflight = [];
    await this.deps.store.forgetNote(this.noteId);
    this.replicaId = await this.deps.store.rotateReplica();
    this.rebuildDoc();
    this.attrsSeeded = false;
    await this.registerReplica();
    await this.refreshNote().catch(() => undefined);
    this.dispatch({ type: "restored", pending: 0, rejected: this.sync.rejected });
    if (!this.closed) this.connect();
  }

  private async handleForbidden(): Promise<void> {
    // 4403: access changed while connected. Reload over REST; the role decides what the editor allows.
    this.role = "NOTE_ROLE_VIEWER";
    await this.refreshNote().catch(() => undefined);
    this.emit();
  }

  private rebuildDoc(ops?: Uint8Array, attrs?: Uint8Array): void {
    this.doc.free();
    this.doc = this.deps.crdt.open(this.replicaId);
    if (ops && ops.length > 0) this.doc.applyRemote(ops);
    if (attrs && attrs.length > 0) this.doc.applyRemoteAttrs(attrs);
    this.emit();
  }

  get canEdit(): boolean {
    return this.role === "NOTE_ROLE_EDITOR";
  }

  get replica(): string {
    return this.replicaId;
  }

  /** Records and sends a local batch the document already applied. */
  async commit(bytes: Uint8Array, kind: "ops" | "attrs" = "ops"): Promise<void> {
    if (bytes.length <= 4) return; // an empty batch is just the header
    const counter = this.doc.version().find((e) => e.replicaId === this.replicaId)?.counter ?? "0";
    const counts = kind === "ops" || this.deps.attributeSync;
    const seq = await this.deps.store.appendLocal(this.noteId, this.replicaId, counter, { kind, bytes, createdAt: this.now() });
    if (counts) this.dispatch({ type: "local-edit" });
    if (!counts) await this.deps.store.removePending([seq]); // kept in the snapshot instead
    this.scheduleSnapshot();
    this.emit();
    if (counts && this.socket?.isOpen && this.sendBatch({ kind, bytes })) await this.afterSend([seq]);
  }

  // ── editing helpers the editor calls ───────────────────────────────────────

  insertText(blockId: string, offset: number, text: string): Promise<void> {
    return this.commit(this.doc.insertText(blockId, offset, text));
  }

  deleteText(blockId: string, from: number, to: number): Promise<void> {
    return this.commit(this.doc.deleteText(blockId, from, to));
  }

  splitBlock(blockId: string, offset: number): string {
    const { ops, newBlockId } = this.doc.splitBlock(blockId, offset);
    void this.commit(ops);
    return newBlockId;
  }

  appendBlock(): string {
    const { ops, newBlockId } = this.doc.appendBlock();
    void this.commit(ops);
    return newBlockId;
  }

  mergeWithPrevious(blockId: string): Promise<void> {
    return this.commit(this.doc.mergeWithPrevious(blockId));
  }

  deleteBlock(blockId: string): Promise<void> {
    return this.commit(this.doc.deleteBlock(blockId));
  }

  setBlockAttrs(blockId: string, attrs: BlockAttrs): Promise<void> {
    return this.commit(this.doc.setBlockAttrs(blockId, attrs), "attrs");
  }

  /**
   * Replaces the first occurrence of `find` in the note's text with
   * `replace`, as the user's own edit. Rejecting an agent suggestion uses
   * this to put the original text back. Returns false when `find` is gone.
   */
  async replaceText(find: string, replace: string): Promise<boolean> {
    const at = this.doc.text().indexOf(find);
    if (!find || at < 0) return false;
    await this.commit(this.doc.deleteInNote(at, at + find.length));
    if (replace) await this.commit(this.doc.insertInNote(at, replace));
    return true;
  }

  /** Undo the replica's last change group on the server (DocumentService.Undo) and apply the inverse here. */
  async undo(direction: "undo" | "redo" = "undo"): Promise<void> {
    const id = direction === "undo" ? "DocumentService_Undo" : "DocumentService_Redo";
    const response = await this.deps.api.call(id, { noteId: this.noteId }, { replicaId: this.replicaId }, { idempotencyKey: true });
    if (response.ops) {
      this.doc.applyRemote(fromBase64(response.ops));
      this.scheduleSnapshot();
      this.emit();
    }
  }

  /** Re-inserts a draft's text as a new block at the end, then discards the draft. */
  async reinsertDraft(draft: Draft): Promise<void> {
    const blockId = this.appendBlock();
    await this.insertText(blockId, 0, draft.text.replace(/\n/g, " ").trim());
    await this.discardDraft(draft);
  }

  async discardDraft(draft: Draft): Promise<void> {
    if (draft.id !== undefined) await this.deps.store.removeDraft(draft.id);
    this.drafts = this.drafts.filter((d) => d !== draft);
    this.dispatch({ type: "draft-resolved", operations: draft.operations });
  }

  /** The local caret, shared with others as presence (throttled). */
  setCaret(caret: Caret | null): void {
    this.lastCaret = caret;
    if (this.presenceTimer !== null) return;
    this.presenceTimer = this.timers.setTimeout(() => {
      this.presenceTimer = null;
      this.sendPresence();
    }, PRESENCE_THROTTLE_MS);
  }

  private sendPresence(): void {
    if (!this.lastCaret || !this.socket?.isOpen) return;
    let anchor: string;
    try {
      anchor = this.doc.anchorAt(this.lastCaret);
    } catch {
      return;
    }
    this.sendFrame(encodeRequest({ kind: "presence", presence: { userId: this.deps.userId, replicaId: this.replicaId, anchor, head: anchor } }, this.subprotocol));
  }

  retryNow(): void {
    this.socket?.retryNow();
  }

  private scheduleSnapshot(): void {
    if (this.snapshotTimer !== null) return;
    this.snapshotTimer = this.timers.setTimeout(() => {
      this.snapshotTimer = null;
      void this.saveSnapshot();
    }, SNAPSHOT_DEBOUNCE_MS);
  }

  private async saveSnapshot(): Promise<void> {
    if (this.closed) return;
    const counter = this.doc.version().find((e) => e.replicaId === this.replicaId)?.counter ?? "0";
    await this.deps.store.saveSnapshot({
      noteId: this.noteId,
      replicaId: this.replicaId,
      counter,
      ops: this.doc.allOps(),
      attrs: this.doc.allAttrs(),
      savedAt: this.now(),
    });
  }

  async close(): Promise<void> {
    if (this.closed) return;
    if (this.snapshotTimer !== null) {
      this.timers.clearTimeout(this.snapshotTimer);
      this.snapshotTimer = null;
      await this.saveSnapshot();
    }
    this.closed = true;
    if (this.ackTimer !== null) this.timers.clearTimeout(this.ackTimer);
    this.socket?.stop();
    this.doc.free();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private dispatch(event: SyncEvent): void {
    this.sync = syncReducer(this.sync, event);
    this.emit();
  }

  private emit(): void {
    this.revision += 1;
    for (const listener of this.listeners) listener();
  }
}
