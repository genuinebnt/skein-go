/**
 * UTF-16 offsets (what the DOM, textarea selections and String.length use)
 * to Unicode code points (what the CRDT, the wire and every API range use),
 * per ADR-0003. Conversion happens once, at the WASM boundary.
 *
 * A TextIndex records where the astral characters (the ones stored as
 * surrogate pairs) sit in a string, the "cached UTF-16 length per run" the
 * ADR describes: for text with none, both units agree and conversion is the
 * identity; otherwise it is a binary search over the pairs.
 */

const isHigh = (unit: number) => unit >= 0xd800 && unit <= 0xdbff;
const isLow = (unit: number) => unit >= 0xdc00 && unit <= 0xdfff;

export class TextIndex {
  /** UTF-16 offsets of each surrogate pair's first unit, ascending. */
  private readonly pairs: number[] = [];
  readonly utf16Length: number;
  readonly codePointLength: number;

  constructor(readonly text: string) {
    for (let i = 0; i < text.length; i++) {
      if (isHigh(text.charCodeAt(i)) && i + 1 < text.length && isLow(text.charCodeAt(i + 1))) {
        this.pairs.push(i);
        i++;
      }
    }
    this.utf16Length = text.length;
    this.codePointLength = text.length - this.pairs.length;
  }

  /**
   * The code-point offset of a UTF-16 offset. An offset inside a surrogate
   * pair (which a browser never reports, but arithmetic can produce) rounds
   * down to the pair's start, so an edit can never split a character.
   */
  toCodePoint(utf16: number): number {
    const offset = Math.max(0, Math.min(utf16, this.utf16Length));
    if (this.pairs.length === 0) return offset;
    // lo = pairs starting before offset. Each whole pair before it is two
    // units but one code point. When offset splits a pair (it starts at
    // offset - 1), rounding down to the pair's start gives the same count:
    // (offset - 1) - (lo - 1).
    let lo = 0;
    let hi = this.pairs.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this.pairs[mid]! < offset) lo = mid + 1;
      else hi = mid;
    }
    return offset - lo;
  }

  /** The UTF-16 offset of a code-point offset. */
  toUtf16(codePoint: number): number {
    const target = Math.max(0, Math.min(codePoint, this.codePointLength));
    if (this.pairs.length === 0) return target;
    // Each pair before the target adds one extra unit.
    let extra = 0;
    for (const start of this.pairs) {
      if (start - extra < target) extra++;
      else break;
    }
    return target + extra;
  }
}

export const utf16ToCodePoint = (text: string, utf16: number) => new TextIndex(text).toCodePoint(utf16);
export const codePointToUtf16 = (text: string, codePoint: number) => new TextIndex(text).toUtf16(codePoint);
export const codePointLength = (text: string) => new TextIndex(text).codePointLength;

/** Slices a string by code points, as API ranges (search highlights, blame spans, comments) are given. */
export function sliceCodePoints(text: string, start: number, end?: number): string {
  const index = new TextIndex(text);
  return text.slice(index.toUtf16(start), end === undefined ? undefined : index.toUtf16(end));
}
