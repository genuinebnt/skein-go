/**
 * SyncRequest and SyncResponse frames (proto/skein/v1/documents.proto) in
 * both subprotocols of docs/api/websocket.md: `skein.v1.proto` (binary
 * protobuf, which the sync client prefers because operations are bytes) and
 * `skein.v1.json` (proto3 JSON, for debugging).
 *
 * Five small messages do not justify a protobuf runtime, so the binary
 * encoding is written out here: varints (uint64 through BigInt, since
 * replica IDs exceed 2^53), length-delimited strings, bytes and submessages.
 * Unknown fields are skipped, as proto3 requires.
 *
 * Field numbers:
 *   SyncRequest  { hello=1 SyncHello, ops=2 bytes, presence=3 Presence }
 *   SyncHello    { note_id=1 string, replica_id=2 uint64, version=3 VersionVector }
 *   SyncResponse { ops=1 bytes, presence=2 Presence, rejected=3 SyncRejected }
 *   SyncRejected { ops=1 bytes, reason=2 string }
 *   Presence     { user_id=1 string, replica_id=2 uint64, anchor=3 string, head=4 string }
 *   VersionVector{ entries=1 VersionEntry }  VersionEntry { replica_id=1 uint64, counter=2 uint64 }
 */

export type VersionEntry = { replicaId: string; counter: string };
export type Presence = { userId: string; replicaId: string; anchor: string; head: string };

export type SyncRequest =
  | { kind: "hello"; noteId: string; replicaId: string; version: VersionEntry[] }
  | { kind: "ops"; ops: Uint8Array }
  | { kind: "presence"; presence: Presence };

export type SyncResponse =
  | { kind: "ops"; ops: Uint8Array }
  | { kind: "presence"; presence: Presence }
  | { kind: "rejected"; ops: Uint8Array; reason: string }
  | { kind: "unknown" };

export type Subprotocol = "skein.v1.proto" | "skein.v1.json";

export class CodecError extends Error {
  override readonly name = "CodecError";
}

// ── binary writer ────────────────────────────────────────────────────────────

class Writer {
  private bytes: number[] = [];

  varint(value: bigint): this {
    let v = BigInt.asUintN(64, value);
    while (v >= 0x80n) {
      this.bytes.push(Number(v & 0x7fn) | 0x80);
      v >>= 7n;
    }
    this.bytes.push(Number(v));
    return this;
  }

  tag(field: number, wireType: number): this {
    return this.varint(BigInt((field << 3) | wireType));
  }

  uint64(field: number, decimal: string): this {
    const v = BigInt(decimal);
    return v === 0n ? this : this.tag(field, 0).varint(v);
  }

  bytesField(field: number, data: Uint8Array, always = false): this {
    if (data.length === 0 && !always) return this;
    this.tag(field, 2).varint(BigInt(data.length));
    for (const b of data) this.bytes.push(b);
    return this;
  }

  string(field: number, value: string): this {
    return value ? this.bytesField(field, new TextEncoder().encode(value)) : this;
  }

  message(field: number, inner: Writer): this {
    return this.bytesField(field, inner.finish(), true);
  }

  finish(): Uint8Array {
    return Uint8Array.from(this.bytes);
  }
}

// ── binary reader ────────────────────────────────────────────────────────────

class Reader {
  private pos = 0;
  constructor(private readonly buf: Uint8Array) {}

  get done(): boolean {
    return this.pos >= this.buf.length;
  }

  varint(): bigint {
    let result = 0n;
    for (let shift = 0n; shift < 70n; shift += 7n) {
      if (this.pos >= this.buf.length) throw new CodecError("truncated varint");
      const b = this.buf[this.pos++]!;
      result |= BigInt(b & 0x7f) << shift;
      if ((b & 0x80) === 0) return BigInt.asUintN(64, result);
    }
    throw new CodecError("varint too long");
  }

  bytes(): Uint8Array {
    const length = Number(this.varint());
    if (this.pos + length > this.buf.length) throw new CodecError("truncated field");
    const out = this.buf.slice(this.pos, this.pos + length);
    this.pos += length;
    return out;
  }

  skip(wireType: number): void {
    switch (wireType) {
      case 0:
        this.varint();
        return;
      case 1:
        this.pos += 8;
        return;
      case 2:
        this.bytes();
        return;
      case 5:
        this.pos += 4;
        return;
      default:
        throw new CodecError(`unsupported wire type ${wireType}`);
    }
  }

  /** Calls `field` for each field; unknown fields must be skipped by the callback via `skip`. */
  each(field: (number: number, wireType: number) => void): void {
    while (!this.done) {
      const tag = Number(this.varint());
      field(tag >>> 3, tag & 7);
    }
    if (this.pos > this.buf.length) throw new CodecError("truncated message");
  }
}

const utf8 = new TextDecoder("utf-8", { fatal: false });

function encodeVersion(entries: VersionEntry[]): Writer {
  const w = new Writer();
  for (const e of entries) w.message(1, new Writer().uint64(1, e.replicaId).uint64(2, e.counter));
  return w;
}

function encodePresence(p: Presence): Writer {
  return new Writer().string(1, p.userId).uint64(2, p.replicaId).string(3, p.anchor).string(4, p.head);
}

function decodePresence(buf: Uint8Array): Presence {
  const p: Presence = { userId: "", replicaId: "0", anchor: "", head: "" };
  const r = new Reader(buf);
  r.each((field, wt) => {
    if (field === 1 && wt === 2) p.userId = utf8.decode(r.bytes());
    else if (field === 2 && wt === 0) p.replicaId = r.varint().toString();
    else if (field === 3 && wt === 2) p.anchor = utf8.decode(r.bytes());
    else if (field === 4 && wt === 2) p.head = utf8.decode(r.bytes());
    else r.skip(wt);
  });
  return p;
}

function decodeVersion(buf: Uint8Array): VersionEntry[] {
  const out: VersionEntry[] = [];
  const r = new Reader(buf);
  r.each((field, wt) => {
    if (field !== 1 || wt !== 2) return r.skip(wt);
    const entry: VersionEntry = { replicaId: "0", counter: "0" };
    const er = new Reader(r.bytes());
    er.each((f, w) => {
      if (f === 1 && w === 0) entry.replicaId = er.varint().toString();
      else if (f === 2 && w === 0) entry.counter = er.varint().toString();
      else er.skip(w);
    });
    out.push(entry);
  });
  return out;
}

// ── base64 for the JSON encoding ─────────────────────────────────────────────

export function toBase64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

export function fromBase64(text: string): Uint8Array {
  const s = atob(text);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

// ── public API ───────────────────────────────────────────────────────────────

export function encodeRequest(msg: SyncRequest, protocol: Subprotocol): string | Uint8Array {
  if (protocol === "skein.v1.json") return JSON.stringify(requestToJson(msg));
  const w = new Writer();
  switch (msg.kind) {
    case "hello":
      w.message(1, new Writer().string(1, msg.noteId).uint64(2, msg.replicaId).message(3, encodeVersion(msg.version)));
      break;
    case "ops":
      w.bytesField(2, msg.ops, true);
      break;
    case "presence":
      w.message(3, encodePresence(msg.presence));
      break;
  }
  return w.finish();
}

export function decodeResponse(data: string | ArrayBuffer | Uint8Array, protocol: Subprotocol): SyncResponse {
  if (protocol === "skein.v1.json") {
    if (typeof data !== "string") throw new CodecError("expected a text frame");
    return responseFromJson(JSON.parse(data) as Record<string, unknown>);
  }
  if (typeof data === "string") throw new CodecError("expected a binary frame");
  const r = new Reader(data instanceof Uint8Array ? data : new Uint8Array(data));
  let out: SyncResponse = { kind: "unknown" };
  r.each((field, wt) => {
    if (field === 1 && wt === 2) out = { kind: "ops", ops: r.bytes() };
    else if (field === 2 && wt === 2) out = { kind: "presence", presence: decodePresence(r.bytes()) };
    else if (field === 3 && wt === 2) {
      const rejected: { kind: "rejected"; ops: Uint8Array; reason: string } = { kind: "rejected", ops: new Uint8Array(), reason: "" };
      const rr = new Reader(r.bytes());
      rr.each((f, w) => {
        if (f === 1 && w === 2) rejected.ops = rr.bytes();
        else if (f === 2 && w === 2) rejected.reason = utf8.decode(rr.bytes());
        else rr.skip(w);
      });
      out = rejected;
    } else r.skip(wt);
  });
  return out;
}

/** The server side of the codec, used by the mock server and by tests. */
export function decodeRequest(data: string | ArrayBuffer | Uint8Array, protocol: Subprotocol): SyncRequest | null {
  if (protocol === "skein.v1.json") {
    const json = JSON.parse(typeof data === "string" ? data : utf8.decode(data)) as Record<string, unknown>;
    if (json.hello) {
      const h = json.hello as { noteId?: string; replicaId?: string; version?: { entries?: VersionEntry[] } };
      return { kind: "hello", noteId: h.noteId ?? "", replicaId: h.replicaId ?? "0", version: h.version?.entries ?? [] };
    }
    if (typeof json.ops === "string") return { kind: "ops", ops: fromBase64(json.ops) };
    if (json.presence) return { kind: "presence", presence: { userId: "", replicaId: "0", anchor: "", head: "", ...(json.presence as Partial<Presence>) } };
    return null;
  }
  if (typeof data === "string") throw new CodecError("expected a binary frame");
  const r = new Reader(data instanceof Uint8Array ? data : new Uint8Array(data));
  let out: SyncRequest | null = null;
  r.each((field, wt) => {
    if (field === 1 && wt === 2) {
      const hello = { kind: "hello" as const, noteId: "", replicaId: "0", version: [] as VersionEntry[] };
      const hr = new Reader(r.bytes());
      hr.each((f, w) => {
        if (f === 1 && w === 2) hello.noteId = utf8.decode(hr.bytes());
        else if (f === 2 && w === 0) hello.replicaId = hr.varint().toString();
        else if (f === 3 && w === 2) hello.version = decodeVersion(hr.bytes());
        else hr.skip(w);
      });
      out = hello;
    } else if (field === 2 && wt === 2) out = { kind: "ops", ops: r.bytes() };
    else if (field === 3 && wt === 2) out = { kind: "presence", presence: decodePresence(r.bytes()) };
    else r.skip(wt);
  });
  return out;
}

export function encodeResponse(msg: SyncResponse, protocol: Subprotocol): string | Uint8Array {
  if (protocol === "skein.v1.json") {
    switch (msg.kind) {
      case "ops":
        return JSON.stringify({ ops: toBase64(msg.ops) });
      case "presence":
        return JSON.stringify({ presence: msg.presence });
      case "rejected":
        return JSON.stringify({ rejected: { ops: toBase64(msg.ops), reason: msg.reason } });
      default:
        return "{}";
    }
  }
  const w = new Writer();
  if (msg.kind === "ops") w.bytesField(1, msg.ops, true);
  if (msg.kind === "presence") w.message(2, encodePresence(msg.presence));
  if (msg.kind === "rejected") w.message(3, new Writer().bytesField(1, msg.ops).string(2, msg.reason));
  return w.finish();
}

function requestToJson(msg: SyncRequest): Record<string, unknown> {
  switch (msg.kind) {
    case "hello":
      return { hello: { noteId: msg.noteId, replicaId: msg.replicaId, version: { entries: msg.version } } };
    case "ops":
      return { ops: toBase64(msg.ops) };
    case "presence":
      return { presence: msg.presence };
  }
}

function responseFromJson(json: Record<string, unknown>): SyncResponse {
  if (typeof json.ops === "string") return { kind: "ops", ops: fromBase64(json.ops) };
  if (json.presence) return { kind: "presence", presence: { userId: "", replicaId: "0", anchor: "", head: "", ...(json.presence as Partial<Presence>) } };
  if (json.rejected) {
    const r = json.rejected as { ops?: string; reason?: string };
    return { kind: "rejected", ops: r.ops ? fromBase64(r.ops) : new Uint8Array(), reason: r.reason ?? "" };
  }
  return { kind: "unknown" };
}
