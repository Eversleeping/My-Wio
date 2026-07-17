import { FormEvent, PointerEvent as ReactPointerEvent, ReactNode, useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  ArrowDownToLine,
  ArrowUpFromLine,
  Ban,
  BellRing,
  Boxes,
  Check,
  ChevronRight,
  Clipboard,
  Code2,
  Copy,
  Cpu,
  Database,
  GitBranch,
  Gauge,
  HardDrive,
  History,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LockKeyhole,
  LogOut,
  MapPin,
  Menu,
  MemoryStick,
  MonitorDot,
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
  Wifi,
  WifiOff,
  X
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { api, APIError, patch, post, postStream, remove, setSession, socketURL } from "./api";
import { currentLocale, useI18n } from "./i18n";
import type {
  Alert,
  Approval,
  AuditEntry,
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
  Workspace
} from "./types";

type View = "dashboard" | "servers" | "projects" | "codex" | "deployments" | "monitoring" | "settings";
type AuthState = "loading" | "setup" | "login" | "authenticated";
type InstallLogEntry = { step: string; status: "running" | "done" | "error"; current: number; total: number; detail: string };

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
    let stopped = false;
    const connect = () => {
      if (stopped) return;
      socket = new WebSocket(socketURL());
      socket.onmessage = () => setRealtime(value => value + 1);
      socket.onclose = () => { timer = window.setTimeout(connect, 2500); };
    };
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
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
    codex: <CodexPage realtime={realtime} approvals={approvals.data ?? []} notify={setToast} />,
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
          <div className="topbar-actions"><LanguageSwitch /><span className="connection"><Wifi size={15} /> {t("app.live")}</span>{(approvals.data?.length ?? 0) > 0 && <button className="approval-pill" onClick={() => selectView("codex")}><ShieldCheck size={15} />{t("codex.approvalCount", { count: approvals.data?.length ?? 0 })}</button>}</div>
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
  const [password, setPassword] = useState("");
  const [result, setResult] = useState<{ totp_uri: string; totp_secret: string; recovery_codes: string[] } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try { setResult(await post("/setup", { username, password })); } catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  return <div className="auth-layout">
    <section className="auth-panel">
      <div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong><LanguageSwitch /></div>
      {!result ? <form onSubmit={submit}>
        <div className="section-heading"><h1>{t("auth.createAdmin")}</h1><span className="status-tag neutral"><LockKeyhole size={14} />{t("auth.singleAdmin")}</span></div>
        <Field label={t("auth.username")}><input value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" required /></Field>
        <Field label={t("auth.password")}><input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="new-password" minLength={12} required /></Field>
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
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try { onLogin(await post<Session>("/auth/login", { username, password, code })); } catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  return <div className="auth-layout"><section className="auth-panel compact">
    <div className="auth-brand"><span className="brand-mark">W</span><strong>{t("app.name")}</strong><LanguageSwitch /></div>
    <form onSubmit={submit}><h1>{t("auth.signIn")}</h1>
      <Field label={t("auth.username")}><input value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" required /></Field>
      <Field label={t("auth.password")}><input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="current-password" required /></Field>
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
  const [metadataForm, setMetadataForm] = useState({ address: "", configuration: "", notes: "" });
  const [form, setForm] = useState({
    name: "", roots: "/srv, /opt, /home", host: "", port: "22", user: "root", authMethod: "private_key",
    password: "", privateKey: "", privateKeyPassphrase: "", configuration: "", notes: "", codexAPIURL: "https://api.openai.com/v1", codexAPIKey: "", codexModel: "gpt-5.4"
  });
  const reset = () => {
    setStep("form"); setError(""); setHostKey(null); setResult(null); setInstallLogs([]); setBusy(false);
    setForm({ name: "", roots: "/srv, /opt, /home", host: "", port: "22", user: "root", authMethod: "private_key", password: "", privateKey: "", privateKeyPassphrase: "", configuration: "", notes: "", codexAPIURL: "https://api.openai.com/v1", codexAPIKey: "", codexModel: "gpt-5.4" });
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
        codex_api_url: form.codexAPIURL.trim(), codex_api_key: form.codexAPIKey, codex_model: form.codexModel.trim()
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
      setForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "", codexAPIKey: "" }));
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
  const updateAgent = async (server: Server) => {
    if (!server.agent_update_available || !confirm(t("server.confirmUpdate", { name: server.name, version: server.agent_target_version }))) return;
    setUpdatingServer(server.id);
    try {
      await post(`/servers/${server.id}/agent-update`, {});
      notify(t("server.updateQueued", { version: server.agent_target_version }));
    } catch (err) { notify(message(err)); } finally { setUpdatingServer(""); }
  };
  return <div className="page-stack"><Section title={t("server.registered")} icon={<ServerIcon size={18} />} action={<button className="primary-button" onClick={open}><Plus size={17} />{t("server.enroll")}</button>}>
    <DataTable headers={[t("column.server"), t("server.information"), t("column.connectivity"), t("column.agent"), t("column.codex"), t("column.lastSeen"), ""]} empty={t("server.none")}>{(servers.data ?? []).map(server => { const updateTitle = server.status !== "online" ? t("server.updateOffline") : server.agent_update_available ? t("server.updateAgent", { version: server.agent_target_version }) : !server.agent_version ? t("common.awaitingHeartbeat") : !server.agent_update_supported ? t("server.updateRequiresReinstall") : server.agent_version === server.agent_target_version ? t("server.agentLatest") : t("server.updateUnavailable"); return <tr key={server.id}><td><div className="cell-main"><strong>{server.name}</strong><small>{server.hostname || t("common.awaitingHeartbeat")}</small></div></td><td><ServerInformation server={server} /></td><td><Status value={server.status} icon={server.status === "online" ? <Wifi size={13} /> : <WifiOff size={13} />} /></td><td><code>{server.agent_version || "-"}</code></td><td><span className={server.codex_ready ? "inline-success" : "muted"}>{server.codex_ready ? <Check size={14} /> : <Ban size={14} />}{server.codex_version || t("common.unavailable")}</span></td><td>{server.last_seen_at ? relative(server.last_seen_at) : t("common.never")}</td><td><div className="row-actions"><button className="icon-button" disabled={server.status !== "online" || !server.agent_update_available || updatingServer !== ""} title={updateTitle} onClick={() => void updateAgent(server)}><RefreshCw className={updatingServer === server.id ? "spin" : ""} size={15} /></button><button className="icon-button" title={t("server.editInformation")} onClick={() => editServer(server)}><Pencil size={15} /></button><button className="icon-button danger" title={t("server.revoke")} onClick={async () => { if (!confirm(t("server.confirmRevoke", { name: server.name }))) return; await remove(`/servers/${server.id}`); notify(t("server.revoked")); servers.reload(); }}><X size={16} /></button></div></td></tr>; })}</DataTable>
  </Section><Dialog open={dialog} title={t("server.enrollLinux")} onClose={close} wide>{step === "form" ? <form onSubmit={probe}>
    {error && <ErrorBanner text={error} />}
    <div className="form-grid"><Field label={t("server.name")}><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required /></Field><Field label={t("server.scanRoots")}><input value={form.roots} onChange={e => setForm({ ...form, roots: e.target.value })} required /></Field></div>
    <div className="form-grid"><Field label={t("server.configuration")}><textarea rows={3} maxLength={4096} value={form.configuration} onChange={e => setForm({ ...form, configuration: e.target.value })} placeholder={t("server.configurationPlaceholder")} /></Field><Field label={t("server.notes")}><textarea rows={3} maxLength={4096} value={form.notes} onChange={e => setForm({ ...form, notes: e.target.value })} placeholder={t("server.notesPlaceholder")} /></Field></div>
    <div className="form-grid thirds"><Field label={t("server.sshHost")}><input value={form.host} onChange={e => setForm({ ...form, host: e.target.value })} placeholder="192.0.2.10" required /></Field><Field label={t("server.sshPort")}><input type="number" min="1" max="65535" value={form.port} onChange={e => setForm({ ...form, port: e.target.value })} required /></Field><Field label={t("server.sshUser")}><input value={form.user} onChange={e => setForm({ ...form, user: e.target.value })} placeholder="root / ubuntu / ec2-user" required /></Field></div>
    <Field label={t("server.authMethod")}><select value={form.authMethod} onChange={e => setForm({ ...form, authMethod: e.target.value })}><option value="private_key">{t("server.authPrivateKey")}</option><option value="password">{t("server.authPassword")}</option></select></Field>
    {form.authMethod === "private_key" ? <div className="form-grid"><Field label={t("server.privateKeyFile")}><input type="file" accept=".pem,.key,text/plain" onChange={e => void choosePrivateKey(e.target.files?.[0])} required={!form.privateKey} /></Field><Field label={t("server.privateKeyPassphrase")}><input type="password" autoComplete="off" value={form.privateKeyPassphrase} onChange={e => setForm({ ...form, privateKeyPassphrase: e.target.value })} placeholder={t("common.optional")} /></Field></div> : <Field label={t("server.sshPassword")}><input type="password" autoComplete="new-password" value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} required /></Field>}
    <div className="form-divider"><span>{t("server.codexAPI")}</span></div>
    <div className="form-grid"><Field label={t("server.codexAPIURL")}><input type="url" value={form.codexAPIURL} onChange={e => setForm({ ...form, codexAPIURL: e.target.value })} required /></Field><Field label={t("server.codexModel")}><input value={form.codexModel} onChange={e => setForm({ ...form, codexModel: e.target.value })} required /></Field></div>
    <Field label={t("server.codexAPIKey")}><input type="password" autoComplete="new-password" value={form.codexAPIKey} onChange={e => setForm({ ...form, codexAPIKey: e.target.value })} required /></Field>
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
  const importState = (project: Project) => project.import_status === "queued" || project.import_status === "delivered" ? "importing" : project.import_status === "failed" ? "failed" : project.workspace_count > 0 || project.import_status === "succeeded" ? "ready" : "pending";
  const importMessage = (project: Project) => /http2 framing|expected flush|timed? out|timeout|could not resolve|temporary failure in name resolution|connection (?:refused|reset)|network is unreachable|dial tcp/i.test(project.import_message) ? t("project.networkFailure") : project.import_message;
  const serverOptions = (servers.data ?? []).map(server => <option key={server.id} value={server.id} disabled={server.status !== "online"}>{server.name}{server.status !== "online" ? ` (${t("status.offline")})` : ""}</option>);
  return <div className="page-stack"><Section title={t("project.title")} icon={<GitBranch size={18} />} action={<button className="primary-button" onClick={() => setDialog(true)}><Plus size={17} />{t("project.import")}</button>}>
    <DataTable headers={[t("column.project"), t("column.remote"), t("column.workspaces"), t("project.importStatus"), t("column.updated"), t("common.actions")]} empty={t("project.none")}>{(projects.data ?? []).map(project => { const state = importState(project); const failed = state === "failed" && project.workspace_count === 0; const action = projectAction?.id === project.id ? projectAction.kind : null; const targetServer = (servers.data ?? []).find(server => server.id === project.import_server_id); const retryAvailable = targetServer?.status === "online"; return <tr key={project.id}><td><div className="cell-main"><strong>{project.name}</strong>{project.import_server_name && <small>{t("project.targetSummary", { server: project.import_server_name })}</small>}</div></td><td><code className="truncate-code">{project.remote_url || t("project.local")}</code></td><td>{project.workspace_count}</td><td><div className="project-import-state"><Status value={state} />{state === "failed" && importMessage(project) && <small className="project-import-message" title={project.import_message}>{importMessage(project)}</small>}</div></td><td>{relative(project.updated_at)}</td><td><div className="row-actions">{failed ? <><button className="icon-button" disabled={projectAction !== null || !retryAvailable} title={retryAvailable ? t("project.retryImport") : t("project.retryOffline")} onClick={() => void retryImport(project)}>{action === "retry" ? <LoaderCircle className="spin" size={15} /> : <RefreshCw size={15} />}</button><button className="icon-button danger" disabled={projectAction !== null} title={t("project.deleteFailed")} onClick={() => void deleteFailedProject(project)}>{action === "delete" ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}</button></> : <span className="muted">-</span>}</div></td></tr>; })}</DataTable>
  </Section><Section title={t("project.workspaces")} icon={<Boxes size={18} />}><DataTable headers={[t("column.project"), t("column.server"), t("column.path"), t("column.branch"), t("column.commit"), t("column.state")]} empty={t("project.noWorkspaces")}>{(workspaces.data ?? []).map(workspace => <tr key={workspace.id}><td><strong>{workspace.project_name}</strong></td><td>{workspace.server_name}</td><td><code className="truncate-code">{workspace.path}</code></td><td><span className="inline"><GitBranch size={13} />{workspace.branch || t("project.detached")}</span></td><td><code>{shortSHA(workspace.commit_sha)}</code></td><td><Status value={workspace.dirty ? "dirty" : "clean"} /></td></tr>)}</DataTable></Section>
  <Dialog open={dialog} title={t("project.import")} onClose={close}><form onSubmit={submit}><div className="segmented-control" role="tablist" aria-label={t("project.importMode")}><button type="button" role="tab" aria-selected={mode === "clone"} className={mode === "clone" ? "active" : ""} onClick={() => setMode("clone")}><GitBranch size={15} />{t("project.cloneMode")}</button><button type="button" role="tab" aria-selected={mode === "discover"} className={mode === "discover" ? "active" : ""} onClick={() => setMode("discover")}><ServerIcon size={15} />{t("project.discoverMode")}</button></div>{mode === "clone" ? <><Field label={t("project.remoteURL")}><input value={form.remote_url} onChange={e => setForm({ ...form, remote_url: e.target.value })} placeholder="https://git.example.com/team/project.git" required /></Field><div className="form-grid"><Field label={t("project.name")}><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></Field><Field label={t("project.targetServer")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value })} required><option value="">{t("project.selectServer")}</option>{serverOptions}</select></Field></div><Field label={t("project.destination")}><input value={form.destination} onChange={e => setForm({ ...form, destination: e.target.value })} placeholder={t("common.optional")} /></Field></> : <Field label={t("project.existingServer")}><select value={form.server_id} onChange={e => setForm({ ...form, server_id: e.target.value })} required><option value="">{t("project.selectServer")}</option>{serverOptions}</select></Field>}<DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={close}>{t("common.cancel")}</button><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : mode === "clone" ? <GitBranch size={16} /> : <Search size={16} />}{busy ? t("project.working") : mode === "clone" ? t("project.queue") : t("project.scan")}</button></DialogActions></form></Dialog></div>;
}

function CodexPage({ realtime, approvals, notify }: PageProps & { approvals: Approval[] }) {
  const { t } = useI18n();
  const threads = useData<Thread[]>("/threads", realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const [selected, setSelected] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [approvalOpen, setApprovalOpen] = useState(approvals.length > 0);
  const active = (threads.data ?? []).find(thread => thread.id === selected) ?? threads.data?.[0];
  useEffect(() => { if (!selected && threads.data?.[0]) setSelected(threads.data[0].id); }, [threads.data, selected]);
  useEffect(() => { if (approvals.length) setApprovalOpen(true); }, [approvals.length]);
  return <div className="codex-layout">
    <section className="thread-list"><div className="panel-heading"><div><Code2 size={18} /><h2>{t("codex.sessions")}</h2></div><button className="icon-button" title={t("codex.newSession")} onClick={() => setCreateOpen(true)}><Plus size={18} /></button></div>{(threads.data ?? []).length === 0 ? <Empty icon={<Code2 size={23} />} text={t("codex.noSessions")} /> : (threads.data ?? []).map(thread => <button key={thread.id} className={active?.id === thread.id ? "thread active" : "thread"} onClick={() => setSelected(thread.id)}><span><strong>{thread.title}</strong><small>{thread.project_name} · {thread.server_name}</small></span><Status value={thread.status} /></button>)}</section>
    <section className="session-panel">{active ? <SessionView thread={active} realtime={realtime} notify={notify} /> : <Empty icon={<SquareTerminal size={28} />} text={t("codex.selectWorkspace")} />}</section>
    <button className={`approval-drawer-button ${approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: approvals.length })}</button>
    <Dialog open={createOpen} title={t("codex.newSession")} onClose={() => setCreateOpen(false)}><CreateThread workspaces={workspaces.data ?? []} onCreated={() => { setCreateOpen(false); threads.reload(); notify(t("codex.sessionCreated")); }} /></Dialog>
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : approvals.map(item => <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time>{relative(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{pretty(item.detail)}</pre><div className="approval-actions"><button className="secondary-button danger" onClick={async () => { await post(`/approvals/${item.id}/decision`, { decision: "denied" }); notify(t("codex.approvalDenied")); }}><Ban size={16} />{t("codex.deny")}</button><button className="primary-button" onClick={async () => { await post(`/approvals/${item.id}/decision`, { decision: "approved" }); notify(t("codex.approvalGranted")); }}><Check size={16} />{t("codex.approveOnce")}</button></div></div>)}</div></Dialog>
  </div>;
}

function CreateThread({ workspaces, onCreated }: { workspaces: Workspace[]; onCreated: () => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState(""); const [title, setTitle] = useState("");
  return <form onSubmit={async e => { e.preventDefault(); await post("/threads", { workspace_id: workspaceID, title }); onCreated(); }}><Field label={t("codex.workspace")}><select value={workspaceID} onChange={e => setWorkspaceID(e.target.value)} required><option value="">{t("codex.selectWorkspaceOption")}</option>{workspaces.map(workspace => <option value={workspace.id} key={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field><Field label={t("codex.sessionTitle")}><input value={title} onChange={e => setTitle(e.target.value)} placeholder={t("codex.newSession")} /></Field><DialogActions><button className="primary-button"><Plus size={16} />{t("codex.createSession")}</button></DialogActions></form>;
}

function SessionView({ thread, realtime, notify }: { thread: Thread; realtime: number; notify: (text: string) => void }) {
  const { t } = useI18n();
  const events = useData<StreamEvent[]>(`/threads/${thread.id}/events`, realtime + thread.id);
  const [prompt, setPrompt] = useState(""); const [model, setModel] = useState(""); const [approvalMode, setApprovalMode] = useState("on-request");
  const send = async (event: FormEvent) => { event.preventDefault(); if (!prompt.trim()) return; try { await post(`/threads/${thread.id}/turns`, { prompt, model, approval_mode: approvalMode }); setPrompt(""); notify(t("codex.turnQueued")); } catch (err) { notify(message(err)); } };
  return <><div className="session-header"><div><h2>{thread.title}</h2><span><GitBranch size={13} />{thread.project_name}<i /> <ServerIcon size={13} />{thread.server_name}</span></div><div className="session-actions"><Status value={thread.status} />{thread.status === "running" && <button className="icon-button danger" title={t("codex.interrupt")} onClick={async () => { await post(`/threads/${thread.id}/interrupt`, {}); notify(t("codex.interruptQueued")); }}><Ban size={16} /></button>}</div></div><div className="event-stream">{(events.data ?? []).length === 0 ? <Empty icon={<SquareTerminal size={26} />} text={t("codex.noMessages")} /> : (events.data ?? []).map(event => <EventItem key={event.event_id} event={event} />)}</div><form className="composer" onSubmit={send}><textarea value={prompt} onChange={e => setPrompt(e.target.value)} placeholder={t("codex.messagePlaceholder")} rows={3} /><div className="composer-bar"><div><select aria-label={t("codex.approveOnRequest")} value={approvalMode} onChange={e => setApprovalMode(e.target.value)}><option value="on-request">{t("codex.approveOnRequest")}</option><option value="untrusted">{t("codex.untrusted")}</option><option value="never">{t("codex.neverApprove")}</option></select><input aria-label={t("codex.modelOverride")} value={model} onChange={e => setModel(e.target.value)} placeholder={t("codex.defaultModel")} /></div><button className="primary-button" disabled={!prompt.trim()}><ChevronRight size={17} />{t("codex.send")}</button></div></form></>;
}

function EventItem({ event }: { event: StreamEvent }) {
  const { t } = useI18n();
  const kind = event.kind;
  const payload = event.payload as Record<string, unknown> | string | null;
  if (kind === "user.message") return <article className="message user"><header><UserRound size={15} /><strong>{t("codex.you")}</strong><time>{formatTime(event.occurred_at)}</time></header><p>{typeof payload === "object" && payload ? String(payload.text ?? "") : String(payload ?? "")}</p></article>;
  const text = extractText(payload);
  const command = kind.toLowerCase().includes("command");
  const diff = kind.toLowerCase().includes("diff") || kind.toLowerCase().includes("filechange");
  return <article className={`message ${command ? "command" : diff ? "diff" : "agent"}`}><header>{command ? <SquareTerminal size={15} /> : diff ? <GitBranch size={15} /> : <Code2 size={15} />}<strong>{command ? t("codex.command") : diff ? t("codex.changes") : readableKind(kind)}</strong><time>{formatTime(event.occurred_at)}</time></header>{text ? <pre>{text}</pre> : <pre>{pretty(payload)}</pre>}</article>;
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
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const audit = useData<AuditEntry[]>("/audit", realtime);
  const [dialog, setDialog] = useState(false); const [name, setName] = useState(""); const [lines, setLines] = useState("");
  const submit = async (event: FormEvent) => { event.preventDefault(); const values: Record<string, string> = {}; for (const line of lines.split("\n")) { const index = line.indexOf("="); if (index > 0) values[line.slice(0, index).trim()] = line.slice(index + 1); } try { await post("/secret-sets", { name, values }); setDialog(false); secrets.reload(); setLines(""); notify(t("settings.secretSaved")); } catch (err) { notify(message(err)); } };
  return <div className="page-stack"><Section title={t("settings.vaultSets")} icon={<Database size={18} />} action={<button className="primary-button" onClick={() => setDialog(true)}><Plus size={17} />{t("settings.newSecretSet")}</button>}><DataTable headers={[t("settings.name"), t("column.updated"), ""]} empty={t("settings.noSecretSets")}>{(secrets.data ?? []).map(item => <tr key={item.id}><td><span className="inline"><KeyRound size={14} /><strong>{item.name}</strong></span></td><td>{relative(item.updated_at)}</td><td><button className="icon-button danger" title={t("settings.deleteSecretSet")} onClick={async () => { if (!confirm(t("settings.confirmDelete", { name: item.name }))) return; await remove(`/secret-sets/${item.id}`); secrets.reload(); }}><X size={16} /></button></td></tr>)}</DataTable></Section><Section title={t("settings.auditLog")} icon={<Clipboard size={18} />}><DataTable headers={[t("column.action"), t("column.resource"), t("column.address"), t("column.time")]} empty={t("settings.noAudit")}>{(audit.data ?? []).map(item => <tr key={item.id}><td><code>{item.action}</code></td><td>{item.resource_type}{item.resource_id ? ` · ${shortSHA(item.resource_id)}` : ""}</td><td><code>{item.ip_address}</code></td><td>{formatDate(item.occurred_at)}</td></tr>)}</DataTable></Section><Dialog open={dialog} title={t("settings.secretSetTitle")} onClose={() => setDialog(false)}><form onSubmit={submit}><Field label={t("settings.name")}><input value={name} onChange={e => setName(e.target.value)} required /></Field><Field label={t("settings.environmentValues")}><textarea value={lines} onChange={e => setLines(e.target.value)} rows={8} placeholder={"DATABASE_URL=...\nAPI_TOKEN=..."} required /></Field><DialogActions><button type="button" className="secondary-button" onClick={() => setDialog(false)}>{t("common.cancel")}</button><button className="primary-button"><KeyRound size={16} />{t("settings.encryptSave")}</button></DialogActions></form></Dialog></div>;
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
  const [data, setData] = useState<T | null>(null); const [error, setError] = useState(""); const [loading, setLoading] = useState(Boolean(path)); const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion(value => value + 1), []);
  useEffect(() => { if (!path) { setLoading(false); return; } let active = true; setLoading(data === null); api<T>(path).then(value => { if (active) { setData(value); setError(""); } }).catch(err => { if (active) setError(message(err)); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [path, dependency, version]);
  return { data, error, loading, reload };
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
function extractText(payload: unknown): string { if (!payload || typeof payload !== "object") return typeof payload === "string" ? payload : ""; const value = payload as Record<string, unknown>; for (const key of ["delta", "text", "diff", "output"]) if (typeof value[key] === "string") return value[key] as string; if (value.item && typeof value.item === "object") return extractText(value.item); return ""; }
function readableKind(kind: string) { return kind.replace(/^codex\./, "").replaceAll(".", " / ").replaceAll("/", " / "); }
function shortSHA(value: string) { return value ? value.slice(0, 8) : "-"; }
function formatDate(value: string) { return new Intl.DateTimeFormat(currentLocale(), { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)); }
function formatTime(value: string) { return new Intl.DateTimeFormat(currentLocale(), { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value)); }
function relative(value: string) { const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000); const formatter = new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" }); const ranges: Array<[number, Intl.RelativeTimeFormatUnit]> = [[60, "second"], [60, "minute"], [24, "hour"], [7, "day"], [4.345, "week"], [12, "month"], [Infinity, "year"]]; let duration = seconds; for (const [amount, unit] of ranges) { if (Math.abs(duration) < amount) return formatter.format(Math.round(duration), unit); duration /= amount; } return formatDate(value); }
