/**
 * The current workspace and what every screen reads from it: its notes,
 * its members (for names, avatars and roles), the inbox's unread count
 * (kept live by the notifications stream), and the theme.
 *
 * The API cannot list a user's workspaces yet. The proposed
 * `GET /v1/workspaces` is tried first (mock mode serves it); otherwise the
 * configured VITE_WORKSPACE_ID or one the user enters is used.
 */

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api/errors";
import type { Member, Note, Notification, WorkspaceRole } from "../api/schema.gen";
import { NotificationChannel } from "../ws/channels";
import { useServices } from "./services";

export type WorkspaceSummary = { id: string; name: string; role?: WorkspaceRole; notes?: number };
type NoteWithTags = Note & { tags?: string[] };

type WorkspaceState = {
  workspaces: WorkspaceSummary[];
  workspace: WorkspaceSummary | null;
  choose: (id: string, name?: string) => void;
  /** Whether the workspace list came from the API (proposed route) or configuration. */
  listed: boolean;
  notes: NoteWithTags[];
  notesById: Map<string, NoteWithTags>;
  notesLoading: boolean;
  reloadNotes: () => void;
  upsertNote: (note: NoteWithTags) => void;
  members: Member[];
  myRole: WorkspaceRole | undefined;
  userName: (id: string | undefined) => string;
  unread: number;
  setUnread: (n: number | ((n: number) => number)) => void;
  onNotification: (listener: (n: Notification) => void) => () => void;
  theme: "dark" | "light";
  toggleTheme: () => void;
  recent: string[];
  touchRecent: (noteId: string) => void;
  closeRecent: (noteId: string) => void;
};

const WorkspaceContext = createContext<WorkspaceState | null>(null);

export function useWorkspace(): WorkspaceState {
  const ws = useContext(WorkspaceContext);
  if (!ws) throw new Error("useWorkspace outside WorkspaceProvider");
  return ws;
}

const WS_KEY = "skein.workspace";
const THEME_KEY = "skein.theme";
const RECENT_KEY = "skein.recent";

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const services = useServices();
  const { api, session } = services;
  const me = session.user;
  const [workspaces, setWorkspaces] = useState<WorkspaceSummary[]>([]);
  const [listed, setListed] = useState(false);
  const [workspaceId, setWorkspaceId] = useState<string | null>(() => localStorage.getItem(WS_KEY) ?? services.defaultWorkspaceId ?? null);
  const [notes, setNotes] = useState<NoteWithTags[]>([]);
  const [notesLoading, setNotesLoading] = useState(true);
  const [notesTick, setNotesTick] = useState(0);
  const [members, setMembers] = useState<Member[]>([]);
  const [unread, setUnread] = useState(0);
  const [theme, setTheme] = useState<"dark" | "light">(() => (localStorage.getItem(THEME_KEY) as "dark" | "light" | null) ?? "dark");
  const [recent, setRecent] = useState<string[]>(() => JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]") as string[]);
  const notificationListeners = useRef(new Set<(n: Notification) => void>());

  useEffect(() => {
    api
      .proposed<{ workspaces?: WorkspaceSummary[] }>("GET", "/v1/workspaces")
      .then((res) => {
        const list = res.workspaces ?? [];
        setWorkspaces(list);
        setListed(true);
        setWorkspaceId((current) => (current && list.some((w) => w.id === current) ? current : (list[0]?.id ?? current)));
      })
      .catch((error: unknown) => {
        // NOT_FOUND / UNIMPLEMENTED: the route is not in the API yet; use configuration.
        if (!(error instanceof ApiError)) return;
        if (workspaceId) setWorkspaces([{ id: workspaceId, name: "workspace" }]);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api]);

  useEffect(() => {
    if (!workspaceId) return;
    localStorage.setItem(WS_KEY, workspaceId);
    let cancelled = false;
    setNotesLoading(true);
    api
      .listAll("DocumentService_ListNotes", { workspaceId }, (r) => r.notes as NoteWithTags[] | undefined)
      .then((list) => !cancelled && setNotes(list))
      .catch(() => !cancelled && setNotes([]))
      .finally(() => !cancelled && setNotesLoading(false));
    return () => {
      cancelled = true;
    };
  }, [api, workspaceId, notesTick]);

  useEffect(() => {
    if (!workspaceId) return;
    api
      .listAll("IdentityService_ListMembers", { workspaceId }, (r) => r.members)
      .then(setMembers)
      .catch(() => setMembers([]));
  }, [api, workspaceId]);

  const refreshUnread = useCallback(() => {
    api
      .call("NotificationService_ListInbox", { unreadOnly: true, "page.pageSize": 1 })
      .then((r) => setUnread(r.unread ?? 0))
      .catch(() => undefined);
  }, [api]);

  useEffect(() => {
    refreshUnread();
    const channel = new NotificationChannel(
      { wsBase: services.wsBase, createSocket: services.createSocket, accessToken: () => session.accessToken, refresh: () => session.refresh(api), network: services.network },
      (n) => {
        setUnread((u) => u + 1);
        for (const l of notificationListeners.current) l(n);
      },
      refreshUnread,
    );
    return () => channel.close();
  }, [api, services, session, refreshUnread]);

  useEffect(() => localStorage.setItem(THEME_KEY, theme), [theme]);
  useEffect(() => localStorage.setItem(RECENT_KEY, JSON.stringify(recent.slice(0, 8))), [recent]);

  const notesById = useMemo(() => new Map(notes.map((n) => [n.id!, n])), [notes]);
  const workspace = useMemo(() => workspaces.find((w) => w.id === workspaceId) ?? (workspaceId ? { id: workspaceId, name: "workspace" } : null), [workspaces, workspaceId]);
  const userName = useCallback(
    (id: string | undefined) => {
      if (!id) return "Someone";
      if (id === me?.id) return me.displayName ?? "You";
      return members.find((m) => m.user?.id === id)?.user?.displayName ?? "Someone";
    },
    [members, me],
  );

  const value: WorkspaceState = {
    workspaces,
    workspace,
    listed,
    choose: (id, name) => {
      setWorkspaceId(id);
      if (name && !workspaces.some((w) => w.id === id)) setWorkspaces((list) => [...list, { id, name }]);
    },
    notes,
    notesById,
    notesLoading,
    reloadNotes: () => setNotesTick((t) => t + 1),
    upsertNote: (note) => setNotes((list) => [note, ...list.filter((n) => n.id !== note.id)]),
    members,
    myRole: members.find((m) => m.user?.id === me?.id)?.role ?? workspaces.find((w) => w.id === workspaceId)?.role,
    userName,
    unread,
    setUnread,
    onNotification: (listener) => {
      notificationListeners.current.add(listener);
      return () => notificationListeners.current.delete(listener);
    },
    theme,
    toggleTheme: () => setTheme((t) => (t === "dark" ? "light" : "dark")),
    recent,
    touchRecent: (id) => setRecent((list) => [id, ...list.filter((x) => x !== id)].slice(0, 8)),
    closeRecent: (id) => setRecent((list) => list.filter((x) => x !== id)),
  };
  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}
