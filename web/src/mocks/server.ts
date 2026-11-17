/**
 * Mock mode's fake gateway: every REST route in gen/openapi/skein.swagger.json
 * and the three WebSocket streams of docs/api/websocket.md, served in-process
 * from testdata/seed/small with the same JSON shapes, status codes, error
 * bodies (google.rpc.Status with typed details), pagination, idempotency
 * replay and rate-limit headers as the real gateway. The app talks to it
 * through the same ApiClient and ReconnectingSocket it uses live; only the
 * injected `fetch` and WebSocket factory differ.
 *
 * Notes are real CRDT documents (the WASM module) loaded from the seed's op
 * logs, so sync, history, diff and blame run on genuine operations. Graph,
 * search and ML answers come from src/mocks/graph.ts and text.ts, checked
 * against the seed's ground truth (oracle.test.ts).
 *
 * Routes not in the spec (marked "proposed") back UI the design has and the
 * API lacks; the PR lists them as backend changes.
 */

import { GrpcCode, type GrpcCodeName } from "../api/errors";
import { operations, type OperationId } from "../api/schema.gen";
import type { CrdtDocument, CrdtModule } from "../crdt/wasm";
import { fromBase64, toBase64, type VersionEntry } from "../ws/syncCodec";
import { adjacency, betweenness, bfs, communities, components, diameter, findCycle, pagerank, readingPath, readingPlan, shortestPath, stronglyConnected, topologicalOrder, type Graph } from "./graph";
import { composeMarkdown, splitFrames, type Seed, type SeedNote, type SeedSource } from "./seed";
import { cosine, diffWords, editDistance, embed, lloyd, miniBatch, objective, silhouette, tokenize } from "./text";
import { decodeBatch, idOf } from "./wire";
import { MockSockets } from "./sockets";

export class MockError extends Error {
  constructor(
    readonly code: GrpcCodeName,
    message: string,
    readonly details: Record<string, unknown>[] = [],
  ) {
    super(message);
  }
}

const HTTP_OF: Record<GrpcCodeName, number> = {
  OK: 200,
  CANCELLED: 499,
  UNKNOWN: 500,
  INVALID_ARGUMENT: 400,
  DEADLINE_EXCEEDED: 504,
  NOT_FOUND: 404,
  ALREADY_EXISTS: 409,
  PERMISSION_DENIED: 403,
  RESOURCE_EXHAUSTED: 429,
  FAILED_PRECONDITION: 400,
  ABORTED: 409,
  OUT_OF_RANGE: 400,
  UNIMPLEMENTED: 501,
  INTERNAL: 500,
  UNAVAILABLE: 503,
  DATA_LOSS: 500,
  UNAUTHENTICATED: 401,
};

export type NoteRoleName = "NOTE_ROLE_VIEWER" | "NOTE_ROLE_COMMENTER" | "NOTE_ROLE_EDITOR";
type WsRole = "owner" | "admin" | "member" | "guest";

/** Grouped edits for history: one version per author per burst of editing. */
export type Version = { vv: Map<string, bigint>; authors: string[]; at: string };
/**
 * What undo needs from a change group. Wire format v1 has no restore
 * operation, so the mock undoes a group's insertions (deleting them) and
 * redo puts that text back; undoing a deletion needs the restore operation
 * of ADR-0005, which the documents service implements.
 */
type ChangeGroup = { inserted: string[] };
type RedoEntry = { text: string; anchor: string };

/** A note's server-side state: its document and its history. */
export type NoteDoc = { doc: CrdtDocument; versions: Version[]; undo: Map<string, ChangeGroup[]>; redo: Map<string, RedoEntry[]>; attrBatches: Uint8Array[] };

const SERVER_REPLICA = "1";
const ACCESS_TTL_MS = 15 * 60_000;

const rid = (): string => crypto.randomUUID();
const page = <T,>(items: T[], query: URLSearchParams): { items: T[]; page: { nextPageToken?: string } } => {
  const size = Math.min(500, Number(query.get("page.pageSize") || 50) || 50);
  const token = query.get("page.pageToken");
  const start = token ? Number(atob(token)) : 0;
  if (!Number.isFinite(start) || start < 0) throw new MockError("INVALID_ARGUMENT", "bad page token", [{ "@type": "type.googleapis.com/google.rpc.BadRequest", fieldViolations: [{ field: "page.pageToken", description: "not a token this server issued" }] }]);
  const end = start + size;
  return { items: items.slice(start, end), page: end < items.length ? { nextPageToken: btoa(String(end)) } : {} };
};
const vvEntries = (vv: Map<string, bigint>): VersionEntry[] => [...vv].sort((a, b) => (BigInt(a[0]) < BigInt(b[0]) ? -1 : 1)).map(([replicaId, c]) => ({ replicaId, counter: c.toString() }));
const vvOf = (entries: { replicaId?: string; counter?: string }[] | undefined) => new Map((entries ?? []).map((e) => [e.replicaId ?? "0", BigInt(e.counter ?? "0")]));
const badField = (field: string, description: string) => new MockError("INVALID_ARGUMENT", `${field} ${description}`, [{ "@type": "type.googleapis.com/google.rpc.BadRequest", fieldViolations: [{ field, description }] }]);

type Ctx = { userId: string | null; query: URLSearchParams; body: Record<string, unknown>; params: Record<string, string> };
type Handler = (ctx: Ctx) => Promise<unknown> | unknown;

export type MockOptions = { source: SeedSource; crdt: CrdtModule; latencyMs?: number; ghosts?: boolean; now?: () => number };

/** Network conditions mock mode can simulate; the app listens to it like `window`. */
export class SimulatedNetwork extends EventTarget {
  private offline = false;
  get onLine(): boolean {
    return !this.offline && (typeof navigator === "undefined" || navigator.onLine !== false);
  }
  setOffline(offline: boolean): void {
    if (this.offline === offline) return;
    this.offline = offline;
    this.dispatchEvent(new Event(offline ? "offline" : "online"));
  }
  get simulatedOffline(): boolean {
    return this.offline;
  }
}

export class MockServer {
  readonly network = new SimulatedNetwork();
  readonly sockets: MockSockets;
  latencyMs: number;
  readonly now: () => number;

  users = new Map<string, Seed["users"][number]>();
  memberships: Seed["memberships"] = [];
  notes = new Map<string, SeedNote>();
  acl = new Map<string, NoteRoleName>();
  replicaOwner = new Map<string, { userId: string; agentRunId: string | null }>();
  revoked = new Set<string>();
  docs = new Map<string, Promise<NoteDoc>>();
  private access = new Map<string, { userId: string; exp: number }>();
  /** Tokens expireTokens() ended; parsed tokens are checked against it too. */
  private expiredBefore = new Set<string>();
  private refreshTokens = new Map<string, { userId: string; family: string; rotated: boolean }>();
  private revokedFamilies = new Set<string>();
  private idempotency = new Map<string, { fingerprint: string; status: number; body: string }>();
  private budget = new Map<string, { used: number; windowStart: number }>();
  notifications: (Seed["notifications"][number] & { pushed?: boolean })[] = [];
  preferences = new Map<string, Map<string, { inApp: boolean; email: boolean }>>();
  comments: Seed["comments"] = [];
  chats: (Seed["chats"][number] & { runs: string[] })[] = [];
  shareLinks: (Seed["shareLinks"][number] & { token?: string })[] = [];
  private routes: { op: OperationId | string; method: string; pattern: RegExp; names: string[]; handler: Handler }[] = [];
  private vectorCache = new Map<string, number[]>();
  readonly stopwords: Set<string>;

  private constructor(
    readonly seed: Seed,
    private readonly source: SeedSource,
    readonly crdt: CrdtModule,
    options: MockOptions,
  ) {
    this.latencyMs = options.latencyMs ?? 0;
    this.now = options.now ?? (() => Date.now());
    this.stopwords = new Set(seed.stopwords);
    for (const u of seed.users) this.users.set(u.id, u);
    this.memberships = seed.memberships.map((m) => ({ ...m }));
    for (const n of seed.notes) this.notes.set(n.id, { ...n });
    for (const a of seed.acl) this.acl.set(`${a.noteId}|${a.userId}`, `NOTE_ROLE_${a.role.toUpperCase()}` as NoteRoleName);
    for (const r of seed.replicas) this.replicaOwner.set(r.replicaId, { userId: r.userId, agentRunId: r.agentRunId });
    this.notifications = seed.notifications.map((n) => ({ ...n }));
    this.comments = seed.comments.map((c) => ({ ...c }));
    this.chats = seed.chats.map((c) => ({ ...c, runs: [] }));
    this.shareLinks = seed.shareLinks.map((s) => ({ ...s }));
    this.sockets = new MockSockets(this, options.ghosts ?? false);
    this.registerRoutes();
  }

  static async create(options: MockOptions): Promise<MockServer> {
    return new MockServer(await options.source.seed(), options.source, options.crdt, options);
  }

  // ── simulation controls ────────────────────────────────────────────────────

  /** Makes every access token expire, so the next call exercises refresh. */
  expireTokens(): void {
    for (const [token, t] of this.access) {
      t.exp = 0;
      this.expiredBefore.add(token);
    }
    this.sockets.closeAll(4401, "token expired");
  }

  /** Lowers a user to viewer on a note, as if an admin did while they were offline. */
  revokeEdit(noteId: string, userId: string): void {
    this.revoked.add(`${noteId}|${userId}`);
  }

  // ── identity helpers ────────────────────────────────────────────────────────

  /**
   * Mock tokens name their user and expiry in the clear, so a page reload
   * (which starts a fresh mock gateway) keeps the session. They are not
   * signed: this is a fake for local use, never a credential.
   */
  issueSession(userId: string, family = rid()) {
    const exp = this.now() + ACCESS_TTL_MS;
    const accessToken = `mock.${userId}.${exp}.${rid().slice(0, 8)}`;
    const refreshToken = `mockr.${userId}.${family}.${rid().slice(0, 8)}`;
    this.access.set(accessToken, { userId, exp });
    this.refreshTokens.set(refreshToken, { userId, family, rotated: false });
    return {
      accessToken,
      accessExpiresAt: new Date(this.now() + ACCESS_TTL_MS).toISOString(),
      refreshToken,
      refreshExpiresAt: new Date(this.now() + 30 * 86_400_000).toISOString(),
    };
  }

  /** The user behind an access token, or a typed UNAUTHENTICATED error. */
  authenticate(token: string | null): string {
    let session = token ? this.access.get(token) : undefined;
    const parsed = !session && token ? /^mock\.([0-9a-f-]{36})\.(\d+)\.\w+$/.exec(token) : null;
    if (parsed && this.users.has(parsed[1]!) && !this.expiredBefore.has(token!)) session = { userId: parsed[1]!, exp: Number(parsed[2]) };
    const errorInfo = (reason: string) => [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason, domain: "skein" }];
    if (!session) throw new MockError("UNAUTHENTICATED", "missing or invalid access token", errorInfo("TOKEN_INVALID"));
    if (session.exp <= this.now()) throw new MockError("UNAUTHENTICATED", "access token expired", errorInfo("TOKEN_EXPIRED"));
    return session.userId;
  }

  userJson(id: string) {
    const u = this.users.get(id);
    return u ? { id: u.id, email: u.email, displayName: u.displayName, createdAt: u.createdAt } : { id };
  }

  wsRole(userId: string, workspaceId: string): WsRole | null {
    const m = this.memberships.find((x) => x.workspaceId === workspaceId && x.userId === userId && !x.removedAt);
    return m ? m.role : null;
  }

  noteRole(userId: string, noteId: string): NoteRoleName | null {
    const note = this.notes.get(noteId);
    if (!note) return null;
    const ws = this.wsRole(userId, note.workspaceId);
    if (!ws) return null;
    const override = this.acl.get(`${noteId}|${userId}`);
    let role: NoteRoleName | null = override ?? (ws === "guest" ? null : "NOTE_ROLE_EDITOR");
    if (role && this.revoked.has(`${noteId}|${userId}`)) role = "NOTE_ROLE_VIEWER";
    return role;
  }

  readableNote(userId: string, noteId: string, allowTrashed = false): SeedNote {
    const note = this.notes.get(noteId);
    if (!note || !this.noteRole(userId, noteId) || (note.trashed && !allowTrashed)) throw new MockError("NOT_FOUND", "note not found", [{ "@type": "type.googleapis.com/google.rpc.ResourceInfo", resourceType: "note", resourceName: noteId }]);
    return note;
  }

  requireNoteRole(userId: string, noteId: string, need: "edit" | "share" | "delete" | "comment"): SeedNote {
    const note = this.readableNote(userId, noteId, need === "delete");
    const role = this.noteRole(userId, noteId);
    const ws = this.wsRole(userId, note.workspaceId);
    const allowed =
      need === "delete" ? ws === "owner" || ws === "admin" || note.createdBy === userId : need === "comment" ? role !== "NOTE_ROLE_VIEWER" : role === "NOTE_ROLE_EDITOR";
    if (!allowed) throw new MockError("PERMISSION_DENIED", `not allowed to ${need} this note`, [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "ACTION_NOT_ALLOWED", domain: "skein" }]);
    return note;
  }

  requireMember(userId: string, workspaceId: string): WsRole {
    const role = this.wsRole(userId, workspaceId);
    if (!role) throw new MockError("NOT_FOUND", "workspace not found", [{ "@type": "type.googleapis.com/google.rpc.ResourceInfo", resourceType: "workspace", resourceName: workspaceId }]);
    return role;
  }

  readableNotes(userId: string, workspaceId: string): SeedNote[] {
    return [...this.notes.values()].filter((n) => n.workspaceId === workspaceId && !n.trashed && this.noteRole(userId, n.id)).sort((a, b) => (a.id < b.id ? -1 : 1));
  }

  noteJson(n: SeedNote, userId?: string) {
    return {
      id: n.id,
      workspaceId: n.workspaceId,
      title: n.title,
      createdBy: n.createdBy,
      createdAt: n.createdAt,
      updatedAt: n.updatedAt,
      trashed: n.trashed || undefined,
      // proposed: tags and the caller's role, which Note and GetNoteResponse do not carry yet
      tags: n.tags,
      ...(userId ? { callerRole: this.noteRole(userId, n.id) ?? undefined } : {}),
    };
  }

  // ── documents ──────────────────────────────────────────────────────────────

  loadDoc(noteId: string): Promise<NoteDoc> {
    let loading = this.docs.get(noteId);
    if (!loading) {
      loading = this.buildDoc(noteId);
      this.docs.set(noteId, loading);
    }
    return loading;
  }

  private async buildDoc(noteId: string): Promise<NoteDoc> {
    const doc = this.crdt.open(SERVER_REPLICA);
    const versions: Version[] = [];
    const batches = this.seed.batches.filter((b) => b.noteId === noteId).sort((a, b) => a.seq - b.seq);
    // Only seed notes with edits have an op log; asking for any other would be a 404.
    const log = batches.length > 0 ? await this.source.ops(noteId) : null;
    const vv = new Map<string, bigint>();
    if (log) {
      const frames = splitFrames(log);
      frames.forEach((frame, i) => {
        doc.applyRemote(frame);
        const b = batches[i];
        for (const op of decodeBatch(frame)) {
          const r = op.replica.toString();
          if ((vv.get(r) ?? 0n) < op.counter) vv.set(r, op.counter);
        }
        if (b) versions.push({ vv: new Map(vv), authors: [b.userId], at: b.deliveredAt });
      });
    }
    // Block attributes travel in blocks.jsonl, not in the op logs (docs/seed-data.md). They are
    // applied as baseline writes, like the browser's, so every version shows them (MarkdownAt).
    const seedBlocks = this.seed.blocks.filter((x) => x.noteId === noteId);
    if (seedBlocks.length > 0) doc.seedAttrsFromMarkdown(composeMarkdown(seedBlocks));
    return { doc, versions, undo: new Map(), redo: new Map(), attrBatches: [] };
  }

  /** Applies a client batch: permission, replica ownership, history and broadcast. Returns null when refused. */
  async acceptOps(userId: string, noteId: string, replicaId: string, ops: Uint8Array, from: unknown): Promise<VersionEntry[] | null> {
    const role = this.noteRole(userId, noteId);
    if (role !== "NOTE_ROLE_EDITOR") return null;
    const owner = this.replicaOwner.get(replicaId);
    if (!owner || owner.userId !== userId) throw new MockError("PERMISSION_DENIED", "replica is not registered to the caller");
    const nd = await this.loadDoc(noteId);
    const decoded = decodeBatch(ops);
    const group: ChangeGroup = { inserted: decoded.filter((op) => op.kind === "insert").map(idOf) };
    nd.doc.applyRemote(ops);
    this.recordVersion(nd, userId, owner.agentRunId);
    if (group.inserted.length > 0 && !owner.agentRunId) {
      const stack = nd.undo.get(replicaId) ?? [];
      stack.push(group);
      nd.undo.set(replicaId, stack);
      nd.redo.delete(replicaId);
    }
    const note = this.notes.get(noteId)!;
    note.updatedAt = new Date(this.now()).toISOString();
    this.sockets.broadcastOps(noteId, ops, from);
    return nd.doc.version();
  }

  recordVersion(nd: NoteDoc, userId: string, agentRunId: string | null): void {
    const vv = vvOf(nd.doc.version());
    const last = nd.versions[nd.versions.length - 1];
    const at = new Date(this.now()).toISOString();
    const author = agentRunId ? `${userId}#agent` : userId;
    // One version per author per minute of editing, as ListVersions groups them.
    if (last && last.authors.length === 1 && last.authors[0] === author && this.now() - Date.parse(last.at) < 60_000) {
      last.vv = vv;
      last.at = at;
    } else nd.versions.push({ vv, authors: [author], at });
  }

  // ── HTTP ───────────────────────────────────────────────────────────────────

  fetch = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    if (this.network.simulatedOffline) throw new TypeError("Failed to fetch (simulated offline)");
    if (this.latencyMs > 0) await new Promise((r) => setTimeout(r, this.latencyMs));
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, "http://mock.local");
    const method = (init.method ?? "GET").toUpperCase();
    const headers = new Headers(init.headers);
    const requestId = headers.get("X-Request-Id") ?? rid();
    const respond = (status: number, body: string, extra: Record<string, string> = {}) =>
      new Response(body, { status, headers: { "Content-Type": "application/json", "X-Request-Id": requestId, ...this.rateHeaders(url.pathname), ...extra } });
    try {
      const route = this.routes.find((r) => r.method === method && r.pattern.test(url.pathname));
      if (!route) throw new MockError("NOT_FOUND", `no route for ${method} ${url.pathname}`);
      const match = route.pattern.exec(url.pathname)!;
      const params = Object.fromEntries(route.names.map((n, i) => [n, decodeURIComponent(match[i + 1]!)]));
      const bodyText = typeof init.body === "string" ? init.body : "";
      const body = bodyText ? (JSON.parse(bodyText) as Record<string, unknown>) : {};
      const anonymous = ["IdentityService_SignUp", "IdentityService_SignIn", "IdentityService_SignInWithOAuth", "IdentityService_Refresh", "IdentityService_SignOut"].includes(route.op);
      const token = headers.get("Authorization")?.replace(/^Bearer /, "") ?? null;
      const userId = anonymous ? null : this.authenticate(token);
      this.spend(userId ?? "anon", url.pathname);
      const key = headers.get("Idempotency-Key");
      const fingerprint = `${method} ${url.pathname} ${bodyText}`;
      if (method === "POST" && key && userId) {
        const prior = this.idempotency.get(`${userId}|${key}`);
        if (prior) {
          if (prior.fingerprint !== fingerprint) throw badField("Idempotency-Key", "was used with a different request");
          return respond(prior.status, prior.body, { "Idempotent-Replayed": "true" });
        }
      }
      const result = await route.handler({ userId, query: url.searchParams, body, params });
      const text = JSON.stringify(result ?? {});
      if (method === "POST" && key && userId) this.idempotency.set(`${userId}|${key}`, { fingerprint, status: 200, body: text });
      return respond(200, text);
    } catch (error) {
      const e = error instanceof MockError ? error : new MockError("INTERNAL", (error as Error).message);
      const details = [...e.details, { "@type": "type.googleapis.com/google.rpc.RequestInfo", requestId }];
      const extra: Record<string, string> = e.code === "RESOURCE_EXHAUSTED" ? { "Retry-After": "3" } : {};
      return respond(HTTP_OF[e.code], JSON.stringify({ code: GrpcCode[e.code], message: e.message, details }), extra);
    }
  };

  private budgetFor(path: string): [string, number] {
    if (/\/search|semantic-search|title-suggestions/.test(path)) return ["search", 240];
    if (/clusters:benchmark|\/chats|\/runs/.test(path)) return ["agent", 60];
    return ["general", 1200];
  }

  private spend(who: string, path: string): void {
    const [name, limit] = this.budgetFor(path);
    const key = `${who}|${name}`;
    const b = this.budget.get(key) ?? { used: 0, windowStart: this.now() };
    if (this.now() - b.windowStart > 60_000) Object.assign(b, { used: 0, windowStart: this.now() });
    b.used++;
    this.budget.set(key, b);
    if (b.used > limit) throw new MockError("RESOURCE_EXHAUSTED", `${name} budget exhausted`, [{ "@type": "type.googleapis.com/google.rpc.QuotaFailure", violations: [{ subject: name }] }, { "@type": "type.googleapis.com/google.rpc.RetryInfo", retryDelay: "3s" }]);
  }

  private rateHeaders(path: string): Record<string, string> {
    const [name, limit] = this.budgetFor(path);
    const used = [...this.budget].filter(([k]) => k.endsWith(`|${name}`)).reduce((s, [, b]) => Math.max(s, b.used), 0);
    return { "RateLimit-Limit": String(limit), "RateLimit-Remaining": String(Math.max(0, limit - used)), "RateLimit-Reset": "60" };
  }

  private on(op: OperationId, handler: Handler): void {
    const spec = operations[op];
    this.addRoute(op, spec.method, spec.path, handler);
  }

  private addRoute(op: string, method: string, path: string, handler: Handler): void {
    const names: string[] = [];
    const pattern = new RegExp(`^${path.replace(/[.*+?^$()|[\]\\]/g, "\\$&").replace(/\{(\w+)\}/g, (_, n: string) => (names.push(n), "([^/:]+)"))}$`);
    this.routes.push({ op, method, pattern, names, handler });
  }

  // ── routes ─────────────────────────────────────────────────────────────────

  private registerRoutes(): void {
    const me = (c: Ctx) => c.userId!;
    const str = (c: Ctx, k: string) => (typeof c.body[k] === "string" ? (c.body[k] as string) : "");

    // identity
    this.on("IdentityService_SignUp", (c) => {
      const email = str(c, "email").toLowerCase();
      if (!email.includes("@")) throw badField("email", "is not an email address");
      if (str(c, "password").length < 8) throw badField("password", "must be at least 8 characters");
      if ([...this.users.values()].some((u) => u.email === email)) throw new MockError("ALREADY_EXISTS", "email already registered", [{ "@type": "type.googleapis.com/google.rpc.ResourceInfo", resourceType: "user", resourceName: email }]);
      const user = { id: rid(), email, displayName: str(c, "displayName") || email.split("@")[0]!, handle: email.split("@")[0]!, createdAt: new Date(this.now()).toISOString(), cast: null };
      this.users.set(user.id, user);
      return { user: this.userJson(user.id), session: this.issueSession(user.id) };
    });
    this.on("IdentityService_SignIn", (c) => {
      const user = [...this.users.values()].find((u) => u.email === str(c, "email").toLowerCase());
      // Mock mode accepts any non-empty password for a seed user; the message is the same either way.
      if (!user || !str(c, "password")) throw new MockError("UNAUTHENTICATED", "invalid email or password", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "INVALID_CREDENTIALS", domain: "skein" }]);
      return { user: this.userJson(user.id), session: this.issueSession(user.id) };
    });
    this.on("IdentityService_SignInWithOAuth", (c) => {
      if (!str(c, "code") || !str(c, "codeVerifier")) throw badField("code", "is required");
      const owner = this.seed.users.find((u) => u.cast === "owner") ?? this.seed.users[0]!;
      return { user: this.userJson(owner.id), session: this.issueSession(owner.id), created: false };
    });
    this.on("IdentityService_Refresh", (c) => {
      const presented = str(c, "refreshToken");
      const parsed = /^mockr\.([0-9a-f-]{36})\.([0-9a-f-]{36})\.\w+$/.exec(presented);
      if (!this.refreshTokens.has(presented) && parsed && this.users.has(parsed[1]!)) this.refreshTokens.set(presented, { userId: parsed[1]!, family: parsed[2]!, rotated: false });
      const t = this.refreshTokens.get(presented);
      if (!t || this.revokedFamilies.has(t.family)) throw new MockError("UNAUTHENTICATED", "invalid refresh token", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "TOKEN_INVALID" }]);
      if (t.rotated) {
        // A rotated token presented again is treated as theft (identity.md).
        this.revokedFamilies.add(t.family);
        throw new MockError("UNAUTHENTICATED", "refresh token reused; session revoked", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "TOKEN_INVALID" }]);
      }
      t.rotated = true;
      return { session: this.issueSession(t.userId, t.family) };
    });
    this.on("IdentityService_SignOut", (c) => {
      const t = this.refreshTokens.get(str(c, "refreshToken"));
      if (t) this.revokedFamilies.add(t.family);
      return {};
    });
    this.on("IdentityService_GetUser", (c) => {
      const shares = this.memberships.some((m) => m.userId === c.params.userId && !m.removedAt && this.wsRole(me(c), m.workspaceId));
      if (!shares && c.params.userId !== me(c)) throw new MockError("NOT_FOUND", "user not found");
      return { user: this.userJson(c.params.userId!) };
    });
    this.on("IdentityService_CreateWorkspace", (c) => {
      const name = str(c, "name").trim();
      if (!name) throw badField("name", "must not be empty");
      const id = rid();
      this.seed.workspaces.push({ id, name, slug: name.toLowerCase().replace(/\W+/g, "-"), ownerId: me(c), createdAt: new Date(this.now()).toISOString() });
      this.memberships.push({ workspaceId: id, userId: me(c), role: "owner", joinedAt: new Date(this.now()).toISOString(), removedAt: null });
      return { workspaceId: id };
    });
    this.on("IdentityService_ListMembers", (c) => {
      this.requireMember(me(c), c.params.workspaceId!);
      const members = this.memberships
        .filter((m) => m.workspaceId === c.params.workspaceId && !m.removedAt)
        .sort((a, b) => ["owner", "admin", "member", "guest"].indexOf(a.role) - ["owner", "admin", "member", "guest"].indexOf(b.role) || a.joinedAt.localeCompare(b.joinedAt))
        .map((m) => ({ user: this.userJson(m.userId), role: `WORKSPACE_ROLE_${m.role.toUpperCase()}` }));
      const p = page(members, c.query);
      return { members: p.items, page: p.page };
    });
    this.on("IdentityService_SetWorkspaceRole", (c) => {
      const ws = c.params.workspaceId!;
      const mine = this.requireMember(me(c), ws);
      if (mine !== "owner" && mine !== "admin") throw new MockError("PERMISSION_DENIED", "only admins manage members", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "ACTION_NOT_ALLOWED" }]);
      const role = str(c, "role").replace("WORKSPACE_ROLE_", "").toLowerCase() as WsRole;
      if (!["owner", "admin", "member", "guest"].includes(role)) throw badField("role", "must be a workspace role");
      const target = this.memberships.find((m) => m.workspaceId === ws && m.userId === c.params.userId && !m.removedAt);
      if ((role === "owner" || target?.role === "owner") && mine !== "owner") throw new MockError("PERMISSION_DENIED", "only an owner can grant or remove owner", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "ACTION_NOT_ALLOWED" }]);
      if (target?.role === "owner" && role !== "owner" && this.memberships.filter((m) => m.workspaceId === ws && m.role === "owner" && !m.removedAt).length === 1)
        throw new MockError("FAILED_PRECONDITION", "a workspace keeps at least one owner", [{ "@type": "type.googleapis.com/google.rpc.PreconditionFailure", violations: [{ type: "LAST_OWNER", description: "A workspace must keep at least one owner." }] }]);
      if (!this.users.has(c.params.userId!)) throw new MockError("NOT_FOUND", "user not found");
      if (target) target.role = role;
      else this.memberships.push({ workspaceId: ws, userId: c.params.userId!, role, joinedAt: new Date(this.now()).toISOString(), removedAt: null });
      return {};
    });
    this.on("IdentityService_RemoveMember", (c) => {
      const ws = c.params.workspaceId!;
      const mine = this.requireMember(me(c), ws);
      if (mine !== "owner" && mine !== "admin") throw new MockError("PERMISSION_DENIED", "only admins manage members", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "ACTION_NOT_ALLOWED" }]);
      const target = this.memberships.find((m) => m.workspaceId === ws && m.userId === c.params.userId && !m.removedAt);
      if (!target) throw new MockError("NOT_FOUND", "member not found");
      if (target.role === "owner" && this.memberships.filter((m) => m.workspaceId === ws && m.role === "owner" && !m.removedAt).length === 1)
        throw new MockError("FAILED_PRECONDITION", "a workspace keeps at least one owner", [{ "@type": "type.googleapis.com/google.rpc.PreconditionFailure", violations: [{ type: "LAST_OWNER", description: "A workspace must keep at least one owner." }] }]);
      target.removedAt = new Date(this.now()).toISOString();
      this.sockets.closeUser(c.params.userId!, 4403, "removed from the workspace");
      return {};
    });
    this.on("IdentityService_SetNoteRole", (c) => {
      this.requireNoteRole(me(c), c.params.noteId!, "share");
      const role = str(c, "role");
      const key = `${c.params.noteId}|${c.params.userId}`;
      if (!role || role === "NOTE_ROLE_UNSPECIFIED") this.acl.delete(key);
      else this.acl.set(key, role as NoteRoleName);
      return {};
    });
    this.on("IdentityService_CreateShareLink", (c) => {
      this.requireNoteRole(me(c), c.params.noteId!, "share");
      const role = str(c, "role") || "NOTE_ROLE_VIEWER";
      if (role === "NOTE_ROLE_EDITOR") throw badField("role", "share links cannot grant editor");
      const id = rid();
      const token = `sl_${rid().replace(/-/g, "").slice(0, 16)}`;
      this.shareLinks.push({ id, noteId: c.params.noteId!, role: role.replace("NOTE_ROLE_", "").toLowerCase(), createdBy: me(c), createdAt: new Date(this.now()).toISOString(), expiresAt: str(c, "expiresAt") || null, revokedAt: null, token });
      return { linkId: id, token };
    });
    this.on("IdentityService_RevokeShareLink", (c) => {
      const link = this.shareLinks.find((l) => l.id === c.params.linkId);
      if (!link) throw new MockError("NOT_FOUND", "share link not found");
      this.requireNoteRole(me(c), link.noteId, "share");
      link.revokedAt = new Date(this.now()).toISOString();
      return {};
    });
    this.on("IdentityService_RegisterReplica", (c) => {
      const replicaId = str(c, "replicaId");
      if (!/^[1-9]\d*$/.test(replicaId)) throw badField("replicaId", "must be a non-zero uint64");
      const owner = this.replicaOwner.get(replicaId);
      if (owner && owner.userId !== me(c)) throw new MockError("ALREADY_EXISTS", "replica registered to another user", [{ "@type": "type.googleapis.com/google.rpc.ResourceInfo", resourceType: "replica", resourceName: replicaId }]);
      this.replicaOwner.set(replicaId, { userId: me(c), agentRunId: null });
      return {};
    });

    // documents
    this.on("DocumentService_CreateNote", (c) => {
      const ws = c.params.workspaceId!;
      const role = this.requireMember(me(c), ws);
      if (role === "guest") throw new MockError("PERMISSION_DENIED", "guests cannot create notes", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "ACTION_NOT_ALLOWED" }]);
      const title = str(c, "title").trim();
      if (!title) throw badField("title", "must not be empty");
      const at = new Date(this.now()).toISOString();
      const note: SeedNote = { id: rid(), workspaceId: ws, title, createdBy: me(c), createdAt: at, updatedAt: at, trashed: false, isRoot: false, charCount: 0, tags: [], topic: null };
      this.notes.set(note.id, note);
      const markdown = str(c, "markdown");
      if (markdown) {
        const nd = this.loadDoc(note.id);
        void nd.then(({ doc }) => {
          for (const line of markdown.split(/\n+/).filter(Boolean)) {
            const { newBlockId } = doc.appendBlock();
            doc.insertText(newBlockId, 0, line.replace(/^#+\s|^[-*]\s/, ""));
          }
        });
      }
      return { note: this.noteJson(note) };
    });
    this.on("DocumentService_GetNote", async (c) => {
      const note = this.readableNote(me(c), c.params.noteId!, true);
      const out: Record<string, unknown> = { note: this.noteJson(note, me(c)), callerRole: this.noteRole(me(c), note.id) };
      if (c.query.get("includeMarkdown") === "true") {
        const nd = await this.loadDoc(note.id);
        out.markdown = nd.doc.markdown();
        out.version = { entries: nd.doc.version() };
      }
      return out;
    });
    this.on("DocumentService_ListNotes", (c) => {
      this.requireMember(me(c), c.params.workspaceId!);
      const trashed = c.query.get("includeTrashed") === "true";
      const all = [...this.notes.values()]
        .filter((n) => n.workspaceId === c.params.workspaceId && (trashed || !n.trashed) && this.noteRole(me(c), n.id))
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || (a.id < b.id ? -1 : 1));
      const p = page(all, c.query);
      return { notes: p.items.map((n) => this.noteJson(n)), page: p.page };
    });
    this.on("DocumentService_DeleteNote", (c) => {
      const note = this.requireNoteRole(me(c), c.params.noteId!, "delete");
      note.trashed = true;
      return {};
    });
    this.on("DocumentService_RestoreNote", (c) => {
      const note = this.requireNoteRole(me(c), c.params.noteId!, "delete");
      note.trashed = false;
      return {};
    });
    this.on("DocumentService_RenameNote", (c) => {
      const note = this.requireNoteRole(me(c), c.params.noteId!, "edit");
      const title = str(c, "title").trim();
      if (!title) throw badField("title", "must not be empty");
      note.title = title;
      note.updatedAt = new Date(this.now()).toISOString();
      return {};
    });
    this.on("DocumentService_ApplyOps", async (c) => {
      this.requireNoteRole(me(c), c.params.noteId!, "edit");
      const version = await this.acceptOps(me(c), c.params.noteId!, str(c, "replicaId"), fromBase64(str(c, "ops")), null);
      return { version: { entries: version ?? [] } };
    });
    this.on("DocumentService_ListVersions", async (c) => {
      this.readableNote(me(c), c.params.noteId!);
      const nd = await this.loadDoc(c.params.noteId!);
      const all = [...nd.versions].reverse().map((v) => ({
        version: { entries: vvEntries(v.vv) },
        authorIds: v.authors.map((a) => a.replace("#agent", "")),
        at: v.at,
        // proposed: whether an agent run made the change
        agent: v.authors.some((a) => a.endsWith("#agent")) || undefined,
      }));
      const p = page(all, c.query);
      return { versions: p.items, page: p.page };
    });
    this.on("DocumentService_GetTextAt", async (c) => {
      this.readableNote(me(c), c.params.noteId!);
      const nd = await this.loadDoc(c.params.noteId!);
      return { markdown: nd.doc.markdownAt(vvEntries(vvOf((c.body.version as { entries?: VersionEntry[] } | undefined)?.entries))) };
    });
    this.on("DocumentService_Diff", async (c) => {
      this.readableNote(me(c), c.params.noteId!);
      const nd = await this.loadDoc(c.params.noteId!);
      const at = (v: unknown) => nd.doc.markdownAt(vvEntries(vvOf((v as { entries?: VersionEntry[] } | undefined)?.entries)));
      return { hunks: diffWords(at(c.body.from), at(c.body.to)) };
    });
    this.on("DocumentService_Blame", async (c) => {
      this.readableNote(me(c), c.params.noteId!);
      const nd = await this.loadDoc(c.params.noteId!);
      const { ids } = nd.doc.exportMapped();
      const spans: { start: number; end: number; userId: string; agentRunId?: string; at: string }[] = [];
      const whenOf = (replica: string, counter: bigint) => nd.versions.find((v) => (v.vv.get(replica) ?? 0n) >= counter)?.at ?? new Date(this.now()).toISOString();
      ids.forEach((id, i) => {
        const [replica, counter] = id.split(":");
        const owner = this.replicaOwner.get(replica!) ?? { userId: this.notes.get(c.params.noteId!)!.createdBy, agentRunId: null };
        const at = whenOf(replica!, BigInt(counter!));
        const last = spans[spans.length - 1];
        if (last && last.userId === owner.userId && last.agentRunId === (owner.agentRunId ?? undefined) && last.at === at) last.end = i + 1;
        else spans.push({ start: i, end: i + 1, userId: owner.userId, agentRunId: owner.agentRunId ?? undefined, at });
      });
      return { spans };
    });
    const undoRedo = (redo: boolean) => async (c: Ctx) => {
      this.requireNoteRole(me(c), c.params.noteId!, "edit");
      const nd = await this.loadDoc(c.params.noteId!);
      const replicaId = str(c, "replicaId");
      const empty = () => new MockError("FAILED_PRECONDITION", `nothing to ${redo ? "redo" : "undo"}`, [{ "@type": "type.googleapis.com/google.rpc.PreconditionFailure", violations: [{ type: "EMPTY_STACK", description: `Nothing to ${redo ? "redo" : "undo"}.` }] }]);
      let ops: Uint8Array;
      if (redo) {
        const entry = nd.redo.get(replicaId)?.pop();
        if (!entry) throw empty();
        const at = nd.doc.positionOf(entry.anchor);
        ops = nd.doc.insertAtCodePoint(at ? at.index + (at.visible ? 1 : 0) : 0, entry.text);
        nd.undo.set(replicaId, [...(nd.undo.get(replicaId) ?? []), { inserted: decodeBatch(ops).map(idOf) }]);
      } else {
        const group = nd.undo.get(replicaId)?.pop();
        if (!group) throw empty();
        const { ops: inverse, redo: entry } = this.undoInsertions(nd, group);
        ops = inverse;
        if (entry) nd.redo.set(replicaId, [...(nd.redo.get(replicaId) ?? []), entry]);
      }
      this.recordVersion(nd, me(c), null);
      this.sockets.broadcastOps(c.params.noteId!, ops, null);
      return { ops: toBase64(ops) };
    };
    this.on("DocumentService_Undo", undoRedo(false));
    this.on("DocumentService_Redo", undoRedo(true));

    // search
    this.on("SearchService_Search", (c) => this.search(me(c), c.params.workspaceId!, c.query));
    this.on("SearchService_SuggestTitles", (c) => {
      this.requireMember(me(c), c.params.workspaceId!);
      const prefix = (c.query.get("prefix") ?? "").toLowerCase();
      const limit = Number(c.query.get("limit") || 8);
      const suggestions = this.readableNotes(me(c), c.params.workspaceId!)
        .map((n) => ({ noteId: n.id, title: n.title, editDistance: editDistance(prefix, n.title.toLowerCase().slice(0, [...prefix].length), 3) }))
        .filter((s) => s.editDistance <= Math.max(1, Math.floor([...prefix].length / 3)))
        .sort((a, b) => a.editDistance - b.editDistance || a.title.localeCompare(b.title))
        .slice(0, limit);
      return { suggestions };
    });

    // graph
    this.on("GraphService_GetLinkGraph", (c) => {
      const g = this.graph(me(c), c.params.workspaceId!);
      return { nodes: g.nodes.map((id) => ({ noteId: id, title: this.notes.get(id)!.title, isRoot: g.roots.has(id) || undefined })), edges: g.edges.map(([from, to]) => ({ from, to })) };
    });
    this.on("GraphService_AnalyzeGraph", (c) => {
      const g = this.graph(me(c), c.params.workspaceId!);
      const comp = components(g);
      const orphan = [...new Set(comp.values())].filter((id) => ![...comp].some(([n, c2]) => c2 === id && g.roots.has(n)));
      const scc = stronglyConnected(g);
      const sizes: number[] = [];
      for (const c2 of scc.values()) sizes[c2] = (sizes[c2] ?? 0) + 1;
      const topo = topologicalOrder(g);
      const comm = communities(g);
      return {
        componentOf: Object.fromEntries(comp),
        orphanComponents: orphan,
        sccOf: Object.fromEntries(scc),
        sccSizes: sizes,
        cycle: findCycle(g),
        topologicalOrder: topo.order,
        unplaced: topo.unplaced,
        betweenness: Object.fromEntries(betweenness(g)),
        pagerank: Object.fromEntries(pagerank(g)),
        communityOf: Object.fromEntries(comm.communityOf),
        modularity: comm.modularity,
        diameter: diameter(g),
      };
    });
    this.on("GraphService_BlastRadius", (c) => {
      const note = this.readableNote(me(c), c.params.noteId!);
      const { out, inn } = adjacency(this.graph(me(c), note.workspaceId));
      const hops = bfs(note.id, c.query.get("direction") === "BLAST_DIRECTION_CITES" ? out : inn, Number(c.query.get("maxHops") || 0));
      const rings: number[] = [];
      for (const d of hops.values()) rings[d] = (rings[d] ?? 0) + 1;
      return { hops: Object.fromEntries(hops), ringSizes: rings };
    });
    this.on("GraphService_ShortestPath", (c) => {
      const from = this.readableNote(me(c), c.params.from!);
      this.readableNote(me(c), c.params.to!);
      const g = this.graph(me(c), from.workspaceId);
      const semantic = c.query.get("metric") === "PATH_METRIC_SEMANTIC";
      const found = shortestPath(g, c.params.from!, c.params.to!, semantic ? (a, b) => 1 - cosine(this.noteVector(a), this.noteVector(b)) : undefined);
      return found ? { found: true, path: found.path, cost: found.cost } : { found: false, path: [] };
    });
    this.on("GraphService_ReadingPath", (c) => {
      const note = this.readableNote(me(c), c.params.noteId!);
      return { steps: readingPath(this.graph(me(c), note.workspaceId), note.id, (id) => this.notes.get(id)?.title ?? id) };
    });
    this.on("GraphService_ReadingPlan", (c) => this.readingPlan(me(c), c.params.workspaceId!, c.query.get("topic") ?? "", Number(c.query.get("minutes") || 30)));

    // ml
    this.on("MLService_SemanticSearch", (c) => {
      const started = performance.now();
      const ws = c.params.workspaceId!;
      this.requireMember(me(c), ws);
      const q = embed(c.query.get("query") ?? "", this.stopwords);
      const tags = c.query.getAll("tags");
      const k = Number(c.query.get("k") || 10);
      const hits = this.readableNotes(me(c), ws)
        .filter((n) => tags.every((t) => n.tags.includes(t)))
        .flatMap((n) => this.blocksOf(n.id).map((b) => ({ location: { noteId: n.id, blockId: b.blockId }, cosine: cosine(q, this.blockVector(n.id, b.blockId, b.text)) })))
        .filter((h) => h.cosine > 0)
        .sort((a, b) => b.cosine - a.cosine)
        .slice(0, k);
      return { hits, recallAtK: c.query.get("measureRecall") === "true" ? 1 : undefined, latencyMs: Math.round((performance.now() - started) * 10) / 10 };
    });
    this.on("MLService_Discover", (c) => {
      const note = this.readableNote(me(c), c.params.noteId!);
      const k = Number(c.query.get("k") || 8);
      const g = this.graph(me(c), note.workspaceId);
      const hops = bfs(note.id, adjacency(g).undirected);
      const mine = this.noteVector(note.id);
      const related = this.readableNotes(me(c), note.workspaceId)
        .filter((n) => n.id !== note.id)
        .map((n) => {
          const shared = n.tags.filter((t) => note.tags.includes(t)).length;
          const union = new Set([...n.tags, ...note.tags]).size;
          const cos = Math.round(cosine(mine, this.noteVector(n.id)) * 1000) / 1000;
          const h = hops.get(n.id) ?? -1;
          return { noteId: n.id, cosine: cos, sharedTags: shared || undefined, tagJaccard: union ? Math.round((shared / union) * 100) / 100 : undefined, hops: h, suggestLink: cos >= 0.55 && (h >= 2 || h < 0) ? true : undefined };
        })
        .sort((a, b) => b.cosine - a.cosine)
        .slice(0, k);
      return { related };
    });
    this.on("MLService_ListClusters", (c) => ({ clusters: this.clusters(me(c), c.params.workspaceId!) }));
    this.on("MLService_BenchmarkClustering", (c) => this.benchmark(me(c), c.params.workspaceId!, c.body));

    // agent
    this.on("AgentService_CreateChat", (c) => {
      const ws = c.params.workspaceId!;
      this.requireMember(me(c), ws);
      const noteId = str(c, "noteId") || null;
      if (noteId) this.readableNote(me(c), noteId);
      const chat = { id: rid(), workspaceId: ws, userId: me(c), noteId, title: noteId ? `About ${this.notes.get(noteId)!.title}` : "New chat", updatedAt: new Date(this.now()).toISOString(), messages: [], runs: [] };
      this.chats.push(chat);
      return { chatId: chat.id };
    });
    this.on("AgentService_ListChats", (c) => {
      this.requireMember(me(c), c.params.workspaceId!);
      const all = this.chats.filter((x) => x.workspaceId === c.params.workspaceId && x.userId === me(c)).sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
      const p = page(all, c.query);
      return { chats: p.items.map((x) => ({ chatId: x.id, title: x.title, updatedAt: x.updatedAt })), page: p.page };
    });
    this.on("AgentService_Confirm", (c) => this.sockets.agent.confirm(me(c), c.params.runId!, str(c, "confirmationId"), c.body.approved === true));
    this.on("AgentService_CancelRun", (c) => this.sockets.agent.cancel(me(c), c.params.runId!));
    this.on("AgentService_GetRun", (c) => this.sockets.agent.getRun(me(c), c.params.runId!));

    // notifications
    this.on("NotificationService_ListInbox", (c) => {
      const unreadOnly = c.query.get("unreadOnly") === "true";
      const mine = this.notifications.filter((n) => n.userId === me(c)).sort((a, b) => b.createdAt.localeCompare(a.createdAt));
      const p = page(unreadOnly ? mine.filter((n) => !n.read) : mine, c.query);
      return { notifications: p.items.map((n) => this.notificationJson(n)), page: p.page, unread: mine.filter((n) => !n.read).length };
    });
    this.on("NotificationService_MarkRead", (c) => {
      const ids = new Set((c.body.ids as string[] | undefined) ?? []);
      for (const n of this.notifications) if (n.userId === me(c) && ids.has(n.id)) n.read = true;
      return {};
    });
    const KINDS = ["NOTIFICATION_KIND_MENTION", "NOTIFICATION_KIND_COMMENT", "NOTIFICATION_KIND_SHARE", "NOTIFICATION_KIND_AGENT_RUN_FINISHED", "NOTIFICATION_KIND_ACCESS_CHANGED"];
    const prefsOf = (userId: string) => {
      let p = this.preferences.get(userId);
      if (!p) {
        p = new Map(KINDS.map((k) => [k, { inApp: true, email: k === "NOTIFICATION_KIND_MENTION" || k === "NOTIFICATION_KIND_SHARE" || k === "NOTIFICATION_KIND_ACCESS_CHANGED" }]));
        this.preferences.set(userId, p);
      }
      return p;
    };
    this.on("NotificationService_GetPreferences", (c) => ({ preferences: [...prefsOf(me(c))].map(([kind, v]) => ({ kind, inApp: v.inApp || undefined, email: v.email || undefined })) }));
    this.on("NotificationService_UpdatePreferences", (c) => {
      const list = (c.body.preferences as { kind: string; inApp?: boolean; email?: boolean }[] | undefined) ?? [];
      if (new Set(list.map((p) => p.kind)).size !== list.length) throw badField("preferences", "lists a kind twice");
      const p = prefsOf(me(c));
      for (const item of list) p.set(item.kind, { inApp: !!item.inApp, email: !!item.email });
      return {};
    });

    // proposed (not in the spec yet; see the PR)
    this.addRoute("proposed:ListWorkspaces", "GET", "/v1/workspaces", (c) => ({
      workspaces: this.seed.workspaces
        .filter((w) => this.wsRole(me(c), w.id))
        .map((w) => ({ id: w.id, name: w.name, role: `WORKSPACE_ROLE_${this.wsRole(me(c), w.id)!.toUpperCase()}`, notes: this.readableNotes(me(c), w.id).length })),
    }));
    this.addRoute("proposed:ListComments", "GET", "/v1/notes/{noteId}/comments", (c) => {
      this.readableNote(me(c), c.params.noteId!);
      return { comments: this.comments.filter((x) => x.noteId === c.params.noteId).sort((a, b) => a.createdAt.localeCompare(b.createdAt)) };
    });
    this.addRoute("proposed:CreateComment", "POST", "/v1/notes/{noteId}/comments", (c) => {
      this.requireNoteRole(me(c), c.params.noteId!, "comment");
      const comment = { id: rid(), noteId: c.params.noteId!, blockId: str(c, "blockId"), start: Number(c.body.start ?? 0), end: Number(c.body.end ?? 0), quote: str(c, "quote"), parentId: str(c, "parentId") || null, authorId: me(c), body: str(c, "body"), createdAt: new Date(this.now()).toISOString(), resolved: false };
      if (!comment.body.trim()) throw badField("body", "must not be empty");
      this.comments.push(comment);
      return { comment };
    });
    this.addRoute("proposed:ResolveComment", "POST", "/v1/comments/{commentId}:resolve", (c) => {
      const comment = this.comments.find((x) => x.id === c.params.commentId);
      if (!comment) throw new MockError("NOT_FOUND", "comment not found");
      this.requireNoteRole(me(c), comment.noteId, "comment");
      for (const x of this.comments) if (x.id === comment.id || x.parentId === comment.id) x.resolved = true;
      return {};
    });
    this.addRoute("proposed:ListNoteAccess", "GET", "/v1/notes/{noteId}/roles", (c) => {
      const note = this.readableNote(me(c), c.params.noteId!);
      return {
        access: this.memberships
          .filter((m) => m.workspaceId === note.workspaceId && !m.removedAt)
          .map((m) => ({ user: this.userJson(m.userId), workspaceRole: `WORKSPACE_ROLE_${m.role.toUpperCase()}`, noteRole: this.noteRole(m.userId, note.id), override: this.acl.has(`${note.id}|${m.userId}`), owner: note.createdBy === m.userId }))
          .filter((a) => a.noteRole),
      };
    });
    this.addRoute("proposed:ListShareLinks", "GET", "/v1/notes/{noteId}/share-links", (c) => {
      this.requireNoteRole(me(c), c.params.noteId!, "share");
      return { links: this.shareLinks.filter((l) => l.noteId === c.params.noteId && !l.revokedAt).map(({ token: _t, ...l }) => ({ ...l, role: `NOTE_ROLE_${l.role.toUpperCase()}` })) };
    });
  }

  notificationJson(n: Seed["notifications"][number]) {
    return {
      id: n.id,
      kind: n.kind,
      title: n.title,
      body: n.body || undefined,
      location: n.location ? { noteId: n.location.noteId, blockId: n.location.blockId || undefined, start: n.location.start || undefined, end: n.location.end || undefined } : undefined,
      actorId: n.actorId || undefined,
      createdAt: n.createdAt,
      read: n.read || undefined,
    };
  }

  /** Deletes a group's still-visible insertions, as one batch, and remembers their text for redo. */
  private undoInsertions(nd: NoteDoc, group: ChangeGroup): { ops: Uint8Array; redo: RedoEntry | null } {
    const visible = group.inserted
      .map((id) => ({ id, at: nd.doc.positionOf(id) }))
      .filter((x): x is { id: string; at: { index: number; visible: boolean } } => !!x.at && x.at.visible)
      .sort((a, b) => a.at.index - b.at.index);
    if (visible.length === 0) return { ops: nd.doc.deleteAtCodePoint(0, 0), redo: null };
    const chars = [...nd.doc.text()];
    const text = visible.map((x) => chars[x.at.index]!).join("");
    const first = visible[0]!.at.index;
    const anchor = first > 0 ? this.idBefore(nd, first) : "0:0";
    // Delete from the end so earlier positions stay valid; the batches merge into one.
    const batches = [...visible].reverse().map((x) => nd.doc.deleteAtCodePoint(x.at.index, 1));
    return { ops: mergeBatches(batches), redo: { text, anchor } };
  }

  /** The ID of the element just before code-point position `pos`, where redo re-inserts. */
  private idBefore(nd: NoteDoc, pos: number): string {
    const blocks = nd.doc.blocks();
    const block = [...blocks].reverse().find((b) => b.start <= pos);
    if (!block) return "0:0";
    return nd.doc.anchorAt({ blockId: block.id, offset: [...block.text].slice(0, pos - block.start).join("").length });
  }

  // ── content views ──────────────────────────────────────────────────────────

  /** A note's blocks: its live document once loaded, else the seed's blocks. */
  blocksOf(noteId: string): { blockId: string; type: string; language: string; text: string }[] {
    return this.seed.blocks.filter((b) => b.noteId === noteId).map((b) => ({ blockId: b.blockId, type: b.type, language: b.language, text: b.text }));
  }

  private blockVector(noteId: string, blockId: string, text: string): number[] {
    const key = `${noteId}/${blockId}`;
    let v = this.vectorCache.get(key);
    if (!v) {
      v = embed(text, this.stopwords);
      this.vectorCache.set(key, v);
    }
    return v;
  }

  noteVector(noteId: string): number[] {
    const key = `note/${noteId}`;
    let v = this.vectorCache.get(key);
    if (!v) {
      const note = this.notes.get(noteId);
      v = embed(`${note?.title ?? ""}\n${this.blocksOf(noteId).map((b) => b.text).join("\n")}`, this.stopwords);
      this.vectorCache.set(key, v);
    }
    return v;
  }

  graph(userId: string, workspaceId: string): Graph {
    this.requireMember(userId, workspaceId);
    const nodes = this.readableNotes(userId, workspaceId);
    const ids = new Set(nodes.map((n) => n.id));
    const seen = new Set<string>();
    const edges: [string, string][] = [];
    for (const l of this.seed.links) {
      if (l.workspaceId !== workspaceId || !ids.has(l.from) || !ids.has(l.to) || l.from === l.to || seen.has(`${l.from}>${l.to}`)) continue;
      seen.add(`${l.from}>${l.to}`);
      edges.push([l.from, l.to]);
    }
    return { nodes: [...ids], edges, roots: new Set(nodes.filter((n) => n.isRoot).map((n) => n.id)) };
  }

  private search(userId: string, workspaceId: string, q: URLSearchParams) {
    this.requireMember(userId, workspaceId);
    const query = q.get("query") ?? "";
    if (!query.trim()) throw badField("query", "must not be empty");
    const regexMode = q.get("mode") === "SEARCH_MODE_REGEX";
    const caseSensitive = q.get("caseSensitive") === "true";
    const wholeWord = q.get("wholeWord") === "true";
    const languages = q.getAll("languages").map((l) => l.toLowerCase());
    const codeOnly = q.get("codeOnly") === "true" || languages.length > 0;
    let pattern: RegExp;
    if (regexMode) {
      try {
        pattern = new RegExp(wholeWord ? `\\b(?:${query})\\b` : query, caseSensitive ? "gu" : "giu");
      } catch (e) {
        throw badField("query", `does not compile: ${(e as Error).message}`);
      }
    } else {
      const terms = tokenize(query);
      const escaped = terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
      pattern = new RegExp(escaped.map((t) => (wholeWord ? `\\b${t}\\b` : t)).join("|") || "$^", caseSensitive ? "gu" : "giu");
    }
    const terms = tokenize(query);
    let candidates = 0;
    const hits: unknown[] = [];
    for (const note of this.readableNotes(userId, workspaceId)) {
      for (const b of this.blocksOf(note.id)) {
        if (codeOnly && b.type !== "code") continue;
        if (languages.length > 0 && !languages.includes(b.language.toLowerCase())) continue;
        candidates++;
        if (!regexMode) {
          // AND semantics, typo-tolerant: each term equals a token or is one edit away (5+ letters).
          const tokens = new Set(tokenize(b.text));
          const ok = terms.every((t) => tokens.has(t) || (t.length >= 5 && [...tokens].some((x) => Math.abs(x.length - t.length) <= 1 && editDistance(x, t, 1) <= 1)));
          if (!ok) continue;
        }
        const matches = [...b.text.matchAll(pattern)].filter((m) => m[0].length > 0);
        if (matches.length === 0 && regexMode) continue;
        const first = matches[0];
        const firstAt = first?.index ?? 0;
        const lineStart = b.text.lastIndexOf("\n", firstAt) + 1;
        const windowStart = b.type === "code" ? lineStart : Math.max(0, firstAt - 60);
        let windowEnd = b.type === "code" ? b.text.indexOf("\n", firstAt + (first?.[0].length ?? 0)) : firstAt + 140;
        if (windowEnd < 0 || windowEnd > b.text.length) windowEnd = b.text.length;
        const snippet = b.text.slice(windowStart, windowEnd);
        const cp = (s: string) => [...s].length;
        const highlights = matches
          .filter((m) => m.index! >= windowStart && m.index! + m[0].length <= windowEnd)
          .map((m) => ({ start: cp(b.text.slice(windowStart, m.index!)), end: cp(b.text.slice(windowStart, m.index! + m[0].length)) }));
        hits.push({
          location: { noteId: note.id, blockId: b.blockId, start: cp(b.text.slice(0, firstAt)), end: cp(b.text.slice(0, firstAt + (first?.[0].length ?? 0))) },
          noteTitle: note.title,
          snippet,
          highlights,
          score: Math.round((matches.length + (terms.some((t) => note.title.toLowerCase().includes(t)) ? 2 : 0)) * 100) / 100,
          language: b.type === "code" ? b.language : undefined,
        });
      }
    }
    if (!regexMode) (hits as { score: number }[]).sort((a, b) => b.score - a.score);
    const p = page(hits, q);
    return { hits: p.items, page: p.page, candidates, matched: hits.length };
  }

  private readingPlan(userId: string, workspaceId: string, topic: string, minutes: number) {
    if (!topic.trim()) throw badField("topic", "must not be empty");
    if (minutes <= 0) throw badField("minutes", "must be positive");
    const g = this.graph(userId, workspaceId);
    const t = embed(topic, this.stopwords);
    const scored = this.readableNotes(userId, workspaceId)
      .map((n) => ({ n, value: Math.round(cosine(t, this.noteVector(n.id)) * 100) / 100 }))
      .filter((x) => x.value > 0)
      .sort((a, b) => b.value - a.value)
      .slice(0, 12);
    const ids = new Set(scored.map((x) => x.n.id));
    const minutesOf = (n: SeedNote) => Math.max(1, Math.round(n.charCount / 5 / 200));
    const plan = readingPlan(
      scored.map(({ n, value }) => ({ noteId: n.id, minutes: minutesOf(n), value, needs: g.edges.filter(([a, b]) => a === n.id && ids.has(b)).map(([, b]) => b) })),
      minutes,
    );
    const chosen = new Set(plan.chosen.map((c) => c.noteId));
    const best = plan.chosen.reduce((m, c) => (c.value > m ? c.value : m), 0);
    return {
      notes: plan.chosen.map((c) => {
        const needs = c.needs.filter((x) => chosen.has(x)).map((x) => this.notes.get(x)!.title);
        return { noteId: c.noteId, minutes: c.minutes, value: c.value, reason: c.value === best ? "most relevant to the topic" : needs.length ? `relevant; needs ${needs.slice(0, 2).join(" and ")} first` : "relevant to the topic" };
      }),
      totalMinutes: plan.totalMinutes,
      totalValue: Math.round(plan.totalValue * 100) / 100,
    };
  }

  private clusterInput(userId: string, workspaceId: string) {
    const notes = this.readableNotes(userId, workspaceId);
    return { notes, points: notes.map((n) => this.noteVector(n.id)) };
  }

  private clusters(userId: string, workspaceId: string) {
    this.requireMember(userId, workspaceId);
    const { notes, points } = this.clusterInput(userId, workspaceId);
    if (notes.length === 0) return [];
    const k = Math.min(6, notes.length);
    const run = miniBatch(points, k, 7, Math.min(64, notes.length), 2, true);
    const groups = Array.from({ length: k }, (_, i) => notes.filter((_, j) => run.assign[j] === i));
    const df = new Map<string, number>();
    const tokensOf = new Map(notes.map((n) => [n.id, new Set(tokenize(`${n.title} ${this.blocksOf(n.id).map((b) => b.text).join(" ")}`).filter((t) => t.length > 2 && !this.stopwords.has(t) && !/^\d+$/.test(t)))]));
    for (const set of tokensOf.values()) for (const t of set) df.set(t, (df.get(t) ?? 0) + 1);
    return groups
      .map((members, id) => {
        const tf = new Map<string, number>();
        for (const m of members) for (const t of tokensOf.get(m.id)!) tf.set(t, (tf.get(t) ?? 0) + 1);
        const topTerms = [...tf]
          .map(([t, c]) => [t, (c / Math.max(1, members.length)) * Math.log(notes.length / (df.get(t) ?? 1))] as const)
          .sort((a, b) => b[1] - a[1])
          .slice(0, 5)
          .map(([t]) => t);
        const topicCounts = new Map<string, number>();
        for (const m of members) if (m.topic) topicCounts.set(m.topic, (topicCounts.get(m.topic) ?? 0) + 1);
        const topTopic = [...topicCounts].sort((a, b) => b[1] - a[1])[0]?.[0];
        const label = this.seed.topics.find((t) => t.id === topTopic)?.name ?? (topTerms[0] ? topTerms[0][0]!.toUpperCase() + topTerms[0].slice(1) : `Cluster ${id + 1}`);
        return { id: id || undefined, label, topTerms, noteIds: members.map((m) => m.id) };
      })
      .filter((c) => c.noteIds.length > 0)
      .sort((a, b) => b.noteIds.length - a.noteIds.length);
  }

  private benchmark(userId: string, workspaceId: string, body: Record<string, unknown>) {
    this.requireMember(userId, workspaceId);
    const { points } = this.clusterInput(userId, workspaceId);
    // The mock implements the Lloyd family and the mini-batch variants; GMM is left to the ml service.
    const all = ["CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS", "CLUSTERING_ALGORITHM_KMEANS_ELKAN", "CLUSTERING_ALGORITHM_E_MEANS", "CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH", "CLUSTERING_ALGORITHM_KMEANS_RANDOM"];
    const algorithms = ((body.algorithms as string[] | undefined)?.length ? (body.algorithms as string[]) : all).filter((a) => all.includes(a));
    const k = Math.min(Number(body.k) || 6, Math.max(1, points.length));
    const runs = Math.min(Number(body.runs) || 1, 5);
    const seed = Number(body.seed || 7);
    const holdout = points.filter((_, i) => i % 10 === 0);
    const train = points.filter((_, i) => i % 10 !== 0);
    return {
      results: algorithms.map((algorithm) => {
        let seconds = 0;
        let last = lloyd(train, k, seed);
        for (let r = 0; r < runs; r++) {
          const t0 = performance.now();
          last =
            algorithm === "CLUSTERING_ALGORITHM_E_MEANS"
              ? miniBatch(train, k, seed + r, Math.min(64, train.length), 2.5, true)
              : algorithm === "CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH"
                ? miniBatch(train, k, seed + r, Math.min(64, train.length), 5, false)
                : lloyd(train, k, seed + r, algorithm !== "CLUSTERING_ALGORITHM_KMEANS_RANDOM");
          seconds += (performance.now() - t0) / 1000;
        }
        return {
          algorithm,
          implementation: "mock-ts",
          meanSeconds: Math.round((seconds / runs) * 1e5) / 1e5,
          meanIterations: last.iterations,
          objective: Math.round(objective(train, last.centroids) * 1e4) / 1e4,
          silhouette: Math.round(silhouette(train, last.assign) * 1e3) / 1e3,
          distanceComputations: String(last.distanceComputations),
          epochs: last.epochs,
          testObjective: Math.round(objective(holdout, last.centroids) * 1e4) / 1e4,
        };
      }),
    };
  }
}

/** Concatenates batches into one (header with the total count, then every op), as one change group. */
export function mergeBatches(batches: Uint8Array[]): Uint8Array {
  let count = 0;
  const bodies: number[] = [];
  for (const batch of batches) {
    count += decodeBatch(batch).length;
    let pos = 3;
    while (batch[pos]! & 0x80) pos++;
    for (let i = pos + 1; i < batch.length; i++) bodies.push(batch[i]!);
  }
  const header = [0x53, 0x4b, 1];
  let n = count;
  do {
    header.push((n & 0x7f) | (n > 0x7f ? 0x80 : 0));
    n = Math.floor(n / 128);
  } while (n > 0);
  return Uint8Array.from([...header, ...bodies]);
}
