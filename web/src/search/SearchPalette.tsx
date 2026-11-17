/**
 * The ⌘K palette (Search board). Text mode is typo-tolerant word search
 * with title suggestions first; Meaning is semantic search with each hit's
 * cosine; Code is regex search over code blocks with case and `lang:`
 * filters, and says how many matches the filters hid. Results are a listbox
 * driven by the keyboard: ↑/↓ move, ↵ opens, ⌥↵ inserts a [[link]], ⇥
 * switches mode, ⌥R and ⌥C toggle regex and case.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { describeError } from "../api/errors";
import type { SearchHit, SemanticHit, TitleSuggestion } from "../api/schema.gen";
import { useServices } from "../app/services";
import { useUi, type PaletteMode } from "../app/ui";
import { useWorkspace } from "../app/workspace";
import { SearchIcon } from "../components/ui";
import { highlightPieces, parseQuery } from "./query";

type Result =
  | { kind: "title"; id: string; noteId: string; title: string; detail: string }
  | { kind: "hit"; id: string; noteId: string; title: string; hit: SearchHit }
  | { kind: "meaning"; id: string; noteId: string; title: string; hit: SemanticHit };

const MODES: [PaletteMode, string, string][] = [
  ["text", "Text", "⌘1"],
  ["meaning", "Meaning", "⌘2"],
  ["code", "Code", "⌘3"],
];

export function SearchPalette() {
  const { api } = useServices();
  const ui = useUi();
  const ws = useWorkspace();
  const navigate = useNavigate();
  const [mode, setMode] = useState<PaletteMode>(ui.palette.mode);
  const [input, setInput] = useState(ui.palette.query);
  const [regex, setRegex] = useState(true);
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [results, setResults] = useState<Result[]>([]);
  const [meta, setMeta] = useState<{ count: string; hidden: Record<string, number>; error?: string }>({ count: "", hidden: {} });
  const [selected, setSelected] = useState(0);
  const [loading, setLoading] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const parsed = useMemo(() => parseQuery(input), [input]);

  useEffect(() => {
    inputRef.current?.focus();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && ui.closePalette();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [ui]);

  useEffect(() => {
    const workspaceId = ws.workspace?.id;
    if (!workspaceId || !parsed.query) {
      setResults([]);
      setMeta({ count: "", hidden: {} });
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      setLoading(true);
      const started = performance.now();
      try {
        const title = (id: string | undefined) => ws.notesById.get(id ?? "")?.title ?? "A note";
        if (mode === "meaning") {
          const res = await api.call("MLService_SemanticSearch", { workspaceId, query: parsed.query, k: 12 }, undefined, { signal: controller.signal });
          setResults((res.hits ?? []).map((h, i) => ({ kind: "meaning", id: `m${i}`, noteId: h.location?.noteId ?? "", title: title(h.location?.noteId), hit: h })));
          setMeta({ count: `k 12 · ${res.latencyMs ?? Math.round(performance.now() - started)} ms`, hidden: {} });
        } else {
          const code = mode === "code";
          const [search, titles] = await Promise.all([
            api.call(
              "SearchService_Search",
              { workspaceId, query: parsed.query, mode: code && regex ? "SEARCH_MODE_REGEX" : "SEARCH_MODE_TEXT", caseSensitive, codeOnly: code || undefined, languages: parsed.languages.length ? parsed.languages : undefined, "page.pageSize": 40 },
              undefined,
              { signal: controller.signal },
            ),
            code ? Promise.resolve({ suggestions: [] as TitleSuggestion[] }) : api.call("SearchService_SuggestTitles", { workspaceId, prefix: parsed.query, limit: 4 }, undefined, { signal: controller.signal }).catch(() => ({ suggestions: [] as TitleSuggestion[] })),
          ]);
          const hits = search.hits ?? [];
          const list: Result[] = [
            ...(titles.suggestions ?? []).map((t, i): Result => ({ kind: "title", id: `t${i}`, noteId: t.noteId ?? "", title: t.title ?? "", detail: t.editDistance ? `${t.editDistance} edit${t.editDistance === 1 ? "" : "s"} away` : "title" })),
            ...hits.map((h, i): Result => ({ kind: "hit", id: `h${i}`, noteId: h.location?.noteId ?? "", title: h.noteTitle ?? title(h.location?.noteId), hit: h })),
          ];
          setResults(list);
          const notes = new Set(hits.map((h) => h.location?.noteId)).size;
          let hidden: Record<string, number> = {};
          if (code && parsed.languages.length > 0) {
            // How many matches the language filter hid, so the palette can offer to widen it.
            const wide = await api.call("SearchService_Search", { workspaceId, query: parsed.query, mode: regex ? "SEARCH_MODE_REGEX" : "SEARCH_MODE_TEXT", caseSensitive, codeOnly: true, "page.pageSize": 100 }, undefined, { signal: controller.signal });
            hidden = {};
            for (const h of wide.hits ?? []) if (h.language && !parsed.languages.includes(h.language)) hidden[h.language] = (hidden[h.language] ?? 0) + 1;
          }
          setMeta({ count: `${hits.length} matches · ${notes} notes · ${Math.round(performance.now() - started)} ms`, hidden });
        }
        setSelected(0);
      } catch (error) {
        if (controller.signal.aborted) return;
        setResults([]);
        setMeta({ count: "", hidden: {}, error: describeError(error) });
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }, 160);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [api, ws.workspace?.id, ws.notesById, mode, parsed, regex, caseSensitive]);

  const current = results[selected];

  function open(result: Result | undefined, insertLink = false) {
    if (!result) return;
    ui.closePalette();
    if (insertLink) {
      void navigator.clipboard?.writeText(`[[${result.title}]]`);
      ui.toast(`Copied [[${result.title}]]; paste it into the note.`);
      return;
    }
    const block = result.kind === "title" ? "" : result.hit.location?.blockId;
    navigate(`/notes/${result.noteId}${block ? `#blk-${block}` : ""}`);
  }

  function onKeyDown(e: React.KeyboardEvent) {
    const mod = e.metaKey || e.ctrlKey;
    if (e.key === "Escape") return ui.closePalette();
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = Math.max(1, results.length);
      setSelected((s) => (s + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      return open(current, e.altKey);
    }
    if (e.key === "Tab") {
      e.preventDefault();
      const i = MODES.findIndex(([m]) => m === mode);
      return setMode(MODES[(i + (e.shiftKey ? 2 : 1)) % 3]![0]);
    }
    if (mod && ["1", "2", "3"].includes(e.key)) {
      e.preventDefault();
      return setMode(MODES[Number(e.key) - 1]![0]);
    }
    if (e.altKey && e.code === "KeyR") return (e.preventDefault(), setRegex((r) => !r));
    if (e.altKey && e.code === "KeyC") return (e.preventDefault(), setCaseSensitive((c) => !c));
  }

  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${selected}"]`)?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  const groups = useMemo(() => {
    const out: { title: string; items: { r: Result; index: number }[] }[] = [];
    results.forEach((r, index) => {
      const key = r.kind === "title" ? "Titles" : r.title;
      const g = out.find((x) => x.title === key);
      if (g) g.items.push({ r, index });
      else out.push({ title: key, items: [{ r, index }] });
    });
    return out;
  }, [results]);

  return (
    <>
      <div className="modal-scrim" onClick={ui.closePalette} aria-hidden="true" />
      <div className="pal-wrap" role="dialog" aria-modal="true" aria-label="Search">
        <div className="hd-pal">
          <div className="in">
            <SearchIcon />
            <label htmlFor="pal-q" className="sr">
              Search
            </label>
            <input
              id="pal-q"
              ref={inputRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={mode === "code" ? "regex, then lang:rust lang:go…" : mode === "meaning" ? "Describe what you're looking for…" : "Search titles and text…"}
              role="combobox"
              aria-expanded={results.length > 0}
              aria-controls="pal-results"
              aria-activedescendant={current ? `pal-${current.id}` : undefined}
              autoComplete="off"
              spellCheck={false}
              onKeyDown={onKeyDown}
              style={{ flex: 1, border: 0, background: "none", outline: "none", font: `500 14px ${mode === "code" ? "var(--code)" : "var(--sans)"}`, color: "var(--fg)" }}
            />
            {loading && <span className="spin" aria-hidden="true" />}
            <div className="seg mono sm" role="tablist" aria-label="Search mode">
              {MODES.map(([m, label, key]) => (
                <button key={m} type="button" role="tab" aria-selected={mode === m} className={mode === m ? "on" : ""} style={{ "--c": "var(--fnc)" } as React.CSSProperties} onClick={() => setMode(m)}>
                  {label}
                  <kbd style={{ fontSize: 9.5, padding: "0 4px" }}>{key}</kbd>
                </button>
              ))}
            </div>
          </div>
          <div className="scope">
            {mode === "code" && (
              <>
                <button className={`chip sm${regex ? " on" : ""}`} type="button" aria-pressed={regex} style={{ "--c": "var(--fnc)" } as React.CSSProperties} title="Regular expression · ⌥R" onClick={() => setRegex((r) => !r)}>
                  .*
                </button>
                <button className={`chip sm${caseSensitive ? " on" : ""}`} type="button" aria-pressed={caseSensitive} style={{ "--c": "var(--fnc)" } as React.CSSProperties} title="Match case · ⌥C" onClick={() => setCaseSensitive((c) => !c)}>
                  Aa
                </button>
                {parsed.languages.map((l) => (
                  <button key={l} className="chip sm on" type="button" style={{ "--c": "var(--acc)" } as React.CSSProperties} aria-label={`Remove lang:${l}`} onClick={() => setInput(input.replace(new RegExp(`\\s*lang:${l}\\b`, "i"), ""))}>
                    lang:{l}
                    <span className="x">×</span>
                  </button>
                ))}
                {["rust", "go", "python", "sql"]
                  .filter((l) => !parsed.languages.includes(l))
                  .slice(0, 2)
                  .map((l) => (
                    <button key={l} className="chip sm" type="button" onClick={() => setInput(`${input} lang:${l}`.trim())}>
                      + lang:{l}
                    </button>
                  ))}
              </>
            )}
            {mode === "text" && <span className="cap">Typo-tolerant: “heartbaet” finds heartbeat. Searches titles, then block text.</span>}
            {mode === "meaning" && <span className="cap">By meaning: finds passages that say the same thing in other words. Each hit shows its cosine.</span>}
            <span className="cap" style={{ marginLeft: "auto" }} aria-live="polite">
              {meta.error ? <span style={{ color: "var(--bad)" }}>{meta.error}</span> : meta.count}
            </span>
          </div>
          <div className="hd-main">
            <ul id="pal-results" ref={listRef} role="listbox" aria-label="Results">
              {!parsed.query && <li className="cap" style={{ padding: "12px" }}>Type to search. Only notes you can open are searched.</li>}
              {parsed.query && !loading && results.length === 0 && !meta.error && <li className="cap" style={{ padding: "12px" }}>No matches.</li>}
              {groups.map((g) => (
                <li key={g.title} role="presentation">
                  <span className="hd-grp">
                    {g.title} · {g.items.length}
                  </span>
                  {g.items.map(({ r, index }) => (
                    <button
                      key={r.id}
                      id={`pal-${r.id}`}
                      data-index={index}
                      type="button"
                      role="option"
                      aria-selected={index === selected}
                      className={`hd-opt${index === selected ? " hd-sel" : ""}`}
                      onMouseEnter={() => setSelected(index)}
                      onClick={() => open(r)}
                    >
                      <ResultBody r={r} />
                    </button>
                  ))}
                </li>
              ))}
              {Object.entries(meta.hidden).map(([lang, n]) => (
                <li key={lang}>
                  <span className="cap" style={{ display: "block", padding: "10px 12px" }}>
                    + {n} match{n === 1 ? "" : "es"} in lang:{lang} hidden by your filters ·{" "}
                    <button type="button" style={{ color: "var(--acc)" }} onClick={() => setInput(`${input} lang:${lang}`)}>
                      add lang:{lang}
                    </button>
                  </span>
                </li>
              ))}
            </ul>
            <aside className="hd-prev" aria-label="Preview">
              <div className="lab">Preview</div>
              {current ? (
                <>
                  <div style={{ display: "grid", gap: 4 }}>
                    <b style={{ fontSize: 16, color: "var(--fg)" }}>{current.title}</b>
                    <span className="cap">
                      {current.kind === "title" ? "note title" : current.kind === "meaning" ? `block ${current.hit.location?.blockId} · cosine ${current.hit.cosine?.toFixed(2)}` : `${current.hit.language ? `code block · ${current.hit.language}` : "text block"}`}
                    </span>
                  </div>
                  {current.kind === "hit" && (
                    <figure className="cb" style={{ margin: 0 }}>
                      <pre style={{ fontSize: 12, padding: 12, whiteSpace: "pre-wrap" }}>
                        {highlightPieces(current.hit.snippet ?? "", current.hit.highlights ?? []).map((p, i) =>
                          p.hit ? (
                            <mark key={i} style={{ background: "color-mix(in oklab, var(--acc) 30%, transparent)", color: "var(--fg)" }}>
                              {p.text}
                            </mark>
                          ) : (
                            <span key={i}>{p.text}</span>
                          ),
                        )}
                      </pre>
                    </figure>
                  )}
                  {current.kind === "meaning" && (
                    <span className="sk-meter" style={{ "--w": `${Math.round((current.hit.cosine ?? 0) * 100)}%`, "--c": "var(--grn)" } as React.CSSProperties}>
                      <u />
                    </span>
                  )}
                  <div style={{ display: "grid", gap: 6 }}>
                    <button className="btn xs go" type="button" style={{ justifyContent: "space-between" }} onClick={() => open(current)}>
                      Open<span className="kb">↵</span>
                    </button>
                    <button className="btn xs" type="button" style={{ justifyContent: "space-between" }} onClick={() => open(current, true)}>
                      Copy as [[link]]<span className="kb">⌥↵</span>
                    </button>
                  </div>
                </>
              ) : (
                <span className="cap">Nothing selected.</span>
              )}
            </aside>
          </div>
          <div className="hd-hint">
            <span>
              <kbd>↑</kbd> <kbd>↓</kbd> move
            </span>
            <span>
              <kbd>↵</kbd> open
            </span>
            <span>
              <kbd>⇥</kbd> next mode
            </span>
            <span>
              <kbd>⌥R</kbd> regex
            </span>
            <span>
              <kbd>⌥C</kbd> case
            </span>
            <span style={{ marginLeft: "auto" }}>only notes you can open are searched</span>
          </div>
        </div>
      </div>
    </>
  );
}

function ResultBody({ r }: { r: Result }) {
  if (r.kind === "title") {
    return (
      <span className="sk-res-top">
        <span className="tp" style={{ "--c": "var(--acc)" } as React.CSSProperties}>
          title
        </span>
        {r.title}
        <small>{r.detail}</small>
      </span>
    );
  }
  if (r.kind === "meaning") {
    return (
      <span className="sk-res-top">
        <span className="tp" style={{ "--c": "var(--grn)" } as React.CSSProperties}>
          meaning
        </span>
        block {r.hit.location?.blockId?.split(":")[1]}
        <small>cosine {r.hit.cosine?.toFixed(2)}</small>
      </span>
    );
  }
  return (
    <>
      <span className="sk-res-top">
        <span className="tp" style={{ "--c": r.hit.language ? "var(--acc)" : "var(--fnc)" } as React.CSSProperties}>
          {r.hit.language ? `code · ${r.hit.language}` : "text"}
        </span>
        <small>{(r.hit.highlights?.length ?? 0) > 1 ? `${r.hit.highlights!.length} matches` : ""}</small>
      </span>
      <span className="sk-snip">
        {highlightPieces(r.hit.snippet ?? "", r.hit.highlights ?? []).map((p, i) => (p.hit ? <mark key={i}>{p.text}</mark> : <span key={i}>{p.text}</span>))}
      </span>
    </>
  );
}
