import { FormEvent, KeyboardEvent as ReactKeyboardEvent, lazy, PointerEvent as ReactPointerEvent, ReactNode, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Archive,
  ArchiveRestore,
  AlertTriangle,
  ArrowDownToLine,
  ArrowRightLeft,
  ArrowUpFromLine,
  Ban,
  BellRing,
  Bot,
  Boxes,
  Braces,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronRight,
  Clipboard,
  Code2,
  Copy,
  Cpu,
  Database,
  ExternalLink,
  EyeOff,
  File as FileIcon,
  FileCode2,
  FileDiff,
  Folder,
  FolderOpen,
  FolderTree,
  GitFork,
  GitBranch,
  GitCommit,
  Gauge,
  HardDrive,
  History,
  Image as ImageIcon,
  KeyRound,
  LayoutDashboard,
  Link,
  LoaderCircle,
  LockKeyhole,
  LogOut,
  MapPin,
  Menu,
  MemoryStick,
  MessageSquare,
  Minimize2,
  MonitorDot,
  Network,
  Pause,
  Pencil,
  Pin,
  PinOff,
  Play,
  Plus,
  RefreshCw,
  Rocket,
  RotateCcw,
  Search,
  Server as ServerIcon,
  Settings,
  ShieldCheck,
  Square,
  SquareTerminal,
  StickyNote,
  Target,
  Trash2,
  Undo2,
  UserRound,
  Users,
  Wrench,
  Wifi,
  WifiOff,
  X
} from "lucide-react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { api, APIError, patch, post, put, remove, setSession, socketURL } from "./api";
import { LoginScreen, SetupScreen, type AuthMode } from "./AuthScreens";
import { ContextMenu, type ContextMenuAction } from "./ContextMenu";
import { SlashCommandMenu, type SlashCommandItem } from "./SlashCommandMenu";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "./components/Dialog";
import { ConfirmDialog } from "./components/ConfirmDialog";
import { DataTable, Empty, ErrorState, PageLoading, Section, Status } from "./components/PageUI";
import { VirtualizedItems, VirtualizedList } from "./components/VirtualizedList";
import { clearCodexComposerPreferences, defaultCodexComposerPreferences, loadCodexComposerPreferences, saveCodexComposerPreferences, type CodexComposerPreferences } from "./codexComposerPreferences";
import { formatDate, formatTime, relative, shortSHA } from "./format";
import { currentLocale, useI18n } from "./i18n";
import { compressImage } from "./imageCompression";
import { clearDataCacheMatching, useData } from "./useData";
import type {
  Alert,
  Approval,
  AuditEntry,
  CodexCLISettings,
  CodexGoal,
  CodexMCPServer,
  CodexSkill,
  CodexSnapshot,
  CodexStatusData,
  CredentialProfile,
  Deployment,
  DeploymentDetail,
  DeploymentTarget,
  Metric,
  OperationMetrics,
  Project,
  SecretSet,
  Server,
  Session,
  ScheduledTask,
  StreamEvent,
  Thread,
  Workspace,
  WorkspaceChange,
  WorkspaceChangesSnapshot,
  WorkspaceDiffPreview,
  WorkspaceGitSnapshot,
  WorkspaceFile,
  WorkspaceFilePreview,
  WorkspaceFilesSnapshot
} from "./types";

type View = "dashboard" | "servers" | "projects" | "codex" | "deployments" | "monitoring" | "settings";
type RealtimeScope = View | "approvals";
type RealtimeRevisions = Record<RealtimeScope, number>;
type ConversationDisplayItem = { type: "event"; event: StreamEvent } | { type: "commandGroup"; events: StreamEvent[] };
type AuthState = "loading" | "setup" | "login" | "authenticated";
type ComposerImage = { id: string; dataURL: string };
export type FilePreviewSelection = { path: string; line?: number; mode?: "file" | "diff" };
type SubagentActivity = { threadID: string; path: string; status: string; message: string; prompt: string; model: string; reasoningEffort: string; updatedAt: string };
const DashboardPage = lazy(() => import("./pages/DashboardPage"));
const ServersPageRoute = lazy(() => import("./pages/ServersPage"));
const ProjectsPageRoute = lazy(() => import("./pages/ProjectsPage"));
const CodexPageRoute = lazy(() => import("./pages/CodexPage"));
const DeploymentsPageRoute = lazy(() => import("./pages/DeploymentsPage"));
const MonitoringPageRoute = lazy(() => import("./pages/MonitoringPage"));
const SettingsPageRoute = lazy(() => import("./pages/SettingsPage"));
const HighlightedFile = lazy(() => import("./FilePreviewCode"));
const HighlightedDiff = lazy(() => import("./FileDiffCode"));
type StreamRevision = { revision: number; minimumSequence: number | null };
type StreamRevisions = Record<string, StreamRevision>;
type ThreadEventsCacheEntry = { events: StreamEvent[]; dependency: unknown; globalRevision: number; streamRevision: number; hasEarlier: boolean };
type ThreadListPage = { items: Thread[]; has_more: boolean; next: number | null };
type AuditListPage = { items: AuditEntry[]; has_more: boolean; next: number | null };

const threadEventsCache = new Map<string, ThreadEventsCacheEntry>();
const codexScrollPositions = new Map<string, number>();
const codexAutoFollowThreshold = 96;
const threadEventsPageSize = 500;
const threadListPageSize = 50;
const auditListPageSize = 50;
const allRealtimeScopes: RealtimeScope[] = ["dashboard", "servers", "projects", "codex", "deployments", "monitoring", "settings", "approvals"];
const initialRealtimeRevisions = (): RealtimeRevisions => ({ dashboard: 0, servers: 0, projects: 0, codex: 0, deployments: 0, monitoring: 0, settings: 0, approvals: 0 });

export function realtimeScopesForEvent(event: Pick<Partial<StreamEvent>, "kind">): RealtimeScope[] {
  const kind = event.kind ?? "";
  if (kind === "inventory.updated") return ["dashboard", "servers", "projects", "deployments", "monitoring"];
  if (kind.startsWith("deployment.")) return ["dashboard", "deployments", "monitoring"];
  if (kind.startsWith("agent.")) return ["dashboard", "servers", "projects", "deployments", "monitoring"];
  if (kind.startsWith("approval.") || kind.startsWith("codex.approval.")) return ["codex", "approvals"];
  if (kind === "user.message" || kind.startsWith("codex.") || kind.startsWith("thread.")) return ["dashboard", "codex"];
  return allRealtimeScopes;
}

function setScrollTopImmediately(element: HTMLElement, top: number) {
  const scrollBehavior = element.style.scrollBehavior;
  element.style.scrollBehavior = "auto";
  element.scrollTop = top;
  element.style.scrollBehavior = scrollBehavior;
}

function latestScrollTop(element: HTMLElement) {
  return Math.max(0, element.scrollHeight - element.clientHeight);
}

function isNearCodexStreamBottom(element: HTMLElement) {
  return latestScrollTop(element) - element.scrollTop <= codexAutoFollowThreshold;
}

export function clearCodexSessionMemory(threadID?: string) {
  const eventPrefix = threadID ? `/threads/${threadID}/events?` : "/threads/";
  clearDataCacheMatching(path => path.startsWith(eventPrefix) && path.includes("/events?"));
  for (const path of threadEventsCache.keys()) {
    if (!threadID || path.startsWith(`/threads/${threadID}/events?`)) threadEventsCache.delete(path);
  }
  for (const key of codexScrollPositions.keys()) {
    if (!threadID || key.startsWith(`${threadID}:`)) codexScrollPositions.delete(key);
  }
}

const defaultCodexModel = defaultCodexComposerPreferences.model;
const codexModelOptions = [
  { value: "gpt-5.6-sol", labelKey: "codex.model56Sol" },
  { value: "gpt-5.6-terra", labelKey: "codex.model56Terra" },
  { value: "gpt-5.6-luna", labelKey: "codex.model56Luna" },
  { value: "gpt-5.5", labelKey: "codex.model55" }
] as const;
const codexReasoningOptions = [
  { value: "low", labelKey: "codex.reasoningLow" },
  { value: "medium", labelKey: "codex.reasoningMedium" },
  { value: "high", labelKey: "codex.reasoningHigh" },
  { value: "xhigh", labelKey: "codex.reasoningExtraHigh" },
  { value: "max", labelKey: "codex.reasoningMax" }
] as const;

const navigation: Array<{ id: View; labelKey: string; icon: typeof LayoutDashboard }> = [
  { id: "dashboard", labelKey: "nav.overview", icon: LayoutDashboard },
  { id: "servers", labelKey: "nav.servers", icon: ServerIcon },
  { id: "projects", labelKey: "nav.projects", icon: GitBranch },
  { id: "codex", labelKey: "nav.codex", icon: Code2 },
  { id: "deployments", labelKey: "nav.deployments", icon: Rocket },
  { id: "monitoring", labelKey: "nav.monitoring", icon: Activity },
  { id: "settings", labelKey: "nav.settings", icon: Settings }
];

function readLocationState(): { view: View; threadID: string } {
  const params = new URLSearchParams(window.location.search);
  const requestedView = params.get("view");
  const view = navigation.some(item => item.id === requestedView) ? requestedView as View : "dashboard";
  return { view, threadID: view === "codex" ? params.get("thread") ?? "" : "" };
}

export function locationFor(view: View, threadID = "") {
  const params = new URLSearchParams();
  if (view !== "dashboard") params.set("view", view);
  if (view === "codex" && threadID) params.set("thread", threadID);
  const query = params.toString();
  return `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`;
}

export default function App() {
  const { t } = useI18n();
  const initialLocation = useMemo(readLocationState, []);
  const [auth, setAuth] = useState<AuthState>("loading");
  const [authMode, setAuthMode] = useState<AuthMode>("totp");
  const [session, setCurrentSession] = useState<Session | null>(null);
  const [view, setView] = useState<View>(initialLocation.view);
  const [codexThreadID, setCodexThreadID] = useState(initialLocation.threadID);
  const [mobileNav, setMobileNav] = useState(false);
  const [realtimeRevisions, setRealtimeRevisions] = useState<RealtimeRevisions>(initialRealtimeRevisions);
  const [streamRevisions, setStreamRevisions] = useState<StreamRevisions>({});
  const [socketConnected, setSocketConnected] = useState(false);
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
        const status = await api<{ configured: boolean; auth_mode?: AuthMode }>("/setup/status");
        if (!status.configured) {
          if (active) setAuth("setup");
          return;
        }
        if (active && status.auth_mode) setAuthMode(status.auth_mode);
        const current = await api<Session>("/auth/session");
        if (active) authenticate(current);
      } catch (error) {
        if (active) setAuth(error instanceof APIError && error.status === 401 ? "login" : "login");
      }
    })();
    return () => { active = false; };
  }, [authenticate]);

  useEffect(() => {
    if (auth !== "authenticated") return;
    let socket: WebSocket | null = null;
    let timer = 0;
    let refreshTimer = 0;
    let stopped = false;
    const pendingStreams = new Map<string, number | null>();
    const pendingRealtimeScopes = new Set<RealtimeScope>();
    const markPendingStream = (streamID: string, sequence?: number) => {
      const nextSequence = typeof sequence === "number" && Number.isInteger(sequence) && sequence > 0 ? sequence : null;
      const current = pendingStreams.get(streamID);
      if (current === undefined) pendingStreams.set(streamID, nextSequence);
      else if (current === null || nextSequence === null) pendingStreams.set(streamID, null);
      else pendingStreams.set(streamID, Math.min(current, nextSequence));
    };
    const markPendingRealtimeScopes = (scopes: RealtimeScope[]) => {
      for (const scope of scopes) pendingRealtimeScopes.add(scope);
    };
    const scheduleRefresh = () => {
      if (refreshTimer) return;
      refreshTimer = window.setTimeout(() => {
        refreshTimer = 0;
        if (pendingRealtimeScopes.size > 0) {
          const scopes = Array.from(pendingRealtimeScopes);
          pendingRealtimeScopes.clear();
          setRealtimeRevisions(current => {
            const next = { ...current };
            for (const scope of scopes) next[scope] += 1;
            return next;
          });
        }
        if (pendingStreams.size > 0) {
          const streams = Array.from(pendingStreams.entries());
          pendingStreams.clear();
          setStreamRevisions(current => {
            const next = { ...current };
            for (const [streamID, minimumSequence] of streams) next[streamID] = { revision: (next[streamID]?.revision ?? 0) + 1, minimumSequence };
            return next;
          });
        }
      }, 100);
    };
    const connect = () => {
      if (stopped) return;
      socket = new WebSocket(socketURL());
      socket.onopen = () => { setSocketConnected(true); markPendingStream("*"); markPendingRealtimeScopes(allRealtimeScopes); scheduleRefresh(); };
      socket.onmessage = messageEvent => {
        try {
          const event = JSON.parse(String(messageEvent.data)) as Partial<StreamEvent>;
          markPendingStream(event.stream_id || "*", event.sequence);
          markPendingRealtimeScopes(realtimeScopesForEvent(event));
        } catch {
          markPendingStream("*");
          markPendingRealtimeScopes(allRealtimeScopes);
        }
        scheduleRefresh();
      };
      socket.onclose = () => {
        setSocketConnected(false);
        if (!stopped) timer = window.setTimeout(connect, 2500);
      };
    };
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      clearTimeout(refreshTimer);
      socket?.close();
    };
  }, [auth]);

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
    await api("/auth/logout", { method: "POST" });
    authenticate(null);
  };
  const selectView = (next: View, threadID = "", replace = false) => {
    const nextThreadID = next === "codex" ? threadID : "";
    setView(next);
    setCodexThreadID(nextThreadID);
    setMobileNav(false);
    const nextLocation = locationFor(next, nextThreadID);
    if (`${window.location.pathname}${window.location.search}${window.location.hash}` !== nextLocation) window.history[replace ? "replaceState" : "pushState"](null, "", nextLocation);
  };
  const page = {
    dashboard: <Suspense fallback={<PageLoading />}><DashboardPage realtime={realtimeRevisions.dashboard} onNavigate={selectView} /></Suspense>,
    servers: <ServersPage realtime={realtimeRevisions.servers} notify={setToast} />,
    projects: <Suspense fallback={<PageLoading />}><ProjectsPageRoute realtime={realtimeRevisions.projects} notify={setToast} /></Suspense>,
    codex: <Suspense fallback={<PageLoading />}><CodexPageRoute realtime={realtimeRevisions.codex} streamRevisions={streamRevisions} approvals={approvals.data ?? []} approvalSignal={approvalSignal} reloadApprovals={approvals.reload} notify={setToast} selectedThreadID={codexThreadID} onSelectThread={(threadID, replace) => selectView("codex", threadID, replace)} /></Suspense>,
    deployments: <Suspense fallback={<PageLoading />}><DeploymentsPageRoute realtime={realtimeRevisions.deployments} notify={setToast} /></Suspense>,
    monitoring: <Suspense fallback={<PageLoading />}><MonitoringPageRoute realtime={realtimeRevisions.monitoring} /></Suspense>,
    settings: <Suspense fallback={<PageLoading />}><SettingsPageRoute realtime={realtimeRevisions.settings} notify={setToast} /></Suspense>
  }[view];

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
        <div className="brand"><span className="brand-mark">W</span><span>{t("app.name")}</span></div>
        <nav aria-label={t("nav.primary")}>
          {navigation.map(item => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => selectView(item.id)}><Icon size={18} /><span>{t(item.labelKey)}</span>{item.id === "codex" && (approvals.data?.length ?? 0) > 0 && <b className="nav-count" aria-live="polite">{approvals.data?.length}</b>}</button>;
          })}
        </nav>
        <div className="sidebar-language"><LanguageSwitch /></div>
        <div className="sidebar-user"><UserRound size={17} /><span>{session?.username}</span><button className="icon-button" onClick={logout} title={t("auth.signOut")}><LogOut size={17} /></button></div>
      </aside>
      {mobileNav && <button className="nav-scrim" onClick={() => setMobileNav(false)} aria-label={t("nav.close")} />}
      <main className="workspace">
        <header className="topbar">
          <button className="icon-button mobile-menu" onClick={() => setMobileNav(true)} title={t("nav.open")}><Menu size={20} /></button>
          <div><p className="eyebrow">{t("app.controlPlane")}</p><h1>{t(navigation.find(item => item.id === view)?.labelKey ?? "nav.overview")}</h1></div>
          <div className="topbar-actions"><LanguageSwitch /><span className={`connection ${socketConnected ? "" : "offline"}`} role="status" aria-live="polite">{socketConnected ? <Wifi size={15} /> : <WifiOff size={15} />} {t(socketConnected ? "app.live" : "app.reconnecting")}</span>{(approvals.data?.length ?? 0) > 0 && <button className="approval-pill" onClick={() => { selectView("codex"); setApprovalSignal(value => value + 1); }}><ShieldCheck size={15} />{t("codex.approvalCount", { count: approvals.data?.length ?? 0 })}</button>}</div>
        </header>
        <div className="page-content">{page}</div>
      </main>
      {toast && <div className="toast" role="status" aria-live="polite"><Check size={17} />{toast}</div>}
    </div>
  );
}

function LoadingScreen() {
  const { t } = useI18n();
  return <div className="auth-layout"><div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong></div><LoaderCircle className="spin" size={28} /></div>;
}

export function ServersPage({ realtime, notify }: PageProps) {
  return <Suspense fallback={<PageLoading />}><ServersPageRoute realtime={realtime} notify={notify} /></Suspense>;
}


export function mergeThreads(...pages: Thread[][]) {
  const merged: Thread[] = [];
  const seen = new Set<string>();
  for (const page of pages) for (const thread of page) {
    if (seen.has(thread.id)) continue;
    seen.add(thread.id);
    merged.push(thread);
  }
  return merged;
}

export function useThreadList(archived: boolean, realtime: unknown) {
  const firstPath = archived ? `/threads?archived=true&limit=${threadListPageSize}` : `/threads?limit=${threadListPageSize}`;
  const firstPage = useData<ThreadListPage>(firstPath, realtime);
  const [tail, setTail] = useState<Thread[]>([]);
  const [tailLoaded, setTailLoaded] = useState(false);
  const [tailNext, setTailNext] = useState<number | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadError, setLoadError] = useState("");
  const requestRef = useRef(0);
  const tailRef = useRef(tail);
  const tailLoadedRef = useRef(tailLoaded);
  tailRef.current = tail;
  tailLoadedRef.current = tailLoaded;
  useEffect(() => () => { requestRef.current += 1; }, []);
  const items = useMemo(() => mergeThreads(firstPage.data?.items ?? [], tail), [firstPage.data?.items, tail]);
  const next = tailLoaded ? tailNext : firstPage.data?.next ?? null;
  const hasMore = tailLoaded ? tailNext !== null : Boolean(firstPage.data?.has_more);
  const loadMore = useCallback(async () => {
    if (loadingMore || next === null) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    try {
      const prefix = archived ? "/threads?archived=true&" : "/threads?";
      const page = await api<ThreadListPage>(`${prefix}limit=${threadListPageSize}&offset=${next}`);
      if (request !== requestRef.current) return;
      setTail(current => mergeThreads(current, page.items));
      setTailLoaded(true);
      setTailNext(page.next);
    } catch (error) {
      if (request === requestRef.current) setLoadError(message(error));
    } finally {
      if (request === requestRef.current) setLoadingMore(false);
    }
  }, [archived, loadingMore, next]);
  useEffect(() => {
    if (!firstPage.data || !tailLoadedRef.current) return;
    const loadedTailCount = tailRef.current.length;
    if (loadedTailCount === 0) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    void (async () => {
      try {
        const refreshed: Thread[] = [];
        let offset: number | null = threadListPageSize;
        while (offset !== null && refreshed.length < loadedTailCount) {
          const prefix = archived ? "/threads?archived=true&" : "/threads?";
          const page: ThreadListPage = await api<ThreadListPage>(`${prefix}limit=${threadListPageSize}&offset=${offset}`);
          if (request !== requestRef.current) return;
          refreshed.push(...page.items);
          offset = page.next;
        }
        if (request !== requestRef.current) return;
        setTail(mergeThreads(refreshed));
        setTailNext(offset);
      } catch (error) {
        if (request === requestRef.current) setLoadError(message(error));
      } finally {
        if (request === requestRef.current) setLoadingMore(false);
      }
    })();
  }, [archived, firstPage.data]);
  const reload = useCallback(() => firstPage.reload(), [firstPage.reload]);
  return { data: firstPage.data ? items : null, error: firstPage.error, loading: firstPage.loading, reload, hasMore, loadingMore, loadError, loadMore };
}

function mergeAuditEntries(...pages: AuditEntry[][]) {
  const merged: AuditEntry[] = [];
  const seen = new Set<string>();
  for (const page of pages) for (const entry of page) {
    if (seen.has(entry.id)) continue;
    seen.add(entry.id);
    merged.push(entry);
  }
  return merged;
}

function useAuditList(realtime: unknown) {
  const firstPage = useData<AuditListPage>(`/audit?limit=${auditListPageSize}`, realtime);
  const [tail, setTail] = useState<AuditEntry[]>([]);
  const [tailLoaded, setTailLoaded] = useState(false);
  const [tailNext, setTailNext] = useState<number | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadError, setLoadError] = useState("");
  const requestRef = useRef(0);
  const tailRef = useRef(tail);
  const tailLoadedRef = useRef(tailLoaded);
  tailRef.current = tail;
  tailLoadedRef.current = tailLoaded;
  useEffect(() => () => { requestRef.current += 1; }, []);
  const items = useMemo(() => mergeAuditEntries(firstPage.data?.items ?? [], tail), [firstPage.data?.items, tail]);
  const next = tailLoaded ? tailNext : firstPage.data?.next ?? null;
  const hasMore = tailLoaded ? tailNext !== null : Boolean(firstPage.data?.has_more);
  const loadMore = useCallback(async () => {
    if (loadingMore || next === null) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    try {
      const page = await api<AuditListPage>(`/audit?limit=${auditListPageSize}&offset=${next}`);
      if (request !== requestRef.current) return;
      setTail(current => mergeAuditEntries(current, page.items));
      setTailLoaded(true);
      setTailNext(page.next);
    } catch (error) {
      if (request === requestRef.current) setLoadError(message(error));
    } finally {
      if (request === requestRef.current) setLoadingMore(false);
    }
  }, [loadingMore, next]);
  useEffect(() => {
    if (!firstPage.data || !tailLoadedRef.current) return;
    const loadedTailCount = tailRef.current.length;
    if (loadedTailCount === 0) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    void (async () => {
      try {
        const refreshed: AuditEntry[] = [];
        let offset: number | null = auditListPageSize;
        while (offset !== null && refreshed.length < loadedTailCount) {
          const page: AuditListPage = await api<AuditListPage>(`/audit?limit=${auditListPageSize}&offset=${offset}`);
          if (request !== requestRef.current) return;
          refreshed.push(...page.items);
          offset = page.next;
        }
        if (request !== requestRef.current) return;
        setTail(mergeAuditEntries(refreshed));
        setTailNext(offset);
      } catch (error) {
        if (request === requestRef.current) setLoadError(message(error));
      } finally {
        if (request === requestRef.current) setLoadingMore(false);
      }
    })();
  }, [firstPage.data]);
  const reload = useCallback(() => firstPage.reload(), [firstPage.reload]);
  return { data: firstPage.data ? items : null, error: firstPage.error, loading: firstPage.loading, reload, hasMore, loadingMore, loadError, loadMore };
}


export type CodexPageProps = PageProps & {
  streamRevisions: Record<string, { revision: number; minimumSequence: number | null }>;
  approvals: Approval[];
  approvalSignal: number;
  reloadApprovals: () => void;
  selectedThreadID: string;
  onSelectThread: (threadID: string, replace?: boolean) => void;
};

export function CodexPage(props: CodexPageProps) {
  return <Suspense fallback={<PageLoading />}><CodexPageRoute {...props} /></Suspense>;
}

export function groupThreadsByWorkspace(threads: Thread[], workspaces: Workspace[]) {
  const workspacesByID = new Map(workspaces.map(workspace => [workspace.id, workspace]));
  const groups = new Map<string, ThreadGroup>();
  for (const thread of threads) {
    const workspace = workspacesByID.get(thread.workspace_id);
    const path = workspace?.path || thread.path;
    const pathName = path.split(/[\\/]/).filter(Boolean).at(-1) || path;
    const workspaceName = workspace?.display_name?.trim();
    const branch = workspace?.branch?.trim();
    const identity = [workspaceName, branch, pathName].find(value => value && value !== thread.project_name) || pathName || thread.workspace_id;
    const group = groups.get(thread.workspace_id) ?? { workspaceID: thread.workspace_id, projectID: thread.project_id, projectName: thread.project_name, label: `${thread.project_name} · ${identity}`, path, pinnedAt: thread.project_pinned_at, threads: [] };
    group.threads.push(thread);
    groups.set(thread.workspace_id, group);
  }
  const pinnedFirst = (left: string | null, right: string | null) => left && !right ? -1 : !left && right ? 1 : left && right ? right.localeCompare(left) : 0;
  for (const group of groups.values()) group.threads.sort((left, right) => pinnedFirst(left.pinned_at, right.pinned_at) || right.updated_at.localeCompare(left.updated_at));
  return Array.from(groups.values()).sort((left, right) => pinnedFirst(left.pinnedAt, right.pinnedAt) || left.projectName.localeCompare(right.projectName) || left.label.localeCompare(right.label));
}

export type ThreadGroup = { workspaceID: string; projectID: string; projectName: string; label: string; path: string; pinnedAt: string | null; threads: Thread[] };

type FileTreeNode = { name: string; path: string; kind: WorkspaceFile["kind"]; size?: number; children: FileTreeNode[] };

export function WorkspaceFilesPanel({ workspaceID, taskID, taskStatus, realtime, notify, writable = true, activePath, activeMode, onOpenFile }: { workspaceID: string | null; taskID: string; taskStatus: string; realtime: number; notify: (text: string) => void; writable?: boolean; activePath: string; activeMode: "file" | "diff"; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<"files" | "changes">("files");
  const snapshot = useData<WorkspaceFilesSnapshot>(workspaceID ? `/workspaces/${workspaceID}/files` : null, `${realtime}:${workspaceID}`);
  const changes = useData<WorkspaceChangesSnapshot>(workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/changes` : null, `${realtime}:${workspaceID}:${mode}`);
  const git = useData<WorkspaceGitSnapshot>(workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/git` : null, `${realtime}:${workspaceID}:${mode}`);
  const [requestedWorkspace, setRequestedWorkspace] = useState("");
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [commitMessage, setCommitMessage] = useState("");
  const [gitAction, setGitAction] = useState<"" | "commit" | "pull" | "push">("");
  const previousTask = useRef({ id: taskID, status: taskStatus });
  const syncedGitUpdate = useRef("");
  const currentSnapshot = snapshot.data?.workspace_id === workspaceID ? snapshot.data : null;
  const currentChanges = changes.data?.workspace_id === workspaceID ? changes.data : null;
  const currentGit = git.data?.workspace_id === workspaceID ? git.data : null;
  const gitData = currentGit?.data;
  const remote = gitData?.remotes?.[0]?.name ?? "";
  const branch = gitData?.status.branch ?? "";
  const fileScanning = currentSnapshot?.status === "scanning";
  const changeScanning = currentChanges?.status === "scanning";
  const refreshFiles = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/files/refresh`, {});
      snapshot.reload();
      if (!silent) notify(t("codex.fileScanQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [notify, snapshot.reload, t, workspaceID]);
  const refreshChanges = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/changes/refresh`, {});
      changes.reload();
      if (!silent) notify(t("codex.changeScanQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [changes.reload, notify, t, workspaceID]);
  const refreshGit = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/git/refresh`, {});
      git.reload();
      if (!silent) notify(t("project.gitRefreshQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [git.reload, notify, t, workspaceID]);
  useEffect(() => {
    setMode("files");
    setExpanded(new Set());
    setRequestedWorkspace("");
    setCommitMessage("");
    setGitAction("");
    syncedGitUpdate.current = "";
  }, [workspaceID]);
  useEffect(() => {
    if (workspaceID && currentSnapshot?.status === "idle" && requestedWorkspace !== workspaceID) {
      setRequestedWorkspace(workspaceID);
      void refreshFiles(true);
    }
  }, [currentSnapshot?.status, refreshFiles, requestedWorkspace, workspaceID]);
  useEffect(() => {
    const previous = previousTask.current;
    previousTask.current = { id: taskID, status: taskStatus };
    const wasActive = previous.status === "queued" || previous.status === "running";
    const isActive = taskStatus === "queued" || taskStatus === "running";
    if (mode === "changes" && workspaceID && previous.id === taskID && wasActive && !isActive) void refreshChanges(true);
  }, [mode, refreshChanges, taskID, taskStatus, workspaceID]);
  useEffect(() => {
    const gitUpdated = currentGit?.updated_at ?? "";
    const changesUpdated = currentChanges?.updated_at ?? "";
    if (mode !== "changes" || currentGit?.status !== "succeeded" || !gitUpdated || gitUpdated <= changesUpdated || syncedGitUpdate.current === gitUpdated) return;
    syncedGitUpdate.current = gitUpdated;
    void refreshChanges(true);
  }, [currentChanges?.updated_at, currentGit?.status, currentGit?.updated_at, mode, refreshChanges]);
  const tree = useMemo(() => buildFileTree(currentSnapshot?.files ?? []), [currentSnapshot?.files]);
  const toggle = (path: string) => setExpanded(current => { const next = new Set(current); if (next.has(path)) next.delete(path); else next.add(path); return next; });
  const toggleMode = () => {
    if (mode === "changes") {
      setMode("files");
      return;
    }
    setMode("changes");
    void refreshChanges(true);
    void refreshGit(true);
  };
  const runGitAction = async (action: "commit" | "pull" | "push") => {
    if (!workspaceID || gitAction) return;
    const messageText = commitMessage.trim();
    if (action === "commit" && !messageText) return;
    setGitAction(action);
    try {
      if (action === "commit") {
        await post(`/workspaces/${workspaceID}/git/commit`, { message: messageText, all: true });
        setCommitMessage("");
      } else if (action === "pull") {
        await post(`/workspaces/${workspaceID}/git/pull`, { remote, branch });
      } else {
        await post(`/workspaces/${workspaceID}/git/push`, { remote, ref: branch, set_upstream: !gitData?.status.upstream });
      }
      notify(t("project.gitActionQueued"));
      git.reload();
      changes.reload();
    } catch (error) {
      notify(message(error));
    } finally {
      setGitAction("");
    }
  };
  const busy = mode === "changes" ? changeScanning || currentGit?.status === "refreshing" : fileScanning;
  const refresh = () => {
    if (mode === "changes") {
      void refreshChanges();
      void refreshGit(true);
    } else {
      void refreshFiles();
    }
  };
  const changeCount = currentChanges?.changes.length ?? 0;
  const writeBusy = Boolean(gitAction) || currentGit?.status === "refreshing";
  const writeDisabled = !workspaceID || !writable || writeBusy;
  return <section className={`workspace-files ${mode === "changes" ? "changes-mode" : ""}`}>
    <div className="panel-heading"><div>{mode === "changes" ? <FileDiff size={17} /> : <FolderTree size={17} />}<h2>{t(mode === "changes" ? "codex.changedFiles" : "codex.projectFiles")}</h2></div><div className="row-actions"><button className={`icon-button ${mode === "changes" ? "active" : ""}`} type="button" aria-pressed={mode === "changes"} disabled={!workspaceID} title={t(mode === "changes" ? "codex.showProjectFiles" : "codex.showChangedFiles")} onClick={toggleMode}><FileDiff size={16} /></button><button className="icon-button" type="button" disabled={!workspaceID || busy} title={t(mode === "changes" ? "codex.refreshChanges" : "codex.refreshFiles")} onClick={refresh}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div></div>
    <div className="workspace-file-body">{mode === "changes" ? <ChangedFilesView workspaceID={workspaceID} snapshot={currentChanges} loading={changes.loading} activePath={activeMode === "diff" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "diff" })} /> : !workspaceID ? <Empty icon={<FolderTree size={22} />} text={t("codex.selectWorkspace")} /> : !currentSnapshot ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : currentSnapshot.status === "failed" ? <div className="file-tree-error"><AlertTriangle size={16} /><span>{currentSnapshot.error || t("codex.fileScanFailed")}</span></div> : fileScanning && tree.length === 0 ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : tree.length === 0 ? <Empty icon={<Folder size={22} />} text={t("codex.noProjectFiles")} /> : <div className="file-tree">{tree.map(node => <FileTreeItem key={node.path} node={node} depth={0} expanded={expanded} onToggle={toggle} activePath={activeMode === "file" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "file" })} />)}{currentSnapshot.truncated && <div className="file-tree-note">{t("codex.fileListTruncated")}</div>}</div>}</div>
    {mode === "changes" && <div className="workspace-change-actions">
      <form onSubmit={event => { event.preventDefault(); void runGitAction("commit"); }}><input aria-label={t("project.gitCommitMessage")} value={commitMessage} maxLength={20 * 1024} disabled={writeDisabled} placeholder={t("project.gitCommitPlaceholder")} onChange={event => setCommitMessage(event.target.value)} /><button className="primary-button small" disabled={writeDisabled || changeCount === 0 || !commitMessage.trim()}>{gitAction === "commit" ? <LoaderCircle className="spin" size={14} /> : <GitCommit size={14} />}{t("project.gitCommitAction")}</button></form>
      <div><button type="button" className="secondary-button small" disabled={writeDisabled || changeCount > 0 || !remote || !branch} title={changeCount > 0 ? t("codex.pullBlockedByChanges") : t("project.gitPull")} onClick={() => void runGitAction("pull")}>{gitAction === "pull" ? <LoaderCircle className="spin" size={14} /> : <ArrowRightLeft size={14} />}{t("project.gitPull")}</button><button type="button" className="secondary-button small" disabled={writeDisabled || !remote || !branch} title={t("project.gitPush")} onClick={() => void runGitAction("push")}>{gitAction === "push" ? <LoaderCircle className="spin" size={14} /> : <ArrowUpFromLine size={14} />}{t("project.gitPush")}</button></div>
    </div>}
  </section>;
}

const changeStatusKeys: Record<string, string> = { modified: "codex.changeModified", added: "codex.changeAdded", deleted: "codex.changeDeleted", renamed: "codex.changeRenamed", copied: "codex.changeCopied", untracked: "codex.changeAdded", conflicted: "codex.changeConflicted" };
const changeStatusCodes: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "?", conflicted: "!" };

export function ChangedFilesView({ workspaceID, snapshot, loading, activePath, onOpenFile }: { workspaceID: string | null; snapshot: WorkspaceChangesSnapshot | null; loading: boolean; activePath: string; onOpenFile: (path: string) => void }) {
  const { t } = useI18n();
  if (!workspaceID) return <Empty icon={<FileDiff size={22} />} text={t("codex.selectWorkspace")} />;
  if (loading || !snapshot) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.status === "failed") return <div className="file-tree-error"><AlertTriangle size={16} /><span>{snapshot.error || t("codex.changeScanFailed")}</span></div>;
  if (snapshot.status === "scanning" && snapshot.changes.length === 0) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.changes.length === 0) return <Empty icon={<FileDiff size={22} />} text={t("codex.noChanges")} />;
  return <VirtualizedList
    className="change-file-list"
    items={snapshot.changes}
    getKey={change => change.path}
    estimateSize={42}
    renderItem={change => {
      const name = change.path.split("/").pop() || change.path;
      return <button type="button" className={`change-file-row ${activePath === change.path ? "active" : ""}`} title={change.path} onClick={() => onOpenFile(change.path)}><span className={`change-file-status ${change.status}`}>{changeStatusCodes[change.status] ?? "M"}</span><span className="change-file-name"><strong>{name}</strong><small>{change.path}</small></span><span className="change-file-label">{t(changeStatusKeys[change.status] ?? "codex.changeModified")}</span></button>;
    }}
  />;
}

function FileTreeItem({ node, depth, expanded, onToggle, activePath, onOpenFile }: { node: FileTreeNode; depth: number; expanded: Set<string>; onToggle: (path: string) => void; activePath: string; onOpenFile: (path: string) => void }) {
  const directory = node.kind === "directory";
  const open = directory && expanded.has(node.path);
  const content = <><span className="file-tree-chevron">{directory ? open ? <ChevronDown size={13} /> : <ChevronRight size={13} /> : null}</span>{directory ? open ? <FolderOpen size={15} /> : <Folder size={15} /> : <FileIcon size={14} />}<span title={node.path}>{node.name}</span></>;
  return <>{<button type="button" className={`file-tree-row ${!directory && activePath === node.path ? "active" : ""}`} style={{ paddingLeft: 8 + depth * 14 }} onClick={() => directory ? onToggle(node.path) : onOpenFile(node.path)}>{content}</button>}{open && node.children.map(child => <FileTreeItem key={child.path} node={child} depth={depth + 1} expanded={expanded} onToggle={onToggle} activePath={activePath} onOpenFile={onOpenFile} />)}</>;
}

function buildFileTree(files: WorkspaceFile[]): FileTreeNode[] {
  type MutableNode = Omit<FileTreeNode, "children"> & { children: Map<string, MutableNode> };
  const root = new Map<string, MutableNode>();
  for (const entry of files) {
    const parts = entry.path.split("/").filter(Boolean);
    let children = root;
    for (let index = 0; index < parts.length; index++) {
      const name = parts[index];
      const path = parts.slice(0, index + 1).join("/");
      const last = index === parts.length - 1;
      let node = children.get(name);
      if (!node) {
        node = { name, path, kind: last ? entry.kind : "directory", size: last ? entry.size : undefined, children: new Map() };
        children.set(name, node);
      } else if (last) {
        node.kind = entry.kind;
        node.size = entry.size;
      }
      children = node.children;
    }
  }
  const convert = (items: Map<string, MutableNode>): FileTreeNode[] => Array.from(items.values()).sort((left, right) => left.kind === right.kind ? left.name.localeCompare(right.name) : left.kind === "directory" ? -1 : 1).map(node => ({ ...node, children: convert(node.children) }));
  return convert(root);
}

export function CreateThread({ workspaces, onCreated }: { workspaces: Workspace[]; onCreated: (thread: Thread) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <form onSubmit={async e => { e.preventDefault(); if (busy) return; setBusy(true); setError(""); try { onCreated(await post<Thread>("/threads", { workspace_id: workspaceID })); } catch (requestError) { setError(message(requestError)); } finally { setBusy(false); } }}>{error && <ErrorBanner text={error} />}<Field label={t("codex.workspace")}><select value={workspaceID} disabled={busy} onChange={e => setWorkspaceID(e.target.value)} required><option value="">{t("codex.selectWorkspaceOption")}</option>{workspaces.map(workspace => <option value={workspace.id} key={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field><DialogActions><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />}{t("codex.createSession")}</button></DialogActions></form>;
}

export function SessionView({ thread, approvals, realtime, globalStreamRevision = 0, streamRevision = 0, invalidationSequence, reloadApprovals, notify, onOpenFile, onNewTask }: { thread: Thread; approvals: Approval[]; realtime: unknown; globalStreamRevision?: number; streamRevision?: number; invalidationSequence?: number | null; reloadApprovals: () => void; notify: (text: string) => void; onOpenFile: (selection: FilePreviewSelection) => void; onNewTask: () => void }) {
  const { t } = useI18n();
  const [rawEvents, setRawEvents] = useState(false);
  const events = useThreadEvents(thread.id, rawEvents, realtime, globalStreamRevision, streamRevision, invalidationSequence);
  const [prompt, setPrompt] = useState("");
  const [images, setImages] = useState<ComposerImage[]>([]);
  const [imageBusy, setImageBusy] = useState(false);
  const imageCompressionRef = useRef<AbortController | null>(null);
  const [sending, setSending] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [editingEventID, setEditingEventID] = useState("");
  const [composerPreferences, setComposerPreferences] = useState(() => loadCodexComposerPreferences(thread.id));
  const { approvalMode, model, reasoningEffort } = composerPreferences;
  const updateComposerPreferences = (changes: Partial<CodexComposerPreferences>) => {
    setComposerPreferences(current => {
      const next = { ...current, ...changes };
      saveCodexComposerPreferences(thread.id, next);
      return next;
    });
  };
  const setModel = (value: string) => updateComposerPreferences({ model: value });
  const setReasoningEffort = (value: string) => updateComposerPreferences({ reasoningEffort: value });
  const setApprovalMode = (value: string) => updateComposerPreferences({ approvalMode: value });
  const [customModelSignal, setCustomModelSignal] = useState(0);
  const [slashMode, setSlashMode] = useState<"commands" | "model" | "reasoning">("commands");
  const [slashDismissedValue, setSlashDismissedValue] = useState("");
  const [statusOpen, setStatusOpen] = useState(false);
  const [goalOpen, setGoalOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [subagentsOpen, setSubagentsOpen] = useState(false);
  const [compactBusy, setCompactBusy] = useState(false);
  const [statusSnapshot, setStatusSnapshot] = useState<CodexSnapshot<CodexStatusData> | null>(null);
  const [mcpSnapshot, setMcpSnapshot] = useState<CodexSnapshot<CodexMCPServer[]> | null>(null);
  const [skillsSnapshot, setSkillsSnapshot] = useState<CodexSnapshot<CodexSkill[]> | null>(null);
  const [goal, setGoal] = useState<CodexGoal | null>(null);
  const [goalForm, setGoalForm] = useState({ objective: "", status: "active", token_budget: "" });
  const [nativeBusy, setNativeBusy] = useState("");
  const [nativeError, setNativeError] = useState("");
  const goalRequestRef = useRef(0);
  const streamRef = useRef<HTMLDivElement>(null);
  const restoredScrollKeyRef = useRef("");
  const autoFollowStreamRef = useRef(true);
  const eventCountRef = useRef<{ key: string; count: number } | null>(null);
  const historyAnchorRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const [hasNewEventsBelow, setHasNewEventsBelow] = useState(false);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const slashKeyboardRef = useRef<((event: ReactKeyboardEvent<HTMLTextAreaElement>) => boolean) | null>(null);
  const sourceEvents = events.data ?? [];
  const chatEvents = useMemo(() => conversationEvents(sourceEvents), [sourceEvents]);
  const displayItems = useMemo(() => groupCommandEvents(chatEvents), [chatEvents]);
  const subagents = useMemo(() => collectSubagentActivity(sourceEvents), [sourceEvents]);
  const activeSubagents = subagents.filter(agent => ["pendingInit", "running"].includes(agent.status));
  const activeTurn = thread.status === "queued" || thread.status === "running";
  const goalRuntimeStatus = goal ? (["paused", "blocked", "usageLimited", "budgetLimited", "complete"].includes(goal.status) ? goal.status : thread.status) : "idle";
  const slashCandidate = /^\/[^\r\n]*$/.test(prompt);
  const slashOpen = !editingEventID && slashCandidate && prompt !== slashDismissedValue;
  const slashQuery = slashMode === "commands" ? prompt.slice(1) : prompt.replace(/^\/(?:model|reasoning)\s*/, "");
  const closeSlash = () => { setSlashDismissedValue(prompt); setSlashMode("commands"); };
  const finishSlash = (action: () => void) => { setPrompt(""); setSlashDismissedValue(""); setSlashMode("commands"); action(); requestAnimationFrame(() => promptRef.current?.focus()); };
  const loadSnapshot = async <T,>(kind: "status" | "mcp" | "skills", refresh = false) => {
    const workspacePath = `/workspaces/${thread.workspace_id}/codex/${kind}`;
    const path = kind === "status" ? `/threads/${thread.id}/codex/status` : workspacePath;
    const setter = kind === "status" ? setStatusSnapshot : kind === "mcp" ? setMcpSnapshot : setSkillsSnapshot;
    setNativeBusy(kind); setNativeError("");
    try {
      let next = await api<CodexSnapshot<unknown>>(path);
      const shouldRefresh = refresh || next.status === "idle";
      if (shouldRefresh) { await post(`${path}/refresh`, {}); next = await api<CodexSnapshot<unknown>>(path); }
      for (let attempt = 0; shouldRefresh && next.status === "loading" && attempt < 20; attempt++) { await waitForBackoff(attempt); next = await api<CodexSnapshot<unknown>>(path); }
      const normalized = { ...next, data: kind === "skills" ? normalizeSkills(next.data) : kind === "mcp" ? normalizeMCP(next.data) : normalizeStatus(next.data) } as CodexSnapshot<T>;
      setter(normalized as never);
    }
    catch (error) { setNativeError(message(error)); }
    finally { setNativeBusy(""); }
  };
  const loadGoal = async (refresh = false) => {
    const requestID = ++goalRequestRef.current;
    setNativeBusy("goal"); setNativeError("");
    try {
      let snapshot = await api<CodexSnapshot<unknown>>(`/threads/${thread.id}/goal`);
      if (refresh) {
        await post(`/threads/${thread.id}/goal/refresh`, {});
        snapshot = await waitForGoalSnapshot(thread.id) ?? snapshot;
      }
      if (requestID !== goalRequestRef.current) return;
      const next = normalizeGoal(snapshot.data);
      setGoal(next);
      setGoalForm({ objective: next?.objective ?? "", status: next?.status ?? "active", token_budget: next?.token_budget == null ? "" : String(next.token_budget) });
      if (!snapshot.supported) setNativeError(snapshot.reason || t("codex.unsupported"));
    }
    catch (error) { if (requestID === goalRequestRef.current) setNativeError(message(error)); }
    finally { if (requestID === goalRequestRef.current) setNativeBusy(""); }
  };
  const syncGoalSnapshot = async (expected?: { objective?: string; status?: string; cleared?: boolean }) => {
    const requestID = ++goalRequestRef.current;
    try {
      const snapshot = await waitForGoalSnapshot(thread.id, expected);
      if (!snapshot || requestID !== goalRequestRef.current) return;
      if (!snapshot.supported || snapshot.status === "failed" || snapshot.status === "unsupported") {
        setNativeError(snapshot.reason || snapshot.error || t("codex.unsupported"));
        return;
      }
      const next = normalizeGoal(snapshot.data);
      if (expected?.cleared && next) return;
      if (expected?.objective && (!next || next.objective !== expected.objective || (expected.status && next.status !== expected.status))) return;
      setGoal(next);
      setGoalForm({ objective: next?.objective ?? "", status: next?.status ?? "active", token_budget: next?.token_budget == null ? "" : String(next.token_budget) });
      if (!snapshot.supported) setNativeError(snapshot.reason || t("codex.unsupported"));
    } catch (error) {
      if (requestID === goalRequestRef.current) setNativeError(message(error));
    }
  };
  const queueGoalTurn = async (objective: string) => {
    await post(`/threads/${thread.id}/turns`, { prompt: objective, model, reasoning_effort: reasoningEffort, approval_mode: approvalMode });
  };
  const saveGoal = async (objective: string, status: string, tokenBudget: number | null, resume = false) => {
    ++goalRequestRef.current;
    const creating = !goal;
    const needsInitialTurn = creating && status === "active";
    const initialCodexThreadID = thread.codex_thread_id;
    if (creating && !initialCodexThreadID && status !== "active") throw new Error(t("codex.goalNeedsActive"));
    await put(`/threads/${thread.id}/goal`, { objective, status, token_budget: tokenBudget });
    if (needsInitialTurn || (resume && status === "active" && !activeTurn)) await queueGoalTurn(objective);
    const optimistic: CodexGoal = { thread_id: initialCodexThreadID, objective, status, token_budget: tokenBudget, tokens_used: goal?.tokens_used ?? 0, time_used_seconds: goal?.time_used_seconds ?? 0, created_at: goal?.created_at ?? Math.floor(Date.now() / 1000), updated_at: Math.floor(Date.now() / 1000) };
    setGoal(optimistic);
    setGoalForm({ objective, status, token_budget: tokenBudget == null ? "" : String(tokenBudget) });
    setGoalOpen(false);
    void syncGoalSnapshot({ objective, status });
  };
  const compactContext = async () => {
    if (compactBusy) return;
    if (activeTurn) { notify(t("codex.waitForTurn")); return; }
    setCompactBusy(true);
    try {
      await post(`/threads/${thread.id}/compact`, {});
      notify(t("codex.compactQueued"));
    } catch (error) {
      notify(message(error));
    } finally {
      setCompactBusy(false);
    }
  };
  const updateGoalStatus = async (status: "active" | "paused") => {
    if (!goal || nativeBusy === "goal") return;
    setNativeBusy("goal"); setNativeError("");
    try {
      if (status === "paused" && activeTurn) {
        try {
          await post(`/threads/${thread.id}/interrupt`, {});
        } catch (error) {
          if (!(error instanceof APIError && error.status === 409)) throw error;
        }
      }
      await saveGoal(goal.objective, status, goal.token_budget, status === "active");
      notify(t(status === "active" ? "codex.goalResumed" : "codex.goalPaused"));
    } catch (error) {
      notify(message(error));
    } finally {
      setNativeBusy("");
    }
  };
  const clearCurrentGoal = async () => {
    if (!goal || nativeBusy === "goal") return;
    ++goalRequestRef.current;
    setNativeBusy("goal"); setNativeError("");
    try {
      if (activeTurn) {
        try {
          await post(`/threads/${thread.id}/interrupt`, {});
        } catch (error) {
          if (!(error instanceof APIError && error.status === 409)) throw error;
        }
      }
      await remove(`/threads/${thread.id}/goal`);
      setGoal(null);
      setGoalForm({ objective: "", status: "active", token_budget: "" });
      setGoalOpen(false);
      void syncGoalSnapshot({ cleared: true });
      notify(t("codex.goalCleared"));
    } catch (error) {
      setNativeError(message(error));
      notify(message(error));
    } finally {
      setNativeBusy("");
    }
  };
  const modelItems: SlashCommandItem[] = [
    { id: "default", name: t("codex.modelServerDefault"), description: t("codex.slashModelDefaultDescription"), icon: Cpu, selected: model === "", onSelect: () => finishSlash(() => setModel("")) },
    ...codexModelOptions.map(option => ({ id: option.value, name: t(option.labelKey), description: option.value, icon: Cpu, selected: model === option.value, onSelect: () => finishSlash(() => setModel(option.value)) }))
  ];
  if (model && !codexModelOptions.some(option => option.value === model)) modelItems.push({ id: model, name: model, description: t("codex.slashCurrentCustomModel"), icon: Cpu, selected: true, onSelect: () => finishSlash(() => setModel(model)) });
  modelItems.push({ id: "custom", name: t("codex.modelCustom"), description: t("codex.slashCustomModelDescription"), icon: Cpu, onSelect: () => finishSlash(() => { setModel(""); setCustomModelSignal(value => value + 1); }) });
  const reasoningItems: SlashCommandItem[] = [
    { id: "default", name: t("codex.reasoningDefault"), description: t("codex.slashReasoningDefaultDescription"), icon: Gauge, selected: reasoningEffort === "", onSelect: () => finishSlash(() => setReasoningEffort("")) },
    ...codexReasoningOptions.map(option => ({ id: option.value, name: t(option.labelKey), description: option.value, icon: Gauge, selected: reasoningEffort === option.value, onSelect: () => finishSlash(() => setReasoningEffort(option.value)) }))
  ];
  const commandItems: SlashCommandItem[] = [
    { id: "model", name: "/model", description: t("codex.slashModelDescription"), detail: model || t("codex.modelServerDefault"), icon: Cpu, onSelect: () => { setPrompt("/model "); setSlashMode("model"); } },
    { id: "reasoning", name: "/reasoning", description: t("codex.slashReasoningDescription"), detail: reasoningEffort ? t(codexReasoningOptions.find(option => option.value === reasoningEffort)?.labelKey ?? "codex.reasoningDefault") : t("codex.reasoningDefault"), icon: Gauge, onSelect: () => { setPrompt("/reasoning "); setSlashMode("reasoning"); } },
    { id: "status", name: "/status", description: t("codex.slashStatusDescription"), icon: Activity, onSelect: () => finishSlash(() => setStatusOpen(true)) },
    { id: "goal", name: "/goal", description: t("codex.slashGoalDescription"), icon: Target, onSelect: () => finishSlash(() => { setGoalOpen(true); void loadGoal(true); }) },
    { id: "compact", name: "/compact", description: t("codex.slashCompactDescription"), icon: Minimize2, onSelect: () => finishSlash(() => void compactContext()) },
    { id: "subagents", name: "/subagents", description: t("codex.slashSubagentsDescription"), detail: subagents.length ? String(subagents.length) : undefined, icon: Users, onSelect: () => finishSlash(() => setSubagentsOpen(true)) },
    { id: "mcp", name: "/mcp", description: t("codex.slashMCPDescription"), icon: Network, onSelect: () => finishSlash(() => { setMcpOpen(true); void loadSnapshot<CodexMCPServer[]>("mcp"); }) },
    { id: "skills", name: "/skills", description: t("codex.slashSkillsDescription"), icon: Boxes, onSelect: () => finishSlash(() => { setSkillsOpen(true); void loadSnapshot<CodexSkill[]>("skills"); }) },
    { id: "plan", name: "/plan", description: t("codex.slashPlanDescription"), detail: t("codex.unsupported"), icon: StickyNote, onSelect: () => finishSlash(() => setPlanOpen(true)) },
    { id: "project", name: "/project", description: t("codex.slashProjectDescription"), icon: Folder, onSelect: () => finishSlash(onNewTask) }
  ];
  const skillItems: SlashCommandItem[] = (skillsSnapshot?.data ?? []).filter(skill => skill.enabled).map(skill => ({ id: `skill:${skill.name}`, name: `$${skill.name}`, description: skill.short_description || skill.description, detail: skill.scope, section: t("codex.availableSkills"), icon: Boxes, onSelect: () => finishSlash(() => { setPrompt(`$${skill.name} `); requestAnimationFrame(() => { promptRef.current?.focus(); promptRef.current?.setSelectionRange(skill.name.length + 2, skill.name.length + 2); }); }) }));
  const slashItems = slashMode === "model" ? modelItems : slashMode === "reasoning" ? reasoningItems : [...commandItems, ...skillItems];
  useEffect(() => { if (slashOpen && slashMode === "commands" && !skillsSnapshot && nativeBusy !== "skills") void loadSnapshot<CodexSkill[]>("skills"); }, [slashOpen, slashMode, thread.workspace_id]);
  useEffect(() => { if (statusOpen && !statusSnapshot) void loadSnapshot<CodexStatusData>("status"); }, [statusOpen, thread.id]);
  useEffect(() => { ++goalRequestRef.current; setRawEvents(false); setPrompt(""); setImages([]); setEditingEventID(""); setGoal(null); setStatusSnapshot(null); setMcpSnapshot(null); setSkillsSnapshot(null); setSubagentsOpen(false); void loadGoal(); }, [thread.id]);
  const scrollStateKey = `${thread.id}:${rawEvents ? "raw" : "conversation"}`;
  const eventsReady = events.data !== null;
  const scrollToLatestEvent = () => {
    const stream = streamRef.current;
    if (!stream) return;
    setScrollTopImmediately(stream, latestScrollTop(stream));
    codexScrollPositions.set(scrollStateKey, stream.scrollTop);
    autoFollowStreamRef.current = true;
    setHasNewEventsBelow(false);
  };
  const loadEarlierEvents = async () => {
    const stream = streamRef.current;
    if (!stream) return;
    historyAnchorRef.current = { scrollHeight: stream.scrollHeight, scrollTop: stream.scrollTop };
    if (!await events.loadEarlier()) historyAnchorRef.current = null;
  };
  useLayoutEffect(() => {
    const stream = streamRef.current;
    if (!stream || !eventsReady) return;
    if (restoredScrollKeyRef.current !== scrollStateKey) {
      setScrollTopImmediately(stream, codexScrollPositions.get(scrollStateKey) ?? latestScrollTop(stream));
      restoredScrollKeyRef.current = scrollStateKey;
      autoFollowStreamRef.current = isNearCodexStreamBottom(stream);
      eventCountRef.current = { key: scrollStateKey, count: sourceEvents.length };
      setHasNewEventsBelow(false);
      return;
    }
    const previous = eventCountRef.current;
    const hasAppendedEvents = previous?.key === scrollStateKey && sourceEvents.length > previous.count;
    eventCountRef.current = { key: scrollStateKey, count: sourceEvents.length };
    const historyAnchor = historyAnchorRef.current;
    if (historyAnchor) {
      historyAnchorRef.current = null;
      setScrollTopImmediately(stream, historyAnchor.scrollTop + stream.scrollHeight - historyAnchor.scrollHeight);
      codexScrollPositions.set(scrollStateKey, stream.scrollTop);
      autoFollowStreamRef.current = isNearCodexStreamBottom(stream);
      return;
    }
    if (!hasAppendedEvents) return;
    if (autoFollowStreamRef.current) {
      setScrollTopImmediately(stream, latestScrollTop(stream));
      codexScrollPositions.set(scrollStateKey, stream.scrollTop);
      setHasNewEventsBelow(false);
      return;
    }
    setHasNewEventsBelow(true);
  }, [eventsReady, scrollStateKey, sourceEvents.length]);
  useLayoutEffect(() => {
    const stream = streamRef.current;
    return () => { if (stream && restoredScrollKeyRef.current === scrollStateKey) codexScrollPositions.set(scrollStateKey, stream.scrollTop); };
  }, [scrollStateKey]);
  useEffect(() => {
    setImageBusy(false);
    return () => {
      imageCompressionRef.current?.abort();
      imageCompressionRef.current = null;
    };
  }, [thread.id]);
  const addImages = async (files: File[]) => {
    const available = 4 - images.length;
    if (available <= 0) { notify(t("codex.imageLimit")); return; }
    imageCompressionRef.current?.abort();
    const controller = new AbortController();
    imageCompressionRef.current = controller;
    setImageBusy(true);
    try {
      const selected = files.slice(0, available);
      const prepared = new Array<ComposerImage>(selected.length);
      let nextIndex = 0;
      const prepareNext = async () => {
        while (nextIndex < selected.length) {
          const index = nextIndex++;
          prepared[index] = { id: crypto.randomUUID(), dataURL: await compressImage(selected[index], { signal: controller.signal }) };
        }
      };
      await Promise.all(Array.from({ length: Math.min(2, selected.length) }, () => prepareNext()));
      setImages(current => [...current, ...prepared].slice(0, 4));
      if (files.length > available) notify(t("codex.imageLimit"));
    } catch {
      const wasCancelled = controller.signal.aborted;
      controller.abort();
      if (!wasCancelled) notify(t("codex.imageFailed"));
    } finally {
      if (imageCompressionRef.current === controller) {
        imageCompressionRef.current = null;
        setImageBusy(false);
      }
    }
  };
  const send = async (event: FormEvent) => {
    event.preventDefault();
    if (slashOpen || (!prompt.trim() && images.length === 0) || imageBusy || sending) return;
    if (activeTurn) { notify(t("codex.waitForTurn")); return; }
    setSending(true);
    try {
      const turnPath = editingEventID ? `/threads/${thread.id}/events/${editingEventID}/rewrite` : `/threads/${thread.id}/turns`;
      await post(turnPath, { prompt, images: images.map(image => ({ data_url: image.dataURL })), model, reasoning_effort: reasoningEffort, approval_mode: approvalMode });
      setPrompt(""); setImages([]); setEditingEventID(""); notify(t(editingEventID ? "codex.rewriteQueued" : "codex.turnQueued"));
    } catch (err) { notify(message(err)); } finally { setSending(false); }
  };
  const editMessage = (eventID: string, text: string) => {
    setPrompt(text);
    setImages([]);
    setEditingEventID(eventID);
    requestAnimationFrame(() => {
      promptRef.current?.focus();
      promptRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      promptRef.current?.setSelectionRange(text.length, text.length);
    });
    notify(t("codex.messageReadyToEdit"));
  };
  const interrupt = async () => {
    if (interrupting) return;
    setInterrupting(true);
    try {
      await post(`/threads/${thread.id}/interrupt`, {});
      notify(t("codex.interruptQueued"));
    } catch (error) {
      notify(message(error));
    } finally {
      setInterrupting(false);
    }
  };
  const renderDisplayItem = (item: ConversationDisplayItem) => item.type === "commandGroup"
    ? <CommandEventGroup events={item.events} />
    : <ConversationEventItem event={item.event} onEdit={thread.archived_at ? undefined : editMessage} notify={notify} workspaceRoot={thread.path} onOpenFile={onOpenFile} />;
  return <>
    <div className="session-header"><div><h2>{thread.title}</h2><span><GitBranch size={13} />{thread.project_name}<i /> <ServerIcon size={13} />{thread.server_name}</span></div><div className="session-actions"><button className={`icon-button ${rawEvents ? "active" : ""}`} aria-pressed={rawEvents} title={rawEvents ? t("codex.showConversation") : t("codex.showRawEvents")} onClick={() => setRawEvents(value => !value)}><Braces size={16} /></button><Status value={thread.status} />{(thread.status === "queued" || thread.status === "running") && <button className="icon-button danger" disabled={interrupting} title={t("codex.interrupt")} onClick={() => void interrupt()}>{interrupting ? <LoaderCircle className="spin" size={16} /> : <Ban size={16} />}</button>}</div></div>
    <div className={`event-stream ${rawEvents ? "raw-stream" : "conversation-stream"}`} ref={streamRef} aria-live="polite" onScroll={event => { const stream = event.currentTarget; autoFollowStreamRef.current = isNearCodexStreamBottom(stream); if (autoFollowStreamRef.current) setHasNewEventsBelow(false); if (restoredScrollKeyRef.current === scrollStateKey) codexScrollPositions.set(scrollStateKey, stream.scrollTop); }}>
      {events.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : events.error && !events.data ? <ErrorState error={events.error} reload={events.reload} /> : <>
        {events.hasEarlier && <div className="history-loader"><button type="button" className="secondary-button small" disabled={events.loadingEarlier} onClick={() => void loadEarlierEvents()}>{events.loadingEarlier ? <LoaderCircle className="spin" size={15} /> : <ArrowUpFromLine size={15} />}{t(events.loadingEarlier ? "codex.loadingEarlier" : "codex.loadEarlier")}</button></div>}
        {events.error && events.data && <div className="snapshot-notice warning"><AlertTriangle size={15} />{events.error}</div>}
        {chatEvents.length === 0 && approvals.length === 0 && thread.status !== "running" ? <Empty icon={<Bot size={26} />} text={t("codex.noMessages")} /> : <>
          {rawEvents
            ? sourceEvents.length > 0 && <VirtualizedItems<StreamEvent> items={sourceEvents} scrollRef={streamRef} getKey={event => event.event_id} estimateSize={116} renderItem={event => <RawEventItem event={event} />} />
            : displayItems.length > 0 && <VirtualizedItems<ConversationDisplayItem> items={displayItems} scrollRef={streamRef} getKey={item => item.type === "commandGroup" ? `commands:${item.events[0].event_id}` : item.event.event_id} estimateSize={item => item.type === "commandGroup" ? 112 : 156} renderItem={renderDisplayItem} />}
          {approvals.map(item => <ApprovalPrompt key={item.id} item={item} onDecided={reloadApprovals} notify={notify} />)}
          {thread.status === "running" && approvals.length === 0 && <WorkingIndicator />}
        </>}</>
      }
    </div>
    {thread.archived_at ? <div className="snapshot-notice"><Archive size={16} />{t("codex.archivedReadOnly")}</div> : <form className="composer" onSubmit={send}>
      {hasNewEventsBelow && <button type="button" className="secondary-button" aria-label={t("codex.jumpToLatestMessages")} onClick={scrollToLatestEvent}><ChevronDown size={16} />{t("codex.newMessages")}</button>}
      {subagents.length > 0 && <button type="button" className="subagent-progress-row" onClick={() => setSubagentsOpen(true)}><Users size={16} /><span><strong>{t("codex.subagentActivity")}</strong><small>{activeSubagents.length > 0 ? t("codex.subagentsRunning", { count: activeSubagents.length }) : t("codex.subagentsRecorded", { count: subagents.length })}</small></span><ChevronRight size={15} /></button>}
      {goal && <div className="goal-progress-row"><Target size={16} /><span title={goal.objective}><strong>{goal.objective}</strong><small>{t("codex.goalUsage", { tokens: goal.tokens_used, seconds: goal.time_used_seconds })}</small></span><Status value={goalRuntimeStatus} /><div>{goal.status === "active" ? <button type="button" className="icon-button" disabled={nativeBusy === "goal"} title={t("codex.pauseGoal")} aria-label={t("codex.pauseGoal")} onClick={() => void updateGoalStatus("paused")}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={14} /> : <Pause size={14} />}</button> : <button type="button" className="icon-button" disabled={nativeBusy === "goal"} title={t("codex.resumeGoal")} aria-label={t("codex.resumeGoal")} onClick={() => void updateGoalStatus("active")}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={14} /> : <Play size={14} />}</button>}<button type="button" className="icon-button" title={t("codex.editGoal")} aria-label={t("codex.editGoal")} onClick={() => setGoalOpen(true)}><Pencil size={14} /></button><button type="button" className="icon-button danger" disabled={nativeBusy === "goal"} title={t("codex.clearGoal")} aria-label={t("codex.clearGoal")} onClick={() => void clearCurrentGoal()}><Trash2 size={14} /></button></div></div>}
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} onClick={() => { setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length > 0 && <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" title={t("common.close")} onClick={() => setImages(current => current.filter(item => item.id !== image.id))}><X size={13} /></button></figure>)}</div>}
      {slashOpen && <SlashCommandMenu items={slashItems} query={slashQuery} label={t("codex.slashMenu")} backLabel={t("codex.slashBackToCommands")} onBack={slashMode === "commands" ? undefined : () => { setPrompt("/"); setSlashMode("commands"); }} onDismiss={closeSlash} keyboardRef={slashKeyboardRef} />}
      <textarea ref={promptRef} value={prompt} onChange={event => { setPrompt(event.target.value); if (event.target.value !== slashDismissedValue) setSlashDismissedValue(""); if (!event.target.value.startsWith("/model ") && !event.target.value.startsWith("/reasoning ")) setSlashMode("commands"); }} onKeyDown={event => { if (slashOpen && slashKeyboardRef.current?.(event)) return; if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} onPaste={event => { const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null); if (files.length) { event.preventDefault(); void addImages(files); } }} placeholder={t("codex.messagePlaceholder")} rows={3} />
      <div className="composer-bar"><div><select aria-label={t("codex.approveOnRequest")} value={approvalMode} onChange={event => setApprovalMode(event.target.value)}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select><CodexModelPicker value={model} onChange={setModel} allowServerDefault requestCustom={customModelSignal} /><select aria-label={t("codex.reasoningEffort")} value={reasoningEffort} onChange={event => setReasoningEffort(event.target.value)}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}</select></div><button className="primary-button" title={activeTurn ? t("codex.waitForTurn") : t("codex.send")} disabled={slashOpen || (!prompt.trim() && images.length === 0) || imageBusy || sending || activeTurn}>{sending ? <LoaderCircle className="spin" size={17} /> : <ChevronRight size={17} />}{t("codex.send")}</button></div>
    </form>}
    <Dialog open={statusOpen} title={t("codex.taskStatus")} onClose={() => setStatusOpen(false)}><SnapshotNotice snapshot={statusSnapshot} loading={nativeBusy === "status"} error={nativeError} /><dl className="task-status-list"><div><dt>{t("codex.statusWioTaskID")}</dt><dd><code>{thread.id}</code></dd></div><div><dt>{t("codex.statusCodexThreadID")}</dt><dd>{thread.codex_thread_id ? <code>{thread.codex_thread_id}</code> : t("codex.notBound")}</dd></div><div><dt>{t("column.project")}</dt><dd>{thread.project_name}</dd></div><div><dt>{t("column.server")}</dt><dd>{thread.server_name}</dd></div><div><dt>{t("codex.statusWorkingDirectory")}</dt><dd><code>{thread.path}</code></dd></div><div><dt>{t("codex.modelOverride")}</dt><dd>{String(statusSnapshot?.data?.model || model || t("codex.modelServerDefault"))}</dd></div><div><dt>{t("codex.reasoningEffort")}</dt><dd>{String(statusSnapshot?.data?.reasoning_effort || (reasoningEffort ? t(codexReasoningOptions.find(option => option.value === reasoningEffort)?.labelKey ?? "codex.reasoningDefault") : t("codex.reasoningDefault")))}</dd></div><div><dt>{t("codex.statusApprovalPolicy")}</dt><dd>{String(statusSnapshot?.data?.approval_policy || t(approvalMode === "on-request" ? "codex.approveOnRequest" : approvalMode === "untrusted" ? "codex.untrusted" : "codex.neverApprove"))}</dd></div><div><dt>{t("column.state")}</dt><dd><Status value={thread.status} /></dd></div>{statusSnapshot?.data?.account_type && <div><dt>{t("codex.statusAccount")}</dt><dd>{statusSnapshot.data.account_type}</dd></div>}{statusSnapshot?.data?.rate_limits_available === false && <div className="status-note-row"><dt>{t("codex.statusRateLimits")}</dt><dd>{t("codex.statusRateLimitsUnavailable")}</dd></div>}{(statusSnapshot?.data?.rate_limits ?? []).map(limit => <div key={limit.name}><dt>{limit.name}</dt><dd>{limit.used_percent == null ? limit.detail || "-" : `${limit.used_percent}%${limit.resets_at ? ` · ${t("codex.resetsAt", { time: formatDate(limit.resets_at) })}` : ""}`}</dd></div>)}</dl><DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "status"} onClick={() => void loadSnapshot<CodexStatusData>("status", true)}>{nativeBusy === "status" ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t("common.refresh")}</button></DialogActions></Dialog>
     <Dialog open={goalOpen} title={t("codex.goalTitle")} onClose={() => setGoalOpen(false)}><SnapshotNotice loading={nativeBusy === "goal"} error={nativeError} />{goal && <div className="goal-dialog-status"><span>{t("codex.goalExecution")}</span><Status value={goalRuntimeStatus} /><small>{t("codex.goalUsage", { tokens: goal.tokens_used, seconds: goal.time_used_seconds })}</small></div>}<form onSubmit={async event => { event.preventDefault(); setNativeBusy("goal"); setNativeError(""); const creating = !goal; try { await saveGoal(goalForm.objective.trim(), goalForm.status, goalForm.token_budget ? Number(goalForm.token_budget) : null, creating && goalForm.status === "active" ? false : goal?.status !== "active" && goalForm.status === "active"); notify(t(creating ? "codex.goalStarted" : "codex.goalSaved")); } catch (error) { setNativeError(message(error)); } finally { setNativeBusy(""); } }}><Field label={t("codex.goalObjective")}><textarea rows={3} value={goalForm.objective} onChange={event => setGoalForm({ ...goalForm, objective: event.target.value })} required /></Field><div className="form-grid"><Field label={t("column.state")}><select value={goalForm.status} onChange={event => setGoalForm({ ...goalForm, status: event.target.value })}><option value="active">active</option><option value="paused">paused</option><option value="blocked">blocked</option><option value="complete">complete</option></select></Field><Field label={t("codex.goalTokenBudget")}><input type="number" min="1" value={goalForm.token_budget} onChange={event => setGoalForm({ ...goalForm, token_budget: event.target.value })} placeholder={t("codex.noLimit")} /></Field></div><DialogActions>{goal && <button type="button" className="secondary-button danger" disabled={nativeBusy === "goal"} onClick={() => void clearCurrentGoal()}><Trash2 size={16} />{t("codex.clearGoal")}</button>}<button className="primary-button" disabled={nativeBusy === "goal" || !goalForm.objective.trim()}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={16} /> : <Target size={16} />}{t("common.save")}</button></DialogActions></form></Dialog>
    <Dialog open={mcpOpen} title={t("codex.mcpTitle")} onClose={() => setMcpOpen(false)}><SnapshotNotice snapshot={mcpSnapshot} loading={nativeBusy === "mcp"} error={nativeError} />{mcpSnapshot?.data?.length ? <div className="native-list">{mcpSnapshot.data.map(server => <article key={server.name}><header><strong>{server.name}</strong><Status value={server.auth_status || "unknown"} /></header>{(server.server_name || server.server_version) && <small>{[server.server_name, server.server_version].filter(Boolean).join(" ")}</small>}<p>{server.tools.length ? server.tools.join(", ") : t("codex.noTools")}</p><small>{t("codex.mcpResources", { resources: server.resource_count, templates: server.resource_template_count })}</small></article>)}</div> : !nativeBusy && <Empty icon={<Network size={24} />} text={t("codex.noMCPServers")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "mcp"} onClick={() => void loadSnapshot<CodexMCPServer[]>("mcp", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={skillsOpen} title={t("codex.skillsTitle")} onClose={() => setSkillsOpen(false)}><SnapshotNotice snapshot={skillsSnapshot} loading={nativeBusy === "skills"} error={nativeError} />{skillsSnapshot?.data?.length ? <div className="native-list">{skillsSnapshot.data.map(skill => <article key={`${skill.scope}:${skill.name}`}><header><strong>{skill.display_name || skill.name}</strong><Status value={skill.enabled ? "enabled" : "disabled"} /></header><p>{skill.short_description || skill.description}</p><small>{skill.scope}</small></article>)}</div> : !nativeBusy && <Empty icon={<Boxes size={24} />} text={t("codex.noSkills")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "skills"} onClick={() => void loadSnapshot<CodexSkill[]>("skills", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={subagentsOpen} title={t("codex.subagentsTitle")} onClose={() => setSubagentsOpen(false)} wide>{subagents.length > 0 ? <div className="subagent-list">{subagents.map(agent => <article key={agent.threadID}><header><span><Users size={15} /><strong>{agent.path || t("codex.subagent")}</strong></span><Status value={agent.status} /></header><code>{agent.threadID}</code>{(agent.model || agent.reasoningEffort) && <small>{[agent.model, agent.reasoningEffort].filter(Boolean).join(" · ")}</small>}{agent.prompt && <p>{agent.prompt}</p>}{agent.message && <p className="subagent-message">{agent.message}</p>}</article>)}</div> : <Empty icon={<Users size={24} />} text={t("codex.noSubagents")} />}</Dialog>
    <Dialog open={planOpen} title={t("codex.planTitle")} onClose={() => setPlanOpen(false)}><div className="unsupported-state"><StickyNote size={24} /><strong>{t("codex.planUnsupportedTitle")}</strong><p>{t("codex.planUnsupportedReason")}</p></div></Dialog>
  </>;
}

function SnapshotNotice({ snapshot, loading, error }: { snapshot?: CodexSnapshot<unknown> | null; loading: boolean; error: string }) {
  const { t } = useI18n();
  if (loading && !snapshot?.data) return <div className="snapshot-notice"><LoaderCircle className="spin" size={16} />{t("common.loading")}</div>;
  if (error) return <ErrorBanner text={error} />;
  if (snapshot && (!snapshot.supported || snapshot.status === "unsupported")) return <div className="snapshot-notice warning"><AlertTriangle size={16} />{snapshot.reason || t("codex.unsupported")}</div>;
  if (snapshot?.status === "failed") return <div className="snapshot-notice warning"><AlertTriangle size={16} />{snapshot.error || t("codex.snapshotFailed")}</div>;
  if (snapshot?.updated_at) return <p className="snapshot-meta">{loading ? t("codex.refreshing") : t("codex.cachedAt", { time: formatDate(snapshot.updated_at) })}</p>;
  return null;
}

async function waitForGoalSnapshot(threadID: string, expected?: { objective?: string; status?: string; cleared?: boolean }) {
  let latest: CodexSnapshot<unknown> | undefined;
  for (let attempt = 0; attempt < 120; attempt++) {
    latest = await api<CodexSnapshot<unknown>>(`/threads/${threadID}/goal`);
    if (latest.status === "failed" || latest.status === "unsupported") return latest;
    if (latest.status !== "loading") {
      if (!expected) return latest;
      const goal = normalizeGoal(latest.data);
      if (expected.cleared ? !goal : Boolean(goal && goal.objective === expected.objective && (!expected.status || goal.status === expected.status))) return latest;
    }
    await waitForBackoff(attempt);
  }
  return latest;
}

export async function waitForThread(threadID: string, timeoutMessage: string) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const threads = await api<Thread[]>("/threads");
    const thread = threads.find(item => item.id === threadID);
    if (thread) return thread;
    await waitForBackoff(attempt);
  }
  throw new Error(timeoutMessage);
}

export async function waitForWorkspace(workspaceID: string, timeoutMessage: string) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const workspaces = await api<Workspace[]>("/workspaces");
      const workspace = workspaces.find(item => item.id === workspaceID);
      if (workspace) return workspace;
    } catch (error) {
      if (error instanceof APIError && error.status >= 400 && error.status < 500) throw error;
    }
    await waitForBackoff(attempt, 600, 3_000);
  }
  throw new Error(timeoutMessage);
}

function waitForBackoff(attempt: number, base = 400, maximum = 2_500) {
  const delay = Math.min(maximum, Math.round(base * Math.pow(1.55, Math.min(attempt, 6))));
  return new Promise<void>(resolve => window.setTimeout(resolve, delay));
}

function normalizeGoal(value: unknown): CodexGoal | null {
  const root = asRecord(value);
  const goal = asRecord(root?.goal) ?? (root && typeof root.objective === "string" ? root : null);
  if (!goal) return null;
  return { thread_id: String(goal.thread_id ?? goal.threadId ?? ""), objective: String(goal.objective ?? ""), status: String(goal.status ?? "active"), token_budget: typeof (goal.token_budget ?? goal.tokenBudget) === "number" ? Number(goal.token_budget ?? goal.tokenBudget) : null, tokens_used: Number(goal.tokens_used ?? goal.tokensUsed ?? 0), time_used_seconds: Number(goal.time_used_seconds ?? goal.timeUsedSeconds ?? 0), created_at: Number(goal.created_at ?? goal.createdAt ?? 0), updated_at: Number(goal.updated_at ?? goal.updatedAt ?? 0) };
}

function normalizeSkills(value: unknown): CodexSkill[] {
  const root = asRecord(value);
  const groups = Array.isArray(value) ? value : root?.data;
  if (!Array.isArray(groups)) return [];
  const skills = Array.isArray(value) ? value : groups.flatMap(groupValue => { const group = asRecord(groupValue); return Array.isArray(group?.skills) ? group.skills : []; });
  return skills.map(skillValue => {
    const skill = asRecord(skillValue) ?? {}; const detail = asRecord(skill.interface);
    return { name: String(skill.name ?? ""), description: String(skill.description ?? ""), path: String(skill.path ?? ""), scope: String(skill.scope ?? ""), enabled: skill.enabled !== false, display_name: String(skill.display_name ?? detail?.displayName ?? ""), short_description: String(skill.short_description ?? detail?.shortDescription ?? "") };
  }).filter(skill => skill.name);
}

function normalizeMCP(value: unknown): CodexMCPServer[] {
  const servers = Array.isArray(value) ? value : asRecord(value)?.data;
  if (!Array.isArray(servers)) return [];
  return servers.map(serverValue => { const server = asRecord(serverValue) ?? {}; const info = asRecord(server.serverInfo ?? server.server_info); const tools = Array.isArray(server.tools) ? server.tools.map(tool => typeof tool === "string" ? tool : String(asRecord(tool)?.name ?? "")).filter(Boolean) : []; return { name: String(server.name ?? ""), auth_status: String(server.authStatus ?? server.auth_status ?? "unknown"), server_name: String(server.server_name ?? info?.name ?? ""), server_version: String(server.server_version ?? info?.version ?? ""), tools, resource_count: Number(server.resourceCount ?? server.resource_count ?? 0), resource_template_count: Number(server.resourceTemplateCount ?? server.resource_template_count ?? 0) }; }).filter(server => server.name);
}

function normalizeStatus(value: unknown): CodexStatusData {
  const root = asRecord(value) ?? {};
  if (Array.isArray(root.rate_limits)) return { ...root, rate_limits: root.rate_limits as CodexStatusData["rate_limits"] };
  const limits = asRecord(root.rateLimits) ?? root;
  const rate_limits = Object.entries(limits).flatMap(([key, raw]) => { const limit = asRecord(raw); if (!limit || (!key.toLowerCase().includes("primary") && !key.toLowerCase().includes("secondary"))) return []; return [{ name: String(limit.limitName ?? limit.limit_name ?? key), used_percent: typeof (limit.usedPercent ?? limit.used_percent) === "number" ? Number(limit.usedPercent ?? limit.used_percent) : undefined, resets_at: typeof (limit.resetsAt ?? limit.resets_at) === "string" ? String(limit.resetsAt ?? limit.resets_at) : undefined }]; });
  return { ...root, rate_limits };
}

function collectSubagentActivity(events: StreamEvent[]): SubagentActivity[] {
  const agents = new Map<string, SubagentActivity>();
  const update = (threadID: string, changes: Partial<SubagentActivity>, occurredAt: string) => {
    if (!threadID) return;
    const previous = agents.get(threadID) ?? { threadID, path: "", status: "pendingInit", message: "", prompt: "", model: "", reasoningEffort: "", updatedAt: occurredAt };
    agents.set(threadID, { ...previous, ...changes, threadID, updatedAt: occurredAt });
  };
  for (const event of events) {
    if (event.kind !== "codex.item.started" && event.kind !== "codex.item.completed") continue;
    const payload = asRecord(event.payload);
    const item = asRecord(payload?.item);
    if (item?.type === "subAgentActivity") {
      const kind = String(item.kind ?? item.status ?? "started").toLowerCase();
      update(String(item.agentThreadId ?? item.newThreadId ?? item.receiverThreadId ?? ""), { path: String(item.agentPath ?? ""), status: kind === "interrupted" ? "interrupted" : kind === "completed" ? "completed" : kind === "failed" ? "errored" : "running" }, event.occurred_at);
      continue;
    }
    if (item?.type === "collabToolCall") {
      const agentStatus = asRecord(item.agentStatus);
      const status = typeof item.agentStatus === "string" ? item.agentStatus : String(agentStatus?.status ?? item.status ?? "running");
      const message = String(agentStatus?.message ?? item.message ?? "");
      const threadIDs = [item.receiverThreadId, item.newThreadId].filter(value => typeof value === "string" && value);
      for (const threadID of new Set(threadIDs as string[])) {
        update(threadID, { status, message, prompt: String(item.prompt ?? ""), model: String(item.model ?? ""), reasoningEffort: String(item.reasoningEffort ?? "") }, event.occurred_at);
      }
      continue;
    }
    if (item?.type !== "collabAgentToolCall") continue;
    const states = asRecord(item.agentsStates) ?? {};
    const receivers = Array.isArray(item.receiverThreadIds) ? item.receiverThreadIds.map(String) : [];
    for (const threadID of receivers) {
      const state = asRecord(states[threadID]);
      update(threadID, {
        status: String(state?.status ?? (item.status === "failed" ? "errored" : item.status === "completed" ? "completed" : "running")),
        message: String(state?.message ?? ""),
        prompt: String(item.prompt ?? ""),
        model: String(item.model ?? ""),
        reasoningEffort: String(item.reasoningEffort ?? "")
      }, event.occurred_at);
    }
    for (const [threadID, rawState] of Object.entries(states)) {
      const state = asRecord(rawState);
      update(threadID, { status: String(state?.status ?? "running"), message: String(state?.message ?? "") }, event.occurred_at);
    }
  }
  return Array.from(agents.values()).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
}

function ApprovalPrompt({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  return <article className="approval-prompt"><header><ShieldCheck size={16} /><strong>{t("codex.pendingApprovals")}</strong><time>{relative(item.expires_at)}</time></header><small>{readableKind(item.kind)}</small><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={onDecided} notify={notify} /></article>;
}

export function ApprovalActions({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const decide = async (decision: "approved" | "denied") => { setBusy(true); try { await post(`/approvals/${item.id}/decision`, { decision }); notify(t(decision === "approved" ? "codex.approvalGranted" : "codex.approvalDenied")); onDecided(); } catch (error) { notify(message(error)); } finally { setBusy(false); } };
  return <div className="approval-actions"><button type="button" className="secondary-button danger" disabled={busy} onClick={() => void decide("denied")}><Ban size={16} />{t("codex.deny")}</button><button type="button" className="primary-button" disabled={busy} onClick={() => void decide("approved")}>{busy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("codex.approveOnce")}</button></div>;
}

function ConversationEventItem({ event, onEdit, notify, workspaceRoot, onOpenFile }: { event: StreamEvent; onEdit?: (eventID: string, text: string) => void; notify: (text: string) => void; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const kind = event.kind;
  const payload = asRecord(event.payload);
  if (kind === "user.message") {
    const text = String(payload?.text ?? "");
    const images = extractImageSources(payload?.images);
    const imageCount = Math.max(images.length, Number(payload?.image_count ?? 0));
    const copyMessage = async () => { try { await copyText(text); notify(t("codex.messageCopied")); } catch (error) { notify(message(error)); } };
    return <article className="message user"><header><UserRound size={15} /><strong>{t("codex.you")}</strong><time>{formatTime(event.occurred_at)}</time></header>{text && <MarkdownContent text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} />}{images.length > 0 ? <MessageImages sources={images} /> : imageCount > 0 && <span className="message-image-count"><ImageIcon size={14} />{imageCount}</span>}<div className="message-actions"><button type="button" className="message-action" disabled={!text} title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => void copyMessage()}><Copy size={14} /></button>{onEdit && <button type="button" className="message-action" disabled={!text} title={t("codex.editMessage")} aria-label={t("codex.editMessage")} onClick={() => onEdit(event.event_id, text)}><Pencil size={14} /></button>}</div></article>;
  }
  if (kind === "codex.turn.cancelled") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
  if (kind === "codex.error" || kind === "codex.turn.failed" || kind === "codex.interrupt.failed" || kind === "codex.approval.failed" || kind === "codex.compact.failed") return <article className="message error"><header><AlertTriangle size={15} /><strong>{t(kind === "codex.turn.failed" || kind === "codex.error" ? "codex.turnFailed" : "codex.actionFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(payload) || t("codex.unknownError")}</div></article>;
  if (kind === "codex.turn.completed") {
    const turn = asRecord(payload?.turn);
    const status = String(turn?.status ?? "failed");
    if (status === "interrupted") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
    return <article className="message error"><header><AlertTriangle size={15} /><strong>{t("codex.turnFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(turn) || t("codex.unknownError")}</div></article>;
  }
  const item = asRecord(payload?.item);
  const itemImages = extractImageSources(item);
  if (item?.type === "contextCompaction") return <article className="context-compaction-event"><header><Minimize2 size={15} /><strong>{t(kind === "codex.item.started" ? "codex.compacting" : "codex.compacted")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{t(kind === "codex.item.started" ? "codex.compactingDetail" : "codex.compactedDetail")}</div></article>;
  if (item?.type === "collabToolCall" || item?.type === "collabAgentToolCall" || item?.type === "subAgentActivity") return <SubagentEvent event={event} item={item} />;
  if (item?.type === "agentMessage" || item?.type === "plan" || itemImages.length > 0) {
    const text = extractText(item);
    return <article className="message assistant"><header><Bot size={15} /><strong>Codex</strong><time>{formatTime(event.occurred_at)}</time></header>{text && <MarkdownContent text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} />}{itemImages.length > 0 && <MessageImages sources={itemImages} />}</article>;
  }
  return <ToolEvent event={event} item={item} />;
}

function MarkdownContent({ text, workspaceRoot, onOpenFile }: { text: string; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  return <div className="message-content markdown-content"><ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={(url, key) => key === "src" ? safeImageSource(url) : defaultUrlTransform(url)} components={{
    a: ({ href, children, node: _node, ...props }) => { const selection = workspaceFileLink(href, workspaceRoot); if (selection) return <a {...props} href={href} onClick={event => { event.preventDefault(); onOpenFile(selection); }}>{children}</a>; if (isExternalLink(href)) return <a {...props} href={href} target="_blank" rel="noreferrer">{children}</a>; if (href?.startsWith("#")) return <a {...props} href={href}>{children}</a>; return <a {...props} className="unavailable-link" href={href} aria-disabled="true" title={t("codex.linkUnavailable")} onClick={event => event.preventDefault()}>{children}</a>; },
    img: ({ src, alt }) => { const source = safeImageSource(src); return source ? <a className="markdown-image" href={source} target="_blank" rel="noreferrer" title={t("codex.openImage")}><img src={source} alt={alt || t("codex.messageImage")} loading="lazy" referrerPolicy="no-referrer" /></a> : null; }
  }}>{text}</ReactMarkdown></div>;
}

function MessageImages({ sources }: { sources: string[] }) {
  const { t } = useI18n();
  const [active, setActive] = useState("");
  useEffect(() => {
    if (!active) return;
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") setActive(""); };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [active]);
  return <><div className={"message-images " + (sources.length === 1 ? "single" : "")}>{sources.map((source, index) => <button type="button" key={source.slice(0, 80) + ":" + index} title={t("codex.openImage")} onClick={() => setActive(source)}><img src={source} alt={t("codex.messageImage")} loading="lazy" referrerPolicy="no-referrer" /></button>)}</div>{active && <div className="image-lightbox" role="dialog" aria-modal="true" aria-label={t("codex.messageImage")} onClick={() => setActive("")}><button type="button" className="image-lightbox-close" title={t("common.close")} aria-label={t("common.close")} onClick={() => setActive("")}><X size={19} /></button><img src={active} alt={t("codex.messageImage")} referrerPolicy="no-referrer" onClick={event => event.stopPropagation()} /></div>}</>;
}

export function FilePreviewPane({ workspaceID, selection, realtime, onClose }: { workspaceID: string; selection: FilePreviewSelection; realtime: number; onClose: () => void }) {
  const { t } = useI18n();
  const preview = useWorkspacePreview<WorkspaceFilePreview>(workspaceID, selection.path, "file-preview", realtime, t("codex.previewTimedOut"));
  const data = preview.data;
  const loading = preview.loading || !data || data.status === "idle" || data.status === "loading";
  const error = preview.error || data?.error || "";
  const showData = data?.status === "succeeded";
  const updatedText = data?.updated_at ? t("codex.previewUpdated", { time: formatDate(data.updated_at) }) : "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  return <section className="file-preview-panel">
    <header className="file-preview-header"><div><FileCode2 size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{showData && <><span className="file-language">{language.label}</span><span className="file-size">{formatFileSize(data.size)}</span></>}<button type="button" className="icon-button" disabled={preview.requesting} title={t("codex.refreshPreview")} aria-label={t("codex.refreshPreview")} onClick={preview.retry}>{preview.requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closePreview")} aria-label={t("codex.closePreview")} onClick={onClose}><X size={16} /></button></div></header>
    <div className="file-preview-body">
      {showData && (error || preview.loading) && <PreviewStatusNote messageText={error ? t("codex.previewStale", { error }) : t("codex.previewRefreshing")} updatedText={updatedText} loading={preview.loading} retry={preview.retry} retryLabel={t("common.retry")} />}
      {error && !showData ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.previewFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={preview.retry}>{t("common.retry")}</button></div> : !showData && loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div> : showData ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.previewTruncated", { size: formatFileSize(data.size) })}</div>}<Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div>}><HighlightedFile content={data.content} language={language.id} targetLine={selection.line} /></Suspense></> : null}
    </div>
  </section>;
}

export function FileDiffPane({ workspaceID, selection, realtime, writable, notify, onClose }: { workspaceID: string; selection: FilePreviewSelection; realtime: number; writable: boolean; notify: (text: string) => void; onClose: () => void }) {
  const { t } = useI18n();
  const [discarding, setDiscarding] = useState(false);
  const [discardConfirmation, setDiscardConfirmation] = useState(false);
  const previewBodyRef = useRef<HTMLDivElement>(null);
  const preview = useWorkspacePreview<WorkspaceDiffPreview>(workspaceID, selection.path, "diff-preview", realtime, t("codex.diffTimedOut"));
  const data = preview.data;
  const loading = preview.loading || !data || data.status === "idle" || data.status === "loading";
  const error = preview.error || data?.error || "";
  const showData = data?.status === "succeeded";
  const updatedText = data?.updated_at ? t("codex.diffUpdated", { time: formatDate(data.updated_at) }) : "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  const retry = preview.retry;
  const discard = async () => {
    if (!writable || discarding) return;
    setDiscarding(true);
    try {
      await post(`/workspaces/${workspaceID}/git/discard`, { paths: [selection.path], all: false, include_staged: true });
      notify(t("codex.discardFileQueued"));
      setDiscardConfirmation(false);
      onClose();
    } catch (error) {
      notify(message(error));
    } finally {
      setDiscarding(false);
    }
  };
  return <><section className="file-preview-panel file-diff-panel">
    <header className="file-preview-header"><div><FileDiff size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{showData && <><span className="diff-stat additions">+{data.additions}</span><span className="diff-stat deletions">-{data.deletions}</span></>}<button type="button" className="icon-button danger" disabled={!writable || discarding} title={t(writable ? "codex.discardFile" : "project.gitReadOnly")} aria-label={t("codex.discardFile")} onClick={() => setDiscardConfirmation(true)}>{discarding ? <LoaderCircle className="spin" size={15} /> : <Undo2 size={15} />}</button><button type="button" className="icon-button" disabled={preview.requesting || discarding} title={t("codex.refreshDiff")} aria-label={t("codex.refreshDiff")} onClick={retry}>{preview.requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closeDiff")} aria-label={t("codex.closeDiff")} onClick={onClose}><X size={16} /></button></div></header>
    <div className="file-preview-body" ref={previewBodyRef}>
      {showData && (error || preview.loading) && <PreviewStatusNote messageText={error ? t("codex.diffStale", { error }) : t("codex.diffRefreshing")} updatedText={updatedText} loading={preview.loading} retry={retry} retryLabel={t("common.retry")} />}
      {error && !showData ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.diffFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={retry}>{t("common.retry")}</button></div> : !showData && loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div> : showData ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.diffTruncated")}</div>}{data.binary ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.binaryDiff")}</span></div> : !data.content ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.noTextDiff")}</span></div> : <Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div>}><HighlightedDiff content={data.content} language={language.id} unchangedLabel={count => t("codex.unchangedLines", { count })} scrollRef={previewBodyRef} /></Suspense>}</> : null}
    </div>
  </section><ConfirmDialog open={discardConfirmation} danger title={t("codex.discardFile")} impact={t("codex.discardFileConfirm", { path: selection.path })} confirmLabel={t("codex.discardFile")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={discarding} onClose={() => setDiscardConfirmation(false)} onConfirm={discard} /></>;
}

function SubagentEvent({ event, item }: { event: StreamEvent; item: Record<string, unknown> }) {
  const { t } = useI18n();
  const activity = item.type === "subAgentActivity";
  const agentStatus = asRecord(item.agentStatus);
  const status = activity ? String(item.kind ?? item.status ?? "started") : String(agentStatus?.status ?? (typeof item.agentStatus === "string" ? item.agentStatus : item.status ?? "inProgress"));
  const receiverIDs = Array.isArray(item.receiverThreadIds) ? item.receiverThreadIds.map(String) : [item.receiverThreadId, item.newThreadId].filter(value => typeof value === "string" && value).map(String);
  const states = asRecord(item.agentsStates) ?? {};
  const summary = activity ? String(item.agentPath ?? item.agentThreadId ?? "") : `${String(item.tool ?? "spawnAgent")} · ${receiverIDs.length || Object.keys(states).length} ${t("codex.subagentThreads")}`;
  return <article className="subagent-event"><header><Users size={15} /><strong>{t(activity ? "codex.subagentActivity" : "codex.subagentToolCall")}</strong><Status value={status} /><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{summary}</div>{typeof item.prompt === "string" && item.prompt && <p>{item.prompt}</p>}{receiverIDs.length > 0 && <code>{receiverIDs.join(", ")}</code>}</article>;
}

function workspaceFileLink(href: string | undefined, workspaceRoot: string): FilePreviewSelection | null {
  if (!href || href.startsWith("#") || isExternalLink(href)) return null;
  let value = href;
  try { value = decodeURIComponent(value); } catch { return null; }
  value = value.replace(/^file:\/\//i, "");
  let line: number | undefined;
  const hashIndex = value.indexOf("#");
  if (hashIndex >= 0) {
    const match = value.slice(hashIndex).match(/^#L?(\d+)/i);
    if (match) line = Number(match[1]);
    value = value.slice(0, hashIndex);
  }
  value = value.split("?", 1)[0];
  const lineMatch = value.match(/:(\d+)(?::\d+)?$/);
  if (lineMatch) {
    line = Number(lineMatch[1]);
    value = value.slice(0, lineMatch.index);
  }
  const root = workspaceRoot.replaceAll("\\", "/").replace(/\/$/, "");
  value = value.replaceAll("\\", "/");
  if (value.startsWith(root + "/")) value = value.slice(root.length + 1);
  else if (value.startsWith("/")) return null;
  const parts: string[] = [];
  for (const part of value.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (parts.length === 0) return null;
      parts.pop();
    } else parts.push(part);
  }
  return parts.length > 0 ? { path: parts.join("/"), line } : null;
}

function isExternalLink(href: string | undefined) {
  if (!href) return false;
  if (href.startsWith("//")) return true;
  const scheme = href.match(/^([a-z][a-z0-9+.-]*):/i)?.[1]?.toLowerCase();
  return Boolean(scheme && scheme !== "file");
}

function previewLanguage(path: string) {
  const fileName = path.split("/").pop()?.toLowerCase() ?? "";
  const extension = fileName.includes(".") ? fileName.split(".").pop() ?? "" : "";
  const special: Record<string, { id: string; label: string }> = {
    dockerfile: { id: "docker", label: "Dockerfile" }, makefile: { id: "makefile", label: "Makefile" },
    ".env": { id: "bash", label: "Environment" }, "go.mod": { id: "go", label: "Go module" }, "go.sum": { id: "plain", label: "Go checksum" }
  };
  if (special[fileName]) return special[fileName];
  const languages: Record<string, { id: string; label: string }> = {
    js: { id: "javascript", label: "JavaScript" }, jsx: { id: "jsx", label: "JSX" }, ts: { id: "typescript", label: "TypeScript" }, tsx: { id: "tsx", label: "TSX" },
    css: { id: "css", label: "CSS" }, scss: { id: "css", label: "SCSS" }, html: { id: "markup", label: "HTML" }, xml: { id: "markup", label: "XML" }, svg: { id: "markup", label: "SVG" },
    go: { id: "go", label: "Go" }, py: { id: "python", label: "Python" }, java: { id: "java", label: "Java" }, c: { id: "c", label: "C" }, h: { id: "c", label: "C header" }, cpp: { id: "cpp", label: "C++" }, cc: { id: "cpp", label: "C++" }, rs: { id: "rust", label: "Rust" }, swift: { id: "swift", label: "Swift" },
    sh: { id: "bash", label: "Shell" }, bash: { id: "bash", label: "Bash" }, ps1: { id: "powershell", label: "PowerShell" }, sql: { id: "sql", label: "SQL" },
    json: { id: "json", label: "JSON" }, jsonc: { id: "json", label: "JSON" }, yaml: { id: "yaml", label: "YAML" }, yml: { id: "yaml", label: "YAML" }, toml: { id: "toml", label: "TOML" }, ini: { id: "plain", label: "INI" },
    md: { id: "markdown", label: "Markdown" }, mdx: { id: "markdown", label: "MDX" }, txt: { id: "plain", label: "Text" }
  };
  return languages[extension] ?? { id: "plain", label: extension ? extension.toUpperCase() : "Text" };
}

function formatFileSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(size < 10 * 1024 ? 1 : 0)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function ToolEvent({ event, item }: { event: StreamEvent; item: Record<string, unknown> | null }) {
  const { t } = useI18n();
  const type = String(item?.type ?? "tool");
  const title = type === "commandExecution" ? t("codex.command") : type === "fileChange" ? t("codex.changes") : type === "webSearch" ? t("codex.webSearch") : type === "mcpToolCall" ? `${String(item?.server ?? "MCP")} / ${String(item?.tool ?? t("codex.toolCall"))}` : String(item?.tool ?? t("codex.toolCall"));
  const summary = toolSummary(item, t);
  const detail = toolDetail(item, t);
  return <details className={`tool-event ${type === "fileChange" ? "change" : ""}`}><summary><span>{type === "fileChange" ? <GitBranch size={15} /> : type === "commandExecution" ? <SquareTerminal size={15} /> : <Wrench size={15} />}<strong>{title}</strong>{summary && <small>{summary}</small>}</span><time>{formatTime(event.occurred_at)}</time></summary>{detail && <pre>{detail}</pre>}</details>;
}

function CommandEventGroup({ events }: { events: StreamEvent[] }) {
  const { t } = useI18n();
  return <details className="command-event-group"><summary><SquareTerminal size={16} /><strong>{t("codex.commandsRun", { count: events.length })}</strong><ChevronRight className="command-group-chevron" size={16} /></summary><div className="command-event-group-items">{events.map(event => { const payload = asRecord(event.payload); return <ToolEvent key={event.event_id} event={event} item={asRecord(payload?.item)} />; })}</div></details>;
}

function RawEventItem({ event }: { event: StreamEvent }) {
  return <details className="raw-event"><summary><span><Braces size={14} /><strong>{readableKind(event.kind)}</strong></span><time>{formatTime(event.occurred_at)}</time></summary><pre>{pretty(event.payload)}</pre></details>;
}

function WorkingIndicator() {
  const { t } = useI18n();
  return <div className="working-indicator"><Bot size={15} /><span>{t("codex.working")}</span><i /><i /><i /></div>;
}








function LanguageSwitch() {
  const { language, setLanguage, t } = useI18n();
  return <div className="language-switch" role="group" aria-label={t("auth.language")}><button aria-pressed={language === "zh-CN"} className={language === "zh-CN" ? "active" : ""} onClick={() => setLanguage("zh-CN")} type="button">中文</button><button aria-pressed={language === "en"} className={language === "en" ? "active" : ""} onClick={() => setLanguage("en")} type="button">EN</button></div>;
}

export function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }
function CodexModelPicker({ value, onChange, allowServerDefault = false, required = false, requestCustom = 0 }: { value: string; onChange: (value: string) => void; allowServerDefault?: boolean; required?: boolean; requestCustom?: number }) {
  const { t } = useI18n();
  const known = value === "" || codexModelOptions.some(option => option.value === value);
  const [customMode, setCustomMode] = useState(!known);
  const [customValue, setCustomValue] = useState(known ? "" : value);
  useEffect(() => { if (known) { setCustomMode(false); setCustomValue(""); } else { setCustomMode(true); setCustomValue(value); } }, [known, value]);
  useEffect(() => { if (requestCustom) { setCustomMode(true); setCustomValue(""); } }, [requestCustom]);
  const selectValue = customMode ? "__custom__" : value;
  return <div className="codex-model-picker"><select aria-label={t("codex.modelOverride")} value={selectValue} required={required} onChange={event => { if (event.target.value === "__custom__") { setCustomMode(true); setCustomValue(""); onChange(""); } else { setCustomMode(false); onChange(event.target.value); } }}>{allowServerDefault && <option value="">{t("codex.modelServerDefault")}</option>}{codexModelOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}<option value="__custom__">{t("codex.modelCustom")}</option></select>{customMode && <input aria-label={t("codex.customModelName")} value={customValue} onChange={event => { setCustomValue(event.target.value); onChange(event.target.value); }} placeholder={t("codex.customModelPlaceholder")} required={required} />}</div>;
}
export function Dialog(props: Omit<DialogProps, "closeLabel">) { const { t } = useI18n(); return <AccessibleDialog {...props} closeLabel={t("common.close")} />; }
export function ErrorBanner({ text }: { text: string }) { return <div className="error-banner"><AlertTriangle size={16} />{text}</div>; }

function PreviewStatusNote({ messageText, updatedText, loading, retry, retryLabel }: { messageText: string; updatedText: string; loading: boolean; retry: () => void; retryLabel: string }) {
  return <div className={`file-preview-note preview-status-note ${loading ? "loading" : "warning"}`} role="status"><span className="preview-status-message">{loading ? <LoaderCircle className="spin" size={14} /> : <AlertTriangle size={14} />}<strong>{messageText}</strong></span>{updatedText && <small>{updatedText}</small>}<button type="button" className="text-button" disabled={loading} onClick={retry}>{retryLabel}</button></div>;
}

export interface PageProps { realtime: number; notify: (text: string) => void }
const previewPollDelays = [750, 1_500, 3_000, 6_000];
const previewPollTimeout = 20_000;
type WorkspacePreviewSnapshot = { path: string; status: string; error: string };

function useWorkspacePreview<T extends WorkspacePreviewSnapshot>(workspaceID: string, path: string, kind: "file-preview" | "diff-preview", realtime: number, timeoutMessage: string) {
  const key = `${workspaceID}:${kind}:${path}`;
  const endpoint = `/workspaces/${workspaceID}/${kind}?path=${encodeURIComponent(path)}`;
  const [requestNonce, setRequestNonce] = useState(0);
  const [state, setState] = useState<{ key: string; data: T | null; error: string; loading: boolean; requesting: boolean }>({ key: "", data: null, error: "", loading: false, requesting: false });
  const dataRef = useRef<T | null>(null);
  const confirmRef = useRef<(() => void) | null>(null);
  const lastRealtimeRef = useRef(realtime);
  const retry = useCallback(() => setRequestNonce(value => value + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    let started = false;
    let checking = false;
    let waitingForTerminalState = true;
    let pollAttempt = 0;
    let timer = 0;
    let startedAt = 0;
    let hiddenAt = document.hidden ? Date.now() : 0;
    let hiddenDuration = 0;
    const clearTimer = () => { if (timer) { window.clearTimeout(timer); timer = 0; } };
    const visibleElapsed = () => startedAt ? Date.now() - startedAt - hiddenDuration - (hiddenAt ? Date.now() - hiddenAt : 0) : 0;
    const fail = (error: string) => {
      if (!active) return;
      waitingForTerminalState = false;
      clearTimer();
      setState(current => current.key === key ? { ...current, error, loading: false, requesting: false } : current);
    };
    const scheduleConfirmation = () => {
      if (!active || !waitingForTerminalState || document.hidden) return;
      const remaining = previewPollTimeout - visibleElapsed();
      if (remaining <= 0) { fail(timeoutMessage); return; }
      const delay = previewPollDelays[Math.min(pollAttempt, previewPollDelays.length - 1)];
      pollAttempt += 1;
      timer = window.setTimeout(() => {
        timer = 0;
        if (!document.hidden) void confirm();
      }, Math.min(delay, remaining));
    };
    const confirm = async () => {
      if (!active || !waitingForTerminalState || checking || document.hidden) return;
      checking = true;
      try {
        const snapshot = await api<T>(endpoint, { signal: controller.signal });
        if (!active) return;
        const terminal = snapshot.status === "succeeded" || snapshot.status === "failed";
        if (terminal) {
          waitingForTerminalState = false;
          clearTimer();
        }
        if (snapshot.status === "succeeded") dataRef.current = snapshot;
        const displayData = snapshot.status === "succeeded" ? snapshot : dataRef.current ?? snapshot;
        setState({ key, data: displayData, error: snapshot.status === "failed" ? snapshot.error : "", loading: !terminal, requesting: false });
        if (!terminal) scheduleConfirmation();
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        fail(message(error));
      } finally {
        checking = false;
      }
    };
    const start = async () => {
      if (!active || started || document.hidden) return;
      started = true;
      if (dataRef.current?.path !== path) dataRef.current = state.key === key ? state.data : null;
      startedAt = Date.now();
      setState(current => current.key === key ? { ...current, error: "", loading: true, requesting: true } : { key, data: null, error: "", loading: true, requesting: true });
      try {
        await api(`/workspaces/${workspaceID}/${kind}`, { method: "POST", body: JSON.stringify({ path }), signal: controller.signal });
        if (!active) return;
        setState(current => current.key === key ? { ...current, requesting: false } : current);
        await confirm();
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        fail(message(error));
      }
    };
    const confirmCurrent = () => { void confirm(); };
    confirmRef.current = confirmCurrent;
    const visibilityChanged = () => {
      if (document.hidden) {
        if (!hiddenAt) hiddenAt = Date.now();
        clearTimer();
        return;
      }
      if (hiddenAt) { hiddenDuration += Date.now() - hiddenAt; hiddenAt = 0; }
      if (!started) void start();
      else void confirm();
    };
    document.addEventListener("visibilitychange", visibilityChanged);
    if (!document.hidden) void start();
    else setState(current => current.key === key ? { ...current, error: "", loading: true, requesting: false } : { key, data: null, error: "", loading: true, requesting: false });
    return () => {
      active = false;
      clearTimer();
      controller.abort();
      document.removeEventListener("visibilitychange", visibilityChanged);
      if (confirmRef.current === confirmCurrent) confirmRef.current = null;
    };
  }, [endpoint, key, kind, path, requestNonce, timeoutMessage, workspaceID]);

  useEffect(() => {
    if (lastRealtimeRef.current === realtime) return;
    lastRealtimeRef.current = realtime;
    confirmRef.current?.();
  }, [realtime]);

  const current = state.key === key ? state : { key, data: null, error: "", loading: true, requesting: false };
  return { ...current, retry };
}

function mergeThreadEvents(existing: StreamEvent[], incoming: StreamEvent[]) {
  const eventIDs = new Set<string>();
  const sequences = new Set<number>();
  const merged: StreamEvent[] = [];
  for (const event of [...existing, ...incoming]) {
    if (eventIDs.has(event.event_id) || sequences.has(event.sequence)) continue;
    eventIDs.add(event.event_id);
    sequences.add(event.sequence);
    merged.push(event);
  }
  return merged.sort((left, right) => left.sequence - right.sequence);
}

function latestEventSequence(events: StreamEvent[]) {
  return events.reduce((latest, event) => Math.max(latest, event.sequence), 0);
}

function useThreadEvents(threadID: string, rawEvents: boolean, realtime: unknown, globalRevision = 0, streamRevision = 0, invalidationSequence?: number | null) {
  const view = rawEvents ? "raw" : "conversation";
  const path = `/threads/${threadID}/events?view=${view}`;
  const [state, setState] = useState<{ path: string; data: StreamEvent[] | null; error: string; loading: boolean; loadingEarlier: boolean }>({ path: "", data: null, error: "", loading: false, loadingEarlier: false });
  const [reloadNonce, setReloadNonce] = useState(0);
  const appliedReloadNonceRef = useRef(0);
  const loadingEarlierRef = useRef(false);
  const reload = useCallback(() => setReloadNonce(value => value + 1), []);

  useEffect(() => {
    const cached = threadEventsCache.get(path);
    const manuallyReloaded = reloadNonce !== appliedReloadNonceRef.current;
    appliedReloadNonceRef.current = reloadNonce;
    const initialLoad = !cached;
    const currentMaximumSequence = latestEventSequence(cached?.events ?? []);
    const globalResync = !initialLoad && cached.globalRevision !== globalRevision;
    const streamChanged = !initialLoad && cached.streamRevision !== streamRevision;
    const hasSafeIncrementalCursor = typeof invalidationSequence === "number" && Number.isInteger(invalidationSequence) && invalidationSequence > 0 && invalidationSequence > currentMaximumSequence;
    const windowReload = globalResync || (streamChanged && !hasSafeIncrementalCursor);
    const incrementalLoad = !initialLoad && !windowReload && (manuallyReloaded || streamChanged || !Object.is(cached.dependency, realtime));
    if (!initialLoad && !windowReload && !incrementalLoad) {
      setState({ path, data: cached.events, error: "", loading: false, loadingEarlier: false });
      return;
    }

    const controller = new AbortController();
    let active = true;
    setState({ path, data: cached?.events ?? null, error: "", loading: !cached, loadingEarlier: false });
    const load = async () => {
      let events = initialLoad || windowReload ? [] : cached?.events ?? [];
      let after = latestEventSequence(events);
      try {
        let page: StreamEvent[];
        do {
          const requestPath = initialLoad || windowReload ? path : `${path}&after=${after}&limit=1000`;
          page = await api<StreamEvent[]>(requestPath, { signal: controller.signal });
          if (!active) return;
          events = mergeThreadEvents(events, page);
          const nextAfter = latestEventSequence(page);
          if (initialLoad || windowReload || page.length < 1000 || nextAfter <= after) break;
          after = nextAfter;
        } while (true);
        if (!active) return;
        const hasEarlier = initialLoad || windowReload ? page.length >= threadEventsPageSize : cached?.hasEarlier ?? false;
        threadEventsCache.set(path, { events, dependency: realtime, globalRevision, streamRevision, hasEarlier });
        setState({ path, data: events, error: "", loading: false, loadingEarlier: false });
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        setState({ path, data: cached?.events ?? null, error: message(error), loading: false, loadingEarlier: false });
      }
    };
    void load();
    return () => { active = false; controller.abort(); };
  }, [path, realtime, globalRevision, streamRevision, invalidationSequence, reloadNonce]);

  const loadEarlier = useCallback(async () => {
    const entry = threadEventsCache.get(path);
    const earliestSequence = entry?.events[0]?.sequence ?? 0;
    if (!entry?.hasEarlier || earliestSequence <= 0 || loadingEarlierRef.current) return false;
    loadingEarlierRef.current = true;
    setState(current => current.path === path ? { ...current, error: "", loadingEarlier: true } : current);
    try {
      const page = await api<StreamEvent[]>(`${path}&before=${earliestSequence}&limit=${threadEventsPageSize}`);
      const current = threadEventsCache.get(path);
      if (!current || current.globalRevision !== entry.globalRevision || current.streamRevision !== entry.streamRevision) return false;
      const events = mergeThreadEvents(page, current.events);
      const next = { ...current, events, hasEarlier: page.length >= threadEventsPageSize };
      threadEventsCache.set(path, next);
      setState(currentState => currentState.path === path ? { path, data: events, error: "", loading: false, loadingEarlier: false } : currentState);
      return true;
    } catch (error) {
      setState(current => current.path === path ? { ...current, error: message(error), loadingEarlier: false } : current);
      return false;
    } finally {
      loadingEarlierRef.current = false;
    }
  }, [path]);

  const cached = threadEventsCache.get(path);
  const current = state.path === path ? state : { path, data: cached?.events ?? null, error: "", loading: !cached, loadingEarlier: false };
  return { data: current.data, error: current.error, loading: current.loading, loadingEarlier: current.loadingEarlier, hasEarlier: cached?.hasEarlier ?? false, loadEarlier, reload };
}

export function message(error: unknown) { return error instanceof Error ? error.message : "Request failed"; }
function pretty(value: unknown) { try { return JSON.stringify(value, null, 2); } catch { return String(value); } }
function approvalDetail(detail: unknown) { const value = asRecord(detail); if (!value) return pretty(detail); for (const key of ["command", "reason", "message", "question"]) if (typeof value[key] === "string" && value[key]) return value[key] as string; return pretty(detail); }
export async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Could not copy message");
}
function conversationEvents(events: StreamEvent[]) {
  const completedTypes = new Set(["agentMessage", "plan", "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabToolCall", "collabAgentToolCall", "subAgentActivity", "contextCompaction", "webSearch"]);
  const result: StreamEvent[] = [];
  for (const event of events) {
    if (event.kind === "user.message") {
      result.push(event);
      continue;
    }
    const payload = asRecord(event.payload);
    if (event.kind === "codex.item.started") {
      const item = asRecord(payload?.item);
      if (item?.type === "contextCompaction") result.push(event);
      continue;
    }
    if (event.kind === "codex.item.completed") {
      const item = asRecord(payload?.item);
      if (item?.type === "userMessage") {
        const images = extractImageSources(item?.content);
        const text = extractText(item?.content);
        if (images.length > 0) {
          for (let index = result.length - 1; index >= 0; index--) {
            if (result[index].kind !== "user.message") continue;
            const messagePayload = asRecord(result[index].payload);
            if (text && String(messagePayload?.text ?? "") !== text) continue;
            result[index] = { ...result[index], payload: { ...messagePayload, images, image_count: images.length } };
            break;
          }
        }
        continue;
      }
      if (completedTypes.has(String(item?.type ?? "")) || extractImageSources(item).length > 0) result.push(event);
      continue;
    }
    if (event.kind === "codex.error") {
      if (payload?.willRetry !== true) result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.completed") {
      const turn = asRecord(payload?.turn);
      if (turn?.status === "failed" || turn?.status === "interrupted") result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.cancelled") {
      result.push(event);
      continue;
    }
    if (event.kind === "codex.turn.failed" || event.kind === "codex.interrupt.failed" || event.kind === "codex.approval.failed" || event.kind === "codex.compact.failed") result.push(event);
  }
  return result;
}
function groupCommandEvents(events: StreamEvent[]): ConversationDisplayItem[] {
  const result: ConversationDisplayItem[] = [];
  for (let index = 0; index < events.length;) {
    if (!isCommandEvent(events[index])) {
      result.push({ type: "event", event: events[index] });
      index++;
      continue;
    }
    const commands: StreamEvent[] = [];
    while (index < events.length && isCommandEvent(events[index])) commands.push(events[index++]);
    if (commands.length === 1) result.push({ type: "event", event: commands[0] });
    else result.push({ type: "commandGroup", events: commands });
  }
  return result;
}
function isCommandEvent(event: StreamEvent) {
  if (event.kind !== "codex.item.completed") return false;
  const payload = asRecord(event.payload);
  return asRecord(payload?.item)?.type === "commandExecution";
}
function asRecord(value: unknown): Record<string, unknown> | null { return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function safeImageSource(value: unknown): string {
  if (typeof value !== "string") return "";
  const source = value.trim();
  if (/^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i.test(source)) return source;
  try {
    const url = new URL(source);
    return url.protocol === "https:" || url.protocol === "http:" ? url.toString() : "";
  } catch {
    return "";
  }
}
function extractImageSources(payload: unknown): string[] {
  const sources: string[] = [];
  const seenSources = new Set<string>();
  const seenObjects = new Set<object>();
  const visit = (value: unknown) => {
    const directSource = safeImageSource(value);
    if (directSource) {
      if (!seenSources.has(directSource)) {
        seenSources.add(directSource);
        sources.push(directSource);
      }
      return;
    }
    if (Array.isArray(value)) {
      for (const item of value) visit(item);
      return;
    }
    const record = asRecord(value);
    if (!record || seenObjects.has(record)) return;
    seenObjects.add(record);
    const type = String(record.type ?? "").toLowerCase();
    if (type === "image" || type === "input_image" || type === "output_image" || type === "image_url") {
      for (const key of ["url", "data_url", "image_url", "data"]) {
        const candidate = asRecord(record[key])?.url ?? record[key];
        const source = safeImageSource(candidate);
        if (source && !seenSources.has(source)) {
          seenSources.add(source);
          sources.push(source);
        }
      }
    }
    for (const [key, child] of Object.entries(record)) {
      if (key === "text" || key === "delta" || key === "message" || key === "output" || key === "aggregatedOutput") continue;
      if (typeof child === "object" && child !== null) visit(child);
    }
  };
  visit(payload);
  return sources.slice(0, 8);
}
function extractText(payload: unknown): string {
  if (typeof payload === "string") return payload;
  if (Array.isArray(payload)) return payload.map(extractText).filter(Boolean).join("\n");
  const value = asRecord(payload);
  if (!value) return "";
  for (const key of ["delta", "text", "message", "diff", "output"]) if (typeof value[key] === "string") return value[key] as string;
  for (const key of ["content", "item", "error"]) { const text = extractText(value[key]); if (text) return text; }
  return "";
}
function errorText(payload: Record<string, unknown> | null) { return extractText(payload?.error) || extractText(payload); }
function toolSummary(item: Record<string, unknown> | null, translate: (key: string, values?: Record<string, string | number>) => string) {
  if (!item) return "";
  const type = String(item.type ?? "");
  const primary = type === "commandExecution" ? String(item.command ?? "") : type === "webSearch" ? String(item.query ?? "") : type === "fileChange" && Array.isArray(item.changes) ? String(item.changes.length) : "";
  const status = typeof item.status === "string" ? item.status : "";
  const statusLabel = status ? translate(`status.${status}`) : "";
  const exitCode = type === "commandExecution" && typeof item.exitCode === "number" ? translate("codex.exitCode", { code: item.exitCode }) : "";
  return [primary, statusLabel, exitCode].filter(Boolean).join(" · ");
}
function toolDetail(item: Record<string, unknown> | null, translate: (key: string, values?: Record<string, string | number>) => string) {
  if (!item) return "";
  const type = String(item.type ?? "");
  if (type === "commandExecution") {
    const output = typeof item.aggregatedOutput === "string" && item.aggregatedOutput.trim() ? item.aggregatedOutput : translate("codex.noCommandOutput");
    const exitCode = typeof item.exitCode === "number" ? translate("codex.exitCode", { code: item.exitCode }) : "";
    return [`$ ${String(item.command ?? "")}`, output, exitCode].filter(Boolean).join("\n\n");
  }
  if (type === "fileChange" && Array.isArray(item.changes)) return item.changes.map(change => { const value = asRecord(change); return value ? `${String(value.kind ?? "updated")} ${String(value.path ?? "")}\n${String(value.diff ?? "")}`.trim() : pretty(change); }).join("\n\n");
  if (type === "webSearch") return String(item.query ?? "");
  return pretty({ arguments: item.arguments, result: item.result, error: item.error });
}
function readableKind(kind: string) { return kind.replace(/^codex\./, "").replaceAll(".", " / ").replaceAll("/", " / "); }
