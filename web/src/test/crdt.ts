import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { loadCrdt, type CrdtModule } from "../crdt/wasm";

/** Loads the built WebAssembly module from public/wasm (pnpm wasm builds it). */
export function loadTestCrdt(): Promise<CrdtModule> {
  const dir = resolve(__dirname, "../../public/wasm");
  return loadCrdt({
    kind: "bytes",
    wasm: readFileSync(resolve(dir, "skein-crdt.wasm")),
    execCode: readFileSync(resolve(dir, "wasm_exec.js"), "utf8"),
  });
}
