import { FormEvent, KeyboardEvent as ReactKeyboardEvent, lazy, PointerEvent as ReactPointerEvent, ReactNode, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Archive,
  ArchiveRestore,
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpFromLine,
  Ban,
  BellRing,
  Bot,
  Boxes,
  Braces,
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
  MonitorDot,
  Network,
  Pencil,
  Pin,
  PinOff,
  Plus,
  RefreshCw,
  Rocket,
  RotateCcw,
  Search,
  Server as ServerIcon,
  Settings,
  ShieldCheck,
  SquareTerminal,
  StickyNote,
  Target,
  Trash2,
  Undo2,
  UserRound,
  Wrench,
  Wifi,
  WifiOff,
  X
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { api, APIError, patch, post, postStream, put, remove, setSession, socketURL } from "./api";
import { ContextMenu, type ContextMenuAction } from "./ContextMenu";
import { SlashCommandMenu, type SlashCommandItem } from "./SlashCommandMenu";
import { currentLocale, useI18n } from "./i18n";
import {
  CreateProjectDialog,
  ProjectDeletionDialog,
  ProjectDetailsDialog,
  ProjectTable,
  WorkspaceGitDialog,
  WorkspaceManagerDialog,
  WorkspaceTable,
  newCreateProjectFormValue,
  type CreateProjectDialogLabels,
  type CreateProjectRequest,
  type ProjectLifecycleState,
  type ProjectListRecord,
  type ProjectEditValue,
  type ProjectDeletionMode,
  type WorkspaceDeletionMode,
  type WorkspaceGitAction,
  type WorkspaceListRecord
} from "./pages/projects";
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
  Project,
  ProjectDeletionPlan,
  ProjectDetail,
  SecretSet,
  Server,
  Session,
  SSHBootstrapResult,
  SSHBootstrapStreamEvent,
  SSHHostKey,
  StreamEvent,
  Summary,
  Thread,
  Workspace,
  WorkspaceChange,
  WorkspaceChangesSnapshot,
  WorkspaceDeletionPlan,
  WorkspaceDiffPreview,
  WorkspaceGitSnapshot,
  WorkspaceFile,
  WorkspaceFilePreview,
  WorkspaceFilesSnapshot
} from "./types";

type View = "dashboard" | "servers" | "projects" | "codex" | "deployments" | "monitoring" | "settings";
type ConversationDisplayItem = { type: "event"; event: StreamEvent } | { type: "commandGroup"; events: StreamEvent[] };
type AuthState = "loading" | "setup" | "login" | "authenticated";
type InstallLogEntry = { step: string; status: "running" | "done" | "error"; current: number; total: number; detail: string };
type ComposerImage = { id: string; dataURL: string };
type FilePreviewSelection = { path: string; line?: number; mode?: "file" | "diff" };
const HighlightedFile = lazy(() => import("./FilePreviewCode"));
const HighlightedDiff = lazy(() => import("./FileDiffCode"));
type StreamRevisions = Record<string, number>;

const defaultCodexModel = "gpt-5.6-sol";
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

function locationFor(view: View, threadID = "") {
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
  const [session, setCurrentSession] = useState<Session | null>(null);
  const [view, setView] = useState<View>(initialLocation.view);
  const [codexThreadID, setCodexThreadID] = useState(initialLocation.threadID);
  const [mobileNav, setMobileNav] = useState(false);
  const [realtime, setRealtime] = useState(0);
  const [streamRevisions, setStreamRevisions] = useState<StreamRevisions>({});
  const [socketConnected, setSocketConnected] = useState(false);
  const [approvalSignal, setApprovalSignal] = useState(0);
  const [toast, setToast] = useState("");
  const approvals = useData<Approval[]>(auth === "authenticated" ? "/approvals" : null, realtime);

  const authenticate = useCallback((value: Session | null) => {
    setSession(value);
    setCurrentSession(value);
    setAuth(value ? "authenticated" : "login");
  }, []);

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const status = await api<{ configured: boolean }>("/setup/status");
        if (!status.configured) {
          if (active) setAuth("setup");
          return;
        }
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
    const pendingStreams = new Set<string>();
    const scheduleRefresh = () => {
      if (refreshTimer) return;
      refreshTimer = window.setTimeout(() => {
        refreshTimer = 0;
        setRealtime(value => value + 1);
        if (pendingStreams.size > 0) {
          const streams = Array.from(pendingStreams);
          pendingStreams.clear();
          setStreamRevisions(current => {
            const next = { ...current };
            for (const streamID of streams) next[streamID] = (next[streamID] ?? 0) + 1;
            return next;
          });
        }
      }, 100);
    };
    const connect = () => {
      if (stopped) return;
      socket = new WebSocket(socketURL());
      socket.onopen = () => { setSocketConnected(true); pendingStreams.add("*"); scheduleRefresh(); };
      socket.onmessage = messageEvent => {
        try {
          const event = JSON.parse(String(messageEvent.data)) as Partial<StreamEvent>;
          pendingStreams.add(event.stream_id || "*");
        } catch {
          pendingStreams.add("*");
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
  if (auth === "setup") return <SetupScreen onReady={() => setAuth("login")} />;
  if (auth === "login") return <LoginScreen onLogin={authenticate} />;

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
    dashboard: <Dashboard realtime={realtime} onNavigate={selectView} />,
    servers: <ServersPage realtime={realtime} notify={setToast} />,
    projects: <ProjectsPage realtime={realtime} notify={setToast} />,
    codex: <CodexPage realtime={realtime} streamRevisions={streamRevisions} approvals={approvals.data ?? []} approvalSignal={approvalSignal} reloadApprovals={approvals.reload} notify={setToast} selectedThreadID={codexThreadID} onSelectThread={(threadID, replace) => selectView("codex", threadID, replace)} />,
    deployments: <DeploymentsPage realtime={realtime} notify={setToast} />,
    monitoring: <MonitoringPage realtime={realtime} />,
    settings: <SettingsPage realtime={realtime} notify={setToast} />
  }[view];

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
        <div className="brand"><span className="brand-mark">W</span><span>{t("app.name")}</span></div>
        <nav aria-label={t("nav.primary")}>
          {navigation.map(item => {
            const Icon = item.icon;
            return <button key={item.id} className={view === item.id ? "active" : ""} onClick={() => selectView(item.id)}><Icon size={18} /><span>{t(item.labelKey)}</span>{item.id === "codex" && (approvals.data?.length ?? 0) > 0 && <b className="nav-count">{approvals.data?.length}</b>}</button>;
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
          <div className="topbar-actions"><LanguageSwitch /><span className={`connection ${socketConnected ? "" : "offline"}`}>{socketConnected ? <Wifi size={15} /> : <WifiOff size={15} />} {t(socketConnected ? "app.live" : "app.reconnecting")}</span>{(approvals.data?.length ?? 0) > 0 && <button className="approval-pill" onClick={() => { selectView("codex"); setApprovalSignal(value => value + 1); }}><ShieldCheck size={15} />{t("codex.approvalCount", { count: approvals.data?.length ?? 0 })}</button>}</div>
        </header>
        <div className="page-content">{page}</div>
      </main>
      {toast && <div className="toast"><Check size={17} />{toast}</div>}
    </div>
  );
}

function LoadingScreen() {
  const { t } = useI18n();
  return <div className="auth-layout"><div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong></div><LoaderCircle className="spin" size={28} /></div>;
}

function SetupScreen({ onReady }: { onReady: () => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("admin");
  const [result, setResult] = useState<{ totp_uri: string; totp_secret: string; recovery_codes: string[] } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try { setResult(await post("/setup", { username })); } catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  return <div className="auth-layout">
    <section className="auth-panel">
      <div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong><LanguageSwitch /></div>
      {!result ? <form onSubmit={submit}>
        <div className="section-heading"><h1>{t("auth.createAdmin")}</h1><span className="status-tag neutral"><LockKeyhole size={14} />{t("auth.singleAdmin")}</span></div>
        <Field label={t("auth.username")}><input value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" required /></Field>
        {error && <ErrorBanner text={error} />}
        <button className="primary-button full" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <ShieldCheck size={17} />}{t("auth.createAdmin")}</button>
      </form> : <div>
        <div className="section-heading"><h1>{t("auth.twoFactor")}</h1><span className="status-tag success"><Check size={14} />{t("auth.ready")}</span></div>
        <div className="totp-grid"><div className="qr"><QRCodeSVG value={result.totp_uri} size={156} /></div><div><Field label={t("auth.totpSecret")}><code className="secret-code">{result.totp_secret}</code></Field><p className="label">{t("auth.recoveryCodes")}</p><div className="recovery-codes">{result.recovery_codes.map(code => <code key={code}>{code}</code>)}</div></div></div>
        <button className="primary-button full" onClick={onReady}><ChevronRight size={17} />{t("auth.continue")}</button>
      </div>}
    </section>
  </div>;
}

function LoginScreen({ onLogin }: { onLogin: (session: Session) => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("admin");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try { onLogin(await post<Session>("/auth/login", { username, code })); } catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  return <div className="auth-layout"><section className="auth-panel compact">
    <div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong><LanguageSwitch /></div>
    <form onSubmit={submit}><h1>{t("auth.signIn")}</h1>
      <Field label={t("auth.username")}><input value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" required /></Field>
      <Field label={t("auth.code")}><input value={code} onChange={e => setCode(e.target.value)} inputMode="numeric" autoComplete="one-time-code" required /></Field>
      {error && <ErrorBanner text={error} />}
      <button className="primary-button full" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <LockKeyhole size={17} />}{t("auth.signIn")}</button>
    </form>
  </section></div>;
}

function Dashboard({ realtime, onNavigate }: { realtime: number; onNavigate: (view: View) => void }) {
  const { t } = useI18n();
  const summary = useData<Summary>("/summary", realtime);
  if (summary.loading) return <PageLoading />;
  if (!summary.data) return <ErrorState error={summary.error} reload={summary.reload} />;
  const stats = [
    [t("nav.servers"), summary.data.counts.online, t("dashboard.registered", { count: summary.data.counts.servers }), ServerIcon, "green", "servers"],
    [t("nav.projects"), summary.data.counts.projects, t("dashboard.codexSessions", { count: summary.data.counts.threads }), GitBranch, "cyan", "projects"],
    [t("dashboard.inProgress"), summary.data.counts.deployments, t("nav.deployments"), Rocket, "amber", "deployments"],
    [t("dashboard.openAlerts"), summary.data.counts.alerts, t("dashboard.requiresAttention"), BellRing, "red", "monitoring"]
  ] as const;
  return <div className="page-stack">
    <section className="stat-grid">{stats.map(([label, value, detail, Icon, tone, target]) => <button className="stat" key={label} onClick={() => onNavigate(target)}><span className={`stat-icon ${tone}`}><Icon size={20} /></span><span><small>{label}</small><strong>{value ?? 0}</strong><em>{detail}</em></span><ChevronRight size={17} /></button>)}</section>
    <div className="two-column">
      <Section title={t("dashboard.recentDeployments")} icon={<Rocket size={18} />} action={<button className="text-button" onClick={() => onNavigate("deployments")}>{t("common.viewAll")}<ChevronRight size={15} /></button>}>
        <DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("column.started")]} empty={t("dashboard.noDeployments")}>{(summary.data.deployments ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td>{relative(item.created_at)}</td></tr>)}</DataTable>
      </Section>
      <Section title={t("dashboard.activeAlerts")} icon={<AlertTriangle size={18} />} action={<button className="text-button" onClick={() => onNavigate("monitoring")}>{t("common.viewAll")}<ChevronRight size={15} /></button>}>
        <div className="alert-list">{(summary.data.alerts ?? []).length === 0 ? <Empty icon={<ShieldCheck size={23} />} text={t("dashboard.noAlerts")} /> : (summary.data.alerts ?? []).map(alert => <div className="alert-row" key={alert.id}><span className={`severity ${alert.severity}`} /><div><strong>{alert.title}</strong><small>{alert.server_name} · {relative(alert.opened_at)}</small></div><Status value={alert.severity} /></div>)}</div>
      </Section>
    </div>
  </div>;
}

function ServersPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const servers = useData<Server[]>("/servers", realtime);
  const credentialProfiles = useData<CredentialProfile[]>("/credential-profiles", realtime);
  const codexProfiles = (credentialProfiles.data ?? []).filter(profile => profile.kind === "codex");
  const gitProfiles = (credentialProfiles.data ?? []).filter(profile => profile.kind === "git");
  const [dialog, setDialog] = useState(false);
  const [step, setStep] = useState<"form" | "fingerprint" | "complete">("form");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [hostKey, setHostKey] = useState<SSHHostKey | null>(null);
  const [result, setResult] = useState<SSHBootstrapResult | null>(null);
  const [installLogs, setInstallLogs] = useState<InstallLogEntry[]>([]);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [metadataBusy, setMetadataBusy] = useState(false);
  const [metadataError, setMetadataError] = useState("");
  const [updatingServer, setUpdatingServer] = useState("");
  const [credentialServer, setCredentialServer] = useState<Server | null>(null);
  const [credentialBusy, setCredentialBusy] = useState(false);
  const [credentialError, setCredentialError] = useState("");
  const [credentialForm, setCredentialForm] = useState({ codexProfileID: "", gitProfileID: "" });
  const [metadataForm, setMetadataForm] = useState({ address: "", configuration: "", notes: "" });
  const [form, setForm] = useState({
    name: "", roots: "/srv, /opt, /home", host: "", port: "22", user: "root", authMethod: "private_key",
    password: "", privateKey: "", privateKeyPassphrase: "", configuration: "", notes: "", codexProfileID: "", gitProfileID: "", allowSudo: false
  });
  useEffect(() => {
    const firstCodexProfile = credentialProfiles.data?.find(profile => profile.kind === "codex");
    if (dialog && firstCodexProfile) setForm(current => current.codexProfileID ? current : { ...current, codexProfileID: firstCodexProfile.id });
  }, [credentialProfiles.data, dialog]);
  const reset = () => {
    setStep("form"); setError(""); setHostKey(null); setResult(null); setInstallLogs([]); setBusy(false);
    setForm({ name: "", roots: "/srv, /opt, /home", host: "", port: "22", user: "root", authMethod: "private_key", password: "", privateKey: "", privateKeyPassphrase: "", configuration: "", notes: "", codexProfileID: codexProfiles[0]?.id ?? "", gitProfileID: "", allowSudo: false });
  };
  const open = () => { reset(); setDialog(true); };
  const close = () => { if (busy) return; setDialog(false); reset(); };
  const probe = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true); setError("");
    try {
      setHostKey(await post<SSHHostKey>("/servers/ssh/probe", { host: form.host.trim(), port: Number(form.port) }));
      setStep("fingerprint");
    } catch (err) { setError(enrollmentMessage(err, t)); } finally { setBusy(false); }
  };
  const install = async () => {
    if (!hostKey) return;
    setBusy(true); setError(""); setInstallLogs([]);
    try {
      let installed: SSHBootstrapResult | null = null;
      let streamedFailure: APIError | null = null;
      await postStream<SSHBootstrapStreamEvent>("/servers/ssh/bootstrap-stream", {
        name: form.name.trim(), scan_roots: form.roots.split(",").map(value => value.trim()).filter(Boolean),
        host: form.host.trim(), port: Number(form.port), user: form.user.trim(), auth_method: form.authMethod,
        password: form.authMethod === "password" ? form.password : "",
        private_key: form.authMethod === "private_key" ? form.privateKey : "",
        private_key_passphrase: form.authMethod === "private_key" ? form.privateKeyPassphrase : "",
        configuration: form.configuration.trim(), notes: form.notes.trim(),
        host_key_fingerprint: hostKey.fingerprint,
        codex_profile_id: form.codexProfileID, git_profile_id: form.gitProfileID, allow_sudo: form.allowSudo
      }, event => {
        if (event.type === "progress" && event.step) {
          setInstallLogs(current => {
            const existing = current.findIndex(entry => entry.step === event.step);
            if (existing >= 0) return current.map((entry, index) => index === existing ? { ...entry, current: event.current ?? entry.current, total: event.total ?? entry.total } : entry);
            return [...current.map<InstallLogEntry>(entry => entry.status === "running" ? { ...entry, status: "done" } : entry), { step: event.step!, status: "running", current: event.current ?? 0, total: event.total ?? 0, detail: "" }];
          });
        } else if (event.type === "error") {
          streamedFailure = new APIError(422, event.error ?? t("server.error.installation_failed"), event.code ?? "installation_failed");
          setInstallLogs(current => current.map<InstallLogEntry>((entry, index) => index === current.length - 1 ? { ...entry, status: "error", detail: event.detail ?? "" } : entry));
        } else if (event.type === "complete" && event.result) {
          installed = event.result;
          setInstallLogs(current => current.map<InstallLogEntry>(entry => ({ ...entry, status: "done" })));
        }
      });
      if (streamedFailure) throw streamedFailure;
      if (!installed) throw new APIError(502, t("server.error.stream_incomplete"), "stream_incomplete");
      setResult(installed); setStep("complete"); servers.reload(); notify(t("server.installed"));
      setForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "" }));
    } catch (err) { setError(enrollmentMessage(err, t)); } finally { setBusy(false); }
  };
  const choosePrivateKey = async (file?: File) => {
    setError("");
    if (!file) { setForm(current => ({ ...current, privateKey: "" })); return; }
    if (file.size > 256 * 1024) { setError(t("server.privateKeyTooLarge")); return; }
    try { const privateKey = await file.text(); setForm(current => ({ ...current, privateKey })); } catch (err) { setError(message(err)); }
  };
  const editServer = (server: Server) => {
    setEditingServer(server); setMetadataError("");
    setMetadataForm({ address: server.address, configuration: server.configuration, notes: server.notes });
  };
  const saveMetadata = async (event: FormEvent) => {
    event.preventDefault();
    if (!editingServer) return;
    setMetadataBusy(true); setMetadataError("");
    try {
      await patch(`/servers/${editingServer.id}`, metadataForm);
      setEditingServer(null); servers.reload(); notify(t("server.informationSaved"));
    } catch (err) { setMetadataError(message(err)); } finally { setMetadataBusy(false); }
  };
  const editCredentials = (server: Server) => {
    setCredentialServer(server); setCredentialError("");
    setCredentialForm({ codexProfileID: server.codex_profile_id, gitProfileID: server.git_profile_id });
  };
  const saveCredentials = async (event: FormEvent) => {
    event.preventDefault();
    if (!credentialServer) return;
    setCredentialBusy(true); setCredentialError("");
    try {
      await post(`/servers/${credentialServer.id}/credential-profiles`, {
        codex_profile_id: credentialForm.codexProfileID,
        git_profile_id: credentialForm.gitProfileID
      });
      setCredentialServer(null); notify(t("server.credentialsQueued"));
    } catch (err) { setCredentialError(message(err)); } finally { setCredentialBusy(false); }
  };
  const updateAgent = async (server: Server) => {
    if (!server.agent_update_available || !confirm(t("server.confirmUpdate", { name: server.name, version: server.agent_target_version }))) return;
    setUpdatingServer(`agent:${server.id}`);
    try {
      await post(`/servers/${server.id}/agent-update`, {});
      notify(t("server.updateQueued", { version: server.agent_target_version }));
    } catch (err) { notify(message(err)); } finally { setUpdatingServer(""); }
  };
  const updateCodex = async (server: Server) => {
    if (!server.codex_update_available || !confirm(t("server.confirmCodexUpdate", { name: server.name, version: server.codex_target_version }))) return;
    setUpdatingServer(`codex:${server.id}`);
    try {
      await post(`/servers/${server.id}/codex-update`, {});
      notify(t("server.codexUpdateQueued", { version: server.codex_target_version }));
    } catch (err) { notify(message(err)); } finally { setUpdatingServer(""); }
  };
  return <div className="page-stack"><Section title={t("server.registered")} icon={<ServerIcon size={18} />} action={<button className="primary-button" onClick={open}><Plus size={17} />{t("server.enroll")}</button>}>
    <DataTable headers={[t("column.server"), t("server.information"), t("column.connectivity"), t("column.agent"), t("column.codex"), t("column.lastSeen"), ""]} empty={t("server.none")}>{(servers.data ?? []).map(server => {
      const agentUpdateTitle = server.status !== "online" ? t("server.updateOffline") : server.agent_update_available ? t("server.updateAgent", { version: server.agent_target_version }) : !server.agent_version ? t("common.awaitingHeartbeat") : !server.agent_update_supported ? t("server.updateRequiresReinstall") : server.agent_version === server.agent_target_version ? t("server.agentLatest") : t("server.updateUnavailable");
      const codexUpdateTitle = server.status !== "online" ? t("server.codexUpdateOffline") : !server.codex_update_supported ? t("server.codexUpdateRequiresAgent") : server.codex_update_available ? t("server.updateCodex", { version: server.codex_target_version }) : t("server.codexLatest", { version: server.codex_target_version });
      return <tr key={server.id}><td><div className="cell-main"><strong>{server.name}</strong><small>{server.hostname || t("common.awaitingHeartbeat")}</small></div></td><td><ServerInformation server={server} /></td><td><Status value={server.status} icon={server.status === "online" ? <Wifi size={13} /> : <WifiOff size={13} />} /></td><td><code>{server.agent_version || "-"}</code></td><td><span className={server.codex_ready ? "inline-success" : "muted"}>{server.codex_ready ? <Check size={14} /> : <Ban size={14} />}{server.codex_version || t("common.unavailable")}</span></td><td>{server.last_seen_at ? relative(server.last_seen_at) : t("common.never")}</td><td><div className="row-actions"><button className="icon-button" disabled={server.status !== "online" || !server.agent_update_available || updatingServer !== ""} title={agentUpdateTitle} onClick={() => void updateAgent(server)}><RefreshCw className={updatingServer === `agent:${server.id}` ? "spin" : ""} size={15} /></button><button className="icon-button" disabled={server.status !== "online" || !server.codex_update_available || updatingServer !== ""} title={codexUpdateTitle} onClick={() => void updateCodex(server)}>{updatingServer === `codex:${server.id}` ? <LoaderCircle className="spin" size={15} /> : <SquareTerminal size={15} />}</button><button className="icon-button" disabled={server.status !== "online" || updatingServer !== ""} title={server.status === "online" ? t("server.editCredentials") : t("server.credentialsOffline")} onClick={() => editCredentials(server)}><KeyRound size={15} /></button><button className="icon-button" title={t("server.editInformation")} onClick={() => editServer(server)}><Pencil size={15} /></button><button className="icon-button danger" title={t("server.revoke")} onClick={async () => { if (!confirm(t("server.confirmRevoke", { name: server.name }))) return; await remove(`/servers/${server.id}`); notify(t("server.revoked")); servers.reload(); }}><X size={16} /></button></div></td></tr>;
    })}</DataTable>
  </Section><Dialog open={dialog} title={t("server.enrollLinux")} onClose={close} wide>{step === "form" ? <form onSubmit={probe}>
    {error && <ErrorBanner text={error} />}
    <div className="form-grid"><Field label={t("server.name")}><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required /></Field><Field label={t("server.scanRoots")}><input value={form.roots} onChange={e => setForm({ ...form, roots: e.target.value })} required /></Field></div>
    <div className="form-grid"><Field label={t("server.configuration")}><textarea rows={3} maxLength={4096} value={form.configuration} onChange={e => setForm({ ...form, configuration: e.target.value })} placeholder={t("server.configurationPlaceholder")} /></Field><Field label={t("server.notes")}><textarea rows={3} maxLength={4096} value={form.notes} onChange={e => setForm({ ...form, notes: e.target.value })} placeholder={t("server.notesPlaceholder")} /></Field></div>
    <div className="form-grid thirds"><Field label={t("server.sshHost")}><input value={form.host} onChange={e => setForm({ ...form, host: e.target.value })} placeholder="192.0.2.10" required /></Field><Field label={t("server.sshPort")}><input type="number" min="1" max="65535" value={form.port} onChange={e => setForm({ ...form, port: e.target.value })} required /></Field><Field label={t("server.sshUser")}><input value={form.user} onChange={e => setForm({ ...form, user: e.target.value })} placeholder="root / ubuntu / ec2-user" required /></Field></div>
    <Field label={t("server.authMethod")}><select value={form.authMethod} onChange={e => setForm({ ...form, authMethod: e.target.value })}><option value="private_key">{t("server.authPrivateKey")}</option><option value="password">{t("server.authPassword")}</option></select></Field>
    {form.authMethod === "private_key" ? <div className="form-grid"><Field label={t("server.privateKeyFile")}><input type="file" accept=".pem,.key,text/plain" onChange={e => void choosePrivateKey(e.target.files?.[0])} required={!form.privateKey} /></Field><Field label={t("server.privateKeyPassphrase")}><input type="password" autoComplete="off" value={form.privateKeyPassphrase} onChange={e => setForm({ ...form, privateKeyPassphrase: e.target.value })} placeholder={t("common.optional")} /></Field></div> : <Field label={t("server.sshPassword")}><input type="password" autoComplete="new-password" value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} required /></Field>}
    <div className="form-divider"><span>{t("server.credentialProfiles")}</span></div>
    <div className="form-grid"><Field label={t("server.codexProfile")}><select value={form.codexProfileID} onChange={e => setForm({ ...form, codexProfileID: e.target.value })} required><option value="">{t(codexProfiles.length ? "server.selectCodexProfile" : "server.noCodexProfiles")}</option>{codexProfiles.map(profile => <option value={profile.id} key={profile.id}>{profile.name} · {profile.model}</option>)}</select></Field><Field label={t("server.gitProfile")}><select value={form.gitProfileID} onChange={e => setForm({ ...form, gitProfileID: e.target.value })}><option value="">{t("server.noGitProfile")}</option>{gitProfiles.map(profile => { const ready = Boolean(profile.commit_name && profile.commit_email); return <option value={profile.id} key={profile.id} disabled={!ready}>{profile.name} · {profile.username}{ready ? "" : ` · ${t("settings.gitIdentityMissing")}`}</option>; })}</select></Field></div>
    <label className={`agent-sudo-option ${form.allowSudo ? "enabled" : ""}`}><input type="checkbox" checked={form.allowSudo} onChange={event => setForm({ ...form, allowSudo: event.target.checked })} /><span><strong>{t("server.allowAgentSudo")}</strong><small>{t("server.allowAgentSudoWarning")}</small></span></label>
    <DialogActions><button type="button" className="secondary-button" onClick={close}>{t("common.cancel")}</button><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : <ShieldCheck size={16} />}{busy ? t("server.probing") : t("server.probeFingerprint")}</button></DialogActions>
  </form> : step === "fingerprint" && hostKey ? <div className="enrollment-step">
    {error && <ErrorBanner text={error} />}
    <div className="fingerprint-status"><ShieldCheck size={28} /><div><strong>{t("server.fingerprint")}</strong><span>{form.host}:{form.port} · {hostKey.key_type}</span></div></div>
    <code className="fingerprint-value">{hostKey.fingerprint}</code>
    <p className="security-notice">{t("server.fingerprintNotice")}</p>
    {installLogs.length > 0 && <div className="install-log" aria-live="polite"><div className="install-log-heading"><SquareTerminal size={16} /><strong>{t("server.installLog")}</strong></div><div className="install-log-lines">{installLogs.map(entry => <div className={`install-log-entry ${entry.status}`} key={entry.step}>{entry.status === "running" ? <LoaderCircle className="spin" size={15} /> : entry.status === "done" ? <Check size={15} /> : <AlertTriangle size={15} />}<span>{t(`server.progress.${entry.step}`)}</span>{entry.total > 0 && <code>{Math.min(100, Math.round((entry.current / entry.total) * 100))}%</code>}{entry.detail && <small>{entry.detail}</small>}</div>)}</div></div>}
    <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => { setStep("form"); setError(""); setInstallLogs([]); }}><Undo2 size={16} />{t("server.back")}</button><button className="primary-button" disabled={busy} onClick={() => void install()}>{busy ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{busy ? t("server.installing") : t("server.confirmInstall")}</button></DialogActions>
  </div> : <div className="enrollment-step enrollment-complete"><div className="completion-mark"><Check size={28} /></div><h3>{t("server.installed")}</h3>{result && <p>{t("server.installedSummary", { hostname: result.hostname, architecture: result.architecture })}</p>}{result && result.warnings.length > 0 && <div className="warning-list"><strong>{t("server.warningTitle")}</strong>{result.warnings.map(warning => <span key={warning}><AlertTriangle size={15} />{t(`server.warning.${warning}`)}</span>)}</div>}<DialogActions><button className="primary-button" onClick={close}><Check size={16} />{t("common.done")}</button></DialogActions></div>}</Dialog>
  <Dialog open={editingServer !== null} title={t("server.editInformation")} onClose={() => { if (!metadataBusy) setEditingServer(null); }}>
    <form onSubmit={saveMetadata}>{metadataError && <ErrorBanner text={metadataError} />}<Field label={t("server.address")}><input maxLength={255} value={metadataForm.address} onChange={e => setMetadataForm({ ...metadataForm, address: e.target.value })} placeholder="192.0.2.10" /></Field><Field label={t("server.configuration")}><textarea rows={4} maxLength={4096} value={metadataForm.configuration} onChange={e => setMetadataForm({ ...metadataForm, configuration: e.target.value })} placeholder={t("server.configurationPlaceholder")} /></Field><Field label={t("server.notes")}><textarea rows={4} maxLength={4096} value={metadataForm.notes} onChange={e => setMetadataForm({ ...metadataForm, notes: e.target.value })} placeholder={t("server.notesPlaceholder")} /></Field><DialogActions><button type="button" className="secondary-button" disabled={metadataBusy} onClick={() => setEditingServer(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={metadataBusy}>{metadataBusy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("server.saveInformation")}</button></DialogActions></form>
  </Dialog>
  <Dialog open={credentialServer !== null} title={t("server.editCredentials")} onClose={() => { if (!credentialBusy) setCredentialServer(null); }}>
    <form onSubmit={saveCredentials}>{credentialError && <ErrorBanner text={credentialError} />}<p className="security-notice">{t("server.credentialsDescription", { name: credentialServer?.name ?? "" })}</p><Field label={t("server.codexProfile")}><select value={credentialForm.codexProfileID} onChange={e => setCredentialForm({ ...credentialForm, codexProfileID: e.target.value })} required><option value="">{t(codexProfiles.length ? "server.selectCodexProfile" : "server.noCodexProfiles")}</option>{codexProfiles.map(profile => <option value={profile.id} key={profile.id}>{profile.name} · {profile.model}</option>)}</select></Field><Field label={t("server.gitProfile")}><select value={credentialForm.gitProfileID} onChange={e => setCredentialForm({ ...credentialForm, gitProfileID: e.target.value })}><option value="">{t("server.noGitProfile")}</option>{gitProfiles.map(profile => { const ready = Boolean(profile.commit_name && profile.commit_email); return <option value={profile.id} key={profile.id} disabled={!ready}>{profile.name} · {profile.username}{ready ? "" : ` · ${t("settings.gitIdentityMissing")}`}</option>; })}</select></Field><DialogActions><button type="button" className="secondary-button" disabled={credentialBusy} onClick={() => setCredentialServer(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={credentialBusy || !credentialForm.codexProfileID || Boolean(credentialForm.gitProfileID && !gitProfiles.some(profile => profile.id === credentialForm.gitProfileID && profile.commit_name && profile.commit_email))}>{credentialBusy ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{credentialBusy ? t("server.credentialsUpdating") : t("server.saveCredentials")}</button></DialogActions></form>
  </Dialog></div>;
}

function ProjectsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const projects = useData<Project[]>("/projects", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const [dialog, setDialog] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createError, setCreateError] = useState("");
  const [form, setForm] = useState(newCreateProjectFormValue());
  const [projectAction, setProjectAction] = useState<{ id: string; kind: "retry" | "restore" } | null>(null);
  const [detailProjectID, setDetailProjectID] = useState<string | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [detailError, setDetailError] = useState("");
  const detail = useData<ProjectDetail>(detailProjectID ? `/projects/${detailProjectID}` : null, realtime);
  const [deleteProjectID, setDeleteProjectID] = useState<string | null>(null);
  const [projectDeletionPlan, setProjectDeletionPlan] = useState<ProjectDeletionPlan | null>(null);
  const [projectDeletionBusy, setProjectDeletionBusy] = useState(false);
  const [projectDeletionError, setProjectDeletionError] = useState("");
  const [manageWorkspaceID, setManageWorkspaceID] = useState<string | null>(null);
  const [workspaceDeletionPlan, setWorkspaceDeletionPlan] = useState<WorkspaceDeletionPlan | null>(null);
  const [workspaceManagerBusy, setWorkspaceManagerBusy] = useState(false);
  const [workspacePlanLoading, setWorkspacePlanLoading] = useState(false);
  const [workspaceManagerError, setWorkspaceManagerError] = useState("");

  const openDialog = () => {
    setForm(newCreateProjectFormValue());
    setCreateError("");
    setDialog(true);
  };
  const close = () => { if (!busy) setDialog(false); };
  const submit = async (request: CreateProjectRequest) => {
    setBusy(true);
    setCreateError("");
    try {
      if (request.mode === "blank") {
        await post("/projects", request);
        notify(t("project.createQueued"));
      } else if (request.mode === "clone") {
        await post("/projects/import", {
          remote_url: request.remote_url,
          name: request.name ?? "",
          server_id: request.server_id,
          destination: request.destination ?? ""
        });
        notify(t("project.queued"));
      } else {
        await post("/projects/discover", { server_id: request.server_id });
        notify(t("project.scanQueued"));
      }
      projects.reload();
      workspaces.reload();
      setDialog(false);
    } catch (err) {
      const error = message(err);
      setCreateError(error);
      notify(error);
    } finally { setBusy(false); }
  };
  const retryProject = async (project: Project) => {
    setProjectAction({ id: project.id, kind: "retry" });
    try {
      const blank = project.status === "failed" || project.status === "partial";
      await post(`/projects/${project.id}/${blank ? "retry-create" : "retry-import"}`, {});
      projects.reload();
      notify(t(blank ? "project.createRetryQueued" : "project.retryQueued"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const openProjectDeletion = async (project: Project) => {
    setDeleteProjectID(project.id);
    setProjectDeletionPlan(null);
    setProjectDeletionError("");
    setProjectDeletionBusy(true);
    try { setProjectDeletionPlan(await post<ProjectDeletionPlan>(`/projects/${project.id}/deletion-plan`, {})); }
    catch (err) { setProjectDeletionError(message(err)); }
    finally { setProjectDeletionBusy(false); }
  };
  const deleteProject = async (mode: ProjectDeletionMode) => {
    if (!deleteProjectID) return;
    setProjectDeletionBusy(true);
    setProjectDeletionError("");
    try {
      await api(`/projects/${deleteProjectID}`, { method: "DELETE", body: JSON.stringify({ mode }) });
      setDeleteProjectID(null);
      setProjectDeletionPlan(null);
      projects.reload();
      workspaces.reload();
      notify(t(mode === "managed-files" ? "project.deleteFilesQueued" : "project.deleted"));
    } catch (err) { setProjectDeletionError(message(err)); }
    finally { setProjectDeletionBusy(false); }
  };
  const restoreProject = async (project: Project) => {
    setProjectAction({ id: project.id, kind: "restore" });
    try {
      await patch<Project>(`/projects/${project.id}`, { hidden: false });
      projects.reload();
      notify(t("project.restored"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const saveProjectDetails = async (value: ProjectEditValue) => {
    if (!detailProjectID) return;
    setDetailBusy(true);
    setDetailError("");
    try {
      await patch(`/projects/${detailProjectID}`, { name: value.name.trim(), description: value.description.trim(), default_branch: value.defaultBranch.trim(), pinned: value.pinned, hidden: value.hidden, archived: value.archived });
      projects.reload();
      detail.reload();
      setDetailProjectID(null);
      notify(t("project.saved"));
    } catch (err) { setDetailError(message(err)); } finally { setDetailBusy(false); }
  };
  const importMessage = (project: ProjectListRecord) => {
    const raw = project.provision_error || project.import_message;
    return /http2 framing|expected flush|timed? out|timeout|could not resolve|temporary failure in name resolution|connection (?:refused|reset)|network is unreachable|dial tcp/i.test(raw) ? t("project.networkFailure") : raw;
  };
  const serverOptions = (servers.data ?? []).map(server => ({ id: server.id, name: server.name, status: server.status }));
  const labels: CreateProjectDialogLabels = {
    title: t("project.createTitle"), modeLabel: t("project.createMode"), blankMode: t("project.blankMode"), cloneMode: t("project.cloneMode"), discoverMode: t("project.discoverMode"),
    projectName: t("project.name"), targetServer: t("project.targetServer"), selectServer: t("project.selectServer"), offline: t("status.offline"), destination: t("project.destination"), optional: t("common.optional"), initialBranch: t("project.initialBranch"),
    remoteSetup: t("project.remoteSetup"), remoteNone: t("project.remoteNone"), remoteExisting: t("project.remoteExisting"), remoteCreate: t("project.remoteCreate"), remoteURL: t("project.remoteURL"), remoteProvider: t("project.remoteProvider"), remoteNamespace: t("project.remoteNamespace"), remoteRepository: t("project.remoteRepository"), remoteVisibility: t("project.remoteVisibility"), visibilityPrivate: t("project.visibilityPrivate"), visibilityInternal: t("project.visibilityInternal"), visibilityPublic: t("project.visibilityPublic"), initializeReadme: t("project.initializeReadme"), existingServer: t("project.existingServer"), comingSoon: t("project.comingSoon"), cancel: t("common.cancel"), working: t("project.working"), create: t("project.create"), clone: t("project.queue"), discover: t("project.scan"),
    nameRequired: t("project.nameRequired"), serverRequired: t("project.serverRequired"), remoteURLRequired: t("project.remoteURLRequired"), initialBranchRequired: t("project.initialBranchRequired"), remoteProviderRequired: t("project.remoteProviderRequired"), remoteRepositoryRequired: t("project.remoteRepositoryRequired"), remoteUnavailable: t("project.remoteUnavailable")
  };
  const detailLabels = {
    title: t("project.detailTitle"), overview: t("project.detailOverview"), history: t("project.detailHistory"), name: t("project.name"), description: t("project.description"), defaultBranch: t("project.defaultBranch"), pinned: t("project.pin"), hidden: t("project.hide"), archived: t("project.archive"), remote: t("column.remote"), noRemote: t("project.noRemote"), operation: t("project.operation"), state: t("project.status"), time: t("column.updated"), result: t("project.result"), noOperations: t("project.noOperations"), cancel: t("common.cancel"), save: t("common.save"), saving: t("common.saving"), loading: t("common.loading")
  };
  const projectLabels = { project: t("column.project"), remote: t("column.remote"), workspaces: t("column.workspaces"), status: t("project.status"), updated: t("column.updated"), actions: t("common.actions"), empty: t("project.none"), local: t("project.local"), hidden: t("project.hidden"), targetServer: (server: string) => t("project.targetSummary", { server }), awaitingWorkspace: t("project.awaitingWorkspace") };
  const workspaceLabels = { project: t("column.project"), server: t("column.server"), path: t("column.path"), branch: t("column.branch"), commit: t("column.commit"), state: t("column.state"), actions: t("common.actions"), empty: t("project.noWorkspaces"), detached: t("project.detached") };
  const [gitWorkspaceID, setGitWorkspaceID] = useState<string | null>(null);
  const [gitBusy, setGitBusy] = useState(false);
  const [gitError, setGitError] = useState("");
  const gitSnapshot = useData<WorkspaceGitSnapshot>(gitWorkspaceID ? `/workspaces/${gitWorkspaceID}/git` : null, realtime);
  const refreshWorkspaceGit = async (workspaceID = gitWorkspaceID, announce = true) => {
    if (!workspaceID) return;
    setGitBusy(true);
    setGitError("");
    try { await post(`/workspaces/${workspaceID}/git/refresh`, {}); gitSnapshot.reload(); if (announce) notify(t("project.gitRefreshQueued")); } catch (err) { setGitError(message(err)); } finally { setGitBusy(false); }
  };
  const openWorkspaceGit = (workspaceID: string) => {
    setGitError("");
    setGitWorkspaceID(workspaceID);
    void refreshWorkspaceGit(workspaceID, false);
  };
  const runGitAction = async (action: WorkspaceGitAction) => {
    if (!gitWorkspaceID) return;
    const base = `/workspaces/${gitWorkspaceID}/git`;
    const segment = (value: string) => encodeURIComponent(value);
    setGitBusy(true);
    setGitError("");
    try {
      switch (action.type) {
        case "branch.create": await post(`${base}/branches`, { name: action.name, start_point: action.startPoint }); break;
        case "branch.rename": await patch(`${base}/branches/${segment(action.branch)}`, { name: action.name }); break;
        case "branch.delete": await remove(`${base}/branches/${segment(action.branch)}?force=${action.force}`); break;
        case "checkout": await post(`${base}/checkout`, { ref: action.ref, detach: action.detach }); break;
        case "remote.add": await post(`${base}/remotes`, { name: action.name, url: action.url }); break;
        case "remote.update": await patch(`${base}/remotes/${segment(action.remote)}`, { url: action.url }); break;
        case "remote.delete": await remove(`${base}/remotes/${segment(action.remote)}`); break;
        case "fetch": await post(`${base}/fetch`, { remote: action.remote }); break;
        case "pull": await post(`${base}/pull`, { remote: action.remote, branch: action.branch }); break;
        case "push": await post(`${base}/push`, { remote: action.remote, ref: action.ref, set_upstream: action.setUpstream }); break;
      }
      notify(t("project.gitActionQueued"));
      gitSnapshot.reload();
    } catch (err) {
      const detail = message(err);
      setGitError(detail);
      throw err;
    } finally { setGitBusy(false); }
  };
  const openWorkspaceManager = (workspace: Workspace) => {
    setManageWorkspaceID(workspace.id);
    setWorkspaceDeletionPlan(null);
    setWorkspaceManagerError("");
  };
  const renameWorkspace = async (name: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await patch(`/workspaces/${manageWorkspaceID}`, { display_name: name }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceSaved")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const moveWorkspace = async (path: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await post(`/workspaces/${manageWorkspaceID}/move`, { path }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceMoveQueued")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const copyWorkspace = async (serverID: string, path: string) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await post(`/workspaces/${manageWorkspaceID}/copy`, { server_id: serverID, path }); workspaces.reload(); setManageWorkspaceID(null); notify(t("project.workspaceCopyQueued")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const loadWorkspaceDeletionPlan = async (force: boolean) => {
    if (!manageWorkspaceID) return;
    setWorkspacePlanLoading(true); setWorkspaceManagerError("");
    try { setWorkspaceDeletionPlan(await post<WorkspaceDeletionPlan>(`/workspaces/${manageWorkspaceID}/deletion-plan?force=${force}`, {})); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspacePlanLoading(false); }
  };
  const deleteWorkspace = async (mode: WorkspaceDeletionMode, force: boolean) => {
    if (!manageWorkspaceID) return;
    setWorkspaceManagerBusy(true); setWorkspaceManagerError("");
    try { await remove(`/workspaces/${manageWorkspaceID}?mode=${mode}&force=${force}`); workspaces.reload(); projects.reload(); setManageWorkspaceID(null); notify(t(mode === "files" ? "project.workspaceDeleteQueued" : "project.workspaceRemoved")); }
    catch (err) { setWorkspaceManagerError(message(err)); } finally { setWorkspaceManagerBusy(false); }
  };
  const gitLabels = { title: t("project.gitTitle"), status: t("project.gitStatus"), branches: t("project.gitBranches"), remotes: t("project.gitRemotes"), commits: t("project.gitCommits"), refresh: t("project.gitRefresh"), refreshing: t("project.gitRefreshing"), branch: t("column.branch"), head: t("project.gitHead"), upstream: t("project.gitUpstream"), ahead: t("project.gitAhead"), behind: t("project.gitBehind"), staged: t("project.gitStaged"), unstaged: t("project.gitUnstaged"), untracked: t("project.gitUntracked"), clean: t("project.gitClean"), dirty: t("project.gitDirty"), noBranches: t("project.gitNoBranches"), noRemotes: t("project.gitNoRemotes"), noCommits: t("project.gitNoCommits"), close: t("common.close"), sync: t("project.gitSync"), remote: t("project.gitRemote"), ref: t("project.gitRef"), fetch: t("project.gitFetch"), pull: t("project.gitPull"), push: t("project.gitPush"), setUpstream: t("project.gitSetUpstream"), createBranch: t("project.gitCreateBranch"), branchName: t("project.gitBranchName"), startPoint: t("project.gitStartPoint"), checkout: t("project.gitCheckout"), detach: t("project.gitDetach"), rename: t("common.rename"), edit: t("common.edit"), delete: t("common.delete"), forceDelete: t("project.gitForceDelete"), addRemote: t("project.gitAddRemote"), remoteName: t("project.gitRemoteName"), remoteURL: t("project.remoteURL"), save: t("common.save"), cancel: t("common.cancel"), current: t("project.gitCurrent"), local: t("project.gitLocal"), remoteBranch: t("project.gitRemoteBranch"), actionQueued: t("project.gitActionQueued") };
  const workspaceManagerLabels = { title: t("project.workspaceManage"), rename: t("common.rename"), move: t("project.workspaceMove"), copy: t("project.workspaceCopy"), delete: t("common.delete"), displayName: t("project.workspaceName"), currentPath: t("project.workspaceCurrentPath"), targetPath: t("project.workspaceTargetPath"), targetServer: t("project.workspaceTargetServer"), sameServer: t("project.workspaceSameServer"), managedOnly: t("project.workspaceManagedOnly"), save: t("common.save"), moving: t("project.workspaceMoving"), copying: t("project.workspaceCopying"), loadingPlan: t("project.deletionLoading"), metadataOnly: t("project.deleteMetadataOnly"), deleteFiles: t("project.workspaceDeleteFiles"), metadataDescription: t("project.workspaceMetadataDescription"), filesDescription: t("project.workspaceFilesDescription"), dirty: t("project.gitDirty"), activeOperations: t("project.deleteActiveOperations"), threads: t("project.workspaceThreads"), childWorkspaces: t("project.workspaceChildren"), force: t("project.workspaceForceDelete"), blockers: t("project.deleteBlockers"), noBlockers: t("project.deleteNoBlockers"), confirmLabel: t("project.deleteConfirmLabel"), confirmPlaceholder: t("project.deleteConfirmPlaceholder"), deleting: t("project.deleting"), cancel: t("common.cancel") };
  const projectDeletionLabels = { title: t("project.deleteTitle"), loading: t("project.deletionLoading"), metadataOnly: t("project.deleteMetadataOnly"), metadataDescription: t("project.deleteMetadataDescription"), managedFiles: t("project.deleteManagedFiles"), managedDescription: t("project.deleteManagedDescription"), workspaces: t("column.workspaces"), managed: t("project.deleteManagedCount"), observed: t("project.deleteObservedCount"), dirty: t("project.gitDirty"), activeOperations: t("project.deleteActiveOperations"), activeTasks: t("project.deleteActiveTasks"), activeDeployments: t("project.deleteActiveDeployments"), remotePreserved: t("project.deleteRemotePreserved"), blockers: t("project.deleteBlockers"), noBlockers: t("project.deleteNoBlockers"), confirmLabel: t("project.deleteConfirmLabel"), confirmPlaceholder: t("project.deleteConfirmPlaceholder"), cancel: t("common.cancel"), deleting: t("project.deleting"), deleteMetadata: t("project.deleteMetadataAction"), deleteFiles: t("project.deleteFilesAction") };
  const managedWorkspace = (workspaces.data ?? []).find(workspace => workspace.id === manageWorkspaceID) ?? null;
  const deletingProject = (projects.data ?? []).find(project => project.id === deleteProjectID) ?? null;
  return <div className="page-stack project-page">
    <Section title={t("project.title")} icon={<GitBranch size={18} />} action={<button className="primary-button" onClick={openDialog}><Plus size={17} />{t("project.createEntry")}</button>}>
      <ProjectTable projects={projects.data ?? []} labels={projectLabels} slots={{ DataTable, Status }} formatTime={relative} formatImportMessage={importMessage} onSelect={project => { setDetailError(""); setDetailProjectID(project.id); }} renderActions={(project, state: ProjectLifecycleState) => {
        const failed = (state === "failed" || state === "partial") && project.workspace_count === 0;
        const action = projectAction?.id === project.id ? projectAction.kind : null;
        const targetServer = (servers.data ?? []).find(server => server.id === project.import_server_id);
        const blankFailure = project.status === "failed" || project.status === "partial";
        const retryAvailable = blankFailure || targetServer?.status === "online";
        return <><button className="icon-button" title={t("project.edit")} onClick={() => { setDetailError(""); setDetailProjectID(project.id); }}><Pencil size={15} /></button>{project.hidden_at && <button className="secondary-button small" disabled={projectAction !== null} onClick={() => void restoreProject(project)}>{action === "restore" ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}{t("project.restore")}</button>}{failed && <button className="icon-button" disabled={projectAction !== null || !retryAvailable} title={retryAvailable ? t(blankFailure ? "project.retryCreate" : "project.retryImport") : t("project.retryOffline")} onClick={() => void retryProject(project)}>{action === "retry" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button>}<button className="icon-button danger" disabled={projectAction !== null || projectDeletionBusy} title={t("project.deleteTitle")} onClick={() => void openProjectDeletion(project)}><Trash2 size={15} /></button></>;
      }} />
    </Section>
    <Section title={t("project.workspaces")} icon={<Boxes size={18} />}>
      <WorkspaceTable workspaces={workspaces.data ?? []} labels={workspaceLabels} slots={{ DataTable, Status }} formatCommit={shortSHA} renderActions={workspace => <><button className="icon-button" title={t("project.viewGit")} onClick={() => openWorkspaceGit(workspace.id)}><GitBranch size={15} /></button><button className="icon-button" title={t("project.workspaceManage")} onClick={() => openWorkspaceManager(workspace)}><Settings size={15} /></button></>} />
    </Section>
    <CreateProjectDialog open={dialog} value={form} servers={serverOptions} labels={labels} slots={{ Dialog, Field, DialogActions }} busy={busy} error={createError} onChange={setForm} onClose={close} onSubmit={submit} />
    <ProjectDetailsDialog open={detailProjectID !== null} detail={detail.data} loading={detail.loading} busy={detailBusy} error={detailError || detail.error} labels={detailLabels} slots={{ Dialog, Field, DialogActions }} onClose={() => { if (!detailBusy) setDetailProjectID(null); }} onSubmit={saveProjectDetails} />
    <WorkspaceGitDialog open={gitWorkspaceID !== null} snapshot={gitSnapshot.data} loading={gitSnapshot.loading} busy={gitBusy} error={gitError} labels={gitLabels} Dialog={Dialog} onClose={() => { if (!gitBusy) setGitWorkspaceID(null); }} onRefresh={() => void refreshWorkspaceGit()} onAction={runGitAction} />
    <WorkspaceManagerDialog open={manageWorkspaceID !== null} workspace={managedWorkspace} servers={servers.data ?? []} plan={workspaceDeletionPlan} planLoading={workspacePlanLoading} busy={workspaceManagerBusy} error={workspaceManagerError} labels={workspaceManagerLabels} Dialog={Dialog} onClose={() => { if (!workspaceManagerBusy) setManageWorkspaceID(null); }} onRename={renameWorkspace} onMove={moveWorkspace} onCopy={copyWorkspace} onLoadDeletionPlan={loadWorkspaceDeletionPlan} onDelete={deleteWorkspace} />
    <ProjectDeletionDialog open={deleteProjectID !== null} project={deletingProject} plan={projectDeletionPlan} loading={projectDeletionBusy && !projectDeletionPlan} busy={projectDeletionBusy} error={projectDeletionError} labels={projectDeletionLabels} Dialog={Dialog} onClose={() => { if (!projectDeletionBusy) setDeleteProjectID(null); }} onSubmit={deleteProject} />
  </div>;
}

function CodexPage({ realtime, streamRevisions, approvals, approvalSignal, reloadApprovals, notify, selectedThreadID, onSelectThread }: PageProps & { streamRevisions: StreamRevisions; approvals: Approval[]; approvalSignal: number; reloadApprovals: () => void; selectedThreadID: string; onSelectThread: (threadID: string, replace?: boolean) => void }) {
  const { t } = useI18n();
  const threads = useData<Thread[]>("/threads", realtime);
  const archivedThreads = useData<Thread[]>("/threads?archived=true", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const [selected, setSelected] = useState(selectedThreadID);
  const [createOpen, setCreateOpen] = useState(false);
  const [approvalOpen, setApprovalOpen] = useState(approvals.length > 0);
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(new Set());
  const [deletingThread, setDeletingThread] = useState("");
  const [threadAction, setThreadAction] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [pendingThread, setPendingThread] = useState<Thread | null>(null);
  const [renameTarget, setRenameTarget] = useState<{ kind: "project" | "thread"; id: string; name: string } | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [renameBusy, setRenameBusy] = useState(false);
  const [worktreeTarget, setWorktreeTarget] = useState<{ kind: "project"; projectID: string; projectName: string } | { kind: "thread"; thread: Thread } | null>(null);
  const [worktreeForm, setWorktreeForm] = useState({ workspace_id: "", branch: "", path: "", base_ref: "HEAD" });
  const [worktreeBusy, setWorktreeBusy] = useState(false);
  const [worktreeError, setWorktreeError] = useState("");
  const [preview, setPreview] = useState<FilePreviewSelection | null>(null);
  const [activePane, setActivePane] = useState<"conversation" | "preview">("conversation");
  const [mobileView, setMobileView] = useState<"sessions" | "files" | "conversation">(selectedThreadID ? "conversation" : "sessions");
  const listedActiveThreads = threads.data ?? [];
  const activeThreads = showArchived ? archivedThreads.data ?? [] : pendingThread && !listedActiveThreads.some(thread => thread.id === pendingThread.id) ? [pendingThread, ...listedActiveThreads] : listedActiveThreads;
  const activeThreadSource = showArchived ? archivedThreads : threads;
  const visibleThreads = useMemo(() => activeThreads.filter(thread => !thread.project_hidden_at), [activeThreads]);
  const active = visibleThreads.find(thread => thread.id === selected) ?? visibleThreads[0];
  const threadGroups = useMemo(() => groupThreadsByProject(visibleThreads), [visibleThreads]);
  const approvalKey = approvals.map(item => item.id).join(",");
  useEffect(() => {
    if (!(showArchived ? archivedThreads.data : threads.data)) return;
    const requested = selectedThreadID && visibleThreads.some(thread => thread.id === selectedThreadID) ? selectedThreadID : "";
    const next = requested || (visibleThreads.some(thread => thread.id === selected) ? selected : visibleThreads[0]?.id ?? "");
    if (next !== selected) setSelected(next);
    if (next !== selectedThreadID) onSelectThread(next, true);
  }, [archivedThreads.data, onSelectThread, selected, selectedThreadID, showArchived, threads.data, visibleThreads]);
  useEffect(() => { if (approvalKey) setApprovalOpen(true); }, [approvalKey, approvalSignal]);
  useEffect(() => { if (pendingThread && listedActiveThreads.some(thread => thread.id === pendingThread.id)) setPendingThread(null); }, [listedActiveThreads, pendingThread]);
  useEffect(() => { setPreview(null); setActivePane("conversation"); if (active) setMobileView("conversation"); }, [active?.workspace_id]);
  const selectThread = (threadID: string) => { setSelected(threadID); setMobileView(threadID ? "conversation" : "sessions"); onSelectThread(threadID); };
  const toggleProject = (projectID: string) => setCollapsedProjects(current => { const next = new Set(current); if (next.has(projectID)) next.delete(projectID); else next.add(projectID); return next; });
  const openFile = (selection: FilePreviewSelection) => { setPreview(selection); setActivePane("preview"); setMobileView("conversation"); };
  const copyValue = async (value: string, success: string) => { try { await copyText(value); notify(success); } catch (error) { notify(message(error)); } };
  const deepLink = (threadID: string) => `${window.location.origin}${locationFor("codex", threadID)}`;
  const beginRename = (kind: "project" | "thread", id: string, name: string) => { setRenameTarget({ kind, id, name }); setRenameValue(name); };
  const submitRename = async (event: FormEvent) => {
    event.preventDefault();
    const value = renameValue.trim();
    if (!renameTarget || !value || renameBusy) return;
    setRenameBusy(true);
    try {
      if (renameTarget.kind === "project") await patch<Project>(`/projects/${renameTarget.id}`, { name: value });
      else await patch<Thread>(`/threads/${renameTarget.id}`, { title: value });
      threads.reload();
      notify(t(renameTarget.kind === "project" ? "codex.projectRenamed" : "codex.threadRenamed"));
      setRenameTarget(null);
    } catch (error) { notify(message(error)); } finally { setRenameBusy(false); }
  };
  const reloadThreadLists = () => { threads.reload(); archivedThreads.reload(); };
  const setThreadArchived = async (thread: Thread, archived: boolean) => {
    if (threadAction) return;
    setThreadAction(`archive:${thread.id}`);
    try {
      await patch<Thread>(`/threads/${thread.id}`, { archived });
      const remaining = visibleThreads.find(item => item.id !== thread.id);
      if (active?.id === thread.id) selectThread(remaining?.id ?? "");
      reloadThreadLists();
      notify(t(archived ? "codex.threadArchived" : "codex.threadRestored"));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const archiveProjectThreads = async (group: ThreadGroup) => {
    if (threadAction || !confirm(t("codex.confirmArchiveProject", { name: group.projectName }))) return;
    setThreadAction(`project:${group.projectID}`);
    try {
      const result = await post<{ archived: number }>(`/projects/${group.projectID}/threads/archive`, {});
      if (active?.project_id === group.projectID) selectThread(visibleThreads.find(item => item.project_id !== group.projectID)?.id ?? "");
      reloadThreadLists();
      notify(t("codex.projectThreadsArchived", { count: result.archived }));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const continueInNewTask = async (thread: Thread) => {
    if (threadAction) return;
    setThreadAction(`fork:${thread.id}`);
    try {
      const result = await post<{ operation_id: string; target_thread_id: string }>(`/threads/${thread.id}/fork`, {});
      const created = await waitForThread(result.target_thread_id, t("codex.threadForkTimeout"));
      setPendingThread(created);
      reloadThreadLists();
      setShowArchived(false);
      selectThread(created.id);
      notify(t("codex.threadForked"));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const openWorktreeDialog = (target: { kind: "project"; projectID: string; projectName: string } | { kind: "thread"; thread: Thread }) => {
    const candidates = target.kind === "project" ? (workspaces.data ?? []).filter(workspace => workspace.project_id === target.projectID) : (workspaces.data ?? []).filter(workspace => workspace.id === target.thread.workspace_id);
    setWorktreeTarget(target);
    setWorktreeForm({ workspace_id: candidates[0]?.id ?? "", branch: "", path: "", base_ref: "HEAD" });
    setWorktreeError("");
  };
  const closeWorktreeDialog = () => { if (!worktreeBusy) { setWorktreeTarget(null); setWorktreeError(""); } };
  const submitWorktree = async (event: FormEvent) => {
    event.preventDefault();
    if (!worktreeTarget || worktreeBusy || !worktreeForm.workspace_id || !worktreeForm.branch.trim()) return;
    setWorktreeBusy(true);
    setWorktreeError("");
    const payload = { branch: worktreeForm.branch.trim(), path: worktreeForm.path.trim(), base_ref: worktreeForm.base_ref.trim() || "HEAD" };
    try {
      if (worktreeTarget.kind === "project") {
        const result = await post<{ operation_id: string; workspace_id: string }>(`/workspaces/${worktreeForm.workspace_id}/worktrees`, payload);
        await waitForWorkspace(result.workspace_id, t("codex.worktreeTimeout"));
        workspaces.reload();
        notify(t("codex.worktreeCreated"));
      } else {
        const result = await post<{ operation_id: string; workspace_id: string; target_thread_id: string }>(`/threads/${worktreeTarget.thread.id}/fork-worktree`, payload);
        const [, created] = await Promise.all([waitForWorkspace(result.workspace_id, t("codex.worktreeTimeout")), waitForThread(result.target_thread_id, t("codex.worktreeTaskTimeout"))]);
        setPendingThread(created);
        workspaces.reload();
        reloadThreadLists();
        setShowArchived(false);
        selectThread(created.id);
        notify(t("codex.continuedInWorktree"));
      }
      setWorktreeTarget(null);
    } catch (error) { setWorktreeError(message(error)); } finally { setWorktreeBusy(false); }
  };
  const projectMenuActions = (group: ThreadGroup): ContextMenuAction[] => [
    { id: "pin", label: t(group.pinnedAt ? "codex.unpinProject" : "codex.pinProject"), icon: group.pinnedAt ? PinOff : Pin, onSelect: async () => { try { await patch<Project>(`/projects/${group.projectID}`, { pinned: !group.pinnedAt }); threads.reload(); notify(t(group.pinnedAt ? "codex.projectUnpinned" : "codex.projectPinned")); } catch (error) { notify(message(error)); } } },
    { id: "rename", label: t("codex.renameProject"), icon: Pencil, onSelect: () => beginRename("project", group.projectID, group.projectName) },
    ...(!showArchived ? [{ id: "create-worktree", label: t("codex.createPermanentWorktree"), icon: GitBranch, disabled: threadAction !== "" || !(workspaces.data ?? []).some(workspace => workspace.project_id === group.projectID), onSelect: () => openWorktreeDialog({ kind: "project", projectID: group.projectID, projectName: group.projectName }) } satisfies ContextMenuAction] : []),
    ...(!showArchived ? [{ id: "archive-tasks", label: t("codex.archiveProjectThreads"), icon: Archive, danger: true, separatorBefore: true, disabled: threadAction !== "", onSelect: () => archiveProjectThreads(group) } satisfies ContextMenuAction] : []),
    { id: "hide", label: t("codex.hideProject"), icon: EyeOff, danger: true, separatorBefore: true, onSelect: async () => { if (!confirm(t("codex.confirmHideProject", { name: group.projectName }))) return; try { await patch<Project>(`/projects/${group.projectID}`, { hidden: true }); if (active?.project_id === group.projectID) selectThread(visibleThreads.find(thread => thread.project_id !== group.projectID)?.id ?? ""); threads.reload(); notify(t("codex.projectHidden")); } catch (error) { notify(message(error)); } } }
  ];
  const threadMenuActions = (thread: Thread): ContextMenuAction[] => [
    ...(showArchived ? [{ id: "restore", label: t("codex.restoreThread"), icon: ArchiveRestore, disabled: threadAction !== "", onSelect: () => setThreadArchived(thread, false) } satisfies ContextMenuAction] : [
    { id: "pin", label: t(thread.pinned_at ? "codex.unpinThread" : "codex.pinThread"), icon: thread.pinned_at ? PinOff : Pin, onSelect: async () => { try { await patch<Thread>(`/threads/${thread.id}`, { pinned: !thread.pinned_at }); threads.reload(); notify(t(thread.pinned_at ? "codex.threadUnpinned" : "codex.threadPinned")); } catch (error) { notify(message(error)); } } },
    { id: "rename", label: t("codex.renameThread"), icon: Pencil, onSelect: () => beginRename("thread", thread.id, thread.title) },
    { id: "fork", label: t("codex.continueInNewTask"), icon: GitFork, disabled: threadAction !== "" || !thread.codex_thread_id, onSelect: () => continueInNewTask(thread) },
    { id: "fork-worktree", label: t("codex.continueInNewWorktree"), icon: GitBranch, disabled: threadAction !== "" || !thread.codex_thread_id || thread.status === "queued" || thread.status === "running", onSelect: () => openWorktreeDialog({ kind: "thread", thread }) },
    { id: "archive", label: t("codex.archiveThread"), icon: Archive, danger: true, separatorBefore: true, disabled: threadAction !== "" || thread.status === "queued" || thread.status === "running", onSelect: () => setThreadArchived(thread, true) }
    ]),
    { id: "copy-path", label: t("codex.copyWorkingDirectory"), icon: FolderOpen, separatorBefore: true, disabled: !thread.path, onSelect: () => copyValue(thread.path, t("codex.workingDirectoryCopied")) },
    { id: "copy-wio-id", label: t("codex.copyWioSessionID"), icon: Copy, onSelect: () => copyValue(thread.id, t("codex.wioSessionIDCopied")) },
    { id: "copy-codex-id", label: t("codex.copyCodexSessionID"), icon: Copy, disabled: !thread.codex_thread_id, onSelect: () => copyValue(thread.codex_thread_id, t("codex.codexSessionIDCopied")) },
    { id: "copy-link", label: t("codex.copyDeepLink"), icon: Link, onSelect: () => copyValue(deepLink(thread.id), t("codex.deepLinkCopied")) },
    { id: "new-window", label: t("codex.openNewWindow"), icon: ExternalLink, separatorBefore: true, onSelect: () => { window.open(deepLink(thread.id), "_blank", "noopener,noreferrer"); } }
  ];
  const deleteSession = async (thread: Thread) => {
    if (thread.status === "queued" || thread.status === "running") return;
    if (!confirm(t("codex.confirmDeleteSession", { title: thread.title }))) return;
    setDeletingThread(thread.id);
    try {
      await remove(`/threads/${thread.id}`);
      const next = visibleThreads.find(item => item.id !== thread.id);
      if (active?.id === thread.id) selectThread(next?.id ?? "");
      threads.reload();
      notify(t("codex.sessionDeleted"));
    } catch (error) {
      notify(message(error));
    } finally {
      setDeletingThread("");
    }
  };
  return <div className="codex-layout">
    <div className="codex-mobile-tabs" role="tablist" aria-label={t("codex.sessionViews")}><button type="button" role="tab" aria-selected={mobileView === "sessions"} className={mobileView === "sessions" ? "active" : ""} onClick={() => setMobileView("sessions")}><Code2 size={15} />{t("codex.sessions")}</button><button type="button" role="tab" aria-selected={mobileView === "files"} className={mobileView === "files" ? "active" : ""} onClick={() => setMobileView("files")}><FolderTree size={15} />{t("codex.projectFiles")}</button><button type="button" role="tab" aria-selected={mobileView === "conversation"} className={mobileView === "conversation" ? "active" : ""} onClick={() => setMobileView("conversation")}><MessageSquare size={15} />{t("codex.conversation")}</button></div>
    <aside className={`codex-sidebar mobile-${mobileView}`}><section className="thread-list"><div className="panel-heading"><div><Code2 size={18} /><h2>{t(showArchived ? "codex.archivedTasks" : "codex.sessions")}</h2></div><div className="row-actions"><button className={`icon-button ${showArchived ? "active" : ""}`} aria-pressed={showArchived} title={t(showArchived ? "codex.showActiveTasks" : "codex.showArchivedTasks")} onClick={() => { setShowArchived(value => !value); setSelected(""); }}><Archive size={18} /></button>{!showArchived && <button className="icon-button" title={t("codex.newSession")} onClick={() => setCreateOpen(true)}><Plus size={18} /></button>}</div></div><div className="thread-items">{(showArchived ? archivedThreads.loading : threads.loading) ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : threadGroups.length === 0 ? <Empty icon={showArchived ? <Archive size={23} /> : <Code2 size={23} />} text={t(showArchived ? "codex.noArchivedTasks" : "codex.noSessions")} /> : threadGroups.map(group => { const collapsed = collapsedProjects.has(group.projectID); return <section className="thread-project" key={group.projectID}><ContextMenu className="thread-project-heading" label={t("codex.projectMenu", { name: group.projectName })} actions={projectMenuActions(group)}><button type="button" className="thread-project-toggle" aria-expanded={!collapsed} title={t(collapsed ? "codex.expandProject" : "codex.collapseProject")} onClick={() => toggleProject(group.projectID)}>{collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong>{group.projectName}</strong>{group.pinnedAt && <Pin className="pinned-icon" size={12} />}<span>{group.threads.length}</span></button></ContextMenu>{!collapsed && <div className="project-threads">{group.threads.map(thread => { const activeThread = thread.status === "queued" || thread.status === "running"; const deleting = deletingThread === thread.id; const acting = threadAction.endsWith(`:${thread.id}`); return <ContextMenu key={thread.id} className={active?.id === thread.id ? "thread active" : "thread"} label={t("codex.threadMenu", { name: thread.title })} actions={threadMenuActions(thread)}><button type="button" className="thread-select" onClick={() => selectThread(thread.id)}><span><strong>{thread.title}</strong><small>{thread.server_name}</small></span>{thread.pinned_at && <Pin className="pinned-icon" size={12} />}</button><div className="thread-actions">{acting ? <LoaderCircle className="spin" size={14} /> : <Status value={thread.status} />}{!showArchived && <button type="button" className="icon-button danger thread-delete" disabled={activeThread || deleting || !!threadAction} title={activeThread ? t("codex.deleteActiveSession") : t("codex.deleteSession")} onClick={() => void deleteSession(thread)}>{deleting ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}</button>}</div></ContextMenu>; })}</div>}</section>; })}</div></section><WorkspaceFilesPanel workspaceID={active?.workspace_id ?? null} realtime={realtime} notify={notify} activePath={preview?.path ?? ""} activeMode={preview?.mode ?? "file"} onOpenFile={openFile} /></aside>
    <section className={`session-area ${mobileView === "conversation" ? "mobile-active" : "mobile-hidden"}`}>{preview && <div className="session-pane-tabs" role="tablist" aria-label={t("codex.sessionViews")}><button type="button" role="tab" aria-selected={activePane === "conversation"} className={activePane === "conversation" ? "active" : ""} onClick={() => setActivePane("conversation")}><MessageSquare size={15} />{t("codex.conversation")}</button><button type="button" role="tab" aria-selected={activePane === "preview"} className={activePane === "preview" ? "active" : ""} onClick={() => setActivePane("preview")}>{preview.mode === "diff" ? <FileDiff size={15} /> : <FileCode2 size={15} />}{t(preview.mode === "diff" ? "codex.fileReview" : "codex.filePreview")}</button></div>}<div className={`session-panes ${preview ? `has-preview ${activePane}-active` : ""}`}><section className="session-panel">{activeThreadSource.error && !activeThreadSource.data ? <ErrorState error={activeThreadSource.error} reload={activeThreadSource.reload} /> : active ? <SessionView key={active.id} thread={active} approvals={approvals.filter(item => item.thread_id === active.id)} realtime={`${streamRevisions["*"] ?? 0}:${streamRevisions[active.id] ?? 0}`} reloadApprovals={reloadApprovals} notify={notify} onOpenFile={openFile} onNewTask={() => setCreateOpen(true)} /> : <Empty icon={<SquareTerminal size={28} />} text={t("codex.selectWorkspace")} />}</section>{active && preview && (preview.mode === "diff" ? <FileDiffPane workspaceID={active.workspace_id} selection={preview} realtime={realtime} onClose={() => { setPreview(null); setActivePane("conversation"); }} /> : <FilePreviewPane workspaceID={active.workspace_id} selection={preview} realtime={realtime} onClose={() => { setPreview(null); setActivePane("conversation"); }} />)}</div></section>
    <button className={`approval-drawer-button ${approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: approvals.length })}</button>
    <Dialog open={createOpen} title={t("codex.newSession")} onClose={() => setCreateOpen(false)}><CreateThread workspaces={workspaces.data ?? []} onCreated={thread => { selectThread(thread.id); setCreateOpen(false); threads.reload(); notify(t("codex.sessionCreated")); }} /></Dialog>
    <Dialog open={renameTarget !== null} title={t(renameTarget?.kind === "project" ? "codex.renameProject" : "codex.renameThread")} onClose={() => { if (!renameBusy) setRenameTarget(null); }}><form onSubmit={submitRename}><Field label={t(renameTarget?.kind === "project" ? "project.name" : "codex.threadName")}><input autoFocus maxLength={180} value={renameValue} onChange={event => setRenameValue(event.target.value)} required /></Field><DialogActions><button type="button" className="secondary-button" disabled={renameBusy} onClick={() => setRenameTarget(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={renameBusy || !renameValue.trim()}>{renameBusy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("common.save")}</button></DialogActions></form></Dialog>
    <Dialog open={worktreeTarget !== null} title={t(worktreeTarget?.kind === "thread" ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")} onClose={closeWorktreeDialog}><form onSubmit={submitWorktree}>{worktreeError && <ErrorBanner text={worktreeError} />}<p className="security-notice">{t(worktreeTarget?.kind === "thread" ? "codex.continueWorktreeDescription" : "codex.createWorktreeDescription")}</p>{worktreeTarget?.kind === "project" && <Field label={t("codex.sourceWorkspace")}><select value={worktreeForm.workspace_id} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, workspace_id: event.target.value })} required><option value="">{t("codex.selectWorkspaceOption")}</option>{(workspaces.data ?? []).filter(workspace => workspace.project_id === worktreeTarget.projectID).map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.server_name} · {workspace.branch || t("project.detached")} · {workspace.path}</option>)}</select></Field>}<div className="form-grid"><Field label={t("codex.worktreeBranch")}><input autoFocus value={worktreeForm.branch} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, branch: event.target.value })} placeholder="feature/my-change" required /></Field><Field label={t("codex.worktreeBaseRef")}><input value={worktreeForm.base_ref} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, base_ref: event.target.value })} placeholder="HEAD" required /></Field></div><Field label={t("codex.worktreePath")}><input value={worktreeForm.path} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, path: event.target.value })} placeholder={t("codex.worktreePathPlaceholder")} /></Field><DialogActions><button type="button" className="secondary-button" disabled={worktreeBusy} onClick={closeWorktreeDialog}>{t("common.cancel")}</button><button className="primary-button" disabled={worktreeBusy || !worktreeForm.workspace_id || !worktreeForm.branch.trim()}>{worktreeBusy ? <LoaderCircle className="spin" size={16} /> : worktreeTarget?.kind === "thread" ? <GitFork size={16} /> : <GitBranch size={16} />}{t(worktreeBusy ? "codex.creatingWorktree" : worktreeTarget?.kind === "thread" ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")}</button></DialogActions></form></Dialog>
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : approvals.map(item => <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time>{relative(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={reloadApprovals} notify={notify} /></div>)}</div></Dialog>
  </div>;
}

function groupThreadsByProject(threads: Thread[]) {
  const groups = new Map<string, ThreadGroup>();
  for (const thread of threads) {
    const group = groups.get(thread.project_id) ?? { projectID: thread.project_id, projectName: thread.project_name, pinnedAt: thread.project_pinned_at, threads: [] };
    group.threads.push(thread);
    groups.set(thread.project_id, group);
  }
  const pinnedFirst = (left: string | null, right: string | null) => left && !right ? -1 : !left && right ? 1 : left && right ? right.localeCompare(left) : 0;
  for (const group of groups.values()) group.threads.sort((left, right) => pinnedFirst(left.pinned_at, right.pinned_at) || right.updated_at.localeCompare(left.updated_at));
  return Array.from(groups.values()).sort((left, right) => pinnedFirst(left.pinnedAt, right.pinnedAt) || left.projectName.localeCompare(right.projectName));
}

type ThreadGroup = { projectID: string; projectName: string; pinnedAt: string | null; threads: Thread[] };

type FileTreeNode = { name: string; path: string; kind: WorkspaceFile["kind"]; size?: number; children: FileTreeNode[] };

function WorkspaceFilesPanel({ workspaceID, realtime, notify, activePath, activeMode, onOpenFile }: { workspaceID: string | null; realtime: number; notify: (text: string) => void; activePath: string; activeMode: "file" | "diff"; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<"files" | "changes">("files");
  const snapshot = useData<WorkspaceFilesSnapshot>(workspaceID ? `/workspaces/${workspaceID}/files` : null, `${realtime}:${workspaceID}`);
  const changes = useData<WorkspaceChangesSnapshot>(workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/changes` : null, `${realtime}:${workspaceID}:${mode}`);
  const [requestedWorkspace, setRequestedWorkspace] = useState("");
  const [requestedChangeWorkspace, setRequestedChangeWorkspace] = useState("");
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const currentSnapshot = snapshot.data?.workspace_id === workspaceID ? snapshot.data : null;
  const currentChanges = changes.data?.workspace_id === workspaceID ? changes.data : null;
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
  useEffect(() => {
    setMode("files");
    setExpanded(new Set());
    setRequestedWorkspace("");
    setRequestedChangeWorkspace("");
  }, [workspaceID]);
  useEffect(() => {
    if (workspaceID && currentSnapshot?.status === "idle" && requestedWorkspace !== workspaceID) {
      setRequestedWorkspace(workspaceID);
      void refreshFiles(true);
    }
  }, [currentSnapshot?.status, refreshFiles, requestedWorkspace, workspaceID]);
  useEffect(() => {
    if (mode === "changes" && workspaceID && currentChanges?.status === "idle" && requestedChangeWorkspace !== workspaceID) {
      setRequestedChangeWorkspace(workspaceID);
      void refreshChanges(true);
    }
  }, [currentChanges?.status, mode, refreshChanges, requestedChangeWorkspace, workspaceID]);
  const tree = useMemo(() => buildFileTree(currentSnapshot?.files ?? []), [currentSnapshot?.files]);
  const toggle = (path: string) => setExpanded(current => { const next = new Set(current); if (next.has(path)) next.delete(path); else next.add(path); return next; });
  const busy = mode === "changes" ? changeScanning : fileScanning;
  const refresh = mode === "changes" ? refreshChanges : refreshFiles;
  return <section className="workspace-files"><div className="panel-heading"><div>{mode === "changes" ? <FileDiff size={17} /> : <FolderTree size={17} />}<h2>{t(mode === "changes" ? "codex.changedFiles" : "codex.projectFiles")}</h2></div><div className="row-actions"><button className={`icon-button ${mode === "changes" ? "active" : ""}`} type="button" aria-pressed={mode === "changes"} disabled={!workspaceID} title={t(mode === "changes" ? "codex.showProjectFiles" : "codex.showChangedFiles")} onClick={() => setMode(current => current === "files" ? "changes" : "files")}><FileDiff size={16} /></button><button className="icon-button" type="button" disabled={!workspaceID || busy} title={t(mode === "changes" ? "codex.refreshChanges" : "codex.refreshFiles")} onClick={() => void refresh()}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div></div><div className="workspace-file-body">{mode === "changes" ? <ChangedFilesView workspaceID={workspaceID} snapshot={currentChanges} loading={changes.loading} activePath={activeMode === "diff" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "diff" })} /> : !workspaceID ? <Empty icon={<FolderTree size={22} />} text={t("codex.selectWorkspace")} /> : !currentSnapshot ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : currentSnapshot.status === "failed" ? <div className="file-tree-error"><AlertTriangle size={16} /><span>{currentSnapshot.error || t("codex.fileScanFailed")}</span></div> : fileScanning && tree.length === 0 ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : tree.length === 0 ? <Empty icon={<Folder size={22} />} text={t("codex.noProjectFiles")} /> : <div className="file-tree">{tree.map(node => <FileTreeItem key={node.path} node={node} depth={0} expanded={expanded} onToggle={toggle} activePath={activeMode === "file" ? activePath : ""} onOpenFile={path => onOpenFile({ path, mode: "file" })} />)}{currentSnapshot.truncated && <div className="file-tree-note">{t("codex.fileListTruncated")}</div>}</div>}</div></section>;
}

const changeStatusKeys: Record<string, string> = { modified: "codex.changeModified", added: "codex.changeAdded", deleted: "codex.changeDeleted", renamed: "codex.changeRenamed", copied: "codex.changeCopied", untracked: "codex.changeUntracked", conflicted: "codex.changeConflicted" };
const changeStatusCodes: Record<string, string> = { modified: "M", added: "A", deleted: "D", renamed: "R", copied: "C", untracked: "?", conflicted: "!" };

function ChangedFilesView({ workspaceID, snapshot, loading, activePath, onOpenFile }: { workspaceID: string | null; snapshot: WorkspaceChangesSnapshot | null; loading: boolean; activePath: string; onOpenFile: (path: string) => void }) {
  const { t } = useI18n();
  if (!workspaceID) return <Empty icon={<FileDiff size={22} />} text={t("codex.selectWorkspace")} />;
  if (loading || !snapshot) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.status === "failed") return <div className="file-tree-error"><AlertTriangle size={16} /><span>{snapshot.error || t("codex.changeScanFailed")}</span></div>;
  if (snapshot.status === "scanning" && snapshot.changes.length === 0) return <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningChanges")}</div>;
  if (snapshot.changes.length === 0) return <Empty icon={<FileDiff size={22} />} text={t("codex.noChanges")} />;
  return <div className="change-file-list">{snapshot.changes.map(change => { const name = change.path.split("/").pop() || change.path; return <button type="button" className={`change-file-row ${activePath === change.path ? "active" : ""}`} title={change.path} onClick={() => onOpenFile(change.path)} key={change.path}><span className={`change-file-status ${change.status}`}>{changeStatusCodes[change.status] ?? "M"}</span><span className="change-file-name"><strong>{name}</strong><small>{change.path}</small></span><span className="change-file-label">{t(changeStatusKeys[change.status] ?? "codex.changeModified")}</span></button>; })}</div>;
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

function CreateThread({ workspaces, onCreated }: { workspaces: Workspace[]; onCreated: (thread: Thread) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <form onSubmit={async e => { e.preventDefault(); if (busy) return; setBusy(true); setError(""); try { onCreated(await post<Thread>("/threads", { workspace_id: workspaceID })); } catch (requestError) { setError(message(requestError)); } finally { setBusy(false); } }}>{error && <ErrorBanner text={error} />}<Field label={t("codex.workspace")}><select value={workspaceID} disabled={busy} onChange={e => setWorkspaceID(e.target.value)} required><option value="">{t("codex.selectWorkspaceOption")}</option>{workspaces.map(workspace => <option value={workspace.id} key={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field><DialogActions><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />}{t("codex.createSession")}</button></DialogActions></form>;
}

function SessionView({ thread, approvals, realtime, reloadApprovals, notify, onOpenFile, onNewTask }: { thread: Thread; approvals: Approval[]; realtime: unknown; reloadApprovals: () => void; notify: (text: string) => void; onOpenFile: (selection: FilePreviewSelection) => void; onNewTask: () => void }) {
  const { t } = useI18n();
  const [rawEvents, setRawEvents] = useState(false);
  const events = useData<StreamEvent[]>(`/threads/${thread.id}/events?view=${rawEvents ? "raw" : "conversation"}`, realtime);
  const [prompt, setPrompt] = useState("");
  const [images, setImages] = useState<ComposerImage[]>([]);
  const [imageBusy, setImageBusy] = useState(false);
  const [sending, setSending] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [editingEventID, setEditingEventID] = useState("");
  const [model, setModel] = useState(defaultCodexModel);
  const [customModelSignal, setCustomModelSignal] = useState(0);
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [approvalMode, setApprovalMode] = useState("on-request");
  const [slashMode, setSlashMode] = useState<"commands" | "model" | "reasoning">("commands");
  const [slashDismissedValue, setSlashDismissedValue] = useState("");
  const [statusOpen, setStatusOpen] = useState(false);
  const [goalOpen, setGoalOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [statusSnapshot, setStatusSnapshot] = useState<CodexSnapshot<CodexStatusData> | null>(null);
  const [mcpSnapshot, setMcpSnapshot] = useState<CodexSnapshot<CodexMCPServer[]> | null>(null);
  const [skillsSnapshot, setSkillsSnapshot] = useState<CodexSnapshot<CodexSkill[]> | null>(null);
  const [goal, setGoal] = useState<CodexGoal | null>(null);
  const [goalForm, setGoalForm] = useState({ objective: "", status: "active", token_budget: "" });
  const [nativeBusy, setNativeBusy] = useState("");
  const [nativeError, setNativeError] = useState("");
  const streamRef = useRef<HTMLDivElement>(null);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const slashKeyboardRef = useRef<((event: ReactKeyboardEvent<HTMLTextAreaElement>) => boolean) | null>(null);
  const sourceEvents = events.data ?? [];
  const chatEvents = useMemo(() => conversationEvents(sourceEvents), [sourceEvents]);
  const displayItems = useMemo(() => groupCommandEvents(chatEvents), [chatEvents]);
  const activeTurn = thread.status === "queued" || thread.status === "running";
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
      for (let attempt = 0; shouldRefresh && next.status === "loading" && attempt < 20; attempt++) { await new Promise(resolve => window.setTimeout(resolve, 500)); next = await api<CodexSnapshot<unknown>>(path); }
      const normalized = { ...next, data: kind === "skills" ? normalizeSkills(next.data) : kind === "mcp" ? normalizeMCP(next.data) : normalizeStatus(next.data) } as CodexSnapshot<T>;
      setter(normalized as never);
    }
    catch (error) { setNativeError(message(error)); }
    finally { setNativeBusy(""); }
  };
  const loadGoal = async () => {
    setNativeBusy("goal"); setNativeError("");
    try { const snapshot = await api<CodexSnapshot<unknown>>(`/threads/${thread.id}/goal`); const next = normalizeGoal(snapshot.data); setGoal(next); setGoalForm({ objective: next?.objective ?? "", status: next?.status ?? "active", token_budget: next?.token_budget == null ? "" : String(next.token_budget) }); if (!snapshot.supported) setNativeError(snapshot.reason || t("codex.unsupported")); }
    catch (error) { setNativeError(message(error)); }
    finally { setNativeBusy(""); }
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
    { id: "goal", name: "/goal", description: t("codex.slashGoalDescription"), icon: Target, onSelect: () => finishSlash(() => { setGoalOpen(true); void loadGoal(); }) },
    { id: "mcp", name: "/mcp", description: t("codex.slashMCPDescription"), icon: Network, onSelect: () => finishSlash(() => { setMcpOpen(true); void loadSnapshot<CodexMCPServer[]>("mcp"); }) },
    { id: "skills", name: "/skills", description: t("codex.slashSkillsDescription"), icon: Boxes, onSelect: () => finishSlash(() => { setSkillsOpen(true); void loadSnapshot<CodexSkill[]>("skills"); }) },
    { id: "plan", name: "/plan", description: t("codex.slashPlanDescription"), detail: t("codex.unsupported"), icon: StickyNote, onSelect: () => finishSlash(() => setPlanOpen(true)) },
    { id: "project", name: "/project", description: t("codex.slashProjectDescription"), icon: Folder, onSelect: () => finishSlash(onNewTask) }
  ];
  const skillItems: SlashCommandItem[] = (skillsSnapshot?.data ?? []).filter(skill => skill.enabled).map(skill => ({ id: `skill:${skill.name}`, name: `$${skill.name}`, description: skill.short_description || skill.description, detail: skill.scope, section: t("codex.availableSkills"), icon: Boxes, onSelect: () => finishSlash(() => { setPrompt(`$${skill.name} `); requestAnimationFrame(() => { promptRef.current?.focus(); promptRef.current?.setSelectionRange(skill.name.length + 2, skill.name.length + 2); }); }) }));
  const slashItems = slashMode === "model" ? modelItems : slashMode === "reasoning" ? reasoningItems : [...commandItems, ...skillItems];
  useEffect(() => { if (slashOpen && slashMode === "commands" && !skillsSnapshot && nativeBusy !== "skills") void loadSnapshot<CodexSkill[]>("skills"); }, [slashOpen, slashMode, thread.workspace_id]);
  useEffect(() => { if (statusOpen && !statusSnapshot) void loadSnapshot<CodexStatusData>("status"); }, [statusOpen, thread.id]);
  useEffect(() => { setRawEvents(false); setPrompt(""); setImages([]); setEditingEventID(""); }, [thread.id]);
  useEffect(() => { const frame = requestAnimationFrame(() => { if (streamRef.current) streamRef.current.scrollTop = streamRef.current.scrollHeight; }); return () => cancelAnimationFrame(frame); }, [thread.id, rawEvents, sourceEvents.length]);
  const addImages = async (files: File[]) => {
    const available = 4 - images.length;
    if (available <= 0) { notify(t("codex.imageLimit")); return; }
    setImageBusy(true);
    try {
      const prepared: ComposerImage[] = [];
      for (const file of files.slice(0, available)) prepared.push({ id: crypto.randomUUID(), dataURL: await compressImage(file) });
      setImages(current => [...current, ...prepared].slice(0, 4));
      if (files.length > available) notify(t("codex.imageLimit"));
    } catch {
      notify(t("codex.imageFailed"));
    } finally {
      setImageBusy(false);
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
  return <>
    <div className="session-header"><div><h2>{thread.title}</h2><span><GitBranch size={13} />{thread.project_name}<i /> <ServerIcon size={13} />{thread.server_name}</span></div><div className="session-actions"><button className={`icon-button ${rawEvents ? "active" : ""}`} aria-pressed={rawEvents} title={rawEvents ? t("codex.showConversation") : t("codex.showRawEvents")} onClick={() => setRawEvents(value => !value)}><Braces size={16} /></button><Status value={thread.status} />{thread.status === "running" && <button className="icon-button danger" disabled={interrupting} title={t("codex.interrupt")} onClick={() => void interrupt()}>{interrupting ? <LoaderCircle className="spin" size={16} /> : <Ban size={16} />}</button>}</div></div>
    <div className={`event-stream ${rawEvents ? "raw-stream" : "conversation-stream"}`} ref={streamRef} aria-live="polite">{events.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : events.error && !events.data ? <ErrorState error={events.error} reload={events.reload} /> : rawEvents ? sourceEvents.map(event => <RawEventItem key={event.event_id} event={event} />) : chatEvents.length === 0 && approvals.length === 0 && thread.status !== "running" ? <Empty icon={<Bot size={26} />} text={t("codex.noMessages")} /> : <>{displayItems.map(item => item.type === "commandGroup" ? <CommandEventGroup key={`commands:${item.events[0].event_id}`} events={item.events} /> : <ConversationEventItem key={item.event.event_id} event={item.event} onEdit={thread.archived_at ? undefined : editMessage} notify={notify} workspaceRoot={thread.path} onOpenFile={onOpenFile} />)}{approvals.map(item => <ApprovalPrompt key={item.id} item={item} onDecided={reloadApprovals} notify={notify} />)}{thread.status === "running" && approvals.length === 0 && <WorkingIndicator />}</>}</div>
    {thread.archived_at ? <div className="snapshot-notice"><Archive size={16} />{t("codex.archivedReadOnly")}</div> : <form className="composer" onSubmit={send}>
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} onClick={() => { setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length > 0 && <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" title={t("common.close")} onClick={() => setImages(current => current.filter(item => item.id !== image.id))}><X size={13} /></button></figure>)}</div>}
      {slashOpen && <SlashCommandMenu items={slashItems} query={slashQuery} label={t("codex.slashMenu")} backLabel={t("codex.slashBackToCommands")} onBack={slashMode === "commands" ? undefined : () => { setPrompt("/"); setSlashMode("commands"); }} onDismiss={closeSlash} keyboardRef={slashKeyboardRef} />}
      <textarea ref={promptRef} value={prompt} onChange={event => { setPrompt(event.target.value); if (event.target.value !== slashDismissedValue) setSlashDismissedValue(""); if (!event.target.value.startsWith("/model ") && !event.target.value.startsWith("/reasoning ")) setSlashMode("commands"); }} onKeyDown={event => { if (slashOpen && slashKeyboardRef.current?.(event)) return; if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} onPaste={event => { const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null); if (files.length) { event.preventDefault(); void addImages(files); } }} placeholder={t("codex.messagePlaceholder")} rows={3} />
      <div className="composer-bar"><div><select aria-label={t("codex.approveOnRequest")} value={approvalMode} onChange={event => setApprovalMode(event.target.value)}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select><CodexModelPicker value={model} onChange={setModel} allowServerDefault requestCustom={customModelSignal} /><select aria-label={t("codex.reasoningEffort")} value={reasoningEffort} onChange={event => setReasoningEffort(event.target.value)}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}</select></div><button className="primary-button" title={activeTurn ? t("codex.waitForTurn") : t("codex.send")} disabled={slashOpen || (!prompt.trim() && images.length === 0) || imageBusy || sending || activeTurn}>{sending ? <LoaderCircle className="spin" size={17} /> : <ChevronRight size={17} />}{t("codex.send")}</button></div>
    </form>}
    <Dialog open={statusOpen} title={t("codex.taskStatus")} onClose={() => setStatusOpen(false)}><SnapshotNotice snapshot={statusSnapshot} loading={nativeBusy === "status"} error={nativeError} /><dl className="task-status-list"><div><dt>{t("codex.statusWioTaskID")}</dt><dd><code>{thread.id}</code></dd></div><div><dt>{t("codex.statusCodexThreadID")}</dt><dd>{thread.codex_thread_id ? <code>{thread.codex_thread_id}</code> : t("codex.notBound")}</dd></div><div><dt>{t("column.project")}</dt><dd>{thread.project_name}</dd></div><div><dt>{t("column.server")}</dt><dd>{thread.server_name}</dd></div><div><dt>{t("codex.statusWorkingDirectory")}</dt><dd><code>{thread.path}</code></dd></div><div><dt>{t("codex.modelOverride")}</dt><dd>{String(statusSnapshot?.data?.model || model || t("codex.modelServerDefault"))}</dd></div><div><dt>{t("codex.reasoningEffort")}</dt><dd>{String(statusSnapshot?.data?.reasoning_effort || (reasoningEffort ? t(codexReasoningOptions.find(option => option.value === reasoningEffort)?.labelKey ?? "codex.reasoningDefault") : t("codex.reasoningDefault")))}</dd></div><div><dt>{t("codex.statusApprovalPolicy")}</dt><dd>{String(statusSnapshot?.data?.approval_policy || t(approvalMode === "on-request" ? "codex.approveOnRequest" : approvalMode === "untrusted" ? "codex.untrusted" : "codex.neverApprove"))}</dd></div><div><dt>{t("column.state")}</dt><dd><Status value={thread.status} /></dd></div>{(statusSnapshot?.data?.rate_limits ?? []).map(limit => <div key={limit.name}><dt>{limit.name}</dt><dd>{limit.used_percent == null ? limit.detail || "-" : `${limit.used_percent}%${limit.resets_at ? ` · ${t("codex.resetsAt", { time: formatDate(limit.resets_at) })}` : ""}`}</dd></div>)}</dl><DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "status"} onClick={() => void loadSnapshot<CodexStatusData>("status", true)}>{nativeBusy === "status" ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={goalOpen} title={t("codex.goalTitle")} onClose={() => setGoalOpen(false)}><SnapshotNotice loading={nativeBusy === "goal"} error={nativeError} /><form onSubmit={async event => { event.preventDefault(); setNativeBusy("goal"); setNativeError(""); try { await put(`/threads/${thread.id}/goal`, { objective: goalForm.objective.trim(), status: goalForm.status, token_budget: goalForm.token_budget ? Number(goalForm.token_budget) : null }); await waitForGoalSnapshot(thread.id); await loadGoal(); notify(t("codex.goalSaved")); } catch (error) { setNativeError(message(error)); } finally { setNativeBusy(""); } }}><Field label={t("codex.goalObjective")}><textarea rows={3} value={goalForm.objective} onChange={event => setGoalForm({ ...goalForm, objective: event.target.value })} required /></Field><div className="form-grid"><Field label={t("column.state")}><select value={goalForm.status} onChange={event => setGoalForm({ ...goalForm, status: event.target.value })}><option value="active">active</option><option value="paused">paused</option><option value="blocked">blocked</option><option value="complete">complete</option></select></Field><Field label={t("codex.goalTokenBudget")}><input type="number" min="1" value={goalForm.token_budget} onChange={event => setGoalForm({ ...goalForm, token_budget: event.target.value })} placeholder={t("codex.noLimit")} /></Field></div>{goal && <p className="snapshot-meta">{t("codex.goalUsage", { tokens: goal.tokens_used, seconds: goal.time_used_seconds })}</p>}<DialogActions>{goal && <button type="button" className="secondary-button danger" disabled={nativeBusy === "goal"} onClick={async () => { setNativeBusy("goal"); try { await remove(`/threads/${thread.id}/goal`); await waitForGoalSnapshot(thread.id); await loadGoal(); notify(t("codex.goalCleared")); } catch (error) { setNativeError(message(error)); } finally { setNativeBusy(""); } }}><Trash2 size={16} />{t("codex.clearGoal")}</button>}<button className="primary-button" disabled={nativeBusy === "goal" || !goalForm.objective.trim()}>{nativeBusy === "goal" ? <LoaderCircle className="spin" size={16} /> : <Target size={16} />}{t("common.save")}</button></DialogActions></form></Dialog>
    <Dialog open={mcpOpen} title={t("codex.mcpTitle")} onClose={() => setMcpOpen(false)}><SnapshotNotice snapshot={mcpSnapshot} loading={nativeBusy === "mcp"} error={nativeError} />{mcpSnapshot?.data?.length ? <div className="native-list">{mcpSnapshot.data.map(server => <article key={server.name}><header><strong>{server.name}</strong><Status value={server.auth_status || "unknown"} /></header>{(server.server_name || server.server_version) && <small>{[server.server_name, server.server_version].filter(Boolean).join(" ")}</small>}<p>{server.tools.length ? server.tools.join(", ") : t("codex.noTools")}</p><small>{t("codex.mcpResources", { resources: server.resource_count, templates: server.resource_template_count })}</small></article>)}</div> : !nativeBusy && <Empty icon={<Network size={24} />} text={t("codex.noMCPServers")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "mcp"} onClick={() => void loadSnapshot<CodexMCPServer[]>("mcp", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
    <Dialog open={skillsOpen} title={t("codex.skillsTitle")} onClose={() => setSkillsOpen(false)}><SnapshotNotice snapshot={skillsSnapshot} loading={nativeBusy === "skills"} error={nativeError} />{skillsSnapshot?.data?.length ? <div className="native-list">{skillsSnapshot.data.map(skill => <article key={`${skill.scope}:${skill.name}`}><header><strong>{skill.display_name || skill.name}</strong><Status value={skill.enabled ? "enabled" : "disabled"} /></header><p>{skill.short_description || skill.description}</p><small>{skill.scope}</small></article>)}</div> : !nativeBusy && <Empty icon={<Boxes size={24} />} text={t("codex.noSkills")} />}<DialogActions><button type="button" className="secondary-button" disabled={nativeBusy === "skills"} onClick={() => void loadSnapshot<CodexSkill[]>("skills", true)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions></Dialog>
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

async function waitForGoalSnapshot(threadID: string) {
  for (let attempt = 0; attempt < 20; attempt++) {
    const snapshot = await api<CodexSnapshot<unknown>>(`/threads/${threadID}/goal`);
    if (snapshot.status !== "loading") return snapshot;
    await new Promise(resolve => window.setTimeout(resolve, 500));
  }
}

async function waitForThread(threadID: string, timeoutMessage: string) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const threads = await api<Thread[]>("/threads");
    const thread = threads.find(item => item.id === threadID);
    if (thread) return thread;
    await new Promise(resolve => window.setTimeout(resolve, 500));
  }
  throw new Error(timeoutMessage);
}

async function waitForWorkspace(workspaceID: string, timeoutMessage: string) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const workspaces = await api<Workspace[]>("/workspaces");
      const workspace = workspaces.find(item => item.id === workspaceID);
      if (workspace) return workspace;
    } catch (error) {
      if (error instanceof APIError && error.status >= 400 && error.status < 500) throw error;
    }
    await new Promise(resolve => window.setTimeout(resolve, 750));
  }
  throw new Error(timeoutMessage);
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
  return { rate_limits };
}

function ApprovalPrompt({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  return <article className="approval-prompt"><header><ShieldCheck size={16} /><strong>{t("codex.pendingApprovals")}</strong><time>{relative(item.expires_at)}</time></header><small>{readableKind(item.kind)}</small><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={onDecided} notify={notify} /></article>;
}

function ApprovalActions({ item, onDecided, notify }: { item: Approval; onDecided: () => void; notify: (text: string) => void }) {
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
  if (kind === "codex.error" || kind === "codex.turn.failed" || kind === "codex.interrupt.failed" || kind === "codex.approval.failed") return <article className="message error"><header><AlertTriangle size={15} /><strong>{t(kind === "codex.turn.failed" || kind === "codex.error" ? "codex.turnFailed" : "codex.actionFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(payload) || t("codex.unknownError")}</div></article>;
  if (kind === "codex.turn.completed") {
    const turn = asRecord(payload?.turn);
    const status = String(turn?.status ?? "failed");
    if (status === "interrupted") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
    return <article className="message error"><header><AlertTriangle size={15} /><strong>{t("codex.turnFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(turn) || t("codex.unknownError")}</div></article>;
  }
  const item = asRecord(payload?.item);
  const itemImages = extractImageSources(item);
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

function FilePreviewPane({ workspaceID, selection, realtime, onClose }: { workspaceID: string; selection: FilePreviewSelection; realtime: number; onClose: () => void }) {
  const { t } = useI18n();
  const [requestVersion, setRequestVersion] = useState(0);
  const [requesting, setRequesting] = useState(false);
  const [requestError, setRequestError] = useState("");
  const endpoint = `/workspaces/${workspaceID}/file-preview?path=${encodeURIComponent(selection.path)}`;
  const preview = useData<WorkspaceFilePreview>(endpoint, `${realtime}:${requestVersion}`);
  const requestPreview = useCallback(async () => {
    setRequesting(true);
    setRequestError("");
    try {
      await post(`/workspaces/${workspaceID}/file-preview`, { path: selection.path });
      preview.reload();
    } catch (error) {
      setRequestError(message(error));
    } finally {
      setRequesting(false);
    }
  }, [preview.reload, selection.path, workspaceID]);
  useEffect(() => { void requestPreview(); }, [requestPreview]);
  const data = preview.data?.path === selection.path ? preview.data : null;
  const loading = requesting || preview.loading || !data || data.status === "idle" || data.status === "loading";
  const error = requestError || preview.error || data?.error || "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  return <section className="file-preview-panel"><header className="file-preview-header"><div><FileCode2 size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{data?.status === "succeeded" && <><span className="file-language">{language.label}</span><span className="file-size">{formatFileSize(data.size)}</span></>}<button type="button" className="icon-button" disabled={requesting} title={t("codex.refreshPreview")} aria-label={t("codex.refreshPreview")} onClick={() => { setRequestVersion(value => value + 1); void requestPreview(); }}>{requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closePreview")} aria-label={t("codex.closePreview")} onClick={onClose}><X size={16} /></button></div></header><div className="file-preview-body">{error ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.previewFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={() => { setRequestVersion(value => value + 1); void requestPreview(); }}><RefreshCw size={15} />{t("common.retry")}</button></div> : loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div> : data?.status === "succeeded" ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.previewTruncated", { size: formatFileSize(data.size) })}</div>}<Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingPreview")}</span></div>}><HighlightedFile content={data.content} language={language.id} targetLine={selection.line} /></Suspense></> : null}</div></section>;
}

function FileDiffPane({ workspaceID, selection, realtime, onClose }: { workspaceID: string; selection: FilePreviewSelection; realtime: number; onClose: () => void }) {
  const { t } = useI18n();
  const [requestVersion, setRequestVersion] = useState(0);
  const [requesting, setRequesting] = useState(false);
  const [requestError, setRequestError] = useState("");
  const endpoint = `/workspaces/${workspaceID}/diff-preview?path=${encodeURIComponent(selection.path)}`;
  const preview = useData<WorkspaceDiffPreview>(endpoint, `${realtime}:${requestVersion}`);
  const requestPreview = useCallback(async () => {
    setRequesting(true);
    setRequestError("");
    try {
      await post(`/workspaces/${workspaceID}/diff-preview`, { path: selection.path });
      preview.reload();
    } catch (error) {
      setRequestError(message(error));
    } finally {
      setRequesting(false);
    }
  }, [preview.reload, selection.path, workspaceID]);
  useEffect(() => { void requestPreview(); }, [requestPreview]);
  const data = preview.data?.path === selection.path ? preview.data : null;
  const loading = requesting || preview.loading || !data || data.status === "idle" || data.status === "loading";
  const error = requestError || preview.error || data?.error || "";
  const language = previewLanguage(selection.path);
  const fileName = selection.path.split("/").pop() || selection.path;
  const retry = () => { setRequestVersion(value => value + 1); void requestPreview(); };
  return <section className="file-preview-panel file-diff-panel"><header className="file-preview-header"><div><FileDiff size={17} /><span><h2>{fileName}</h2><small title={selection.path}>{selection.path}</small></span></div><div className="file-preview-actions">{data?.status === "succeeded" && <><span className="diff-stat additions">+{data.additions}</span><span className="diff-stat deletions">-{data.deletions}</span></>}<button type="button" className="icon-button" disabled={requesting} title={t("codex.refreshDiff")} aria-label={t("codex.refreshDiff")} onClick={retry}>{requesting ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button type="button" className="icon-button" title={t("codex.closeDiff")} aria-label={t("codex.closeDiff")} onClick={onClose}><X size={16} /></button></div></header><div className="file-preview-body">{error ? <div className="file-preview-error"><AlertTriangle size={22} /><strong>{t("codex.diffFailed")}</strong><span>{error}</span><button type="button" className="secondary-button" onClick={retry}><RefreshCw size={15} />{t("common.retry")}</button></div> : loading ? <div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div> : data?.status === "succeeded" ? <>{data.truncated && <div className="file-preview-note"><AlertTriangle size={14} />{t("codex.diffTruncated")}</div>}{data.binary ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.binaryDiff")}</span></div> : !data.content ? <div className="file-preview-empty"><FileDiff size={24} /><span>{t("codex.noTextDiff")}</span></div> : <Suspense fallback={<div className="file-preview-loading"><LoaderCircle className="spin" size={20} /><span>{t("codex.loadingDiff")}</span></div>}><HighlightedDiff content={data.content} language={language.id} unchangedLabel={count => t("codex.unchangedLines", { count })} /></Suspense>}</> : null}</div></section>;
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

export function DeploymentsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const targets = useData<DeploymentTarget[]>("/deployment-targets", realtime);
  const deployments = useData<Deployment[]>("/deployments", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const emptyForm = { source_type: "workspace" as "workspace" | "remote", workspace_id: "", server_id: "", secret_set_id: "", environment: "production", repository: "", git_ref: "", compose_file: "compose.yaml", build_mode: "build", health: "" };
  const [targetDialog, setTargetDialog] = useState(false);
  const [editingTarget, setEditingTarget] = useState<DeploymentTarget | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [busy, setBusy] = useState("");
  const [detailID, setDetailID] = useState("");
  const detail = useData<DeploymentDetail>(detailID ? `/deployments/${detailID}` : null, realtime);
  const active = (status: string) => ["queued", "preparing", "running"].includes(status);
  const openCreate = () => { setEditingTarget(null); setForm(emptyForm); setTargetDialog(true); };
  const openEdit = (target: DeploymentTarget) => {
    let health = "";
    try { health = (JSON.parse(target.health_checks) as Array<{ address?: string }>).map(check => check.address ?? "").filter(Boolean).join("\n"); } catch { health = ""; }
    setEditingTarget(target);
    setForm({ source_type: target.source_type || "remote", workspace_id: target.workspace_id || "", server_id: target.server_id, secret_set_id: target.secret_set_id, environment: target.environment, repository: target.repository, git_ref: target.git_ref, compose_file: target.compose_file, build_mode: target.build_mode, health });
    setTargetDialog(true);
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { health, ...target } = form;
    const health_checks = health.split(/\r?\n/).map(address => address.trim()).filter(Boolean).map(address => ({ type: address.startsWith("http") ? "http" : "tcp", address, timeout_seconds: 60 }));
    setBusy(editingTarget ? `edit:${editingTarget.id}` : "create");
    try {
      if (editingTarget) await put(`/deployment-targets/${editingTarget.id}`, { ...target, health_checks });
      else await post("/deployment-targets", { ...target, health_checks });
      setTargetDialog(false);
      targets.reload();
      notify(t(editingTarget ? "deployment.targetUpdated" : "deployment.targetCreated"));
    } catch (err) { notify(message(err)); } finally { setBusy(""); }
  };
  const run = async (target: DeploymentTarget) => { setBusy(`deploy:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/deploy`, { commit_ref: target.git_ref }); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.queued")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const rollback = async (target: DeploymentTarget) => { if (!confirm(t("deployment.confirmRollback", { project: target.project_name, environment: target.environment }))) return; setBusy(`rollback:${target.id}`); try { const response = await post<{ deployment: Deployment }>(`/deployment-targets/${target.id}/rollback`, {}); deployments.reload(); setDetailID(response.deployment.id); notify(t("deployment.rollbackQueued")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const deleteTarget = async (target: DeploymentTarget) => { if (!confirm(t("deployment.confirmDeleteTarget", { project: target.project_name, environment: target.environment }))) return; setBusy(`delete-target:${target.id}`); try { await remove(`/deployment-targets/${target.id}`); targets.reload(); deployments.reload(); notify(t("deployment.targetDeleted")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const deleteHistory = async (item: Deployment) => { if (!confirm(t("deployment.confirmDeleteHistory"))) return; setBusy(`delete-deployment:${item.id}`); try { await remove(`/deployments/${item.id}`); if (detailID === item.id) setDetailID(""); deployments.reload(); notify(t("deployment.historyDeleted")); } catch (err) { notify(message(err)); } finally { setBusy(""); } };
  const availableWorkspaces = (workspaces.data ?? []).filter(item => item.server_id === form.server_id && item.status === "ready");
  return <div className="page-stack deployment-page"><Section title={t("deployment.targets")} icon={<Rocket size={18} />} action={<button className="primary-button" onClick={openCreate}><Plus size={17} />{t("deployment.newTarget")}</button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.server"), t("column.gitRef"), t("column.compose"), t("common.actions")]} empty={t("deployment.noTargets")}>{(targets.data ?? []).map(target => <tr key={target.id}><td><div className="cell-main"><strong>{target.project_name}</strong><small>{target.source_type === "workspace" ? target.workspace_path : target.repository}</small></div></td><td><Status value={target.environment} /></td><td>{target.server_name}</td><td><code>{target.git_ref}</code></td><td><code>{target.compose_file}</code></td><td><div className="row-actions"><button className="primary-button small" disabled={busy !== ""} onClick={() => void run(target)}><Rocket className={busy === `deploy:${target.id}` ? "spin" : ""} size={14} />{t("deployment.deploy")}</button><button className="icon-button" disabled={busy !== ""} title={t("deployment.rollback")} onClick={() => void rollback(target)}><Undo2 size={15} /></button><button className="icon-button" disabled={busy !== ""} title={t("deployment.editTarget")} onClick={() => openEdit(target)}><Pencil size={15} /></button><button className="icon-button danger" disabled={busy !== ""} title={t("deployment.deleteTarget")} onClick={() => void deleteTarget(target)}><Trash2 size={15} /></button></div></td></tr>)}</DataTable></Section><Section title={t("deployment.history")} icon={<History size={18} />} action={<button className="icon-button" title={t("common.refresh")} onClick={deployments.reload}><RefreshCw size={16} /></button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("deployment.duration"), t("column.message"), t("column.created"), t("common.actions")]} empty={t("deployment.noHistory")}>{(deployments.data ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td className="deployment-duration">{deploymentDuration(item)}</td><td className="message-cell" title={item.message}>{item.message || "-"}</td><td>{relative(item.created_at)}</td><td><div className="row-actions"><button className="icon-button" title={t("deployment.viewLogs")} onClick={() => setDetailID(item.id)}><SquareTerminal size={15} /></button><button className="icon-button danger" disabled={active(item.status) || busy !== ""} title={active(item.status) ? t("deployment.activeCannotDelete") : t("deployment.deleteHistory")} onClick={() => void deleteHistory(item)}><Trash2 size={15} /></button></div></td></tr>)}</DataTable></Section>
  <Dialog open={targetDialog} title={t(editingTarget ? "deployment.editTargetTitle" : "deployment.targetTitle")} onClose={() => setTargetDialog(false)} wide><form onSubmit={submit}><div className="segmented-control deployment-source-control" role="group" aria-label={t("deployment.sourceType")}><button type="button" className={form.source_type === "workspace" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "workspace", repository: "", git_ref: "", workspace_id: "" })}><ServerIcon size={15} />{t("deployment.sourceWorkspace")}</button><button type="button" className={form.source_type === "remote" ? "active" : ""} onClick={() => setForm({ ...form, source_type: "remote", workspace_id: "", git_ref: form.git_ref || "main" })}><GitBranch size={15} />{t("deployment.sourceRemote")}</button></div><div className="form-grid"><Field label={t("column.server")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value, workspace_id: "" })} required><option value="">{t("deployment.selectServer")}</option>{(servers.data ?? []).filter(item => item.status === "online").map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>{form.source_type === "workspace" ? <Field label={t("deployment.workspace")}><select value={form.workspace_id} onChange={e => { const workspace = availableWorkspaces.find(item => item.id === e.target.value); setForm({ ...form, workspace_id: e.target.value, git_ref: workspace?.branch || "" }); }} required><option value="">{t("deployment.selectWorkspace")}</option>{availableWorkspaces.map(item => <option key={item.id} value={item.id}>{item.project_name} · {item.display_name || item.path}</option>)}</select></Field> : <Field label={t("deployment.repository")}><input value={form.repository} onChange={e => setForm({ ...form, repository: e.target.value })} placeholder="https://example.com/team/project.git" required /></Field>}<Field label={t("column.environment")}><input value={form.environment} onChange={e => setForm({ ...form, environment: e.target.value })} required /></Field><Field label={t("deployment.secretSet")}><select value={form.secret_set_id} onChange={e => setForm({ ...form, secret_set_id: e.target.value })}><option value="">{t("common.none")}</option>{(secrets.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field></div><div className="form-grid thirds"><Field label={t("column.gitRef")}><input value={form.git_ref} onChange={e => setForm({ ...form, git_ref: e.target.value })} placeholder={form.source_type === "workspace" ? t("deployment.currentBranch") : "main"} /></Field><Field label={t("deployment.composeFile")}><input value={form.compose_file} onChange={e => setForm({ ...form, compose_file: e.target.value })} /></Field><Field label={t("deployment.buildMode")}><select value={form.build_mode} onChange={e => setForm({ ...form, build_mode: e.target.value })}><option value="build">{t("deployment.build")}</option><option value="pull">{t("deployment.pull")}</option></select></Field></div><Field label={t("deployment.healthCheck")}><textarea rows={2} value={form.health} onChange={e => setForm({ ...form, health: e.target.value })} placeholder={t("deployment.healthPlaceholder")} /></Field><div className="deployment-preflight-note"><ShieldCheck size={17} /><span>{t("deployment.preflightNote")}</span></div><DialogActions><button type="button" className="secondary-button" onClick={() => setTargetDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={busy !== ""}>{busy ? <LoaderCircle className="spin" size={16} /> : editingTarget ? <Check size={16} /> : <Rocket size={16} />}{t(editingTarget ? "deployment.saveTarget" : "deployment.createTarget")}</button></DialogActions></form></Dialog>
  <Dialog open={Boolean(detailID)} title={t("deployment.logTitle")} onClose={() => setDetailID("")} wide className="deployment-log-dialog"><div className="deployment-log-content">{detail.loading && <div className="deployment-log-loading"><LoaderCircle className="spin" size={20} />{t("common.loading")}</div>}{detail.error && <ErrorBanner text={detail.error} />}{detail.data && <><div className="deployment-log-summary"><div><small>{t("column.project")}</small><strong>{detail.data.deployment.project_name}</strong></div><div><small>{t("column.environment")}</small><Status value={detail.data.deployment.environment} /></div><div><small>{t("column.commit")}</small><code>{shortSHA(detail.data.deployment.resolved_commit || detail.data.deployment.commit_ref)}</code></div><div><small>{t("deployment.duration")}</small><strong>{deploymentDuration(detail.data.deployment)}</strong></div></div><div className="deployment-event-list">{(detail.data.events ?? []).length ? (detail.data.events ?? []).map(event => <article className={`deployment-event ${event.status}`} key={event.id}><span className="deployment-event-marker" /><header><Status value={event.status} /><strong>{event.message || t("deployment.processStep")}</strong><time>{formatTime(event.occurred_at)}</time></header>{event.content && <pre>{event.content}</pre>}</article>) : <Empty icon={<SquareTerminal size={22} />} text={t("deployment.noLogs")} />}</div></>}</div></Dialog></div>;
}

function deploymentDuration(deployment: Deployment) {
  const start = deployment.started_at || deployment.created_at;
  const finish = deployment.finished_at || (deployment.status === "running" || deployment.status === "preparing" ? new Date().toISOString() : null);
  if (!finish) return "-";
  const seconds = Math.max(0, Math.round((new Date(finish).getTime() - new Date(start).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return seconds < 3600 ? `${minutes}m ${seconds % 60}s` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function MonitoringPage({ realtime }: { realtime: number }) {
  const { t } = useI18n();
  const servers = useData<Server[]>("/servers", realtime);
  const alerts = useData<Alert[]>("/alerts", realtime);
  const [serverID, setServerID] = useState("");
  const [rangeHours, setRangeHours] = useState(24);
  useEffect(() => { if (!serverID && servers.data?.[0]) setServerID(servers.data[0].id); }, [servers.data, serverID]);
  const metrics = useData<Metric[]>(serverID ? `/servers/${serverID}/metrics?hours=${rangeHours}` : null, `${realtime}:${serverID}:${rangeHours}`);
  const activeServer = servers.data?.find(server => server.id === serverID);
  const sampled = useMemo(() => downsampleMetrics(metrics.data ?? [], 420), [metrics.data]);
  const network = useMemo(() => networkRates(sampled), [sampled]);
  const rawNetwork = useMemo(() => networkRates(metrics.data ?? []), [metrics.data]);
  const latest = metrics.data?.at(-1);
  const latestNetwork = rawNetwork.at(-1);
  const resourcePoints = sampled.map(point => ({ time: point.bucket_at, values: [point.cpu_percent, point.memory_percent, point.disk_percent] }));
  const loadPoints = sampled.map(point => ({ time: point.bucket_at, values: [point.load_1] }));
  const networkPoints = network.map(point => ({ time: point.time, values: [point.rx, point.tx] }));
  const ranges = [1, 6, 24, 168];
  const metricAction = <div className="monitor-actions"><select className="compact-select" aria-label={t("monitor.selectServer")} value={serverID} onChange={event => setServerID(event.target.value)}>{(servers.data ?? []).map(server => <option key={server.id} value={server.id}>{server.name}</option>)}</select><div className="range-control" role="group" aria-label={t("monitor.timeRange")}>{ranges.map(hours => <button type="button" aria-pressed={rangeHours === hours} className={rangeHours === hours ? "active" : ""} key={hours} onClick={() => setRangeHours(hours)}>{t(`monitor.range.${hours}`)}</button>)}</div></div>;
  return <div className="page-stack">
    <Section title={t("monitor.serverMetrics")} icon={<MonitorDot size={18} />} action={metricAction}>
      <div className="monitor-header"><div><strong>{activeServer?.name ?? t("server.none")}</strong><ServerInformation server={activeServer} className="monitor-server-information" /><small>{latest ? t("monitor.lastSample", { time: formatDate(latest.bucket_at) }) : t("monitor.awaitingData")}</small></div><div className="monitor-header-meta"><span>{t("monitor.samples", { count: String(metrics.data?.length ?? 0) })}</span><Status value={activeServer?.status ?? "offline"} /></div></div>
      <div className="metric-summary-grid">
        <MetricStat icon={<Cpu size={17} />} label={t("monitor.cpu")} value={formatPercent(latest?.cpu_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.cpu_percent)) })} tone="green" />
        <MetricStat icon={<MemoryStick size={17} />} label={t("monitor.memory")} value={formatPercent(latest?.memory_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.memory_percent)) })} tone="cyan" />
        <MetricStat icon={<HardDrive size={17} />} label={t("monitor.disk")} value={formatPercent(latest?.disk_percent)} detail={t("monitor.peak", { value: formatPercent(metricPeak(metrics.data, point => point.disk_percent)) })} tone="amber" />
        <MetricStat icon={<Gauge size={17} />} label={t("monitor.load1")} value={formatDecimal(latest?.load_1)} detail={t("monitor.peak", { value: formatDecimal(metricPeak(metrics.data, point => point.load_1)) })} tone="violet" />
        <MetricStat icon={<ArrowDownToLine size={17} />} label={t("monitor.networkIn")} value={formatByteRate(latestNetwork?.rx)} detail={t("monitor.peak", { value: formatByteRate(metricPeak(rawNetwork, point => point.rx)) })} tone="blue" />
        <MetricStat icon={<ArrowUpFromLine size={17} />} label={t("monitor.networkOut")} value={formatByteRate(latestNetwork?.tx)} detail={t("monitor.peak", { value: formatByteRate(metricPeak(rawNetwork, point => point.tx)) })} tone="red" />
      </div>
      <div className="monitor-chart-layout">
        <TimeSeriesChart className="resource-chart" title={t("monitor.resourceTrend")} data={resourcePoints} fixedMax={100} axisFormat={value => `${Math.round(value ?? 0)}%`} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.cpu"), color: "#168f68", format: formatPercent }, { label: t("monitor.memory"), color: "#1684a3", format: formatPercent }, { label: t("monitor.disk"), color: "#d28a1d", format: formatPercent }]} />
        <TimeSeriesChart title={t("monitor.loadTrend")} data={loadPoints} axisFormat={formatDecimal} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.load1"), color: "#7b61a8", format: formatDecimal }]} />
        <TimeSeriesChart title={t("monitor.networkTrend")} data={networkPoints} axisFormat={formatByteRate} empty={t("monitor.noMetrics")} series={[{ label: t("monitor.networkIn"), color: "#267aa3", format: formatByteRate }, { label: t("monitor.networkOut"), color: "#b65555", format: formatByteRate }]} />
      </div>
    </Section>
    <Section title={t("monitor.alerts")} icon={<BellRing size={18} />}><DataTable headers={[t("column.severity"), t("column.alert"), t("column.server"), t("column.state"), t("column.started"), ""]} empty={t("monitor.noAlerts")}>{(alerts.data ?? []).map(alert => <tr key={alert.id}><td><Status value={alert.severity} /></td><td><div className="cell-main"><strong>{alert.title}</strong><small>{alert.detail}</small></div></td><td>{alert.server_name}</td><td><Status value={alert.status} /></td><td>{relative(alert.opened_at)}</td><td>{!alert.acknowledged_at && <button className="icon-button" title={t("monitor.acknowledge")} onClick={async () => { await post(`/alerts/${alert.id}/acknowledge`, {}); alerts.reload(); }}><Check size={16} /></button>}</td></tr>)}</DataTable></Section>
  </div>;
}

function SettingsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const codexSettings = useData<CodexCLISettings>("/settings/codex-cli", realtime);
  const profiles = useData<CredentialProfile[]>("/credential-profiles", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const audit = useData<AuditEntry[]>("/audit", realtime);
  const [profileDialog, setProfileDialog] = useState(false);
  const [profileBusy, setProfileBusy] = useState(false);
  const [profileForm, setProfileForm] = useState({ id: "", kind: "codex" as "codex" | "git", name: "", endpoint: "https://api.openai.com/v1", username: "", model: defaultCodexModel, commit_name: "", commit_email: "", secret: "" });
  const [secretDialog, setSecretDialog] = useState(false);
  const [name, setName] = useState("");
  const [lines, setLines] = useState("");
  const [codexTargetBusy, setCodexTargetBusy] = useState(false);
  const [codexVersions, setCodexVersions] = useState<string[]>([]);
  const [selectedCodexVersion, setSelectedCodexVersion] = useState("");
  useEffect(() => {
    if (!codexSettings.data) return;
    setCodexVersions(codexSettings.data.versions?.length ? codexSettings.data.versions : [codexSettings.data.target_version]);
    setSelectedCodexVersion(codexSettings.data.target_version);
  }, [codexSettings.data]);
  const openProfile = (profile?: CredentialProfile) => {
    setProfileForm(profile ? { id: profile.id, kind: profile.kind, name: profile.name, endpoint: profile.endpoint, username: profile.username, model: profile.kind === "codex" ? profile.model || defaultCodexModel : "", commit_name: profile.commit_name, commit_email: profile.commit_email, secret: "" } : { id: "", kind: "codex", name: "", endpoint: "https://api.openai.com/v1", username: "", model: defaultCodexModel, commit_name: "", commit_email: "", secret: "" });
    setProfileDialog(true);
  };
  const changeProfileKind = (kind: "codex" | "git") => setProfileForm(current => current.id ? current : { ...current, kind, endpoint: kind === "codex" ? "https://api.openai.com/v1" : "https://github.com", username: "", model: kind === "codex" ? defaultCodexModel : "", commit_name: "", commit_email: "", secret: "" });
  const saveProfile = async (event: FormEvent) => {
    event.preventDefault(); setProfileBusy(true);
    try {
      await post("/credential-profiles", profileForm);
      setProfileDialog(false); profiles.reload(); notify(t("settings.profileSaved"));
    } catch (err) { notify(message(err)); } finally { setProfileBusy(false); }
  };
  const submitSecretSet = async (event: FormEvent) => { event.preventDefault(); const values: Record<string, string> = {}; for (const line of lines.split("\n")) { const index = line.indexOf("="); if (index > 0) values[line.slice(0, index).trim()] = line.slice(index + 1); } try { await post("/secret-sets", { name, values }); setSecretDialog(false); secrets.reload(); setLines(""); notify(t("settings.secretSaved")); } catch (err) { notify(message(err)); } };
  const checkCodexUpdates = async () => { setCodexTargetBusy(true); try { const result = await post<CodexCLISettings>("/settings/codex-cli/check-updates", {}); setCodexVersions(result.versions ?? [result.target_version]); setSelectedCodexVersion(result.target_version); codexSettings.reload(); notify(t(result.updated ? "settings.codexUpdateFound" : "settings.codexAlreadyLatest", { version: result.latest_version ?? result.target_version })); } catch (err) { notify(message(err)); } finally { setCodexTargetBusy(false); } };
  const applyCodexVersion = async () => { if (!selectedCodexVersion) return; setCodexTargetBusy(true); try { const result = await post<CodexCLISettings>("/settings/codex-cli/select-version", { version: selectedCodexVersion }); setCodexVersions(result.versions ?? [result.target_version]); setSelectedCodexVersion(result.target_version); codexSettings.reload(); notify(t("settings.codexVersionApplied", { version: result.target_version })); } catch (err) { notify(message(err)); } finally { setCodexTargetBusy(false); } };
  return <div className="page-stack">
    <Section title={t("settings.codexCLIManagement")} icon={<SquareTerminal size={18} />}>
      <div className="codex-version-control"><div className="codex-version-summary"><small>{t("settings.codexTargetVersion")}</small><strong>{codexSettings.data?.target_version ?? "-"}</strong><span><ShieldCheck size={14} />{t("settings.codexStableRelease")}</span></div><div className="codex-version-actions"><select aria-label={t("settings.selectCodexVersion")} value={selectedCodexVersion} disabled={codexTargetBusy || !codexVersions.length} onChange={event => setSelectedCodexVersion(event.target.value)}>{codexVersions.map((version, index) => <option key={version} value={version}>{index === 0 ? t("settings.latestCodexVersion", { version }) : version}</option>)}</select><button className="secondary-button" disabled={codexTargetBusy || !selectedCodexVersion || selectedCodexVersion === codexSettings.data?.target_version} onClick={() => void applyCodexVersion()}><Check size={16} />{t("settings.applyCodexVersion")}</button><button className="primary-button" disabled={codexTargetBusy || !codexSettings.data} onClick={() => void checkCodexUpdates()}>{codexTargetBusy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t(codexTargetBusy ? "settings.checkingCodexUpdates" : "settings.checkCodexUpdates")}</button></div></div>
    </Section>
    <Section title={t("settings.credentialProfiles")} icon={<KeyRound size={18} />} action={<button className="primary-button" onClick={() => openProfile()}><Plus size={17} />{t("settings.newProfile")}</button>}>
      <DataTable headers={[t("settings.type"), t("settings.name"), t("settings.endpoint"), t("settings.profileDetail"), t("column.updated"), ""]} empty={t("settings.noProfiles")}>{(profiles.data ?? []).map(profile => <tr key={profile.id}><td><Status value={profile.kind} /></td><td><strong>{profile.name}</strong></td><td><code className="truncate-code">{profile.endpoint}</code></td><td>{profile.kind === "codex" ? <code>{profile.model}</code> : <div className="cell-main"><span className="inline"><UserRound size={14} />{profile.username}</span><small>{profile.commit_name && profile.commit_email ? `${profile.commit_name} · ${profile.commit_email}` : t("settings.gitIdentityMissing")}</small></div>}</td><td>{relative(profile.updated_at)}</td><td><div className="row-actions"><button className="icon-button" title={t("settings.editProfile")} onClick={() => openProfile(profile)}><Pencil size={15} /></button><button className="icon-button danger" title={t("settings.deleteProfile")} onClick={async () => { if (!confirm(t("settings.confirmDeleteProfile", { name: profile.name }))) return; await remove(`/credential-profiles/${profile.id}`); profiles.reload(); notify(t("settings.profileDeleted")); }}><Trash2 size={15} /></button></div></td></tr>)}</DataTable>
    </Section>
    <Section title={t("settings.vaultSets")} icon={<Database size={18} />} action={<button className="primary-button" onClick={() => setSecretDialog(true)}><Plus size={17} />{t("settings.newSecretSet")}</button>}><DataTable headers={[t("settings.name"), t("column.updated"), ""]} empty={t("settings.noSecretSets")}>{(secrets.data ?? []).map(item => <tr key={item.id}><td><span className="inline"><KeyRound size={14} /><strong>{item.name}</strong></span></td><td>{relative(item.updated_at)}</td><td><button className="icon-button danger" title={t("settings.deleteSecretSet")} onClick={async () => { if (!confirm(t("settings.confirmDelete", { name: item.name }))) return; await remove(`/secret-sets/${item.id}`); secrets.reload(); }}><X size={16} /></button></td></tr>)}</DataTable></Section>
    <Section title={t("settings.auditLog")} icon={<Clipboard size={18} />}><DataTable headers={[t("column.action"), t("column.resource"), t("column.address"), t("column.time")]} empty={t("settings.noAudit")}>{(audit.data ?? []).map(item => <tr key={item.id}><td><code>{item.action}</code></td><td>{item.resource_type}{item.resource_id ? ` · ${shortSHA(item.resource_id)}` : ""}</td><td><code>{item.ip_address}</code></td><td>{formatDate(item.occurred_at)}</td></tr>)}</DataTable></Section>
    <Dialog open={profileDialog} title={t(profileForm.id ? "settings.editProfile" : "settings.newProfile")} onClose={() => { if (!profileBusy) setProfileDialog(false); }} wide><form onSubmit={saveProfile}>
      <div className="segmented-control" role="tablist" aria-label={t("settings.type")}><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "codex"} aria-selected={profileForm.kind === "codex"} className={profileForm.kind === "codex" ? "active" : ""} onClick={() => changeProfileKind("codex")}><Code2 size={15} />{t("settings.codexType")}</button><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "git"} aria-selected={profileForm.kind === "git"} className={profileForm.kind === "git" ? "active" : ""} onClick={() => changeProfileKind("git")}><GitBranch size={15} />{t("settings.gitType")}</button></div>
      <div className="form-grid"><Field label={t("settings.name")}><input value={profileForm.name} onChange={e => setProfileForm({ ...profileForm, name: e.target.value })} required /></Field><Field label={t("settings.endpoint")}><input type="url" value={profileForm.endpoint} onChange={e => setProfileForm({ ...profileForm, endpoint: e.target.value })} required /></Field></div>
      {profileForm.kind === "codex" ? <Field label={t("server.codexModel")}><CodexModelPicker value={profileForm.model} onChange={model => setProfileForm({ ...profileForm, model })} required /></Field> : <><Field label={t("settings.gitUsername")}><input value={profileForm.username} onChange={e => setProfileForm({ ...profileForm, username: e.target.value })} autoComplete="username" required /></Field><div className="form-divider"><span>{t("settings.gitCommitIdentity")}</span></div><div className="form-grid"><Field label={t("settings.gitCommitName")}><input value={profileForm.commit_name} onChange={e => setProfileForm({ ...profileForm, commit_name: e.target.value })} autoComplete="name" required /></Field><Field label={t("settings.gitCommitEmail")}><input type="email" value={profileForm.commit_email} onChange={e => setProfileForm({ ...profileForm, commit_email: e.target.value })} autoComplete="email" placeholder={t("settings.gitCommitEmailPlaceholder")} required /></Field></div></>}
      <Field label={t(profileForm.kind === "codex" ? "server.codexAPIKey" : "settings.gitToken")}><input type="password" autoComplete="new-password" value={profileForm.secret} onChange={e => setProfileForm({ ...profileForm, secret: e.target.value })} placeholder={profileForm.id ? t("settings.keepExistingSecret") : ""} required={!profileForm.id} /></Field>
      <DialogActions><button type="button" className="secondary-button" disabled={profileBusy} onClick={() => setProfileDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={profileBusy}>{profileBusy ? <LoaderCircle className="spin" size={16} /> : <LockKeyhole size={16} />}{t("settings.encryptSave")}</button></DialogActions>
    </form></Dialog>
    <Dialog open={secretDialog} title={t("settings.secretSetTitle")} onClose={() => setSecretDialog(false)}><form onSubmit={submitSecretSet}><Field label={t("settings.name")}><input value={name} onChange={e => setName(e.target.value)} required /></Field><Field label={t("settings.environmentValues")}><textarea value={lines} onChange={e => setLines(e.target.value)} rows={8} placeholder={"DATABASE_URL=...\nAPI_TOKEN=..."} required /></Field><DialogActions><button type="button" className="secondary-button" onClick={() => setSecretDialog(false)}>{t("common.cancel")}</button><button className="primary-button"><KeyRound size={16} />{t("settings.encryptSave")}</button></DialogActions></form></Dialog>
  </div>;
}

type ChartPoint = { time: string; values: number[] };
type ChartSeries = { label: string; color: string; format: (value?: number) => string };
type NetworkRate = { time: string; rx: number; tx: number };

function MetricStat({ icon, label, value, detail, tone }: { icon: ReactNode; label: string; value: string; detail: string; tone: string }) {
  return <div className={`metric-stat ${tone}`}><span className="metric-stat-icon">{icon}</span><div><small>{label}</small><strong>{value}</strong><span>{detail}</span></div></div>;
}

function TimeSeriesChart({ title, data, series, axisFormat, empty, fixedMax, className = "" }: { title: string; data: ChartPoint[]; series: ChartSeries[]; axisFormat: (value?: number) => string; empty: string; fixedMax?: number; className?: string }) {
  const [hoverIndex, setHoverIndex] = useState<number | null>(null);
  const width = 640; const left = 48; const right = 626; const top = 12; const bottom = 164;
  const observedMax = Math.max(0, ...data.flatMap(point => point.values).filter(Number.isFinite));
  const maximum = fixedMax ?? niceMaximum(observedMax);
  const x = (index: number) => data.length <= 1 ? (left + right) / 2 : left + (index / (data.length - 1)) * (right - left);
  const y = (value: number) => bottom - (Math.max(0, Math.min(maximum, Number.isFinite(value) ? value : 0)) / maximum) * (bottom - top);
  const hovered = hoverIndex === null ? null : data[hoverIndex];
  const span = data.length > 1 ? new Date(data.at(-1)!.time).getTime() - new Date(data[0].time).getTime() : 0;
  const timeIndexes = data.length ? Array.from(new Set([0, Math.floor((data.length - 1) / 2), data.length - 1])) : [];
  const move = (event: ReactPointerEvent<SVGSVGElement>) => {
    if (!data.length) return;
    const bounds = event.currentTarget.getBoundingClientRect();
    const viewX = ((event.clientX - bounds.left) / bounds.width) * width;
    const ratio = Math.max(0, Math.min(1, (viewX - left) / (right - left)));
    setHoverIndex(Math.round(ratio * (data.length - 1)));
  };
  return <div className={`timeseries-chart ${className}`}>
    <div className="timeseries-heading"><strong>{title}</strong><div className="chart-legend">{series.map(item => <span key={item.label}><i style={{ background: item.color }} />{item.label}</span>)}</div></div>
    {data.length === 0 ? <div className="chart-empty">{empty}</div> : <div className="chart-canvas">
      <svg viewBox={`0 0 ${width} 190`} preserveAspectRatio="none" role="img" aria-label={title} onPointerMove={move} onPointerLeave={() => setHoverIndex(null)}>
        {[maximum, maximum / 2, 0].map((tick, index) => { const py = top + index * ((bottom - top) / 2); return <g key={tick}><line x1={left} y1={py} x2={right} y2={py} className="chart-gridline" /><text x={left - 8} y={py + 3} textAnchor="end" className="chart-axis-label">{axisFormat(tick)}</text></g>; })}
        {timeIndexes.map(index => <text x={x(index)} y="184" textAnchor={index === 0 ? "start" : index === data.length - 1 ? "end" : "middle"} className="chart-axis-label" key={index}>{formatAxisTime(data[index].time, span)}</text>)}
        {series.map((item, seriesIndex) => <polyline key={item.label} points={data.map((point, index) => `${x(index)},${y(point.values[seriesIndex] ?? 0)}`).join(" ")} fill="none" stroke={item.color} strokeWidth="2" vectorEffect="non-scaling-stroke" />)}
        {hovered && <g><line x1={x(hoverIndex!)} y1={top} x2={x(hoverIndex!)} y2={bottom} className="chart-cursor" />{series.map((item, seriesIndex) => <circle key={item.label} cx={x(hoverIndex!)} cy={y(hovered.values[seriesIndex] ?? 0)} r="4" fill={item.color} stroke="#fff" strokeWidth="2" vectorEffect="non-scaling-stroke" />)}</g>}
      </svg>
      {hovered && <div className="chart-tooltip" style={{ left: `${(x(hoverIndex!) / width) * 100}%`, transform: hoverIndex! > data.length * .7 ? "translateX(-100%)" : "translateX(0)" }}><strong>{formatDate(hovered.time)}</strong>{series.map((item, index) => <span key={item.label}><i style={{ background: item.color }} />{item.label}<b>{item.format(hovered.values[index])}</b></span>)}</div>}
    </div>}
  </div>;
}

function downsampleMetrics(points: Metric[], maximumPoints: number): Metric[] {
  if (points.length <= maximumPoints) return points;
  const size = Math.ceil(points.length / maximumPoints);
  const output: Metric[] = [];
  for (let start = 0; start < points.length; start += size) {
    const bucket = points.slice(start, start + size);
    const last = bucket[bucket.length - 1];
    const average = (read: (point: Metric) => number) => bucket.reduce((sum, point) => sum + read(point), 0) / bucket.length;
    output.push({ ...last, cpu_percent: average(point => point.cpu_percent), memory_percent: average(point => point.memory_percent), disk_percent: average(point => point.disk_percent), load_1: average(point => point.load_1) });
  }
  return output;
}

function networkRates(points: Metric[]): NetworkRate[] {
  return points.map((point, index) => {
    if (index === 0) return { time: point.bucket_at, rx: 0, tx: 0 };
    const previous = points[index - 1];
    const seconds = Math.max(1, (new Date(point.bucket_at).getTime() - new Date(previous.bucket_at).getTime()) / 1000);
    return { time: point.bucket_at, rx: point.net_rx_bytes >= previous.net_rx_bytes ? (point.net_rx_bytes - previous.net_rx_bytes) / seconds : 0, tx: point.net_tx_bytes >= previous.net_tx_bytes ? (point.net_tx_bytes - previous.net_tx_bytes) / seconds : 0 };
  });
}

function metricPeak<T>(values: T[] | null | undefined, read: (value: T) => number): number | undefined {
  return values?.length ? Math.max(...values.map(read).filter(Number.isFinite)) : undefined;
}

function niceMaximum(value: number) {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const padded = value * 1.1;
  const magnitude = 10 ** Math.floor(Math.log10(padded));
  return Math.max(1, Math.ceil((padded / magnitude) * 2) / 2 * magnitude);
}

function formatPercent(value?: number) { return Number.isFinite(value) ? `${value!.toFixed(1)}%` : "-"; }
function formatDecimal(value?: number) { return Number.isFinite(value) ? value!.toFixed(value! >= 10 ? 1 : 2) : "-"; }
function formatByteRate(value?: number) { if (!Number.isFinite(value)) return "-"; const units = ["B/s", "KB/s", "MB/s", "GB/s"]; let scaled = Math.max(0, value!); let unit = 0; while (scaled >= 1024 && unit < units.length - 1) { scaled /= 1024; unit++; } return `${scaled.toFixed(scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2)} ${units[unit]}`; }
function formatAxisTime(value: string, span: number) { return new Intl.DateTimeFormat(currentLocale(), span > 24 * 60 * 60 * 1000 ? { month: "short", day: "numeric", hour: "2-digit" } : { hour: "2-digit", minute: "2-digit" }).format(new Date(value)); }

function LanguageSwitch() {
  const { language, setLanguage, t } = useI18n();
  return <div className="language-switch" role="group" aria-label={t("auth.language")}><button aria-pressed={language === "zh-CN"} className={language === "zh-CN" ? "active" : ""} onClick={() => setLanguage("zh-CN")} type="button">中文</button><button aria-pressed={language === "en"} className={language === "en" ? "active" : ""} onClick={() => setLanguage("en")} type="button">EN</button></div>;
}

function Section({ title, icon, action, children }: { title: string; icon?: ReactNode; action?: ReactNode; children: ReactNode }) { return <section className="section"><div className="section-heading"><div>{icon}<h2>{title}</h2></div>{action}</div>{children}</section>; }
function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }
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
function ServerInformation({ server, className = "" }: { server?: Server; className?: string }) {
  const { t } = useI18n();
  if (!server || (!server.address && !server.configuration && !server.notes)) return <span className="muted">{t("server.noInformation")}</span>;
  return <div className={`server-information ${className}`}>{server.address && <span className="server-information-line" title={`${t("server.address")}: ${server.address}`}><MapPin size={13} /><code>{server.address}</code></span>}{server.configuration && <span className="server-information-line" title={`${t("server.configuration")}: ${server.configuration}`}><Settings size={13} /><span>{server.configuration}</span></span>}{server.notes && <span className="server-information-line" title={`${t("server.notes")}: ${server.notes}`}><StickyNote size={13} /><span>{server.notes}</span></span>}</div>;
}
function DialogActions({ children }: { children: ReactNode }) { return <div className="dialog-actions">{children}</div>; }
function Dialog({ open, title, onClose, children, wide = false, className = "" }: { open: boolean; title: string; onClose: () => void; children: ReactNode; wide?: boolean; className?: string }) { const { t } = useI18n(); if (!open) return null; return <div className="dialog-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target) onClose(); }}><div className={`dialog ${wide ? "wide" : ""} ${className}`.trim()} role="dialog" aria-modal="true" aria-label={title}><div className="dialog-heading"><h2>{title}</h2><button className="icon-button" onClick={onClose} title={t("common.close")}><X size={18} /></button></div>{children}</div></div>; }
function DataTable({ headers, empty, children }: { headers: string[]; empty: string; children: ReactNode }) { const count = Array.isArray(children) ? children.length : children ? 1 : 0; return <div className="table-wrap"><table><thead><tr>{headers.map(header => <th key={header}>{header}</th>)}</tr></thead><tbody>{count ? children : <tr><td colSpan={headers.length}><Empty icon={<Boxes size={22} />} text={empty} /></td></tr>}</tbody></table></div>; }
function Empty({ icon, text }: { icon: ReactNode; text: string }) { return <div className="empty">{icon}<span>{text}</span></div>; }
function Status({ value, icon }: { value: string; icon?: ReactNode }) { const { t } = useI18n(); const normalized = value.toLowerCase().replaceAll("_", "-"); const translated = t(`status.${normalized}`); return <span className={`status-tag ${normalized}`}>{icon}{translated.startsWith("status.") ? value.replaceAll("_", " ") : translated}</span>; }
function ErrorBanner({ text }: { text: string }) { return <div className="error-banner"><AlertTriangle size={16} />{text}</div>; }
function PageLoading() { return <div className="page-loading"><LoaderCircle className="spin" size={24} /></div>; }
function ErrorState({ error, reload }: { error: string; reload: () => void }) { const { t } = useI18n(); return <div className="error-state"><AlertTriangle size={25} /><strong>{t("error.load")}</strong><span>{error}</span><button className="secondary-button" onClick={reload}><RefreshCw size={16} />{t("common.retry")}</button></div>; }

interface PageProps { realtime: number; notify: (text: string) => void }
function useData<T>(path: string | null, dependency: unknown) {
  const [result, setResult] = useState<{ path: string | null; data: T | null }>({ path: null, data: null }); const [failure, setFailure] = useState<{ path: string | null; text: string }>({ path: null, text: "" }); const [settledPath, setSettledPath] = useState<string | null>(null); const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion(value => value + 1), []);
  useEffect(() => { if (!path) { setSettledPath(null); return; } const controller = new AbortController(); api<T>(path, { signal: controller.signal }).then(value => { setResult({ path, data: value }); setFailure({ path, text: "" }); }).catch(err => { if (err instanceof DOMException && err.name === "AbortError") return; setFailure({ path, text: message(err) }); }).finally(() => { if (!controller.signal.aborted) setSettledPath(path); }); return () => controller.abort(); }, [path, dependency, version]);
  const data = result.path === path ? result.data : null;
  const error = failure.path === path ? failure.text : "";
  return { data, error, loading: Boolean(path) && data === null && settledPath !== path, reload };
}

function message(error: unknown) { return error instanceof Error ? error.message : "Request failed"; }
function enrollmentMessage(error: unknown, translate: (key: string) => string) {
  if (error instanceof APIError && error.code) {
    const localized = translate(`server.error.${error.code}`);
    if (!localized.startsWith("server.error.")) return localized;
  }
  return message(error);
}
function pretty(value: unknown) { try { return JSON.stringify(value, null, 2); } catch { return String(value); } }
function approvalDetail(detail: unknown) { const value = asRecord(detail); if (!value) return pretty(detail); for (const key of ["command", "reason", "message", "question"]) if (typeof value[key] === "string" && value[key]) return value[key] as string; return pretty(detail); }
async function compressImage(file: File): Promise<string> {
  if (!new Set(["image/png", "image/jpeg", "image/webp"]).has(file.type)) throw new Error("Unsupported image type");
  const sourceURL = URL.createObjectURL(file);
  try {
    const image = document.createElement("img");
    image.src = sourceURL;
    await new Promise<void>((resolve, reject) => { image.onload = () => resolve(); image.onerror = () => reject(new Error("Could not read image")); });
    let maxDimension = 1600;
    let quality = 0.84;
    for (let attempt = 0; attempt < 6; attempt++) {
      const scale = Math.min(1, maxDimension / Math.max(image.naturalWidth, image.naturalHeight));
      const canvas = document.createElement("canvas");
      canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
      canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
      const context = canvas.getContext("2d");
      if (!context) throw new Error("Could not process image");
      context.drawImage(image, 0, 0, canvas.width, canvas.height);
      const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, "image/webp", quality));
      if (blob && blob.size <= 900 * 1024) return await blobDataURL(blob);
      maxDimension = Math.round(maxDimension * 0.82);
      quality = Math.max(0.62, quality - 0.05);
    }
    throw new Error("Image is too large");
  } finally { URL.revokeObjectURL(sourceURL); }
}
function blobDataURL(blob: Blob) { return new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = () => reject(new Error("Could not read image")); reader.readAsDataURL(blob); }); }
async function copyText(value: string) {
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
  const completedTypes = new Set(["agentMessage", "plan", "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch"]);
  const result: StreamEvent[] = [];
  for (const event of events) {
    if (event.kind === "user.message") {
      result.push(event);
      continue;
    }
    const payload = asRecord(event.payload);
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
    if (event.kind === "codex.turn.failed" || event.kind === "codex.interrupt.failed" || event.kind === "codex.approval.failed") result.push(event);
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
function shortSHA(value: string) { return value ? value.slice(0, 8) : "-"; }
function formatDate(value: string) { return new Intl.DateTimeFormat(currentLocale(), { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)); }
function formatTime(value: string) { return new Intl.DateTimeFormat(currentLocale(), { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value)); }
function relative(value: string) { const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000); const formatter = new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" }); const ranges: Array<[number, Intl.RelativeTimeFormatUnit]> = [[60, "second"], [60, "minute"], [24, "hour"], [7, "day"], [4.345, "week"], [12, "month"], [Infinity, "year"]]; let duration = seconds; for (const [amount, unit] of ranges) { if (Math.abs(duration) < amount) return formatter.format(Math.round(duration), unit); duration /= amount; } return formatDate(value); }
