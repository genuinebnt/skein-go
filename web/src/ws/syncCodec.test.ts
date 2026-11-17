import { describe, expect, it } from "vitest";
import { decodeRequest, decodeResponse, encodeRequest, encodeResponse, type SyncRequest, type SyncResponse } from "./syncCodec";

const hex = (b: Uint8Array | string) => (typeof b === "string" ? b : [...b].map((x) => x.toString(16).padStart(2, "0")).join(" "));

describe("sync frames, skein.v1.proto", () => {
  it("encodes a hello with exact protobuf bytes", () => {
    const frame = encodeRequest({ kind: "hello", noteId: "n", replicaId: "300", version: [{ replicaId: "1", counter: "2" }] }, "skein.v1.proto");
    // 0a len { 0a 01 'n' | 10 ac 02 | 1a 06 { 0a 04 { 08 01 10 02 } } }
    expect(hex(frame)).toBe("0a 0e 0a 01 6e 10 ac 02 1a 06 0a 04 08 01 10 02");
  });

  it("keeps 64-bit replica IDs exact past 2^53", () => {
    const big = "18446744073709551614";
    const frame = encodeRequest({ kind: "presence", presence: { userId: "u", replicaId: big, anchor: `${big}:3`, head: `${big}:3` } }, "skein.v1.proto");
    const back = decodeRequest(frame, "skein.v1.proto");
    expect(back).toEqual({ kind: "presence", presence: { userId: "u", replicaId: big, anchor: `${big}:3`, head: `${big}:3` } });
  });

  it.each<SyncResponse>([
    { kind: "ops", ops: Uint8Array.of(0x53, 0x4b, 1, 0) },
    { kind: "presence", presence: { userId: "u1", replicaId: "9120384756102938475", anchor: "1:2", head: "1:4" } },
    { kind: "rejected", ops: Uint8Array.of(1, 2, 3), reason: "access to the note was revoked" },
  ])("round-trips a %s response", (msg) => {
    expect(decodeResponse(encodeResponse(msg, "skein.v1.proto") as Uint8Array, "skein.v1.proto")).toEqual(msg);
  });

  it("skips fields it does not know, as proto3 requires", () => {
    // field 9 varint 5, then ops (field 1) = [7]
    const frame = Uint8Array.of(0x48, 0x05, 0x0a, 0x01, 0x07);
    expect(decodeResponse(frame, "skein.v1.proto")).toEqual({ kind: "ops", ops: Uint8Array.of(7) });
  });

  it("rejects a truncated frame instead of misreading it", () => {
    expect(() => decodeResponse(Uint8Array.of(0x0a, 0x05, 0x01), "skein.v1.proto")).toThrow(/truncated/);
  });
});

describe("sync frames, skein.v1.json", () => {
  it("writes the proto3 JSON the docs show", () => {
    const frame = encodeRequest({ kind: "hello", noteId: "n", replicaId: "42", version: [{ replicaId: "42", counter: "7" }] }, "skein.v1.json");
    expect(JSON.parse(frame as string)).toEqual({ hello: { noteId: "n", replicaId: "42", version: { entries: [{ replicaId: "42", counter: "7" }] } } });
  });

  it.each<SyncRequest>([
    { kind: "ops", ops: Uint8Array.of(1, 2, 255) },
    { kind: "presence", presence: { userId: "u", replicaId: "5", anchor: "5:1", head: "5:2" } },
  ])("round-trips a %s request", (msg) => {
    expect(decodeRequest(encodeRequest(msg, "skein.v1.json"), "skein.v1.json")).toEqual(msg);
  });

  it("decodes base64 operations in a rejected frame", () => {
    expect(decodeResponse('{"rejected":{"ops":"AQID","reason":"r"}}', "skein.v1.json")).toEqual({ kind: "rejected", ops: Uint8Array.of(1, 2, 3), reason: "r" });
  });
});
