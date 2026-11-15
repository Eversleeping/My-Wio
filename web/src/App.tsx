import { lazy, useMemo, useState, useCallback, useEffect, Suspense } from "react";
import { useI18n } from "./i18n";
import { readLocationState, type View, navigateToLocation, navigation } from "./navigation";
import { type AuthMode, SetupScreen, LoginScreen } from "./AuthScreens";
import { type Session, type Approval } from "./types";
import { useRealtime } from "./realtime/useRealtime";
import { useData } from "./useData";
import { clearCodexSessionMemory, invalidateThreadEvents } from "./codex/useThreadEvents";
import { setSession, api, APIError, onUnauthorized } from "./api";
import { LoadingScreen, LanguageSwitch } from "./components/FormUI";
import { PageLoading } from "./components/PageUI";
import { ServersPage, CodexPageRoute } from "./pages/lazyRoutes";
import { ThemeSwitch } from "./theme";
import { UserRound, LogOut, Menu, Wifi, WifiOff, ShieldCheck, Check } from "lucide-react";

type AuthState = "loading" | "setup" | "login" | "authenticated";

const DashboardPage = lazy(() => import("./pages/DashboardPage"));

const ProjectsPageRoute = lazy(() => import("./pages/ProjectsPage"));

const GeminiPageRoute = lazy(() => import("./sessions/engines/gemini"));

const ClaudePageRoute = lazy(() => import("./sessions/engines/claude"));

const DeploymentsPageRoute = lazy(() => import("./pages/DeploymentsPage"));

const OperationsPageRoute = lazy(() => import("./pages/OperationsPage"));

const MonitoringPageRoute = lazy(() => import("./pages/MonitoringPage"));

const SettingsPageRoute = lazy(() => import("./pages/SettingsPage"));

export default function App() {
  const { t } = useI18n();
  const initialLocation = useMemo(readLocationState, []);
  const [auth, setAuth] = useState<AuthState>("loading");
  const [authMode, setAuthMode] = useState<AuthMode>("totp");
  const [session, setCurrentSession] = useState<Session | null>(null);
  const [view, setView] = useState<View>(initialLocation.view);
  const [codexThreadID, setCodexThreadID] = useState(initialLocation.threadID);
  const [mobileNav, setMobileNav] = useState(false);
  const { realtimeRevisions, streamRevisions, socketConnected, subscribe } = useRealtime(auth === "authenticated");
  const [approvalSignal, setApprovalSignal] = useState(0);
  const [toast, setToast] = useState("");
  const approvals = useData<Approval[]>(auth === "authenticated" ? "/approvals" : null, realtimeRevisions.approvals);

  const authenticate = useCallback((value: Session | null) => {
    if (!value) clearCodexSessionMemory();
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
    if (streamID !== "*") invalidateThreadEvents(streamID, minimumSequence);
  }), [subscribe]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 3500);
    return () => clearTimeout(timer);
  }, [toast]);

  useEffect(() => {
    const onPopState = () => {
      const location = readLocationState();
      setView(location.view);
      setCodexThreadID(location.threadID);
      setMobileNav(false);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

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
    const nextThreadID = next === "codex" || next === "gemini" || next === "claude" ? threadID : "";
    setView(next);
    setCodexThreadID(nextThreadID);
    setMobileNav(false);
    navigateToLocation(next, nextThreadID, replace);
  };
  const page = {
    dashboard: <Suspense fallback={<PageLoading />}><DashboardPage realtime={realtimeRevisions.dashboard} onNavigate={selectView} /></Suspense>,
    servers: <ServersPage realtime={realtimeRevisions.servers} notify={setToast} />,
    projects: <Suspense fallback={<PageLoading />}><ProjectsPageRoute realtime={realtimeRevisions.projects} notify={setToast} /></Suspense>,
    codex: <Suspense fallback={<PageLoading />}><CodexPageRoute realtime={realtimeRevisions.codex} streamRevisions={streamRevisions} approvals={approvals.data ?? []} approvalSignal={approvalSignal} reloadApprovals={approvals.reload} notify={setToast} selectedThreadID={codexThreadID} onSelectThread={(threadID, replace) => selectView("codex", threadID, replace)} /></Suspense>,
    gemini: <Suspense fallback={<PageLoading />}><GeminiPageRoute realtime={realtimeRevisions.gemini} notify={setToast} selectedThreadID={codexThreadID} onSelectThread={(threadID, replace) => selectView("gemini", threadID, replace)} /></Suspense>,
    claude: <Suspense fallback={<PageLoading />}><ClaudePageRoute realtime={realtimeRevisions.claude} notify={setToast} selectedThreadID={codexThreadID} onSelectThread={(threadID, replace) => selectView("claude", threadID, replace)} /></Suspense>,
    deployments: <Suspense fallback={<PageLoading />}><DeploymentsPageRoute realtime={realtimeRevisions.deployments} notify={setToast} /></Suspense>,
    operations: <Suspense fallback={<PageLoading />}><OperationsPageRoute realtime={realtimeRevisions.operations} /></Suspense>,
    monitoring: <Suspense fallback={<PageLoading />}><MonitoringPageRoute realtime={realtimeRevisions.monitoring} /></Suspense>,
    settings: <Suspense fallback={<PageLoading />}><SettingsPageRoute realtime={realtimeRevisions.settings} notify={setToast} /></Suspense>
  }[view];

  return (
    <div className={`app-shell ${view === "codex" || view === "claude" || view === "gemini" ? "agent-shell" : ""}`}>
      <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
        <div className="brand"><span className="brand-mark">W</span><span>{t("app.name")}</span></div>
        <nav aria-label={t("nav.primary")}>
          {navigation.map(item => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => selectView(item.id)}><Icon size={18} /><span>{t(item.labelKey)}</span>{item.id === "codex" && (approvals.data?.length ?? 0) > 0 && <b className="nav-count" aria-live="polite">{approvals.data?.length}</b>}</button>;
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
          <div className="topbar-actions"><ThemeSwitch /><LanguageSwitch /><span className={`connection ${socketConnected ? "" : "offline"}`} role="status" aria-live="polite">{socketConnected ? <Wifi size={15} /> : <WifiOff size={15} />} {t(socketConnected ? "app.live" : "app.reconnecting")}</span>{(approvals.data?.length ?? 0) > 0 && <button className="approval-pill" onClick={() => { selectView("codex"); setApprovalSignal(value => value + 1); }}><ShieldCheck size={15} />{t("codex.approvalCount", { count: approvals.data?.length ?? 0 })}</button>}</div>
        </header>
        <div className="page-content">{page}</div>
      </main>
      {toast && <div className="toast" role="status" aria-live="polite"><Check size={17} />{toast}</div>}
    </div>
  );
}
