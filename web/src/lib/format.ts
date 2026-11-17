/** Display helpers shared by every screen. */

import type { NoteRole, WorkspaceRole } from "../api/schema.gen";

export function initials(name: string | undefined): string {
  if (!name) return "?";
  const parts = name.replace(/\(.*?\)/g, "").trim().split(/[\s-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? "") + (parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? "") : "")).toUpperCase() || "?";
}

const PRESENCE = ["var(--fnc)", "var(--grn)", "var(--pink)", "var(--warn)", "var(--vio)", "var(--acc)"];

/** A stable colour per user, for avatars, cursors and blame. */
export function colorFor(id: string | undefined): string {
  if (!id) return "var(--dim)";
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return PRESENCE[h % PRESENCE.length]!;
}

export function relativeTime(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const seconds = Math.round((now - Date.parse(iso)) / 1000);
  if (seconds < 45) return "now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} h`;
  const days = Math.round(hours / 24);
  if (days === 1) return "yesterday";
  if (days < 7) return `${days} days`;
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

export function clockTime(iso: string | undefined): string {
  return iso ? new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : "";
}

export const workspaceRoleName = (role: WorkspaceRole | undefined) => (role ? role.replace("WORKSPACE_ROLE_", "").toLowerCase() : "member");
export const noteRoleName = (role: NoteRole | undefined) => (role ? role.replace("NOTE_ROLE_", "").toLowerCase() : "viewer");
export const capitalise = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export function plural(n: number, word: string, many = `${word}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? word : many}`;
}

export function wordCount(text: string): number {
  return text.split(/\s+/).filter((w) => /\w/.test(w)).length;
}

/** Downloads rows as a CSV file. */
export function downloadCsv(name: string, rows: (string | number)[][]): void {
  const csv = rows.map((r) => r.map((c) => (/[",\n]/.test(String(c)) ? `"${String(c).replace(/"/g, '""')}"` : String(c))).join(",")).join("\n");
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}
