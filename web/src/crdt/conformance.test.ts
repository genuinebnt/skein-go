/**
 * The shared CRDT conformance fixtures (testdata/conformance/crdt, format in
 * docs/spec/crdt-sequence.md section 8) run against the WebAssembly module
 * through the browser-facing wrapper. Fixture indexes count code points; the
 * test converts them to UTF-16 first and lets the wrapper convert back, so
 * the boundary conversion of ADR-0003 is exercised on every edit, including
 * the multibyte fixture.
 */
import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { TextIndex } from "./utf16";
import type { CrdtDocument, CrdtModule } from "./wasm";
import { loadTestCrdt } from "../test/crdt";

type Step =
  | { replica: string; insert: { index: number; text: string } }
  | { replica: string; delete: { index: number; count: number } }
  | { sync: "all" | { from: string; to: string } }
  | { expect: Record<string, string> };

type Fixture = { name: string; why: string; replicas: Record<string, number>; steps: Step[]; expect: Record<string, string> };

const dir = resolve(__dirname, "../../../testdata/conformance/crdt");
const fixtures = readdirSync(dir)
  .filter((f) => f.endsWith(".json"))
  .sort()
  .map((f) => [f, JSON.parse(readFileSync(resolve(dir, f), "utf8")) as Fixture] as const);

let crdt: CrdtModule;
beforeAll(async () => {
  crdt = await loadTestCrdt();
});

function syncInto(from: CrdtDocument, to: CrdtDocument) {
  to.applyRemote(from.opsSince(to.version()));
}

describe("CRDT conformance fixtures through the WASM bridge", () => {
  it("finds the fixtures", () => expect(fixtures.length).toBeGreaterThanOrEqual(7));

  for (const [file, fx] of fixtures) {
    it(`${file}: ${fx.name}`, () => {
      const names = Object.keys(fx.replicas).sort();
      const docs = new Map(names.map((n) => [n, crdt.open(String(fx.replicas[n]))]));
      const get = (n: string) => docs.get(n)!;
      const check = (expected: Record<string, string>) => {
        for (const [who, want] of Object.entries(expected)) {
          for (const n of who === "all" ? names : [who]) expect(get(n).text(), `${n}: ${fx.why}`).toBe(want);
        }
      };
      for (const step of fx.steps) {
        if ("insert" in step) {
          const d = get(step.replica);
          d.insertInNote(new TextIndex(d.text()).toUtf16(step.insert.index), step.insert.text);
          d.checkInvariants();
        } else if ("delete" in step) {
          const d = get(step.replica);
          const index = new TextIndex(d.text());
          d.deleteInNote(index.toUtf16(step.delete.index), index.toUtf16(step.delete.index + step.delete.count));
          d.checkInvariants();
        } else if ("sync" in step) {
          const pairs: [string, string][] =
            step.sync === "all" ? names.flatMap((a) => names.filter((b) => b !== a).map((b) => [a, b] as [string, string])) : [[step.sync.from, step.sync.to]];
          for (const [from, to] of pairs) {
            syncInto(get(from), get(to));
            get(to).checkInvariants();
          }
        } else if ("expect" in step) {
          check(step.expect);
        }
      }
      check(fx.expect);
      for (const d of docs.values()) d.free();
    });
  }
});
