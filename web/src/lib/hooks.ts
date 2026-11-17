/** Small React hooks shared by the screens. */

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { describeError } from "../api/errors";

export type Async<T> = { data: T | undefined; error: string | null; loading: boolean; reload: () => void };

/** Runs an async loader when `deps` change, ignoring results from superseded runs. */
export function useAsync<T>(load: (signal: AbortSignal) => Promise<T>, deps: unknown[]): Async<T> {
  const [state, setState] = useState<{ data: T | undefined; error: string | null; loading: boolean }>({ data: undefined, error: null, loading: true });
  const [tick, setTick] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    const controller = new AbortController();
    setState((s) => ({ ...s, loading: true, error: null }));
    loadRef
      .current(controller.signal)
      .then((data) => !controller.signal.aborted && setState({ data, error: null, loading: false }))
      .catch((error: unknown) => {
        if (controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError")) return;
        setState((s) => ({ ...s, error: describeError(error), loading: false }));
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { ...state, reload };
}

export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const list = window.matchMedia(query);
      list.addEventListener("change", onChange);
      return () => list.removeEventListener("change", onChange);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

type KeyOptions = { meta?: boolean; alt?: boolean; shift?: boolean; allowInInputs?: boolean };

const isTyping = (target: EventTarget | null) => target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));

/** A global keyboard shortcut. `meta` matches ⌘ on macOS and Ctrl elsewhere. */
export function useHotkey(key: string, handler: (e: KeyboardEvent) => void, options: KeyOptions = {}): void {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== key.toLowerCase() && e.code !== key) return;
      if (!!options.meta !== (e.metaKey || e.ctrlKey)) return;
      if (!!options.alt !== e.altKey) return;
      if (options.shift !== undefined && options.shift !== e.shiftKey) return;
      if (!options.allowInInputs && !options.meta && !options.alt && isTyping(e.target)) return;
      e.preventDefault();
      ref.current(e);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [key, options.meta, options.alt, options.shift, options.allowInInputs]);
}

/** Subscribes to an object with `subscribe` and a changing `revision`. */
export function useRevision(source: { subscribe: (l: () => void) => () => void; revision: number } | null): number {
  return useSyncExternalStore(
    (l) => (source ? source.subscribe(l) : () => {}),
    () => source?.revision ?? 0,
    () => 0,
  );
}
