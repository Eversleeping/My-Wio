import { DataProvider, clearResourceMemory } from "./data/resources";
import { lazy, useMemo, useState, useCallback, useEffect, Suspense, type MouseEvent } from "react";
import { useI18n } from "./i18n";
import { readLocationState, isEngine, type View, navigateToLocation, navigation, tasksLocation, type TaskLocation, settingsLocation, type SettingsSection } from "./navigation";
import { type AuthMode, SetupScreen, LoginScreen } from "./AuthScreens";
import { type Session, type Approval } from "./types";
import { useRealtime } from "./realtime/useRealtime";
import { useData } from "./useData";
import { clearSessionMemory, invalidateSessionMemory } from "./sessions/memory";
import { setSession, api, APIError, onUnauthorized } from "./api";
import { LoadingScreen, LanguageSwitch } from "./components/FormUI";
import { PageLoading } from "./components/PageUI";
import { ServersPage } from "./pages/lazyRoutes";
import { ThemeSwitch } from "./theme";
import { UserRound, LogOut, Menu, Wifi, WifiOff, ShieldCheck, Check } from "lucide-react";

type AuthState = "loading" | "setup" | "login" | "authenticated";

const UnifiedSessionsPage = lazy(() => import("./sessions/UnifiedSessionsPage"));

const TasksPage = lazy(() => import("./tasks/TasksPage"));

const DashboardPage = lazy(() => import("./pages/DashboardPage"));

const ProjectsPageRoute = lazy(() => import("./pages/ProjectsPage"));

const DeploymentsPageRoute = lazy(() => import("./pages/DeploymentsPage"));

const LogsPageRoute = lazy(() => import("./pages/LogsPage"));

const MonitoringPageRoute = lazy(() => import("./pages/MonitoringPage"));

const SettingsPageRoute = lazy(() => import("./pages/SettingsPage"));

export default function App() {
  const { t } = useI18n();
  const initialLocation = useMemo(readLocationState, []);
  const [auth, setAuth] = useState<AuthState>("loading");
  const [authMode, setAuthMode] = useState<AuthMode>("totp");
  const [session, setCurrentSession] = useState<Session | null>(null);
  const [view, setView] = useState<View>(initialLocation.view);
  const [sessionThreadID, setSessionThreadID] = useState(initialLocation.threadID);
  const [sessionEngine, setSessionEngine] = useState(initialLocation.engine);
  const [taskLocation, setTaskLocation] = useState<TaskLocation>(initialLocation.task ?? {});
  const [settingsSection, setSettingsSection] = useState(initialLocation.settingsSection);
  const [sessionEntry, setSessionEntry] = useState(initialLocation.entry);
  const [initialFilter, setInitialFilter] = useState(() => initialLocation.initialFilter ? {engine: initialLocation.initialFilter} : undefined);
  const [mobileNav, setMobileNav] = useState(false);
  const { realtimeRevisions, streamRevisions, socketConnected, subscribe } = useRealtime(auth === "authenticated");
  const [approvalSignal, setApprovalSignal] = useState(0);
  const [toast, setToast] = useState("");
  const approvals = useData<Approval[]>(auth === "authenticated" ? "/approvals" : null, realtimeRevisions.approvals);

  const authenticate = useCallback((value: Session | null) => {
    if (!value) { clearSessionMemory(); clearResourceMemory(); }
    setSession(value);
    setCurrentSession(value);
    setAuth(value ? "authenticated" : "login");
  }, []);

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        // Start authentication alongside setup discovery to avoid an extra
        // network round trip for returning users. Handle rejection immediately
        // even when a new installation exits through the setup screen.
        const sessionRequest = api<Session>("/auth/session", { ignoreUnauthorized: true }).then(
          value => ({ value, error: null }),
          error => ({ value: null, error })
        );
        const status = await api<{ configured: boolean; auth_mode?: AuthMode }>("/setup/status");
        if (!status.configured) {
          if (active) setAuth("setup");
          return;
        }
        if (active && status.auth_mode) setAuthMode(status.auth_mode);
        const current = await sessionRequest;
        if (current.error) throw current.error;
        if (active) authenticate(current.value);
      } catch (error) {
        if (active) setAuth(error instanceof APIError && error.status === 401 ? "login" : "login");
      }
    })();
    return () => { active = false; };
  }, [authenticate]);

  useEffect(() => {
    if (auth !== "authenticated") return;
    // Any request rejected for an expired session returns the console to sign-in.
    return onUnauthorized(() => authenticate(null));
  }, [auth, authenticate]);

  useEffect(() => subscribe((streamID, minimumSequence) => {
    if (streamID !== "*") invalidateSessionMemory(streamID, minimumSequence);
  }), [subscribe]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 3500);
    return () => clearTimeout(timer);
  }, [toast]);

  const restoreLocation = useCallback(() => {
    const location = readLocationState();
    setView(location.view);
    setSessionThreadID(location.threadID);
    setSessionEngine(location.engine);
    setSessionEntry(location.entry);
    setTaskLocation(location.task ?? {});
    setSettingsSection(location.settingsSection);
    setInitialFilter(location.initialFilter ? { engine: location.initialFilter } : undefined);
    if (isEngine(new URLSearchParams(window.location.search).get("view"))) {
      navigateToLocation(location.view, location.threadID, true, location.engine);
    }
    setMobileNav(false);
  }, []);
  useEffect(() => {
    if (isEngine(new URLSearchParams(window.location.search).get("view"))) restoreLocation();
    window.addEventListener("popstate", restoreLocation);
    return () => window.removeEventListener("popstate", restoreLocation);
  }, [restoreLocation]);
  const followConsoleLink = (event: MouseEvent<HTMLDivElement>) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || !(event.target instanceof Element)) return;
    const link = event.target.closest<HTMLAnchorElement>("a[href]");
    if (!link || (link.target && link.target !== "_self") || link.hasAttribute("download")) return;
    const url = new URL(link.href);
    if (url.origin !== window.location.origin || url.pathname !== window.location.pathname || !url.searchParams.has("view")) return;
    const view = url.searchParams.get("view");
    if (!isEngine(view) && !navigation.some(item => item.id === view)) return;
    event.preventDefault();
    if (url.href === window.location.href) return;
    window.history.pushState(null, "", url);
    restoreLocation();
  };

  if (auth === "loading") return <LoadingScreen />;
  if (auth === "setup") return <SetupScreen onReady={mode => { setAuthMode(mode); setAuth("login"); }} />;
  if (auth === "login") return <LoginScreen authMode={authMode} onLogin={authenticate} />;

  const logout = async () => {
    try {
      await api("/auth/logout", { method: "POST" });
    } catch {
      // Sign out locally even when the server cannot be reached.
    } finally {
      authenticate(null);
    }
  };
  const selectView = (next: View, threadID = "", replace = false) => {
    const nextThreadID = next === "sessions" ? threadID : "";
    setSessionEngine(undefined); setInitialFilter(undefined); setSessionEntry(undefined);
    if (next === "tasks") setTaskLocation({});
    setSettingsSection(undefined);
    setView(next);
    setSessionThreadID(nextThreadID);
    setMobileNav(false);
    navigateToLocation(next, nextThreadID, replace);
  };
  const selectSession = (ref: import("./sessions/types").SessionRef | null, replace = false) => {
    setSessionEntry(undefined);
    setView("sessions"); setSessionThreadID(ref?.id ?? ""); setSessionEngine(ref?.engine); setMobileNav(false);
    navigateToLocation("sessions", ref?.id ?? "", replace, ref?.engine);
  };
  const selectTask = (next: TaskLocation, replace = false) => {
    const url = tasksLocation(next);
    if (url !== `${window.location.pathname}${window.location.search}${window.location.hash}`) {
      window.history[replace ? "replaceState" : "pushState"](null, "", url);
    }
    setTaskLocation(next);
  };
  const selectSettingsSection = (next: SettingsSection) => {
    const url = settingsLocation(next);
    if (url !== `${window.location.pathname}${window.location.search}${window.location.hash}`) window.history.pushState(null, "", url);
    setSettingsSection(next);
  };
  const page = {
    dashboard: <Suspense fallback={<PageLoading />}><DashboardPage onNavigate={selectView} /></Suspense>,
    servers: <ServersPage notify={setToast} />,
    projects: <Suspense fallback={<PageLoading />}><ProjectsPageRoute notify={setToast} /></Suspense>,
    sessions: <Suspense fallback={<PageLoading />}><UnifiedSessionsPage entry={sessionEntry} onEntryConsumed={() => { setSessionEntry(undefined); navigateToLocation("sessions", sessionThreadID, true, sessionEngine); }} initialFilter={initialFilter} realtime={realtimeRevisions.codex + realtimeRevisions.gemini + realtimeRevisions.claude} streamRevisions={streamRevisions} approvals={approvals.data ?? []} approvalSignal={approvalSignal} reloadApprovals={approvals.reload} notify={setToast} selected={sessionEngine && sessionThreadID ? { engine: sessionEngine, id: sessionThreadID } : undefined} onSelect={selectSession} /></Suspense>,
    tasks: <Suspense fallback={<PageLoading />}><TasksPage location={taskLocation} onNavigate={selectTask} connected={socketConnected} /></Suspense>,
    deployments: <Suspense fallback={<PageLoading />}><DeploymentsPageRoute notify={setToast} /></Suspense>,
    operations: <Suspense fallback={<PageLoading />}><LogsPageRoute /></Suspense>,
    monitoring: <Suspense fallback={<PageLoading />}><MonitoringPageRoute /></Suspense>,
    settings: <Suspense fallback={<PageLoading />}><SettingsPageRoute notify={setToast} section={settingsSection} onSectionChange={selectSettingsSection} /></Suspense>
  }[view];

  return (
    <div className={`app-shell ${view === "sessions" ? "agent-shell" : ""}`} onClick={followConsoleLink}>
      <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
        <div className="brand"><span className="brand-mark">W</span><span>{t("app.name")}</span></div>
        <nav aria-label={t("nav.primary")}>
          {navigation.map(item => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => selectView(item.id)}><Icon size={18} /><span>{t(item.labelKey)}</span>{item.id === "sessions" && (approvals.data?.length ?? 0) > 0 && <b className="nav-count" aria-live="polite">{approvals.data?.length}</b>}</button>;
          })}
        </nav>
        <div className="sidebar-language"><ThemeSwitch /><LanguageSwitch /></div>
        <div className="sidebar-user"><UserRound size={17} /><span>{session?.username}</span><button className="icon-button" onClick={logout} title={t("auth.signOut")}><LogOut size={17} /></button></div>
      </aside>
      {mobileNav && <button className="nav-scrim" onClick={() => setMobileNav(false)} aria-label={t("nav.close")} />}
      <main className="workspace">
        <header className="topbar">
          <button className="icon-button mobile-menu" onClick={() => setMobileNav(true)} title={t("nav.open")}><Menu size={20} /></button>
          <div><p className="eyebrow">{t("app.controlPlane")}</p><h1>{t(navigation.find(item => item.id === view)?.labelKey ?? "nav.overview")}</h1></div>
          <div className="topbar-actions"><ThemeSwitch /><LanguageSwitch /><span className={`connection ${socketConnected ? "" : "offline"}`} role="status" aria-live="polite">{socketConnected ? <Wifi size={15} /> : <WifiOff size={15} />} {t(socketConnected ? "app.live" : "app.reconnecting")}</span>{(approvals.data?.length ?? 0) > 0 && <button className="approval-pill" onClick={() => { selectView("sessions"); setApprovalSignal(value => value + 1); }}><ShieldCheck size={15} />{t("codex.approvalCount", { count: approvals.data?.length ?? 0 })}</button>}</div>
        </header>
        <div className="page-content"><DataProvider revisions={realtimeRevisions} connected={socketConnected}>{page}</DataProvider></div>
      </main>
      {toast && <div className="toast" role="status" aria-live="polite"><Check size={17} />{toast}</div>}
    </div>
  );
}
