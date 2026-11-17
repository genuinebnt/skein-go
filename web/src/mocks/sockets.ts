/**
 * The mock gateway's WebSocket endpoints (docs/api/websocket.md), as
 * in-process sockets that implement the browser's WebSocket surface:
 * subprotocol negotiation with the `bearer.` token entry, one message per
 * frame, close codes 4400/4401/4403/4404, and a drop (1006) when the
 * simulated network goes away.
 *
 * A failed WebSocket upgrade is indistinguishable from a network drop in a
 * browser (both surface as close 1006), so the mock accepts the upgrade and
 * closes with the documented stream code instead; the PR suggests the
 * gateway do the same.
 */

import type { WebSocketLike } from "../ws/socket";
import { decodeRequest, encodeResponse, fromBase64, toBase64, type Subprotocol } from "../ws/syncCodec";
import { MockAgent } from "./agent";
import { MockError, type MockServer } from "./server";

type Listener = { onopen: WebSocketLike["onopen"]; onclose: WebSocketLike["onclose"]; onmessage: WebSocketLike["onmessage"] };

/** One connection: the client-facing WebSocket and the server's handle on it. */
export class MockSocket implements WebSocketLike {
  readyState = 0;
  binaryType: BinaryType = "blob";
  onopen: WebSocketLike["onopen"] = null;
  onclose: WebSocketLike["onclose"] = null;
  onerror: WebSocketLike["onerror"] = null;
  onmessage: WebSocketLike["onmessage"] = null;
  /** The subprotocol the server accepted. */
  protocol: Subprotocol = "skein.v1.json";
  serverReceive: (data: string | Uint8Array) => void = () => {};
  serverClosed: () => void = () => {};

  constructor(readonly url: string) {}

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (this.readyState !== 1) throw new DOMException("WebSocket is not open", "InvalidStateError");
    const frame = typeof data === "string" ? data : data instanceof Uint8Array ? data.slice() : new Uint8Array(data as ArrayBuffer);
    setTimeout(() => this.readyState === 1 && this.serverReceive(frame as string | Uint8Array), 0);
  }

  close(code = 1000, reason = ""): void {
    if (this.readyState >= 2) return;
    this.readyState = 3;
    this.serverClosed();
    // A client-initiated close still reports its own close event.
    setTimeout(() => (this as Listener).onclose?.({ code, reason } as CloseEvent), 0);
  }

  /** Server side: deliver a frame to the client. */
  deliver(data: string | Uint8Array): void {
    if (this.readyState !== 1) return;
    const payload = typeof data === "string" ? data : (data.slice().buffer as ArrayBuffer);
    setTimeout(() => this.readyState === 1 && this.onmessage?.({ data: payload } as MessageEvent), 0);
  }

  /** Server side: close the connection with a code. */
  serverClose(code: number, reason = ""): void {
    if (this.readyState >= 2) return;
    this.readyState = 3;
    this.serverClosed();
    setTimeout(() => this.onclose?.({ code, reason } as CloseEvent), 0);
  }

  open(): void {
    if (this.readyState !== 0) return; // closed while connecting: a browser never reports open then
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
}

type SyncConn = { socket: MockSocket; userId: string; noteId: string; replicaId: string | null; ghostTimer: ReturnType<typeof setInterval> | null };

export class MockSockets {
  readonly agent: MockAgent;
  private sync = new Set<SyncConn>();
  private live = new Map<MockSocket, string>();
  private all = new Set<{ socket: MockSocket; userId: string }>();

  constructor(
    private readonly server: MockServer,
    private readonly ghosts: boolean,
  ) {
    this.agent = new MockAgent(server);
    server.network.addEventListener("offline", () => {
      for (const c of this.all) c.socket.serverClose(1006, "");
      this.all.clear();
    });
  }

  create = (url: string, protocols: string[]): WebSocketLike => {
    const socket = new MockSocket(url);
    const offered = protocols.filter((p) => !p.startsWith("bearer."));
    socket.protocol = (offered.find((p) => p === "skein.v1.proto" || p === "skein.v1.json") as Subprotocol | undefined) ?? "skein.v1.json";
    const token = protocols.find((p) => p.startsWith("bearer."))?.slice("bearer.".length) ?? null;
    setTimeout(() => {
      if (this.server.network.simulatedOffline) {
        socket.serverClose(1006);
        return;
      }
      let userId: string;
      try {
        userId = this.server.authenticate(token);
      } catch {
        socket.open();
        socket.serverClose(4401, "unauthenticated");
        return;
      }
      const path = new URL(url, "http://mock.local").pathname;
      const entry = { socket, userId };
      this.all.add(entry);
      const prevClosed = socket.serverClosed;
      socket.serverClosed = () => {
        this.all.delete(entry);
        prevClosed();
      };
      socket.open();
      let m: RegExpExecArray | null;
      if ((m = /^\/v1\/notes\/([^/]+)\/sync$/.exec(path))) this.openSync(socket, userId, decodeURIComponent(m[1]!));
      else if ((m = /^\/v1\/chats\/([^/]+)\/messages$/.exec(path))) this.openAgent(socket, userId, decodeURIComponent(m[1]!));
      else if (path === "/v1/notifications/live") this.openLive(socket, userId);
      else socket.serverClose(4404, "no such stream");
    }, this.server.latencyMs);
    return socket;
  };

  private openSync(socket: MockSocket, userId: string, noteId: string): void {
    const conn: SyncConn = { socket, userId, noteId, replicaId: null, ghostTimer: null };
    this.sync.add(conn);
    socket.serverClosed = ((prev) => () => {
      this.sync.delete(conn);
      if (conn.ghostTimer) clearInterval(conn.ghostTimer);
      prev();
    })(socket.serverClosed);
    socket.serverReceive = (data) => void this.syncFrame(conn, data);
  }

  private async syncFrame(conn: SyncConn, data: string | Uint8Array): Promise<void> {
    const { socket } = conn;
    try {
      if (typeof data === "string" && conn.replicaId) {
        // The proposed `attrs` field travels as a JSON text frame (mock only).
        const ext = JSON.parse(data) as { attrs?: string };
        if (ext.attrs) {
          if (this.server.noteRole(conn.userId, conn.noteId) !== "NOTE_ROLE_EDITOR") return;
          const nd = await this.server.loadDoc(conn.noteId);
          const bytes = fromBase64(ext.attrs);
          nd.doc.applyRemoteAttrs(bytes);
          nd.attrBatches.push(bytes);
          for (const other of this.sync) if (other !== conn && other.noteId === conn.noteId) other.socket.deliver(data);
          return;
        }
      }
      const frame = decodeRequest(data, typeof data === "string" ? "skein.v1.json" : socket.protocol);
      if (!frame) return;
      if (!conn.replicaId) {
        if (frame.kind !== "hello" || frame.noteId !== conn.noteId) return socket.serverClose(4400, "first frame must be a hello for this note");
        try {
          this.server.readableNote(conn.userId, conn.noteId);
        } catch {
          return socket.serverClose(4404, "note not found");
        }
        const owner = this.server.replicaOwner.get(frame.replicaId);
        if (!owner || owner.userId !== conn.userId) return socket.serverClose(4403, "replica is not registered to you");
        conn.replicaId = frame.replicaId;
        const nd = await this.server.loadDoc(conn.noteId);
        const missing = nd.doc.opsSince(frame.version);
        if (missing.length > 4) socket.deliver(encodeResponse({ kind: "ops", ops: missing }, socket.protocol));
        for (const attrs of nd.attrBatches) socket.deliver(JSON.stringify({ attrs: toBase64(attrs) }));
        if (this.ghosts) this.startGhost(conn);
        return;
      }
      if (frame.kind === "ops") {
        const accepted = await this.server.acceptOps(conn.userId, conn.noteId, conn.replicaId, frame.ops, socket);
        if (!accepted) {
          const reason = this.server.revoked.has(`${conn.noteId}|${conn.userId}`) ? "access to the note was revoked" : "your role on this note does not allow editing";
          socket.deliver(encodeResponse({ kind: "rejected", ops: frame.ops, reason }, socket.protocol));
        }
      } else if (frame.kind === "presence") {
        for (const other of this.sync) {
          if (other !== conn && other.noteId === conn.noteId) other.socket.deliver(encodeResponse({ kind: "presence", presence: { ...frame.presence, userId: conn.userId, replicaId: conn.replicaId } }, other.socket.protocol));
        }
      }
    } catch (error) {
      if (error instanceof MockError && error.code === "PERMISSION_DENIED") socket.serverClose(4403, error.message);
      else socket.serverClose(4400, (error as Error).message.slice(0, 120));
    }
  }

  /** A collaborator whose cursor wanders through the note, so presence shows in mock mode. */
  private startGhost(conn: SyncConn): void {
    const note = this.server.notes.get(conn.noteId);
    if (!note) return;
    const other = this.server.memberships.find((m) => m.workspaceId === note.workspaceId && m.userId !== conn.userId && !m.removedAt && m.role !== "guest");
    if (!other) return;
    let step = 0;
    conn.ghostTimer = setInterval(async () => {
      const nd = await this.server.loadDoc(conn.noteId);
      const blocks = nd.doc.blocks().filter((b) => b.text.length > 0);
      const block = blocks[step % Math.max(1, blocks.length)];
      step += 1;
      if (!block) return;
      const anchor = nd.doc.anchorAt({ blockId: block.id, offset: Math.min(block.text.length, 8 + ((step * 13) % 40)) });
      conn.socket.deliver(encodeResponse({ kind: "presence", presence: { userId: other.userId, replicaId: "424242", anchor, head: anchor } }, conn.socket.protocol));
    }, 2500);
  }

  broadcastOps(noteId: string, ops: Uint8Array, from: unknown): void {
    for (const c of this.sync) if (c.noteId === noteId && c.socket !== from && c.replicaId) c.socket.deliver(encodeResponse({ kind: "ops", ops }, c.socket.protocol));
  }

  private openAgent(socket: MockSocket, userId: string, chatId: string): void {
    const chat = this.server.chats.find((c) => c.id === chatId && c.userId === userId);
    if (!chat) return socket.serverClose(4404, "chat not found");
    socket.serverReceive = (data) => {
      try {
        const frame = JSON.parse(typeof data === "string" ? data : new TextDecoder().decode(data)) as { text?: string; chatId?: string };
        if (frame.chatId && frame.chatId !== chatId) return socket.serverClose(4400, "chatId does not match the path");
        if (!frame.text?.trim()) return socket.serverClose(4400, "text is required");
        void this.agent.start(socket, userId, chat, frame.text);
      } catch {
        socket.serverClose(4400, "frame does not decode");
      }
    };
  }

  private openLive(socket: MockSocket, userId: string): void {
    this.live.set(socket, userId);
    socket.serverClosed = ((prev) => () => {
      this.live.delete(socket);
      prev();
    })(socket.serverClosed);
  }

  pushNotification(userId: string, notification: unknown): void {
    for (const [socket, uid] of this.live) if (uid === userId) socket.deliver(JSON.stringify({ notification }));
  }

  closeAll(code: number, reason: string): void {
    for (const c of [...this.all]) c.socket.serverClose(code, reason);
  }

  closeUser(userId: string, code: number, reason: string): void {
    for (const c of [...this.all]) if (c.userId === userId) c.socket.serverClose(code, reason);
  }
}
