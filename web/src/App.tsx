import { FormEvent, lazy, PointerEvent as ReactPointerEvent, ReactNode, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
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
  File as FileIcon,
  FileCode2,
  Folder,
  FolderOpen,
  FolderTree,
  GitBranch,
  Gauge,
  HardDrive,
  History,
  Image as ImageIcon,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LockKeyhole,
  LogOut,
  MapPin,
  Menu,
  MemoryStick,
  MessageSquare,
  MonitorDot,
  Paperclip,
  Pencil,
  Plus,
  RefreshCw,
  Rocket,
  Search,
  Server as ServerIcon,
  Settings,
  ShieldCheck,
  SquareTerminal,
  StickyNote,
  Trash2,
  Undo2,
  UserRound,
  Wrench,
  Wifi,
  WifiOff,
  X
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { api, APIError, patch, post, postStream, remove, setSession, socketURL } from "./api";
import { currentLocale, useI18n } from "./i18n";
import type {
  Alert,
  Approval,
  AuditEntry,
  CodexCLISettings,
  CredentialProfile,
  Deployment,
  DeploymentTarget,
  Metric,
  Project,
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
  WorkspaceFile,
  WorkspaceFilePreview,
  WorkspaceFilesSnapshot
} from "./types";

type View = "dashboard" | "servers" | "projects" | "codex" | "deployments" | "monitoring" | "settings";
type ConversationDisplayItem = { type: "event"; event: StreamEvent } | { type: "commandGroup"; events: StreamEvent[] };
type AuthState = "loading" | "setup" | "login" | "authenticated";
type InstallLogEntry = { step: string; status: "running" | "done" | "error"; current: number; total: number; detail: string };
type ComposerImage = { id: string; dataURL: string };
type FilePreviewSelection = { path: string; line?: number };
const HighlightedFile = lazy(() => import("./FilePreviewCode"));
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

export default function App() {
  const { t } = useI18n();
  const [auth, setAuth] = useState<AuthState>("loading");
  const [session, setCurrentSession] = useState<Session | null>(null);
  const [view, setView] = useState<View>("dashboard");
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

  if (auth === "loading") return <LoadingScreen />;
  if (auth === "setup") return <SetupScreen onReady={() => setAuth("login")} />;
  if (auth === "login") return <LoginScreen onLogin={authenticate} />;

  const logout = async () => {
    await api("/auth/logout", { method: "POST" });
    authenticate(null);
  };
  const selectView = (next: View) => { setView(next); setMobileNav(false); };
  const page = {
    dashboard: <Dashboard realtime={realtime} onNavigate={selectView} />,
    servers: <ServersPage realtime={realtime} notify={setToast} />,
    projects: <ProjectsPage realtime={realtime} notify={setToast} />,
    codex: <CodexPage realtime={realtime} streamRevisions={streamRevisions} approvals={approvals.data ?? []} approvalSignal={approvalSignal} reloadApprovals={approvals.reload} notify={setToast} />,
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
  const [mode, setMode] = useState<"clone" | "discover">("clone");
  const [busy, setBusy] = useState(false);
  const [projectAction, setProjectAction] = useState<{ id: string; kind: "retry" | "delete" } | null>(null);
  const [form, setForm] = useState({ name: "", remote_url: "", server_id: "", destination: "" });
  const close = () => { if (!busy) setDialog(false); };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      if (mode === "clone") {
        await post("/projects/import", form);
        notify(t("project.queued"));
        projects.reload();
      } else {
        await post("/projects/discover", { server_id: form.server_id });
        notify(t("project.scanQueued"));
      }
      setDialog(false);
    } catch (err) { notify(message(err)); } finally { setBusy(false); }
  };
  const retryImport = async (project: Project) => {
    setProjectAction({ id: project.id, kind: "retry" });
    try {
      await post(`/projects/${project.id}/retry-import`, {});
      projects.reload();
      notify(t("project.retryQueued"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const deleteFailedProject = async (project: Project) => {
    if (!confirm(t("project.confirmDelete", { name: project.name }))) return;
    setProjectAction({ id: project.id, kind: "delete" });
    try {
      await remove(`/projects/${project.id}`);
      projects.reload();
      notify(t("project.deleted"));
    } catch (err) { notify(message(err)); } finally { setProjectAction(null); }
  };
  const importState = (project: Project) => project.import_status === "queued" || project.import_status === "delivered" ? "importing" : project.import_status === "failed" ? "failed" : project.workspace_count > 0 ? "ready" : project.import_status === "succeeded" ? "syncing" : "pending";
  const importMessage = (project: Project) => /http2 framing|expected flush|timed? out|timeout|could not resolve|temporary failure in name resolution|connection (?:refused|reset)|network is unreachable|dial tcp/i.test(project.import_message) ? t("project.networkFailure") : project.import_message;
  const serverOptions = (servers.data ?? []).map(server => <option key={server.id} value={server.id} disabled={server.status !== "online"}>{server.name}{server.status !== "online" ? ` (${t("status.offline")})` : ""}</option>);
  return <div className="page-stack"><Section title={t("project.title")} icon={<GitBranch size={18} />} action={<button className="primary-button" onClick={() => setDialog(true)}><Plus size={17} />{t("project.import")}</button>}>
    <DataTable headers={[t("column.project"), t("column.remote"), t("column.workspaces"), t("project.importStatus"), t("column.updated"), t("common.actions")]} empty={t("project.none")}>{(projects.data ?? []).map(project => { const state = importState(project); const failed = state === "failed" && project.workspace_count === 0; const action = projectAction?.id === project.id ? projectAction.kind : null; const targetServer = (servers.data ?? []).find(server => server.id === project.import_server_id); const retryAvailable = targetServer?.status === "online"; return <tr key={project.id}><td><div className="cell-main"><strong>{project.name}</strong>{project.import_server_name && <small>{t("project.targetSummary", { server: project.import_server_name })}</small>}</div></td><td><code className="truncate-code">{project.remote_url || t("project.local")}</code></td><td>{project.workspace_count}</td><td><div className="project-import-state"><Status value={state} />{state === "syncing" && <small className="project-import-message syncing">{t("project.awaitingWorkspace")}</small>}{state === "failed" && importMessage(project) && <small className="project-import-message" title={project.import_message}>{importMessage(project)}</small>}</div></td><td>{relative(project.updated_at)}</td><td><div className="row-actions">{failed ? <><button className="icon-button" disabled={projectAction !== null || !retryAvailable} title={retryAvailable ? t("project.retryImport") : t("project.retryOffline")} onClick={() => void retryImport(project)}>{action === "retry" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button className="icon-button danger" disabled={projectAction !== null} title={t("project.deleteFailed")} onClick={() => void deleteFailedProject(project)}>{action === "delete" ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}</button></> : <span className="muted">-</span>}</div></td></tr>; })}</DataTable>
  </Section><Section title={t("project.workspaces")} icon={<Boxes size={18} />}><DataTable headers={[t("column.project"), t("column.server"), t("column.path"), t("column.branch"), t("column.commit"), t("column.state")]} empty={t("project.noWorkspaces")}>{(workspaces.data ?? []).map(workspace => <tr key={workspace.id}><td><strong>{workspace.project_name}</strong></td><td>{workspace.server_name}</td><td><code className="truncate-code">{workspace.path}</code></td><td><span className="inline"><GitBranch size={13} />{workspace.branch || t("project.detached")}</span></td><td><code>{shortSHA(workspace.commit_sha)}</code></td><td><Status value={workspace.dirty ? "dirty" : "clean"} /></td></tr>)}</DataTable></Section>
  <Dialog open={dialog} title={t("project.import")} onClose={close}><form onSubmit={submit}><div className="segmented-control" role="tablist" aria-label={t("project.importMode")}><button type="button" role="tab" aria-selected={mode === "clone"} className={mode === "clone" ? "active" : ""} onClick={() => setMode("clone")}><GitBranch size={15} />{t("project.cloneMode")}</button><button type="button" role="tab" aria-selected={mode === "discover"} className={mode === "discover" ? "active" : ""} onClick={() => setMode("discover")}><ServerIcon size={15} />{t("project.discoverMode")}</button></div>{mode === "clone" ? <><Field label={t("project.remoteURL")}><input value={form.remote_url} onChange={e => setForm({ ...form, remote_url: e.target.value })} placeholder="https://git.example.com/team/project.git" required /></Field><div className="form-grid"><Field label={t("project.name")}><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></Field><Field label={t("project.targetServer")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value })} required><option value="">{t("project.selectServer")}</option>{serverOptions}</select></Field></div><Field label={t("project.destination")}><input value={form.destination} onChange={e => setForm({ ...form, destination: e.target.value })} placeholder={t("common.optional")} /></Field></> : <Field label={t("project.existingServer")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value })} required><option value="">{t("project.selectServer")}</option>{serverOptions}</select></Field>}<DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={close}>{t("common.cancel")}</button><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : mode === "clone" ? <GitBranch size={16} /> : <Search size={16} />}{busy ? t("project.working") : mode === "clone" ? t("project.queue") : t("project.scan")}</button></DialogActions></form></Dialog></div>;
}

function CodexPage({ realtime, streamRevisions, approvals, approvalSignal, reloadApprovals, notify }: PageProps & { streamRevisions: StreamRevisions; approvals: Approval[]; approvalSignal: number; reloadApprovals: () => void }) {
  const { t } = useI18n();
  const threads = useData<Thread[]>("/threads", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const [selected, setSelected] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [approvalOpen, setApprovalOpen] = useState(approvals.length > 0);
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(new Set());
  const [deletingThread, setDeletingThread] = useState("");
  const [preview, setPreview] = useState<FilePreviewSelection | null>(null);
  const [activePane, setActivePane] = useState<"conversation" | "preview">("conversation");
  const active = selected ? (threads.data ?? []).find(thread => thread.id === selected) : threads.data?.[0];
  const threadGroups = useMemo(() => groupThreadsByProject(threads.data ?? []), [threads.data]);
  const approvalKey = approvals.map(item => item.id).join(",");
  useEffect(() => { if (!selected && threads.data?.[0]) setSelected(threads.data[0].id); }, [threads.data, selected]);
  useEffect(() => { if (approvalKey) setApprovalOpen(true); }, [approvalKey, approvalSignal]);
  useEffect(() => { setPreview(null); setActivePane("conversation"); }, [active?.workspace_id]);
  const toggleProject = (projectID: string) => setCollapsedProjects(current => { const next = new Set(current); if (next.has(projectID)) next.delete(projectID); else next.add(projectID); return next; });
  const openFile = (selection: FilePreviewSelection) => { setPreview(selection); setActivePane("preview"); };
  const deleteSession = async (thread: Thread) => {
    if (thread.status === "queued" || thread.status === "running") return;
    if (!confirm(t("codex.confirmDeleteSession", { title: thread.title }))) return;
    setDeletingThread(thread.id);
    try {
      await remove(`/threads/${thread.id}`);
      const next = (threads.data ?? []).find(item => item.id !== thread.id);
      if (active?.id === thread.id) setSelected(next?.id ?? "");
      threads.reload();
      notify(t("codex.sessionDeleted"));
    } catch (error) {
      notify(message(error));
    } finally {
      setDeletingThread("");
    }
  };
  return <div className="codex-layout">
    <aside className="codex-sidebar"><section className="thread-list"><div className="panel-heading"><div><Code2 size={18} /><h2>{t("codex.sessions")}</h2></div><button className="icon-button" title={t("codex.newSession")} onClick={() => setCreateOpen(true)}><Plus size={18} /></button></div><div className="thread-items">{threadGroups.length === 0 ? <Empty icon={<Code2 size={23} />} text={t("codex.noSessions")} /> : threadGroups.map(group => { const collapsed = collapsedProjects.has(group.projectID); return <section className="thread-project" key={group.projectID}><button type="button" className="thread-project-toggle" aria-expanded={!collapsed} title={t(collapsed ? "codex.expandProject" : "codex.collapseProject")} onClick={() => toggleProject(group.projectID)}>{collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong>{group.projectName}</strong><span>{group.threads.length}</span></button>{!collapsed && <div className="project-threads">{group.threads.map(thread => { const activeThread = thread.status === "queued" || thread.status === "running"; const deleting = deletingThread === thread.id; return <div key={thread.id} className={active?.id === thread.id ? "thread active" : "thread"}><button type="button" className="thread-select" onClick={() => setSelected(thread.id)}><span><strong>{thread.title}</strong><small>{thread.server_name}</small></span></button><div className="thread-actions"><Status value={thread.status} /><button type="button" className="icon-button danger thread-delete" disabled={activeThread || deleting} title={activeThread ? t("codex.deleteActiveSession") : t("codex.deleteSession")} onClick={() => void deleteSession(thread)}>{deleting ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}</button></div></div>; })}</div>}</section>; })}</div></section><WorkspaceFilesPanel workspaceID={active?.workspace_id ?? null} realtime={realtime} notify={notify} activePath={preview?.path ?? ""} onOpenFile={path => openFile({ path })} /></aside>
    <section className="session-area">{preview && <div className="session-pane-tabs" role="tablist" aria-label={t("codex.sessionViews")}><button type="button" role="tab" aria-selected={activePane === "conversation"} className={activePane === "conversation" ? "active" : ""} onClick={() => setActivePane("conversation")}><MessageSquare size={15} />{t("codex.conversation")}</button><button type="button" role="tab" aria-selected={activePane === "preview"} className={activePane === "preview" ? "active" : ""} onClick={() => setActivePane("preview")}><FileCode2 size={15} />{t("codex.filePreview")}</button></div>}<div className={`session-panes ${preview ? `has-preview ${activePane}-active` : ""}`}><section className="session-panel">{threads.error && !threads.data ? <ErrorState error={threads.error} reload={threads.reload} /> : active ? <SessionView key={active.id} thread={active} approvals={approvals.filter(item => item.thread_id === active.id)} realtime={`${streamRevisions["*"] ?? 0}:${streamRevisions[active.id] ?? 0}`} reloadApprovals={reloadApprovals} notify={notify} onOpenFile={openFile} /> : <Empty icon={<SquareTerminal size={28} />} text={t("codex.selectWorkspace")} />}</section>{active && preview && <FilePreviewPane workspaceID={active.workspace_id} selection={preview} realtime={realtime} onClose={() => { setPreview(null); setActivePane("conversation"); }} />}</div></section>
    <button className={`approval-drawer-button ${approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: approvals.length })}</button>
    <Dialog open={createOpen} title={t("codex.newSession")} onClose={() => setCreateOpen(false)}><CreateThread workspaces={workspaces.data ?? []} onCreated={thread => { setSelected(thread.id); setCreateOpen(false); threads.reload(); notify(t("codex.sessionCreated")); }} /></Dialog>
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : approvals.map(item => <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time>{relative(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={reloadApprovals} notify={notify} /></div>)}</div></Dialog>
  </div>;
}

function groupThreadsByProject(threads: Thread[]) {
  const groups = new Map<string, { projectID: string; projectName: string; threads: Thread[] }>();
  for (const thread of threads) {
    const group = groups.get(thread.project_id) ?? { projectID: thread.project_id, projectName: thread.project_name, threads: [] };
    group.threads.push(thread);
    groups.set(thread.project_id, group);
  }
  return Array.from(groups.values());
}

type FileTreeNode = { name: string; path: string; kind: WorkspaceFile["kind"]; size?: number; children: FileTreeNode[] };

function WorkspaceFilesPanel({ workspaceID, realtime, notify, activePath, onOpenFile }: { workspaceID: string | null; realtime: number; notify: (text: string) => void; activePath: string; onOpenFile: (path: string) => void }) {
  const { t } = useI18n();
  const snapshot = useData<WorkspaceFilesSnapshot>(workspaceID ? `/workspaces/${workspaceID}/files` : null, `${realtime}:${workspaceID}`);
  const [requestedWorkspace, setRequestedWorkspace] = useState("");
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const currentSnapshot = snapshot.data?.workspace_id === workspaceID ? snapshot.data : null;
  const scanning = currentSnapshot?.status === "scanning";
  const refresh = useCallback(async (silent = false) => {
    if (!workspaceID) return;
    try {
      await post(`/workspaces/${workspaceID}/files/refresh`, {});
      snapshot.reload();
      if (!silent) notify(t("codex.fileScanQueued"));
    } catch (error) {
      notify(message(error));
    }
  }, [notify, snapshot.reload, t, workspaceID]);
  useEffect(() => {
    setExpanded(new Set());
    setRequestedWorkspace("");
  }, [workspaceID]);
  useEffect(() => {
    if (workspaceID && currentSnapshot?.status === "idle" && requestedWorkspace !== workspaceID) {
      setRequestedWorkspace(workspaceID);
      void refresh(true);
    }
  }, [currentSnapshot?.status, refresh, requestedWorkspace, workspaceID]);
  const tree = useMemo(() => buildFileTree(currentSnapshot?.files ?? []), [currentSnapshot?.files]);
  const toggle = (path: string) => setExpanded(current => { const next = new Set(current); if (next.has(path)) next.delete(path); else next.add(path); return next; });
  return <section className="workspace-files"><div className="panel-heading"><div><FolderTree size={17} /><h2>{t("codex.projectFiles")}</h2></div><button className="icon-button" type="button" disabled={!workspaceID || scanning} title={t("codex.refreshFiles")} onClick={() => void refresh()}>{scanning ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div><div className="workspace-file-body">{!workspaceID ? <Empty icon={<FolderTree size={22} />} text={t("codex.selectWorkspace")} /> : !currentSnapshot ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : currentSnapshot.status === "failed" ? <div className="file-tree-error"><AlertTriangle size={16} /><span>{currentSnapshot.error || t("codex.fileScanFailed")}</span></div> : scanning && tree.length === 0 ? <div className="file-tree-state"><LoaderCircle className="spin" size={17} />{t("codex.scanningFiles")}</div> : tree.length === 0 ? <Empty icon={<Folder size={22} />} text={t("codex.noProjectFiles")} /> : <div className="file-tree">{tree.map(node => <FileTreeItem key={node.path} node={node} depth={0} expanded={expanded} onToggle={toggle} activePath={activePath} onOpenFile={onOpenFile} />)}{currentSnapshot.truncated && <div className="file-tree-note">{t("codex.fileListTruncated")}</div>}</div>}</div></section>;
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

function SessionView({ thread, approvals, realtime, reloadApprovals, notify, onOpenFile }: { thread: Thread; approvals: Approval[]; realtime: unknown; reloadApprovals: () => void; notify: (text: string) => void; onOpenFile: (selection: FilePreviewSelection) => void }) {
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
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [approvalMode, setApprovalMode] = useState("on-request");
  const streamRef = useRef<HTMLDivElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const sourceEvents = events.data ?? [];
  const chatEvents = useMemo(() => conversationEvents(sourceEvents), [sourceEvents]);
  const displayItems = useMemo(() => groupCommandEvents(chatEvents), [chatEvents]);
  const activeTurn = thread.status === "queued" || thread.status === "running";
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
    if ((!prompt.trim() && images.length === 0) || imageBusy || sending) return;
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
    <div className={`event-stream ${rawEvents ? "raw-stream" : "conversation-stream"}`} ref={streamRef} aria-live="polite">{events.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : events.error && !events.data ? <ErrorState error={events.error} reload={events.reload} /> : rawEvents ? sourceEvents.map(event => <RawEventItem key={event.event_id} event={event} />) : chatEvents.length === 0 && approvals.length === 0 && thread.status !== "running" ? <Empty icon={<Bot size={26} />} text={t("codex.noMessages")} /> : <>{displayItems.map(item => item.type === "commandGroup" ? <CommandEventGroup key={`commands:${item.events[0].event_id}`} events={item.events} /> : <ConversationEventItem key={item.event.event_id} event={item.event} onEdit={editMessage} notify={notify} workspaceRoot={thread.path} onOpenFile={onOpenFile} />)}{approvals.map(item => <ApprovalPrompt key={item.id} item={item} onDecided={reloadApprovals} notify={notify} />)}{thread.status === "running" && approvals.length === 0 && <WorkingIndicator />}</>}</div>
    <form className="composer" onSubmit={send}>
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} onClick={() => { setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length > 0 && <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" title={t("common.close")} onClick={() => setImages(current => current.filter(item => item.id !== image.id))}><X size={13} /></button></figure>)}</div>}
      <textarea ref={promptRef} value={prompt} onChange={event => setPrompt(event.target.value)} onPaste={event => { const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null); if (files.length) { event.preventDefault(); void addImages(files); } }} placeholder={t("codex.messagePlaceholder")} rows={3} />
      <div className="composer-bar"><div><input ref={imageInputRef} className="hidden-input" type="file" accept="image/png,image/jpeg,image/webp" multiple onChange={event => { const files = Array.from(event.target.files ?? []); event.target.value = ""; if (files.length) void addImages(files); }} /><button type="button" className="icon-button" disabled={imageBusy || images.length >= 4} title={t("codex.attachImage")} onClick={() => imageInputRef.current?.click()}>{imageBusy ? <LoaderCircle className="spin" size={16} /> : <Paperclip size={16} />}</button><select aria-label={t("codex.approveOnRequest")} value={approvalMode} onChange={event => setApprovalMode(event.target.value)}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select><CodexModelPicker value={model} onChange={setModel} allowServerDefault /><select aria-label={t("codex.reasoningEffort")} value={reasoningEffort} onChange={event => setReasoningEffort(event.target.value)}><option value="">{t("codex.reasoningDefault")}</option>{codexReasoningOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}</select></div><button className="primary-button" title={activeTurn ? t("codex.waitForTurn") : t("codex.send")} disabled={(!prompt.trim() && images.length === 0) || imageBusy || sending || activeTurn}>{sending ? <LoaderCircle className="spin" size={17} /> : <ChevronRight size={17} />}{t("codex.send")}</button></div>
    </form>
  </>;
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

function ConversationEventItem({ event, onEdit, notify, workspaceRoot, onOpenFile }: { event: StreamEvent; onEdit: (eventID: string, text: string) => void; notify: (text: string) => void; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const kind = event.kind;
  const payload = asRecord(event.payload);
  if (kind === "user.message") { const text = String(payload?.text ?? ""); const imageCount = Number(payload?.image_count ?? 0); const copyMessage = async () => { try { await copyText(text); notify(t("codex.messageCopied")); } catch (error) { notify(message(error)); } }; return <article className="message user"><header><UserRound size={15} /><strong>{t("codex.you")}</strong><time>{formatTime(event.occurred_at)}</time></header>{text && <MarkdownContent text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} />}{imageCount > 0 && <span className="message-image-count"><ImageIcon size={14} />{imageCount}</span>}<div className="message-actions"><button type="button" className="message-action" disabled={!text} title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => void copyMessage()}><Copy size={14} /></button><button type="button" className="message-action" disabled={!text} title={t("codex.editMessage")} aria-label={t("codex.editMessage")} onClick={() => onEdit(event.event_id, text)}><Pencil size={14} /></button></div></article>; }
  if (kind === "codex.error" || kind === "codex.turn.failed" || kind === "codex.interrupt.failed" || kind === "codex.approval.failed") return <article className="message error"><header><AlertTriangle size={15} /><strong>{t(kind === "codex.turn.failed" || kind === "codex.error" ? "codex.turnFailed" : "codex.actionFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(payload) || t("codex.unknownError")}</div></article>;
  if (kind === "codex.turn.completed") {
    const turn = asRecord(payload?.turn);
    const status = String(turn?.status ?? "failed");
    if (status === "interrupted") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
    return <article className="message error"><header><AlertTriangle size={15} /><strong>{t("codex.turnFailed")}</strong><time>{formatTime(event.occurred_at)}</time></header><div className="message-content">{errorText(turn) || t("codex.unknownError")}</div></article>;
  }
  const item = asRecord(payload?.item);
  if (item?.type === "agentMessage" || item?.type === "plan") return <article className="message assistant"><header><Bot size={15} /><strong>Codex</strong><time>{formatTime(event.occurred_at)}</time></header><MarkdownContent text={extractText(item)} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} /></article>;
  return <ToolEvent event={event} item={item} />;
}

function MarkdownContent({ text, workspaceRoot, onOpenFile }: { text: string; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  return <div className="message-content markdown-content"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ({ href, children, node: _node, ...props }) => { const selection = workspaceFileLink(href, workspaceRoot); if (selection) return <a {...props} href={href} onClick={event => { event.preventDefault(); onOpenFile(selection); }}>{children}</a>; if (isExternalLink(href)) return <a {...props} href={href} target="_blank" rel="noreferrer">{children}</a>; if (href?.startsWith("#")) return <a {...props} href={href}>{children}</a>; return <a {...props} className="unavailable-link" href={href} aria-disabled="true" title={t("codex.linkUnavailable")} onClick={event => event.preventDefault()}>{children}</a>; } }}>{text}</ReactMarkdown></div>;
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

function DeploymentsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const targets = useData<DeploymentTarget[]>("/deployment-targets", realtime);
  const deployments = useData<Deployment[]>("/deployments", realtime);
  const projects = useData<Project[]>("/projects", realtime);
  const servers = useData<Server[]>("/servers", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const [dialog, setDialog] = useState(false);
  const [form, setForm] = useState({ project_id: "", server_id: "", secret_set_id: "", environment: "production", repository: "", git_ref: "main", compose_file: "compose.yaml", working_dir: "", build_mode: "build", release_root: "/var/lib/wio-agent/releases", health: "" });
  const submit = async (event: FormEvent) => { event.preventDefault(); try { await post("/deployment-targets", { ...form, health_checks: form.health ? [{ type: form.health.startsWith("http") ? "http" : "tcp", address: form.health, timeout_seconds: 60 }] : [] }); setDialog(false); targets.reload(); notify(t("deployment.targetCreated")); } catch (err) { notify(message(err)); } };
  return <div className="page-stack"><Section title={t("deployment.targets")} icon={<Rocket size={18} />} action={<button className="primary-button" onClick={() => setDialog(true)}><Plus size={17} />{t("deployment.newTarget")}</button>}><DataTable headers={[t("column.project"), t("column.environment"), t("column.server"), t("column.gitRef"), t("column.compose"), t("common.actions")]} empty={t("deployment.noTargets")}>{(targets.data ?? []).map(target => <tr key={target.id}><td><strong>{target.project_name}</strong></td><td><Status value={target.environment} /></td><td>{target.server_name}</td><td><code>{target.git_ref}</code></td><td><code>{target.compose_file}</code></td><td><div className="row-actions"><button className="primary-button small" onClick={async () => { try { await post(`/deployment-targets/${target.id}/deploy`, { commit_ref: target.git_ref }); deployments.reload(); notify(t("deployment.queued")); } catch (err) { notify(message(err)); } }}><Rocket size={14} />{t("deployment.deploy")}</button><button className="icon-button" title={t("deployment.rollback")} onClick={async () => { if (!confirm(t("deployment.confirmRollback", { project: target.project_name, environment: target.environment }))) return; await post(`/deployment-targets/${target.id}/rollback`, {}); deployments.reload(); notify(t("deployment.rollbackQueued")); }}><Undo2 size={15} /></button></div></td></tr>)}</DataTable></Section><Section title={t("deployment.history")} icon={<History size={18} />}><DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("column.message"), t("column.created")]} empty={t("deployment.noHistory")}>{(deployments.data ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td className="message-cell">{item.message || "-"}</td><td>{relative(item.created_at)}</td></tr>)}</DataTable></Section>
  <Dialog open={dialog} title={t("deployment.targetTitle")} onClose={() => setDialog(false)} wide><form onSubmit={submit}><div className="form-grid"><Field label={t("column.project")}><select value={form.project_id} onChange={e => { const project = projects.data?.find(item => item.id === e.target.value); setForm({ ...form, project_id: e.target.value, repository: project?.remote_url || form.repository }); }} required><option value="">{t("deployment.selectProject")}</option>{(projects.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Field label={t("column.server")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value })} required><option value="">{t("deployment.selectServer")}</option>{(servers.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Field label={t("column.environment")}><input value={form.environment} onChange={e => setForm({ ...form, environment: e.target.value })} required /></Field><Field label={t("deployment.secretSet")}><select value={form.secret_set_id} onChange={e => setForm({ ...form, secret_set_id: e.target.value })}><option value="">{t("common.none")}</option>{(secrets.data ?? []).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field></div><Field label={t("deployment.repository")}><input value={form.repository} onChange={e => setForm({ ...form, repository: e.target.value })} required /></Field><div className="form-grid thirds"><Field label={t("column.gitRef")}><input value={form.git_ref} onChange={e => setForm({ ...form, git_ref: e.target.value })} /></Field><Field label={t("deployment.composeFile")}><input value={form.compose_file} onChange={e => setForm({ ...form, compose_file: e.target.value })} /></Field><Field label={t("deployment.buildMode")}><select value={form.build_mode} onChange={e => setForm({ ...form, build_mode: e.target.value })}><option value="build">{t("deployment.build")}</option><option value="pull">{t("deployment.pull")}</option></select></Field></div><div className="form-grid"><Field label={t("deployment.workingDirectory")}><input value={form.working_dir} onChange={e => setForm({ ...form, working_dir: e.target.value })} placeholder={t("deployment.repositoryRoot")} /></Field><Field label={t("deployment.healthCheck")}><input value={form.health} onChange={e => setForm({ ...form, health: e.target.value })} placeholder={t("deployment.healthPlaceholder")} /></Field></div><Field label={t("deployment.releaseRoot")}><input value={form.release_root} onChange={e => setForm({ ...form, release_root: e.target.value })} /></Field><DialogActions><button type="button" className="secondary-button" onClick={() => setDialog(false)}>{t("common.cancel")}</button><button className="primary-button"><Rocket size={16} />{t("deployment.createTarget")}</button></DialogActions></form></Dialog></div>;
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
function CodexModelPicker({ value, onChange, allowServerDefault = false, required = false }: { value: string; onChange: (value: string) => void; allowServerDefault?: boolean; required?: boolean }) {
  const { t } = useI18n();
  const known = value === "" || codexModelOptions.some(option => option.value === value);
  const [customMode, setCustomMode] = useState(!known);
  const [customValue, setCustomValue] = useState(known ? "" : value);
  const selectValue = customMode ? "__custom__" : value;
  return <div className="codex-model-picker"><select aria-label={t("codex.modelOverride")} value={selectValue} required={required} onChange={event => { if (event.target.value === "__custom__") { setCustomMode(true); setCustomValue(""); onChange(""); } else { setCustomMode(false); onChange(event.target.value); } }}>{allowServerDefault && <option value="">{t("codex.modelServerDefault")}</option>}{codexModelOptions.map(option => <option value={option.value} key={option.value}>{t(option.labelKey)}</option>)}<option value="__custom__">{t("codex.modelCustom")}</option></select>{customMode && <input aria-label={t("codex.customModelName")} value={customValue} onChange={event => { setCustomValue(event.target.value); onChange(event.target.value); }} placeholder={t("codex.customModelPlaceholder")} required={required} />}</div>;
}
function ServerInformation({ server, className = "" }: { server?: Server; className?: string }) {
  const { t } = useI18n();
  if (!server || (!server.address && !server.configuration && !server.notes)) return <span className="muted">{t("server.noInformation")}</span>;
  return <div className={`server-information ${className}`}>{server.address && <span className="server-information-line" title={`${t("server.address")}: ${server.address}`}><MapPin size={13} /><code>{server.address}</code></span>}{server.configuration && <span className="server-information-line" title={`${t("server.configuration")}: ${server.configuration}`}><Settings size={13} /><span>{server.configuration}</span></span>}{server.notes && <span className="server-information-line" title={`${t("server.notes")}: ${server.notes}`}><StickyNote size={13} /><span>{server.notes}</span></span>}</div>;
}
function DialogActions({ children }: { children: ReactNode }) { return <div className="dialog-actions">{children}</div>; }
function Dialog({ open, title, onClose, children, wide = false }: { open: boolean; title: string; onClose: () => void; children: ReactNode; wide?: boolean }) { const { t } = useI18n(); if (!open) return null; return <div className="dialog-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target) onClose(); }}><div className={`dialog ${wide ? "wide" : ""}`} role="dialog" aria-modal="true" aria-label={title}><div className="dialog-heading"><h2>{title}</h2><button className="icon-button" onClick={onClose} title={t("common.close")}><X size={18} /></button></div>{children}</div></div>; }
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
  return events.filter(event => {
    if (event.kind === "user.message" || event.kind === "codex.turn.failed" || event.kind === "codex.interrupt.failed" || event.kind === "codex.approval.failed") return true;
    const payload = asRecord(event.payload);
    if (event.kind === "codex.error") return payload?.willRetry !== true;
    if (event.kind === "codex.turn.completed") {
      const turn = asRecord(payload?.turn);
      return turn?.status === "failed" || turn?.status === "interrupted";
    }
    if (event.kind !== "codex.item.completed") return false;
    const item = asRecord(payload?.item);
    return completedTypes.has(String(item?.type ?? ""));
  });
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
