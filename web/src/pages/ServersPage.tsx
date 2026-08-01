import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import {
  AlertTriangle,
  Ban,
  Check,
  Database,
  GitFork,
  HardDrive,
  KeyRound,
  LoaderCircle,
  LockKeyhole,
  MapPin,
  Pencil,
  Plus,
  RefreshCw,
  Server as ServerIcon,
  Settings,
  ShieldCheck,
  SquareTerminal,
  StickyNote,
  Trash2,
  Undo2,
  Wifi,
  WifiOff,
  Wrench,
  X
} from "lucide-react";
import { APIError, patch, post, postStream, remove } from "../api";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "../components/Dialog";
import { DataTable, Section, Status } from "../components/PageUI";
import { relative } from "../format";
import { useI18n } from "../i18n";
import type { CredentialProfile, Server, SSHBootstrapResult, SSHBootstrapStreamEvent, SSHHostKey } from "../types";
import { useData } from "../useData";

export interface ServersPageProps {
  realtime: number;
  notify: (text: string) => void;
}

type InstallLogEntry = {
  step: string;
  status: "running" | "done" | "error";
  current: number;
  total: number;
  detail: string;
};

type ServerConfirmation =
  | { type: "force-revoke"; server: Server }
  | { type: "agent-update"; server: Server }
  | { type: "codex-update"; server: Server };

export function ServersPage({ realtime, notify }: ServersPageProps) {
  const { t } = useI18n();
  const servers = useData<Server[]>("/servers", realtime);
  const credentialProfiles = useData<CredentialProfile[]>("/credential-profiles", realtime);
  const codexProfiles = (credentialProfiles.data ?? []).filter(profile => profile.kind === "codex");
  const gitProfiles = (credentialProfiles.data ?? []).filter(profile => profile.kind === "git");
  const [dialog, setDialog] = useState(false);
	const [repairingServer, setRepairingServer] = useState<Server | null>(null);
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
  const [serverConfirmation, setServerConfirmation] = useState<ServerConfirmation | null>(null);
  const [serverConfirmationError, setServerConfirmationError] = useState("");
  const [credentialServer, setCredentialServer] = useState<Server | null>(null);
  const [credentialBusy, setCredentialBusy] = useState(false);
  const [credentialError, setCredentialError] = useState("");
  const [credentialForm, setCredentialForm] = useState({ codexProfileID: "", gitProfileIDs: [] as string[] });
  const [retiringServer, setRetiringServer] = useState<Server | null>(null);
  const [retireStep, setRetireStep] = useState<"form" | "fingerprint">("form");
  const [retireBusy, setRetireBusy] = useState(false);
  const [retireError, setRetireError] = useState("");
  const [retireHostKey, setRetireHostKey] = useState<SSHHostKey | null>(null);
  const [retireForm, setRetireForm] = useState({ host: "", port: "22", user: "root", authMethod: "private_key", password: "", privateKey: "", privateKeyPassphrase: "", confirmation: "" });
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
  const open = () => { reset(); setRepairingServer(null); setDialog(true); };
  const repair = (server: Server) => {
	reset();
	setRepairingServer(server);
	setForm(current => ({ ...current, name: server.name, host: server.address, configuration: server.configuration, notes: server.notes, codexProfileID: server.codex_profile_id || codexProfiles[0]?.id || "", gitProfileID: server.git_profile_id, allowSudo: false }));
	setDialog(true);
  };
  const close = () => { if (busy) return; setDialog(false); setRepairingServer(null); reset(); };
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
      const installPath = repairingServer ? `/servers/${repairingServer.id}/ssh/repair-stream` : "/servers/ssh/bootstrap-stream";
      await postStream<SSHBootstrapStreamEvent>(installPath, {
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
      setResult(installed); setStep("complete"); servers.reload(); notify(t(repairingServer ? "server.repaired" : "server.installed"));
      setForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "" }));
    } catch (err) { setError(enrollmentMessage(err, t)); } finally { setBusy(false); }
  };
  const choosePrivateKey = async (file?: File) => {
    setError("");
    if (!file) { setForm(current => ({ ...current, privateKey: "" })); return; }
    if (file.size > 256 * 1024) { setError(t("server.privateKeyTooLarge")); return; }
    try { const privateKey = await file.text(); setForm(current => ({ ...current, privateKey })); } catch (err) { setError(message(err)); }
  };
  const openRetirement = (server: Server) => {
    setRetiringServer(server); setRetireStep("form"); setRetireBusy(false); setRetireError(""); setRetireHostKey(null);
    setRetireForm({ host: server.address, port: "22", user: "root", authMethod: "private_key", password: "", privateKey: "", privateKeyPassphrase: "", confirmation: "" });
  };
  const closeRetirement = () => {
    if (retireBusy || serverConfirmation?.type === "force-revoke") return;
    setRetiringServer(null); setRetireStep("form"); setRetireError(""); setRetireHostKey(null);
    setRetireForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "", confirmation: "" }));
  };
  const probeRetirement = async (event: FormEvent) => {
    event.preventDefault();
    if (!retiringServer || retireForm.confirmation !== retiringServer.name) return;
    setRetireBusy(true); setRetireError("");
    try {
      setRetireHostKey(await post<SSHHostKey>("/servers/ssh/probe", { host: retireForm.host.trim(), port: Number(retireForm.port) }));
      setRetireStep("fingerprint");
    } catch (err) { setRetireError(enrollmentMessage(err, t)); } finally { setRetireBusy(false); }
  };
  const uninstallServer = async () => {
    if (!retiringServer || !retireHostKey || retireForm.confirmation !== retiringServer.name) return;
    setRetireBusy(true); setRetireError("");
    try {
      await post(`/servers/${retiringServer.id}/ssh/uninstall`, {
        host: retireForm.host.trim(), port: Number(retireForm.port), user: retireForm.user.trim(), auth_method: retireForm.authMethod,
        password: retireForm.authMethod === "password" ? retireForm.password : "",
        private_key: retireForm.authMethod === "private_key" ? retireForm.privateKey : "",
        private_key_passphrase: retireForm.authMethod === "private_key" ? retireForm.privateKeyPassphrase : "",
        host_key_fingerprint: retireHostKey.fingerprint, confirmation: retireForm.confirmation
      });
      setRetiringServer(null); setRetireHostKey(null); servers.reload(); notify(t("server.retired"));
      setRetireForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "", confirmation: "" }));
    } catch (err) { setRetireError(enrollmentMessage(err, t)); } finally { setRetireBusy(false); }
  };
  const forceRevokeServer = async () => {
    const target = serverConfirmation?.type === "force-revoke" ? serverConfirmation.server : null;
    if (!retiringServer || !target || target.id !== retiringServer.id || retireForm.confirmation !== retiringServer.name) return;
    setRetireBusy(true); setRetireError("");
    try {
      await remove(`/servers/${target.id}`);
      setServerConfirmation(null); setServerConfirmationError(""); setRetiringServer(null); servers.reload(); notify(t("server.revoked"));
      setRetireForm(current => ({ ...current, password: "", privateKey: "", privateKeyPassphrase: "", confirmation: "" }));
    } catch (err) { const detail = message(err); setRetireError(detail); setServerConfirmationError(detail); } finally { setRetireBusy(false); }
  };
  const chooseRetirementPrivateKey = async (file?: File) => {
    setRetireError("");
    if (!file) { setRetireForm(current => ({ ...current, privateKey: "" })); return; }
    if (file.size > 256 * 1024) { setRetireError(t("server.privateKeyTooLarge")); return; }
    try { const privateKey = await file.text(); setRetireForm(current => ({ ...current, privateKey })); } catch (err) { setRetireError(message(err)); }
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
    setCredentialForm({ codexProfileID: server.codex_profile_id, gitProfileIDs: server.git_profiles?.map(profile => profile.id) ?? (server.git_profile_id ? [server.git_profile_id] : []) });
  };
  const saveCredentials = async (event: FormEvent) => {
    event.preventDefault();
    if (!credentialServer) return;
    setCredentialBusy(true); setCredentialError("");
    try {
      await post(`/servers/${credentialServer.id}/credential-profiles`, {
        codex_profile_id: credentialForm.codexProfileID,
        git_profile_ids: credentialForm.gitProfileIDs
      });
      setCredentialServer(null); servers.reload(); notify(t("server.credentialsQueued"));
    } catch (err) { setCredentialError(message(err)); } finally { setCredentialBusy(false); }
  };
  const updateAgent = async (server: Server) => {
    if (!server.agent_update_available || serverConfirmation?.type !== "agent-update" || serverConfirmation.server.id !== server.id) return;
    setUpdatingServer(`agent:${server.id}`);
    try {
      await post(`/servers/${server.id}/agent-update`, {});
      setServerConfirmation(null); setServerConfirmationError("");
      notify(t("server.updateQueued", { version: server.agent_target_version }));
    } catch (err) { const detail = message(err); setServerConfirmationError(detail); notify(detail); } finally { setUpdatingServer(""); }
  };
  const updateCodex = async (server: Server) => {
    if (!server.codex_update_available || serverConfirmation?.type !== "codex-update" || serverConfirmation.server.id !== server.id) return;
    setUpdatingServer(`codex:${server.id}`);
    try {
      await post(`/servers/${server.id}/codex-update`, {});
      setServerConfirmation(null); setServerConfirmationError("");
      notify(t("server.codexUpdateQueued", { version: server.codex_target_version }));
    } catch (err) { const detail = message(err); setServerConfirmationError(detail); notify(detail); } finally { setUpdatingServer(""); }
  };
  const requestServerConfirmation = (confirmation: ServerConfirmation) => {
    setServerConfirmationError("");
    setServerConfirmation(confirmation);
  };
  const closeServerConfirmation = () => {
    if (retireBusy || updatingServer !== "") return;
    setServerConfirmation(null);
    setServerConfirmationError("");
  };
  return <div className="page-stack"><Section title={t("server.registered")} icon={<ServerIcon size={18} />} action={<button className="primary-button" onClick={open}><Plus size={17} />{t("server.enroll")}</button>}>
    <DataTable headers={[t("column.server"), t("server.information"), t("server.boundCredentials"), t("column.connectivity"), t("column.agent"), t("column.codex"), t("column.lastSeen"), ""]} empty={t("server.none")}>{(servers.data ?? []).map(server => {
      const agentUpdateTitle = server.status !== "online" ? t("server.updateOffline") : server.agent_update_available ? t("server.updateAgent", { version: server.agent_target_version }) : !server.agent_version ? t("common.awaitingHeartbeat") : !server.agent_update_supported ? t("server.updateRequiresReinstall") : server.agent_version === server.agent_target_version ? t("server.agentLatest") : t("server.updateUnavailable");
      const codexUpdateTitle = server.status !== "online" ? t("server.codexUpdateOffline") : !server.codex_update_supported ? t("server.codexUpdateRequiresAgent") : server.codex_update_available ? t("server.updateCodex", { version: server.codex_target_version }) : t("server.codexLatest", { version: server.codex_target_version });
      const serverName = server.is_control_plane ? t("server.controlPlane") : server.name;
      return <tr key={server.id}><td><div className="cell-main"><strong>{serverName}</strong>{server.is_control_plane && <span className="status-tag neutral"><LockKeyhole size={13} />{t("server.builtIn")}</span>}<small>{server.hostname || t("common.awaitingHeartbeat")}</small></div></td><td><ServerInformation server={server} /></td><td><ServerCredentialSummary server={server} /></td><td><Status value={server.status} icon={server.status === "online" ? <Wifi size={13} /> : <WifiOff size={13} />} /></td><td><code>{server.agent_version || "-"}</code></td><td><span className={server.codex_ready ? "inline-success" : "muted"}>{server.codex_ready ? <Check size={14} /> : <Ban size={14} />}{server.codex_version || t("common.unavailable")}</span></td><td>{server.last_seen_at ? relative(server.last_seen_at) : t("common.never")}</td><td><div className="row-actions"><button className="icon-button" disabled={server.status !== "online" || !server.agent_update_available || updatingServer !== "" || serverConfirmation !== null} title={agentUpdateTitle} onClick={() => requestServerConfirmation({ type: "agent-update", server })}><RefreshCw className={updatingServer === `agent:${server.id}` ? "spin" : ""} size={15} /></button><button className="icon-button" disabled={server.status !== "online" || !server.codex_update_available || updatingServer !== "" || serverConfirmation !== null} title={codexUpdateTitle} onClick={() => requestServerConfirmation({ type: "codex-update", server })}>{updatingServer === `codex:${server.id}` ? <LoaderCircle className="spin" size={15} /> : <SquareTerminal size={15} />}</button><button className="icon-button" disabled={server.status !== "online" || updatingServer !== "" || serverConfirmation !== null} title={server.status === "online" ? t("server.editCredentials") : t("server.credentialsOffline")} onClick={() => editCredentials(server)}><KeyRound size={15} /></button><button className="icon-button" title={t("server.repair")} onClick={() => repair(server)}><Wrench size={15} /></button><button className="icon-button" title={t("server.editInformation")} onClick={() => editServer(server)}><Pencil size={15} /></button>{server.is_control_plane ? <button className="icon-button" disabled title={t("server.controlPlaneProtected")}><LockKeyhole size={16} /></button> : <button className="icon-button danger" disabled={serverConfirmation !== null} title={t("server.retire")} onClick={() => openRetirement(server)}><Trash2 size={16} /></button>}</div></td></tr>;
    })}</DataTable>
  </Section><Dialog open={dialog} title={t(repairingServer ? "server.repairTitle" : "server.enrollLinux")} onClose={close} wide>{step === "form" ? <form onSubmit={probe}>
    {error && <ErrorBanner text={error} />}
    {repairingServer && <p className="security-notice">{t("server.repairDescription")}</p>}
    {!repairingServer && <div className="form-grid"><Field label={t("server.name")}><input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required /></Field><Field label={t("server.scanRoots")}><input value={form.roots} onChange={e => setForm({ ...form, roots: e.target.value })} required /></Field></div>}
    <div className="form-grid"><Field label={t("server.configuration")}><textarea rows={3} maxLength={4096} value={form.configuration} onChange={e => setForm({ ...form, configuration: e.target.value })} placeholder={t("server.configurationPlaceholder")} /></Field><Field label={t("server.notes")}><textarea rows={3} maxLength={4096} value={form.notes} onChange={e => setForm({ ...form, notes: e.target.value })} placeholder={t("server.notesPlaceholder")} /></Field></div>
    <div className="form-grid thirds"><Field label={t("server.sshHost")}><input value={form.host} onChange={e => setForm({ ...form, host: e.target.value })} placeholder="192.0.2.10" required /></Field><Field label={t("server.sshPort")}><input type="number" min="1" max="65535" value={form.port} onChange={e => setForm({ ...form, port: e.target.value })} required /></Field><Field label={t("server.sshUser")}><input value={form.user} onChange={e => setForm({ ...form, user: e.target.value })} placeholder="root / ubuntu / ec2-user" required /></Field></div>
    <Field label={t("server.authMethod")}><select value={form.authMethod} onChange={e => setForm({ ...form, authMethod: e.target.value })}><option value="private_key">{t("server.authPrivateKey")}</option><option value="password">{t("server.authPassword")}</option></select></Field>
    {form.authMethod === "private_key" ? <div className="form-grid"><Field label={t("server.privateKeyFile")}><input type="file" accept=".pem,.key,text/plain" onChange={e => void choosePrivateKey(e.target.files?.[0])} required={!form.privateKey} /></Field><Field label={t("server.privateKeyPassphrase")}><input type="password" autoComplete="off" value={form.privateKeyPassphrase} onChange={e => setForm({ ...form, privateKeyPassphrase: e.target.value })} placeholder={t("common.optional")} /></Field></div> : <Field label={t("server.sshPassword")}><input type="password" autoComplete="new-password" value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} required /></Field>}
    <div className="form-divider"><span>{t("server.credentialProfiles")}</span></div>
    <div className="form-grid"><Field label={t("server.codexProfile")}><select value={form.codexProfileID} onChange={e => setForm({ ...form, codexProfileID: e.target.value })} required><option value="">{t(codexProfiles.length ? "server.selectCodexProfile" : "server.noCodexProfiles")}</option>{codexProfiles.map(profile => <option value={profile.id} key={profile.id}>{profile.name} &middot; {profile.model}</option>)}</select></Field><Field label={t("server.gitProfile")}><select value={form.gitProfileID} onChange={e => setForm({ ...form, gitProfileID: e.target.value })}><option value="">{t("server.noGitProfile")}</option>{gitProfiles.map(profile => { const ready = Boolean(profile.commit_name && profile.commit_email); return <option value={profile.id} key={profile.id} disabled={!ready}>{profile.name} &middot; {profile.username}{ready ? "" : ` ${String.fromCharCode(183)} ${t("settings.gitIdentityMissing")}`}</option>; })}</select></Field></div>
    <label className={`agent-sudo-option ${form.allowSudo ? "enabled" : ""}`}><input type="checkbox" checked={form.allowSudo} onChange={event => setForm({ ...form, allowSudo: event.target.checked })} /><span><strong>{t("server.allowAgentSudo")}</strong><small>{t("server.allowAgentSudoWarning")}</small></span></label>
    <DialogActions><button type="button" className="secondary-button" onClick={close}>{t("common.cancel")}</button><button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={16} /> : <ShieldCheck size={16} />}{busy ? t("server.probing") : t("server.probeFingerprint")}</button></DialogActions>
  </form> : step === "fingerprint" && hostKey ? <div className="enrollment-step">
    {error && <ErrorBanner text={error} />}
    <div className="fingerprint-status"><ShieldCheck size={28} /><div><strong>{t("server.fingerprint")}</strong><span>{form.host}:{form.port} &middot; {hostKey.key_type}</span></div></div>
    <code className="fingerprint-value">{hostKey.fingerprint}</code>
    <p className="security-notice">{t("server.fingerprintNotice")}</p>
    {installLogs.length > 0 && <div className="install-log" aria-live="polite"><div className="install-log-heading"><SquareTerminal size={16} /><strong>{t("server.installLog")}</strong></div><div className="install-log-lines">{installLogs.map(entry => <div className={`install-log-entry ${entry.status}`} key={entry.step}>{entry.status === "running" ? <LoaderCircle className="spin" size={15} /> : entry.status === "done" ? <Check size={15} /> : <AlertTriangle size={15} />}<span>{t(`server.progress.${entry.step}`)}</span>{entry.total > 0 && <code>{Math.min(100, Math.round((entry.current / entry.total) * 100))}%</code>}{entry.detail && <small>{entry.detail}</small>}</div>)}</div></div>}
    <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => { setStep("form"); setError(""); setInstallLogs([]); }}><Undo2 size={16} />{t("server.back")}</button><button className="primary-button" disabled={busy} onClick={() => void install()}>{busy ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{busy ? t("server.installing") : t("server.confirmInstall")}</button></DialogActions>
  </div> : <div className="enrollment-step enrollment-complete"><div className="completion-mark"><Check size={28} /></div><h3>{t(repairingServer ? "server.repaired" : "server.installed")}</h3>{result && <p>{t("server.installedSummary", { hostname: result.hostname, architecture: result.architecture })}</p>}{result && result.warnings.length > 0 && <div className="warning-list"><strong>{t("server.warningTitle")}</strong>{result.warnings.map(warning => <span key={warning}><AlertTriangle size={15} />{t(`server.warning.${warning}`)}</span>)}</div>}<DialogActions><button className="primary-button" onClick={close}><Check size={16} />{t("common.done")}</button></DialogActions></div>}</Dialog>
  <Dialog open={retiringServer !== null} title={t("server.retireTitle", { name: retiringServer?.name ?? "" })} onClose={closeRetirement} wide>
    {retireStep === "form" ? <form onSubmit={probeRetirement}>
      {retireError && <ErrorBanner text={retireError} />}
      <div className="warning-list retirement-warning"><strong>{t("server.retireRemovesTitle")}</strong><span><Trash2 size={15} />{t("server.retireRemovesAgent")}</span><span><HardDrive size={15} />{t("server.retireRemovesData")}</span><span><Database size={15} />{t("server.retireRemovesControlRecords")}</span></div>
      <p className="security-notice">{t("server.retirePreserves")}</p>
      <div className="form-grid thirds"><Field label={t("server.sshHost")}><input value={retireForm.host} onChange={event => setRetireForm({ ...retireForm, host: event.target.value })} placeholder="192.0.2.10" required /></Field><Field label={t("server.sshPort")}><input type="number" min="1" max="65535" value={retireForm.port} onChange={event => setRetireForm({ ...retireForm, port: event.target.value })} required /></Field><Field label={t("server.sshUser")}><input value={retireForm.user} onChange={event => setRetireForm({ ...retireForm, user: event.target.value })} placeholder="root / ubuntu / ec2-user" required /></Field></div>
      <Field label={t("server.authMethod")}><select value={retireForm.authMethod} onChange={event => setRetireForm({ ...retireForm, authMethod: event.target.value })}><option value="private_key">{t("server.authPrivateKey")}</option><option value="password">{t("server.authPassword")}</option></select></Field>
      {retireForm.authMethod === "private_key" ? <div className="form-grid"><Field label={t("server.privateKeyFile")}><input type="file" accept=".pem,.key,text/plain" onChange={event => void chooseRetirementPrivateKey(event.target.files?.[0])} required={!retireForm.privateKey} /></Field><Field label={t("server.privateKeyPassphrase")}><input type="password" autoComplete="off" value={retireForm.privateKeyPassphrase} onChange={event => setRetireForm({ ...retireForm, privateKeyPassphrase: event.target.value })} placeholder={t("common.optional")} /></Field></div> : <Field label={t("server.sshPassword")}><input type="password" autoComplete="new-password" value={retireForm.password} onChange={event => setRetireForm({ ...retireForm, password: event.target.value })} required /></Field>}
      <Field label={t("server.retireConfirmation", { name: retiringServer?.name ?? "" })}><input value={retireForm.confirmation} onChange={event => setRetireForm({ ...retireForm, confirmation: event.target.value })} placeholder={retiringServer?.name ?? ""} autoComplete="off" required /></Field>
      <DialogActions><button type="button" className="secondary-button" disabled={retireBusy} onClick={closeRetirement}>{t("common.cancel")}</button><button type="button" className="secondary-button danger" disabled={retireBusy || retireForm.confirmation !== retiringServer?.name || serverConfirmation !== null} onClick={() => retiringServer && requestServerConfirmation({ type: "force-revoke", server: retiringServer })}><X size={16} />{t("server.forceRevoke")}</button><button className="primary-button" disabled={retireBusy || retireForm.confirmation !== retiringServer?.name || serverConfirmation !== null}>{retireBusy ? <LoaderCircle className="spin" size={16} /> : <ShieldCheck size={16} />}{retireBusy ? t("server.probing") : t("server.probeFingerprint")}</button></DialogActions>
    </form> : retireHostKey ? <div className="enrollment-step">
      {retireError && <ErrorBanner text={retireError} />}
      <div className="fingerprint-status"><ShieldCheck size={28} /><div><strong>{t("server.fingerprint")}</strong><span>{retireForm.host}:{retireForm.port} &middot; {retireHostKey.key_type}</span></div></div>
      <code className="fingerprint-value">{retireHostKey.fingerprint}</code>
      <p className="security-notice">{t("server.retireFingerprintNotice")}</p>
      <div className="warning-list retirement-warning"><strong>{t("server.retireFinalWarning")}</strong><span><Trash2 size={15} />{t("server.retireRemovesAgent")}</span><span><HardDrive size={15} />{t("server.retireRemovesData")}</span><span><Database size={15} />{t("server.retireRemovesControlRecords")}</span></div>
      <DialogActions><button type="button" className="secondary-button" disabled={retireBusy} onClick={() => { setRetireStep("form"); setRetireError(""); setRetireHostKey(null); }}><Undo2 size={16} />{t("server.back")}</button><button className="primary-button danger" disabled={retireBusy} onClick={() => void uninstallServer()}>{retireBusy ? <LoaderCircle className="spin" size={16} /> : <Trash2 size={16} />}{retireBusy ? t("server.retiring") : t("server.confirmRetire")}</button></DialogActions>
    </div> : null}
  </Dialog>
  <Dialog open={editingServer !== null} title={t("server.editInformation")} onClose={() => { if (!metadataBusy) setEditingServer(null); }}>
    <form onSubmit={saveMetadata}>{metadataError && <ErrorBanner text={metadataError} />}<Field label={t("server.address")}><input maxLength={255} value={metadataForm.address} onChange={e => setMetadataForm({ ...metadataForm, address: e.target.value })} placeholder="192.0.2.10" /></Field><Field label={t("server.configuration")}><textarea rows={4} maxLength={4096} value={metadataForm.configuration} onChange={e => setMetadataForm({ ...metadataForm, configuration: e.target.value })} placeholder={t("server.configurationPlaceholder")} /></Field><Field label={t("server.notes")}><textarea rows={4} maxLength={4096} value={metadataForm.notes} onChange={e => setMetadataForm({ ...metadataForm, notes: e.target.value })} placeholder={t("server.notesPlaceholder")} /></Field><DialogActions><button type="button" className="secondary-button" disabled={metadataBusy} onClick={() => setEditingServer(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={metadataBusy}>{metadataBusy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("server.saveInformation")}</button></DialogActions></form>
  </Dialog>
  <Dialog open={credentialServer !== null} title={t("server.editCredentials")} onClose={() => { if (!credentialBusy) setCredentialServer(null); }}>
    <form onSubmit={saveCredentials}>{credentialError && <ErrorBanner text={credentialError} />}<p className="security-notice">{t("server.credentialsDescription", { name: credentialServer?.name ?? "" })}</p><Field label={t("server.codexProfile")}><select value={credentialForm.codexProfileID} onChange={e => setCredentialForm({ ...credentialForm, codexProfileID: e.target.value })} required><option value="">{t(codexProfiles.length ? "server.selectCodexProfile" : "server.noCodexProfiles")}</option>{codexProfiles.map(profile => <option value={profile.id} key={profile.id}>{profile.name} &middot; {profile.model}</option>)}</select></Field><Field label={t("server.gitProfiles")}><div className="credential-profile-options">{gitProfiles.length === 0 && <span className="muted">{t("server.noGitProfile")}</span>}{gitProfiles.map(profile => { const ready = Boolean(profile.commit_name && profile.commit_email); const checked = credentialForm.gitProfileIDs.includes(profile.id); return <label className={`credential-profile-option ${checked ? "selected" : ""} ${ready ? "" : "disabled"}`} key={profile.id}><input type="checkbox" checked={checked} disabled={!ready} onChange={event => setCredentialForm(current => ({ ...current, gitProfileIDs: event.target.checked ? [...current.gitProfileIDs, profile.id] : current.gitProfileIDs.filter(id => id !== profile.id) }))} /><span><strong>{profile.name}</strong><small>{profile.username}{ready ? "" : ` ${String.fromCharCode(183)} ${t("settings.gitIdentityMissing")}`}</small></span></label>; })}</div></Field><DialogActions><button type="button" className="secondary-button" disabled={credentialBusy} onClick={() => setCredentialServer(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={credentialBusy || !credentialForm.codexProfileID}>{credentialBusy ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{credentialBusy ? t("server.credentialsUpdating") : t("server.saveCredentials")}</button></DialogActions></form>
  </Dialog>{serverConfirmation && <ConfirmDialog open danger title={t(serverConfirmation.type === "force-revoke" ? "server.forceRevoke" : serverConfirmation.type === "agent-update" ? "server.updateAgent" : "server.updateCodex", { version: serverConfirmation.type === "codex-update" ? serverConfirmation.server.codex_target_version : serverConfirmation.server.agent_target_version })} description={serverConfirmationError || undefined} impact={t(serverConfirmation.type === "force-revoke" ? "server.confirmForceRevoke" : serverConfirmation.type === "agent-update" ? "server.confirmUpdate" : "server.confirmCodexUpdate", { name: serverConfirmation.server.name, version: serverConfirmation.type === "codex-update" ? serverConfirmation.server.codex_target_version : serverConfirmation.server.agent_target_version })} confirmLabel={t(serverConfirmation.type === "force-revoke" ? "server.forceRevoke" : serverConfirmation.type === "agent-update" ? "server.updateAgent" : "server.updateCodex", { version: serverConfirmation.type === "codex-update" ? serverConfirmation.server.codex_target_version : serverConfirmation.server.agent_target_version })} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={serverConfirmation.type === "force-revoke" ? retireBusy : updatingServer !== ""} onClose={closeServerConfirmation} onConfirm={() => { if (serverConfirmation.type === "force-revoke") return forceRevokeServer(); if (serverConfirmation.type === "agent-update") return updateAgent(serverConfirmation.server); return updateCodex(serverConfirmation.server); }} />}</div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function ServerInformation({ server, className = "" }: { server?: Server; className?: string }) {
  const { t } = useI18n();
  if (!server || (!server.address && !server.configuration && !server.notes)) return <span className="muted">{t("server.noInformation")}</span>;
  return <div className={`server-information ${className}`}>{server.address && <span className="server-information-line" title={`${t("server.address")}: ${server.address}`}><MapPin size={13} /><code>{server.address}</code></span>}{server.configuration && <span className="server-information-line" title={`${t("server.configuration")}: ${server.configuration}`}><Settings size={13} /><span>{server.configuration}</span></span>}{server.notes && <span className="server-information-line" title={`${t("server.notes")}: ${server.notes}`}><StickyNote size={13} /><span>{server.notes}</span></span>}</div>;
}

function ServerCredentialSummary({ server }: { server: Server }) {
  const { t } = useI18n();
  const gitProfiles = server.git_profiles ?? [];
  if (!server.codex_profile_name && gitProfiles.length === 0 && !server.git_profile_name) return <span className="muted">{t("server.noBoundCredentials")}</span>;
  return <div className="server-credential-summary">{server.codex_profile_name && <span title={server.codex_profile_name}><SquareTerminal size={13} /><span>{server.codex_profile_name}</span></span>}{gitProfiles.length ? gitProfiles.map(profile => <span key={profile.id} title={`${profile.name} ${String.fromCharCode(183)} ${profile.username}`}><GitFork size={13} /><span>{profile.name}</span></span>) : server.git_profile_name && <span title={server.git_profile_name}><GitFork size={13} /><span>{server.git_profile_name}</span></span>}</div>;
}

function Dialog(props: Omit<DialogProps, "closeLabel">) {
  const { t } = useI18n();
  return <AccessibleDialog {...props} closeLabel={t("common.close")} />;
}

function ErrorBanner({ text }: { text: string }) {
  return <div className="error-banner"><AlertTriangle size={16} />{text}</div>;
}

function message(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}

function enrollmentMessage(error: unknown, translate: (key: string) => string) {
  if (error instanceof APIError && error.code) {
    const localized = translate(`server.error.${error.code}`);
    if (!localized.startsWith("server.error.")) return localized;
  }
  return message(error);
}

export default ServersPage;
