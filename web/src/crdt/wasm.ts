/**
 * Loads the CRDT WebAssembly module (web/wasm, ADR-0007) and wraps it in a
 * typed, UTF-16-facing API. The raw module counts code points; every
 * position that enters or leaves CrdtDocument here is UTF-16 within one
 * block, converted with TextIndex (ADR-0003). A Rust build of the module can
 * replace the Go one as long as it registers the same `skeinCrdt` functions.
 */

import { TextIndex } from "./utf16";
import type { VersionEntry } from "../ws/syncCodec";

export type BlockType =
  | "paragraph"
  | "heading1"
  | "heading2"
  | "heading3"
  | "bulleted"
  | "numbered"
  | "todo"
  | "quote"
  | "code"
  | "divider"
  | "table";

export type BlockAttrs = { type: BlockType; indent: number; checked: boolean; language: string };

/** A block as the editor draws it; `start` is its first character's code point in the note text. */
export type Block = BlockAttrs & { id: string; text: string; start: number };

export type InlineKind = "text" | "escape" | "code" | "emphasis" | "strong" | "link" | "wikilink" | "mention";

/** An inline Markdown node with UTF-16 offsets into its block's text. */
export type InlineNode = {
  kind: InlineKind;
  start: number;
  end: number;
  contentStart: number;
  contentEnd: number;
  value: string;
  children: InlineNode[];
};

export type OpsSummary = { inserted: string; inserts: number; deletes: number };

type Failure = { error: string };

/** The raw functions the module registers on globalThis.skeinCrdt. */
export type RawModule = {
  newDocument(replica: string): number | Failure;
  free(h: number): void;
  applyRemote(h: number, ops: Uint8Array): { pending: number } | Failure;
  applyRemoteAttrs(h: number, attrs: Uint8Array): null | Failure;
  insert(h: number, pos: number, text: string): Uint8Array | Failure;
  delete(h: number, pos: number, count: number): Uint8Array | Failure;
  setAttrs(h: number, blockId: string, attrs: BlockAttrs): Uint8Array | Failure;
  seedAttrsFromMarkdown(h: number, markdown: string): number | Failure;
  text(h: number): string | Failure;
  blocks(h: number): Block[] | Failure;
  version(h: number): VersionEntry[] | Failure;
  lamport(h: number): string | Failure;
  opsSince(h: number, version: VersionEntry[]): Uint8Array | Failure;
  allAttrs(h: number): Uint8Array | Failure;
  idAt(h: number, pos: number): string | Failure;
  indexOf(h: number, id: string): { index: number; visible: boolean; ok: boolean } | Failure;
  markdown(h: number): string | Failure;
  markdownAt(h: number, version: VersionEntry[]): string | Failure;
  exportMapped(h: number): { markdown: string; ids: string[] } | Failure;
  renderInline(text: string): RawNode[];
  describeOps(ops: Uint8Array): OpsSummary | Failure;
  checkInvariants(h: number): null | Failure;
  replaceWithMarkdown(h: number, markdown: string): { ops: Uint8Array; attrs: Uint8Array } | Failure;
};

type RawNode = Omit<InlineNode, "children"> & { children: RawNode[] };

/**
 * Go's CopyBytesToGo accepts only this realm's Uint8Array; bytes that come
 * back from IndexedDB or another frame may be a structurally identical
 * array from another realm, so they are copied into a local one.
 */
function local(bytes: Uint8Array): Uint8Array {
  return bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes as ArrayLike<number>);
}

export class CrdtError extends Error {
  override readonly name = "CrdtError";
}

function ok<T>(value: T | Failure): T {
  if (value !== null && typeof value === "object" && "error" in (value as object) && typeof (value as Failure).error === "string") {
    throw new CrdtError((value as Failure).error);
  }
  return value as T;
}

declare global {
  var skeinCrdt: RawModule | undefined;
  var Go: (new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> }) | undefined;
}

let loading: Promise<CrdtModule> | null = null;

/** The module's sources: URLs in the browser, bytes and glue code in tests. */
export type ModuleSource =
  | { kind: "url"; wasmUrl: string; execUrl: string }
  | { kind: "bytes"; wasm: BufferSource; execCode: string };

const defaultSource: ModuleSource = {
  kind: "url",
  wasmUrl: `${import.meta.env.BASE_URL}wasm/skein-crdt.wasm`,
  execUrl: `${import.meta.env.BASE_URL}wasm/wasm_exec.js`,
};

async function loadGlue(source: ModuleSource): Promise<void> {
  if (globalThis.Go) return;
  if (source.kind === "bytes") {
    // The glue is a classic script that defines globalThis.Go.
    new Function(source.execCode)();
    return;
  }
  await new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = source.execUrl;
    script.onload = () => resolve();
    script.onerror = () => reject(new CrdtError(`could not load ${source.execUrl}`));
    document.head.appendChild(script);
  });
}

/** Loads the module once per page; later calls share it. */
export function loadCrdt(source: ModuleSource = defaultSource): Promise<CrdtModule> {
  loading ??= (async () => {
    await loadGlue(source);
    if (!globalThis.Go) throw new CrdtError("wasm_exec.js did not define Go");
    const go = new globalThis.Go();
    const instance =
      source.kind === "bytes"
        ? (await WebAssembly.instantiate(source.wasm, go.importObject)).instance
        : (await WebAssembly.instantiateStreaming(fetch(source.wasmUrl), go.importObject)).instance;
    void go.run(instance); // resolves only when the program exits, which it never does
    const raw = globalThis.skeinCrdt;
    if (!raw) throw new CrdtError("the CRDT module did not register skeinCrdt");
    return new CrdtModule(raw);
  })();
  loading.catch(() => {
    loading = null;
  });
  return loading;
}

function convertNodes(nodes: RawNode[], index: TextIndex): InlineNode[] {
  return nodes.map((n) => ({
    kind: n.kind,
    start: index.toUtf16(n.start),
    end: index.toUtf16(n.end),
    contentStart: index.toUtf16(n.contentStart),
    contentEnd: index.toUtf16(n.contentEnd),
    value: n.value,
    children: convertNodes(n.children ?? [], index),
  }));
}

export class CrdtModule {
  constructor(readonly raw: RawModule) {}

  open(replicaId: string): CrdtDocument {
    return new CrdtDocument(this.raw, ok(this.raw.newDocument(replicaId)), replicaId);
  }

  /** Parses inline Markdown, with offsets in UTF-16 units of `text`. */
  renderInline(text: string): InlineNode[] {
    return convertNodes(this.raw.renderInline(text), new TextIndex(text));
  }

  describeOps(ops: Uint8Array): OpsSummary {
    return ok(this.raw.describeOps(local(ops)));
  }
}

/** Where a cursor is, in editor terms. */
export type Caret = { blockId: string; offset: number };

const MARKER = "\u001e";

export class CrdtDocument {
  private cachedBlocks: Block[] | null = null;
  private freed = false;

  constructor(
    private readonly raw: RawModule,
    private readonly handle: number,
    readonly replicaId: string,
  ) {}

  free(): void {
    if (this.freed) return;
    this.freed = true;
    this.raw.free(this.handle);
  }

  private changed(): void {
    this.cachedBlocks = null;
  }

  blocks(): Block[] {
    this.cachedBlocks ??= ok(this.raw.blocks(this.handle));
    return this.cachedBlocks;
  }

  block(id: string): Block | undefined {
    return this.blocks().find((b) => b.id === id);
  }

  text(): string {
    return ok(this.raw.text(this.handle));
  }

  version(): VersionEntry[] {
    return ok(this.raw.version(this.handle));
  }

  lamport(): bigint {
    return BigInt(ok(this.raw.lamport(this.handle)));
  }

  /** Applies a remote batch; returns how many operations wait for dependencies. */
  applyRemote(ops: Uint8Array): number {
    const result = ok(this.raw.applyRemote(this.handle, local(ops)));
    this.changed();
    return result.pending;
  }

  applyRemoteAttrs(attrs: Uint8Array): void {
    ok(this.raw.applyRemoteAttrs(this.handle, local(attrs)));
    this.changed();
  }

  seedAttrsFromMarkdown(markdown: string): number {
    const n = ok(this.raw.seedAttrsFromMarkdown(this.handle, markdown));
    this.changed();
    return n;
  }

  private blockOrThrow(blockId: string): Block {
    const b = this.block(blockId);
    if (!b) throw new CrdtError(`no block ${blockId}`);
    return b;
  }

  private notePosition(block: Block, utf16: number): number {
    return block.start + new TextIndex(block.text).toCodePoint(utf16);
  }

  /**
   * Inserts at a UTF-16 offset of the whole note text (markers included).
   * Used for edits that span blocks, such as undoing an agent suggestion.
   */
  insertInNote(utf16Offset: number, text: string): Uint8Array {
    const pos = new TextIndex(this.text()).toCodePoint(utf16Offset);
    const ops = ok(this.raw.insert(this.handle, pos, text));
    this.changed();
    return ops;
  }

  /** Deletes the UTF-16 range [from, to) of the whole note text. */
  deleteInNote(from: number, to: number): Uint8Array {
    const index = new TextIndex(this.text());
    const start = index.toCodePoint(Math.min(from, to));
    const end = index.toCodePoint(Math.max(from, to));
    const ops = ok(this.raw.delete(this.handle, start, end - start));
    this.changed();
    return ops;
  }

  /** Inserts text at a UTF-16 offset inside a block. Returns the batch to send. */
  insertText(blockId: string, utf16Offset: number, text: string): Uint8Array {
    if (text.includes(MARKER)) throw new CrdtError("block text cannot contain the block marker");
    const b = this.blockOrThrow(blockId);
    const ops = ok(this.raw.insert(this.handle, this.notePosition(b, utf16Offset), text));
    this.changed();
    return ops;
  }

  /** Deletes the UTF-16 range [from, to) inside a block. */
  deleteText(blockId: string, from: number, to: number): Uint8Array {
    const b = this.blockOrThrow(blockId);
    const index = new TextIndex(b.text);
    const start = index.toCodePoint(Math.min(from, to));
    const end = index.toCodePoint(Math.max(from, to));
    const ops = ok(this.raw.delete(this.handle, b.start + start, end - start));
    this.changed();
    return ops;
  }

  /**
   * Splits a block at a UTF-16 offset (Enter): inserts a marker, so the text
   * after the caret becomes a new block. Returns the batch and the new block's ID.
   */
  splitBlock(blockId: string, utf16Offset: number): { ops: Uint8Array; newBlockId: string } {
    const b = this.blockOrThrow(blockId);
    const pos = this.notePosition(b, utf16Offset);
    const ops = ok(this.raw.insert(this.handle, pos, MARKER));
    this.changed();
    return { ops, newBlockId: ok(this.raw.idAt(this.handle, pos)) };
  }

  /** Appends an empty block after every other block. */
  appendBlock(): { ops: Uint8Array; newBlockId: string } {
    const pos = [...this.text()].length;
    const ops = ok(this.raw.insert(this.handle, pos, MARKER));
    this.changed();
    return { ops, newBlockId: ok(this.raw.idAt(this.handle, pos)) };
  }

  /** Backspace at a block's start: deletes its marker, joining it to the previous block. */
  mergeWithPrevious(blockId: string): Uint8Array {
    const b = this.blockOrThrow(blockId);
    if (b.start === 0) throw new CrdtError("the first block has nothing before it");
    const ops = ok(this.raw.delete(this.handle, b.start - 1, 1));
    this.changed();
    return ops;
  }

  /** Deletes a whole block, marker and text. */
  deleteBlock(blockId: string): Uint8Array {
    const b = this.blockOrThrow(blockId);
    const markerAt = b.start === 0 ? 0 : b.start - 1;
    const ops = ok(this.raw.delete(this.handle, markerAt, b.start - markerAt + new TextIndex(b.text).codePointLength));
    this.changed();
    return ops;
  }

  setBlockAttrs(blockId: string, attrs: BlockAttrs): Uint8Array {
    const ops = ok(this.raw.setAttrs(this.handle, blockId, attrs));
    this.changed();
    return ops;
  }

  opsSince(version: VersionEntry[]): Uint8Array {
    return ok(this.raw.opsSince(this.handle, version));
  }

  allOps(): Uint8Array {
    return this.opsSince([]);
  }

  allAttrs(): Uint8Array {
    return ok(this.raw.allAttrs(this.handle));
  }

  /**
   * Anchors a caret to an element ID, so a remote edit elsewhere does not
   * move it: the element just before the caret (the block's marker at
   * offset 0).
   */
  anchorAt(caret: Caret): string {
    const b = this.blockOrThrow(caret.blockId);
    const pos = this.notePosition(b, caret.offset);
    return ok(this.raw.idAt(this.handle, pos - 1 < 0 ? 0 : pos - 1));
  }

  /** Where an anchored caret is now, or null when its element is unknown. */
  caretOf(anchor: string): Caret | null {
    const found = ok(this.raw.indexOf(this.handle, anchor));
    if (!found.ok) return null;
    const pos = found.visible ? found.index + 1 : found.index;
    const blocks = this.blocks();
    for (let i = blocks.length - 1; i >= 0; i--) {
      const b = blocks[i]!;
      if (b.start <= pos) {
        const cp = Math.min(pos - b.start, new TextIndex(b.text).codePointLength);
        return { blockId: b.id, offset: new TextIndex(b.text).toUtf16(cp) };
      }
    }
    return blocks[0] ? { blockId: blocks[0].id, offset: 0 } : null;
  }

  /** Code-point position of an element (where it would be when deleted), or null if unknown. */
  positionOf(id: string): { index: number; visible: boolean } | null {
    const found = ok(this.raw.indexOf(this.handle, id));
    return found.ok ? { index: found.index, visible: found.visible } : null;
  }

  /** Code-point level edits, for server-side use in mock mode (the editor uses UTF-16 methods). */
  insertAtCodePoint(pos: number, text: string): Uint8Array {
    const ops = ok(this.raw.insert(this.handle, pos, text));
    this.changed();
    return ops;
  }

  deleteAtCodePoint(pos: number, count: number): Uint8Array {
    const ops = ok(this.raw.delete(this.handle, pos, count));
    this.changed();
    return ops;
  }

  markdown(): string {
    return ok(this.raw.markdown(this.handle));
  }

  markdownAt(version: VersionEntry[]): string {
    return ok(this.raw.markdownAt(this.handle, version));
  }

  exportMapped(): { markdown: string; ids: string[] } {
    return ok(this.raw.exportMapped(this.handle));
  }

  /** Replaces the whole content with a Markdown document as one local change (restoring a version). */
  replaceWithMarkdown(markdown: string): { ops: Uint8Array; attrs: Uint8Array } {
    const result = ok(this.raw.replaceWithMarkdown(this.handle, markdown));
    this.changed();
    return result;
  }

  checkInvariants(): void {
    ok(this.raw.checkInvariants(this.handle));
  }
}
