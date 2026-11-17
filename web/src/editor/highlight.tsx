/**
 * A small syntax highlighter for code blocks, enough for the six languages
 * the seed uses (go, rust, python, sql, typescript, yaml) plus toml and json:
 * comments, strings, numbers, keywords, types and calls, line by line, with
 * folio.css's k-* token classes.
 */

import type { ReactNode } from "react";

const KEYWORDS: Record<string, string[]> = {
  go: "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false".split(" "),
  rust: "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while".split(" "),
  python: "and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield".split(" "),
  typescript: "abstract as async await break case catch class const continue default delete do else enum export extends false finally for from function if implements import in instanceof interface let new null of private protected public readonly return static super switch this throw true try type typeof undefined var void while yield".split(" "),
  sql: "select from where and or not insert into values update set delete create table index primary key foreign references join left right inner outer on group by order having limit offset as count distinct null is in exists case when then else end begin commit".split(" "),
  yaml: "true false null yes no".split(" "),
  toml: "true false".split(" "),
};
const ALIASES: Record<string, string> = { ts: "typescript", js: "typescript", javascript: "typescript", tsx: "typescript", py: "python", rs: "rust", golang: "go", yml: "yaml" };

const COMMENT: Record<string, RegExp> = { python: /#.*/y, yaml: /#.*/y, toml: /#.*/y, sql: /--.*/y };

export function highlightLine(line: string, language: string): ReactNode[] {
  const lang = ALIASES[language] ?? language;
  const keywords = new Set(KEYWORDS[lang] ?? []);
  const comment = COMMENT[lang] ?? /\/\/.*/y;
  const out: ReactNode[] = [];
  let i = 0;
  let plain = "";
  const flush = () => {
    if (plain) out.push(plain);
    plain = "";
  };
  const push = (cls: string, text: string) => {
    flush();
    out.push(
      <span key={out.length} className={cls}>
        {text}
      </span>,
    );
  };
  while (i < line.length) {
    comment.lastIndex = i;
    const c = comment.exec(line);
    if (c && c.index === i) {
      push("k-com", c[0]);
      break;
    }
    const ch = line[i]!;
    if (ch === '"' || ch === "'" || ch === "`") {
      let j = i + 1;
      while (j < line.length && line[j] !== ch) j += line[j] === "\\" ? 2 : 1;
      push("k-str", line.slice(i, j + 1));
      i = j + 1;
      continue;
    }
    const num = /^\d[\d_.]*(?:e[+-]?\d+)?[a-z]*/i.exec(line.slice(i));
    if (num && !/\w/.test(line[i - 1] ?? "")) {
      push("k-num", num[0]);
      i += num[0].length;
      continue;
    }
    const word = /^[A-Za-z_][\w]*/.exec(line.slice(i));
    if (word) {
      const w = word[0];
      const next = line[i + w.length];
      if (keywords.has(lang === "sql" ? w.toLowerCase() : w)) push("k-kw", w);
      else if (next === "(" || next === "!") push(next === "!" ? "k-mac" : "k-fn", w);
      else if (/^[A-Z]/.test(w) && lang !== "sql") push("k-ty", w);
      else plain += w;
      i += w.length;
      continue;
    }
    plain += ch;
    i++;
  }
  flush();
  return out;
}
