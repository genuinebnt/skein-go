/**
 * Parses what is typed into the search palette: `lang:rust` tokens become
 * language filters and the rest is the query (text, meaning or regex).
 */

export type ParsedQuery = { query: string; languages: string[] };

export function parseQuery(input: string): ParsedQuery {
  const languages: string[] = [];
  const rest: string[] = [];
  for (const token of input.split(/(\s+)/)) {
    const m = /^lang:([\w+#.-]+)$/i.exec(token);
    if (m) languages.push(m[1]!.toLowerCase());
    else rest.push(token);
  }
  return { query: rest.join("").trim(), languages: [...new Set(languages)] };
}

/** Splits a snippet into plain and highlighted pieces from code-point ranges. */
export function highlightPieces(snippet: string, ranges: { start?: number; end?: number }[]): { text: string; hit: boolean }[] {
  const chars = [...snippet];
  const marks = new Array<boolean>(chars.length).fill(false);
  for (const r of ranges) for (let i = r.start ?? 0; i < Math.min(chars.length, r.end ?? 0); i++) marks[i] = true;
  const out: { text: string; hit: boolean }[] = [];
  chars.forEach((ch, i) => {
    const last = out[out.length - 1];
    if (last && last.hit === marks[i]) last.text += ch;
    else out.push({ text: ch, hit: marks[i]! });
  });
  return out;
}
