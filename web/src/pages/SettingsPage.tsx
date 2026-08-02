import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  ArrowDownToLine,
  CalendarClock,
  Check,
  Clipboard,
  Code2,
  Database,
  GitBranch,
  KeyRound,
  LoaderCircle,
  LockKeyhole,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  ShieldCheck,
  SquareTerminal,
  Trash2,
  UserRound,
  X
} from "lucide-react";
import { api, post, put, remove } from "../api";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Dialog as AccessibleDialog, DialogActions, type DialogProps } from "../components/Dialog";
import { DataTable, Section, Status } from "../components/PageUI";
import { defaultCodexComposerPreferences } from "../codexComposerPreferences";
import { formatDate, relative, shortSHA } from "../format";
import { useI18n } from "../i18n";
import type { AuditEntry, CodexCLISettings, CredentialProfile, SecretSet, ScheduledTask, Thread } from "../types";
import { useData } from "../useData";
import { ScheduledTaskDialog } from "./ScheduledTaskDialog";

export interface PageProps {
  realtime: number;
  notify: (text: string) => void;
}

type AuditListPage = { items: AuditEntry[]; has_more: boolean; next: number | null };
const auditListPageSize = 50;
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

function message(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

function Dialog(props: Omit<DialogProps, "closeLabel">) {
  const { t } = useI18n();
  return <AccessibleDialog {...props} closeLabel={t("common.close")} />;
}

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

function ErrorBanner({ text }: { text: string }) {
  return <div className="error-banner"><AlertTriangle size={16} />{text}</div>;
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



type SettingsConfirmation =
  | { type: "scheduled-task"; task: ScheduledTask }
  | { type: "credential-profile"; profile: CredentialProfile }
  | { type: "secret-set"; secret: SecretSet };

export function SettingsPage({ realtime, notify }: PageProps) {
  const { t } = useI18n();
  const codexSettings = useData<CodexCLISettings>("/settings/codex-cli", realtime);
  const profiles = useData<CredentialProfile[]>("/credential-profiles", realtime);
  const secrets = useData<SecretSet[]>("/secret-sets", realtime);
  const audit = useAuditList(realtime);
  const scheduledTasks = useData<ScheduledTask[]>("/scheduled-tasks", realtime);
  const threads = useData<Thread[]>("/threads", realtime);
  const [profileDialog, setProfileDialog] = useState(false);
  const [profileBusy, setProfileBusy] = useState(false);
  const [profileForm, setProfileForm] = useState({ id: "", kind: "codex" as "codex" | "git", name: "", endpoint: "https://api.openai.com/v1", username: "", model: defaultCodexModel, commit_name: "", commit_email: "", secret: "" });
  const [secretDialog, setSecretDialog] = useState(false);
  const [name, setName] = useState("");
  const [lines, setLines] = useState("");
  const [codexTargetBusy, setCodexTargetBusy] = useState(false);
  const [codexVersions, setCodexVersions] = useState<string[]>([]);
  const [selectedCodexVersion, setSelectedCodexVersion] = useState("");
  const [scheduledTarget, setScheduledTarget] = useState<{ task?: ScheduledTask; threadID?: string } | null>(null);
  const [scheduledBusy, setScheduledBusy] = useState("");
  const [settingsConfirmation, setSettingsConfirmation] = useState<SettingsConfirmation | null>(null);
  const [settingsDeleteBusy, setSettingsDeleteBusy] = useState("");
  const settingsDeleteBusyRef = useRef("");
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
  const openScheduledTask = (task?: ScheduledTask) => setScheduledTarget(task ? { task } : {});
  const toggleScheduledTask = async (task: ScheduledTask) => {
    if (scheduledBusy) return;
    setScheduledBusy(task.id);
    try { await put(`/scheduled-tasks/${task.id}`, { enabled: !task.enabled }); scheduledTasks.reload(); } catch (err) { notify(message(err)); } finally { setScheduledBusy(""); }
  };
  const deleteScheduledTask = async (task: ScheduledTask) => {
    if (scheduledBusy || settingsDeleteBusyRef.current) return;
    const busyKey = `scheduled-task:${task.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await remove(`/scheduled-tasks/${task.id}`); scheduledTasks.reload(); notify(t("settings.scheduledTaskDeleted")); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  const deleteProfile = async (profile: CredentialProfile) => {
    if (settingsDeleteBusyRef.current) return;
    const busyKey = `credential-profile:${profile.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await remove(`/credential-profiles/${profile.id}`); profiles.reload(); notify(t("settings.profileDeleted")); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  const deleteSecretSet = async (secret: SecretSet) => {
    if (settingsDeleteBusyRef.current) return;
    const busyKey = `secret-set:${secret.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await remove(`/secret-sets/${secret.id}`); secrets.reload(); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  return <div className="page-stack">
    <Section title={t("settings.codexCLIManagement")} icon={<SquareTerminal size={18} />}>
      <div className="codex-version-control"><div className="codex-version-summary"><small>{t("settings.codexTargetVersion")}</small><strong>{codexSettings.data?.target_version ?? "-"}</strong><span><ShieldCheck size={14} />{t("settings.codexStableRelease")}</span></div><div className="codex-version-actions"><select aria-label={t("settings.selectCodexVersion")} value={selectedCodexVersion} disabled={codexTargetBusy || !codexVersions.length} onChange={event => setSelectedCodexVersion(event.target.value)}>{codexVersions.map((version, index) => <option key={version} value={version}>{index === 0 ? t("settings.latestCodexVersion", { version }) : version}</option>)}</select><button className="secondary-button" disabled={codexTargetBusy || !selectedCodexVersion || selectedCodexVersion === codexSettings.data?.target_version} onClick={() => void applyCodexVersion()}><Check size={16} />{t("settings.applyCodexVersion")}</button><button className="primary-button" disabled={codexTargetBusy || !codexSettings.data} onClick={() => void checkCodexUpdates()}>{codexTargetBusy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t(codexTargetBusy ? "settings.checkingCodexUpdates" : "settings.checkCodexUpdates")}</button></div></div>
    </Section>
    <Section title={t("settings.scheduledTasks")} icon={<CalendarClock size={18} />} action={<button className="primary-button" onClick={() => openScheduledTask()}><Plus size={17} />{t("settings.newScheduledTask")}</button>}>
      <DataTable headers={[t("settings.name"), t("settings.scheduledThread"), t("settings.schedule"), t("settings.nextRun"), t("column.state"), ""]} empty={t("settings.noScheduledTasks")}>
        {(scheduledTasks.data ?? []).map(task => <tr key={task.id}><td><div className="cell-main"><strong>{task.name}</strong><small title={task.prompt}>{task.prompt}</small></div></td><td><div className="cell-main"><strong>{task.thread_title}</strong><small>{task.project_name} / {task.server_name}</small></div></td><td><code title={task.timezone}>{task.schedule}</code></td><td>{task.enabled ? formatDate(task.next_run_at) : t("settings.scheduleDisabled")}</td><td><div className="cell-main"><Status value={task.enabled ? "enabled" : "disabled"} />{task.last_run_status && <small title={task.last_run_message || undefined}>{t("settings.lastRun", { status: task.last_run_status })}</small>}</div></td><td><div className="row-actions"><button className="icon-button" title={t("common.edit")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => openScheduledTask(task)}><Pencil size={15} /></button><button className="icon-button" title={t(task.enabled ? "settings.disableScheduledTask" : "settings.enableScheduledTask")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => void toggleScheduledTask(task)}>{scheduledBusy === task.id ? <LoaderCircle className="spin" size={15} /> : task.enabled ? <Pause size={15} /> : <Play size={15} />}</button><button className="icon-button danger" title={t("settings.deleteScheduledTask")} disabled={Boolean(scheduledBusy || settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "scheduled-task", task })}><Trash2 size={15} /></button></div></td></tr>)}
      </DataTable>
    </Section>
    <Section title={t("settings.credentialProfiles")} icon={<KeyRound size={18} />} action={<button className="primary-button" onClick={() => openProfile()}><Plus size={17} />{t("settings.newProfile")}</button>}>
      <DataTable headers={[t("settings.type"), t("settings.name"), t("settings.endpoint"), t("settings.profileDetail"), t("column.updated"), ""]} empty={t("settings.noProfiles")}>{(profiles.data ?? []).map(profile => <tr key={profile.id}><td><Status value={profile.kind} /></td><td><strong>{profile.name}</strong></td><td><code className="truncate-code" title={profile.endpoint}>{profile.endpoint}</code></td><td>{profile.kind === "codex" ? <code>{profile.model}</code> : <div className="cell-main"><span className="inline"><UserRound size={14} />{profile.username}</span><small>{profile.commit_name && profile.commit_email ? `${profile.commit_name} · ${profile.commit_email}` : t("settings.gitIdentityMissing")}</small></div>}</td><td>{relative(profile.updated_at)}</td><td><div className="row-actions"><button className="icon-button" title={t("settings.editProfile")} disabled={Boolean(settingsDeleteBusy)} onClick={() => openProfile(profile)}><Pencil size={15} /></button><button className="icon-button danger" title={t("settings.deleteProfile")} disabled={Boolean(settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "credential-profile", profile })}><Trash2 size={15} /></button></div></td></tr>)}</DataTable>
    </Section>
    <Section title={t("settings.vaultSets")} icon={<Database size={18} />} action={<button className="primary-button" onClick={() => setSecretDialog(true)}><Plus size={17} />{t("settings.newSecretSet")}</button>}><DataTable headers={[t("settings.name"), t("column.updated"), ""]} empty={t("settings.noSecretSets")}>{(secrets.data ?? []).map(item => <tr key={item.id}><td><span className="inline"><KeyRound size={14} /><strong>{item.name}</strong></span></td><td>{relative(item.updated_at)}</td><td><button className="icon-button danger" title={t("settings.deleteSecretSet")} disabled={Boolean(settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "secret-set", secret: item })}><X size={16} /></button></td></tr>)}</DataTable></Section>
    <Section title={t("settings.auditLog")} icon={<Clipboard size={18} />}><DataTable headers={[t("column.action"), t("column.resource"), t("column.address"), t("column.time")]} empty={t("settings.noAudit")}>{(audit.data ?? []).map(item => <tr key={item.id}><td><code>{item.action}</code></td><td>{item.resource_type}{item.resource_id ? ` · ${shortSHA(item.resource_id)}` : ""}</td><td><code>{item.ip_address}</code></td><td>{formatDate(item.occurred_at)}</td></tr>)}</DataTable>{audit.loadError && <div className="snapshot-notice warning"><AlertTriangle size={15} />{audit.loadError}</div>}{audit.hasMore && <div className="history-loader"><button type="button" className="secondary-button small" disabled={audit.loadingMore} onClick={() => void audit.loadMore()}>{audit.loadingMore ? <LoaderCircle className="spin" size={15} /> : <ArrowDownToLine size={15} />}{t(audit.loadingMore ? "settings.loadingMoreAudit" : "settings.loadMoreAudit")}</button></div>}</Section>
    <Dialog open={profileDialog} title={t(profileForm.id ? "settings.editProfile" : "settings.newProfile")} onClose={() => { if (!profileBusy) setProfileDialog(false); }} wide><form onSubmit={saveProfile}>
      <div className="segmented-control" role="tablist" aria-label={t("settings.type")}><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "codex"} aria-selected={profileForm.kind === "codex"} className={profileForm.kind === "codex" ? "active" : ""} onClick={() => changeProfileKind("codex")}><Code2 size={15} />{t("settings.codexType")}</button><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "git"} aria-selected={profileForm.kind === "git"} className={profileForm.kind === "git" ? "active" : ""} onClick={() => changeProfileKind("git")}><GitBranch size={15} />{t("settings.gitType")}</button></div>
      <div className="form-grid"><Field label={t("settings.name")}><input value={profileForm.name} onChange={e => setProfileForm({ ...profileForm, name: e.target.value })} required /></Field><Field label={t("settings.endpoint")}><input type="url" value={profileForm.endpoint} onChange={e => setProfileForm({ ...profileForm, endpoint: e.target.value })} required /></Field></div>
      {profileForm.kind === "codex" ? <Field label={t("server.codexModel")}><CodexModelPicker value={profileForm.model} onChange={model => setProfileForm({ ...profileForm, model })} required /></Field> : <><Field label={t("settings.gitUsername")}><input value={profileForm.username} onChange={e => setProfileForm({ ...profileForm, username: e.target.value })} autoComplete="username" required /></Field><div className="form-divider"><span>{t("settings.gitCommitIdentity")}</span></div><div className="form-grid"><Field label={t("settings.gitCommitName")}><input value={profileForm.commit_name} onChange={e => setProfileForm({ ...profileForm, commit_name: e.target.value })} autoComplete="name" required /></Field><Field label={t("settings.gitCommitEmail")}><input type="email" value={profileForm.commit_email} onChange={e => setProfileForm({ ...profileForm, commit_email: e.target.value })} autoComplete="email" placeholder={t("settings.gitCommitEmailPlaceholder")} required /></Field></div></>}
      <Field label={t(profileForm.kind === "codex" ? "server.codexAPIKey" : "settings.gitToken")}><input type="password" autoComplete="new-password" value={profileForm.secret} onChange={e => setProfileForm({ ...profileForm, secret: e.target.value })} placeholder={profileForm.id ? t("settings.keepExistingSecret") : ""} required={!profileForm.id} /></Field>
      <DialogActions><button type="button" className="secondary-button" disabled={profileBusy} onClick={() => setProfileDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={profileBusy}>{profileBusy ? <LoaderCircle className="spin" size={16} /> : <LockKeyhole size={16} />}{t("settings.encryptSave")}</button></DialogActions>
    </form></Dialog>
    <Dialog open={secretDialog} title={t("settings.secretSetTitle")} onClose={() => setSecretDialog(false)}><form onSubmit={submitSecretSet}><Field label={t("settings.name")}><input value={name} onChange={e => setName(e.target.value)} required /></Field><Field label={t("settings.environmentValues")}><textarea value={lines} onChange={e => setLines(e.target.value)} rows={8} placeholder={"DATABASE_URL=...\nAPI_TOKEN=..."} required /></Field><DialogActions><button type="button" className="secondary-button" onClick={() => setSecretDialog(false)}>{t("common.cancel")}</button><button className="primary-button"><KeyRound size={16} />{t("settings.encryptSave")}</button></DialogActions></form></Dialog>
    <ScheduledTaskDialog open={scheduledTarget !== null} task={scheduledTarget?.task} threads={threads.data ?? []} notify={notify} onClose={() => setScheduledTarget(null)} onSaved={scheduledTasks.reload} />
    {settingsConfirmation?.type === "scheduled-task" ? <ConfirmDialog open danger title={t("settings.deleteScheduledTask")} impact={t("settings.confirmDeleteScheduledTask", { name: settingsConfirmation.task.name })} confirmLabel={t("settings.deleteScheduledTask")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteScheduledTask(settingsConfirmation.task)} /> : settingsConfirmation?.type === "credential-profile" ? <ConfirmDialog open danger title={t("settings.deleteProfile")} impact={t("settings.confirmDeleteProfile", { name: settingsConfirmation.profile.name })} confirmLabel={t("settings.deleteProfile")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteProfile(settingsConfirmation.profile)} /> : settingsConfirmation?.type === "secret-set" ? <ConfirmDialog open danger title={t("settings.deleteSecretSet")} impact={t("settings.confirmDelete", { name: settingsConfirmation.secret.name })} confirmLabel={t("settings.deleteSecretSet")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteSecretSet(settingsConfirmation.secret)} /> : null}
  </div>;
}

export default SettingsPage;
