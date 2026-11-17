/** Small presentational pieces the boards reuse: icons, avatars, role and sync badges. */

import type { ReactNode } from "react";
import type { NoteRole, WorkspaceRole } from "../api/schema.gen";
import type { SyncStatus } from "../crdt/syncState";
import { capitalise, colorFor, initials, noteRoleName, workspaceRoleName } from "../lib/format";

export const Spark = ({ style }: { style?: React.CSSProperties }) => (
  <svg className="ai-spark" style={style} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <path d="M8 1.5 9.4 6.6 14.5 8 9.4 9.4 8 14.5 6.6 9.4 1.5 8 6.6 6.6z" />
  </svg>
);

export const SearchIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
    <circle cx="7" cy="7" r="4.5" />
    <path d="M10.5 10.5 14 14" />
  </svg>
);

export const LockIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
    <rect x="3.5" y="7" width="9" height="7" rx="1.5" />
    <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
  </svg>
);

export const PlusIcon = ({ size = 14 }: { size?: number }) => (
  <svg viewBox="0 0 16 16" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
    <path d="M8 3v10M3 8h10" />
  </svg>
);

export const DragIcon = () => (
  <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    {[4, 8, 12].map((y) => (
      <g key={y}>
        <circle cx="6" cy={y} r="1.1" />
        <circle cx="10" cy={y} r="1.1" />
      </g>
    ))}
  </svg>
);

export const CheckIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M3.5 8.5l3 3 6-7" />
  </svg>
);

export const CrossIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
    <path d="M4 4l8 8M12 4l-8 8" />
  </svg>
);

export const OfflineIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
    <path d="M2 2l12 12" />
    <path d="M4.5 12h7a3 3 0 0 0 .6-5.9A4.5 4.5 0 0 0 4 6.8 2.6 2.6 0 0 0 4.5 12z" />
  </svg>
);

export const WarnIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
    <path d="M8 2 14.5 13.5h-13z" />
    <path d="M8 6.5v3M8 11.5v.01" />
  </svg>
);

export const EyeIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
    <path d="M1.5 8s2.5-4.5 6.5-4.5S14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8z" />
    <circle cx="8" cy="8" r="2" />
  </svg>
);

export function Avatar({ id, name, size = "sm", idle, title }: { id?: string; name?: string; size?: "xs" | "sm" | "md"; idle?: boolean; title?: string }) {
  return (
    <span className={`av ${size === "md" ? "" : size}${idle ? " idle" : ""}`} style={{ "--c": colorFor(id) } as React.CSSProperties} title={title ?? name} aria-label={title ?? name}>
      {initials(name)}
    </span>
  );
}

export function NoteRoleBadge({ role, suffix }: { role: NoteRole | undefined; suffix?: string }) {
  const name = noteRoleName(role);
  return (
    <span className={`sk-role ${name}`}>
      {name === "viewer" && <LockIcon />}
      {suffix ?? capitalise(name)}
    </span>
  );
}

export function WorkspaceRoleBadge({ role }: { role: WorkspaceRole | undefined }) {
  const name = workspaceRoleName(role);
  return <span className={`sk-role ${name}`}>{capitalise(name)}</span>;
}

export function SyncBadge({ status, label, count, title, onClick }: { status: SyncStatus; label: string; count?: string; title: string; onClick?: () => void }) {
  const body = (
    <>
      <span className="dot" aria-hidden="true" />
      {label}
      {count ? <b>{count}</b> : null}
    </>
  );
  return onClick ? (
    <button type="button" className={`sk-sync ${status}`} aria-live="polite" title={`${title} · click to reconnect now`} aria-label={`${label}${count ? `, ${count}` : ""}. ${title}`} onClick={onClick}>
      {body}
    </button>
  ) : (
    <span className={`sk-sync ${status}`} role="status" title={title} aria-label={`${label}${count ? `, ${count}` : ""}. ${title}`}>
      {body}
    </span>
  );
}

/** Marks UI the design has but the API cannot back yet; the PR lists each as a backend change. */
export function Pending({ children = "needs backend" }: { children?: ReactNode }) {
  return (
    <span className="sk-pending" title="Shown as designed; the API has no route for this yet">
      {children}
    </span>
  );
}

export function Spinner() {
  return <span className="spin" aria-hidden="true" />;
}
