/**
 * A reader for the CRDT wire format v1 (docs/spec/crdt-sequence.md section
 * 7), so the mock server can see which elements a batch inserted or
 * deleted: it needs that for undo and for counting a batch's operations.
 * The browser never needs this; the WASM module decodes batches itself.
 */

export type WireOp =
  | { kind: "insert"; replica: bigint; counter: bigint; lamport: bigint; parent: string; side: number; value: number }
  | { kind: "delete"; replica: bigint; counter: bigint; lamport: bigint; target: string };

export function decodeBatch(bytes: Uint8Array): WireOp[] {
  let pos = 0;
  const varint = (): bigint => {
    let result = 0n;
    let shift = 0n;
    for (;;) {
      if (pos >= bytes.length) throw new Error("truncated batch");
      const b = bytes[pos++]!;
      result |= BigInt(b & 0x7f) << shift;
      if ((b & 0x80) === 0) return result;
      shift += 7n;
    }
  };
  if (bytes[0] !== 0x53 || bytes[1] !== 0x4b || bytes[2] !== 1) throw new Error("not a v1 batch");
  pos = 3;
  const count = Number(varint());
  const ops: WireOp[] = [];
  for (let i = 0; i < count; i++) {
    const kind = bytes[pos++];
    const replica = varint();
    const counter = varint();
    const lamport = varint();
    if (kind === 1) {
      const parent = `${varint()}:${varint()}`;
      const side = bytes[pos++]!;
      ops.push({ kind: "insert", replica, counter, lamport, parent, side, value: Number(varint()) });
    } else {
      ops.push({ kind: "delete", replica, counter, lamport, target: `${varint()}:${varint()}` });
    }
  }
  return ops;
}

export const idOf = (op: WireOp) => `${op.replica}:${op.counter}`;
