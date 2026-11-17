/**
 * Routes and providers. Public routes (sign in, OAuth callback) sit outside
 * the workspace; everything else needs a session and a workspace.
 */

import { Suspense, lazy, useSyncExternalStore, type ReactNode } from "react";
import { BrowserRouter, MemoryRouter, Navigate, Route, Routes, useLocation } from "react-router";
import { SearchPalette } from "../search/SearchPalette";
import { AgentArea, Drafts, NotesHome, OAuthCallback, WorkspacePicker } from "../screens/Misc";
import { NoteScreen } from "../screens/NoteScreen";
import { SignIn } from "../screens/SignIn";
// Screens away from the editor load on first visit, keeping the first load small.
const Clusters = lazy(() => import("../screens/Clusters").then((m) => ({ default: m.Clusters })));
const Discover = lazy(() => import("../screens/Discover").then((m) => ({ default: m.Discover })));
const GraphExplorer = lazy(() => import("../screens/Graph").then((m) => ({ default: m.GraphExplorer })));
const History = lazy(() => import("../screens/History").then((m) => ({ default: m.History })));
const Inbox = lazy(() => import("../screens/Inbox").then((m) => ({ default: m.Inbox })));
const Members = lazy(() => import("../screens/Members").then((m) => ({ default: m.Members })));
const ReadingPlan = lazy(() => import("../screens/ReadingPlan").then((m) => ({ default: m.ReadingPlan })));

import { ServicesContext, useServices, type Services } from "./services";
import { Toasts, UiProvider, useUi } from "./ui";
import { WorkspaceProvider, useWorkspace } from "./workspace";

export function App({ services, initialPath }: { services: Services; initialPath?: string }) {
  const Router = initialPath ? MemoryRouter : BrowserRouter;
  return (
    <ServicesContext.Provider value={services}>
      <Router {...(initialPath ? { initialEntries: [initialPath] } : {})}>
        <Routes>
          <Route path="/signin" element={<Themed>{<SignIn />}</Themed>} />
          <Route path="/auth/callback" element={<Themed>{<OAuthCallback />}</Themed>} />
          <Route path="/*" element={<RequireSession />} />
        </Routes>
      </Router>
    </ServicesContext.Provider>
  );
}

/** Public pages follow the stored theme too. */
function Themed({ children }: { children: ReactNode }) {
  const theme = (localStorage.getItem("skein.theme") as "dark" | "light" | null) ?? "dark";
  // The design draws the dark theme first; a stored choice wins.
  return <div className={`fx t-${theme}`}>{children}</div>;
}

function RequireSession() {
  const { session } = useServices();
  const location = useLocation();
  const signedIn = useSyncExternalStore(session.subscribe, () => session.signedIn);
  if (!signedIn) return <Navigate to="/signin" replace state={{ from: location.pathname }} />;
  return (
    <WorkspaceProvider>
      <UiProvider>
        <Shell />
      </UiProvider>
    </WorkspaceProvider>
  );
}

function Shell() {
  const ws = useWorkspace();
  const ui = useUi();
  if (!ws.workspace && ws.listed === false && ws.workspaces.length === 0) {
    return (
      <div className={`fx t-${ws.theme}`}>
        <WorkspacePicker />
      </div>
    );
  }
  return (
    <div className={`fx t-${ws.theme}`} style={{ minHeight: "100dvh" }}>
      <Suspense fallback={<div className="sk-empty" aria-busy="true"><span className="spin" /></div>}>
      <Routes>
        <Route path="/" element={<Navigate to="/notes" replace />} />
        <Route path="/notes" element={<NotesHome />} />
        <Route path="/notes/drafts" element={<Drafts />} />
        <Route path="/notes/:noteId" element={<NoteScreen />} />
        <Route path="/notes/:noteId/history" element={<History />} />
        <Route path="/discover" element={<Discover />} />
        <Route path="/discover/clusters" element={<Clusters />} />
        <Route path="/discover/:noteId" element={<Discover />} />
        <Route path="/graph" element={<GraphExplorer />} />
        <Route path="/graph/reading-plan" element={<ReadingPlan />} />
        <Route path="/agent" element={<AgentArea />} />
        <Route path="/inbox" element={<Inbox />} />
        <Route path="/admin/members" element={<Members />} />
        <Route path="*" element={<Navigate to="/notes" replace />} />
      </Routes>
      </Suspense>
      {ui.palette.open && <SearchPalette />}
      <Toasts />
    </div>
  );
}
