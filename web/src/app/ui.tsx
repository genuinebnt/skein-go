/**
 * App-wide UI state that crosses screens: whether the search palette is
 * open, what the header's sync badge shows (the open note's state, or the
 * device's queue elsewhere), and toasts.
 */

import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { SyncStatus } from "../crdt/syncState";

export type HeaderSync = { status: SyncStatus; label: string; count: string; title: string } | null;
type Toast = { id: number; tone: "info" | "warn" | "bad" | "ok"; text: string };
export type PaletteMode = "text" | "meaning" | "code";

type UiState = {
  palette: { open: boolean; mode: PaletteMode; query: string };
  openPalette: (mode?: PaletteMode, query?: string) => void;
  closePalette: () => void;
  sync: HeaderSync;
  setSync: (s: HeaderSync) => void;
  toasts: Toast[];
  toast: (text: string, tone?: Toast["tone"]) => void;
  dismiss: (id: number) => void;
};

const UiContext = createContext<UiState | null>(null);

export function useUi(): UiState {
  const ui = useContext(UiContext);
  if (!ui) throw new Error("useUi outside UiProvider");
  return ui;
}

let toastId = 0;

export function UiProvider({ children }: { children: ReactNode }) {
  const [palette, setPalette] = useState<UiState["palette"]>({ open: false, mode: "text", query: "" });
  const [sync, setSync] = useState<HeaderSync>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const dismiss = useCallback((id: number) => setToasts((list) => list.filter((t) => t.id !== id)), []);
  const toast = useCallback(
    (text: string, tone: Toast["tone"] = "info") => {
      const id = ++toastId;
      setToasts((list) => [...list.slice(-3), { id, tone, text }]);
      setTimeout(() => dismiss(id), 5000);
    },
    [dismiss],
  );
  const value = useMemo<UiState>(
    () => ({
      palette,
      openPalette: (mode = "text", query = "") => setPalette({ open: true, mode, query }),
      closePalette: () => setPalette((p) => ({ ...p, open: false })),
      sync,
      setSync,
      toasts,
      toast,
      dismiss,
    }),
    [palette, sync, toasts, toast, dismiss],
  );
  return <UiContext.Provider value={value}>{children}</UiContext.Provider>;
}

export function Toasts() {
  const { toasts, dismiss } = useUi();
  const colour = { info: "var(--fnc)", warn: "var(--warn)", bad: "var(--bad)", ok: "var(--grn)" };
  return (
    <div className="toast-stack" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className="sk-banner" role={t.tone === "bad" ? "alert" : "status"} style={{ "--c": colour[t.tone] } as React.CSSProperties}>
          <span>{t.text}</span>
          <span className="sp" />
          <button type="button" className="btn xs ghost" onClick={() => dismiss(t.id)} aria-label="Dismiss">
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
