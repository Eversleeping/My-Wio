import { Code2, Database, GitBranch, KeyRound, LoaderCircle, LockKeyhole, Pencil, Plus, Trash2, UserRound, X } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { defaultCodexComposerPreferences } from "../../codexComposerPreferences";
import { CodexModelPicker } from "../../components/CodexModelPicker";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { DialogActions } from "../../components/Dialog";
import { Dialog, Field } from "../../components/FormUI";
import { DataTable, Section, Status } from "../../components/PageUI";
import { useResource } from "../../data/resources";
import { settings as settingEndpoints } from "../../data/settings";
import { relative } from "../../format";
import { useI18n } from "../../i18n";
import { message } from "../../text";
import type { CredentialProfile, SecretSet } from "../../types";

const defaultCodexModel = defaultCodexComposerPreferences.model;

type CredentialConfirmation =
  | { type: "credential-profile"; profile: CredentialProfile }
  | { type: "secret-set"; secret: SecretSet };

/** Server credential profiles and the Vault secret sets used by deployments. */
export function CredentialSettings({ notify, active }: { notify: (text: string) => void; active: boolean }) {
  const { t } = useI18n();
  const profiles = useResource(settingEndpoints.profiles());
  const secrets = useResource(settingEndpoints.secrets());
  const [profileDialog, setProfileDialog] = useState(false);
  const [profileBusy, setProfileBusy] = useState(false);
  const [profileForm, setProfileForm] = useState({ id: "", kind: "codex" as CredentialProfile["kind"], name: "", endpoint: "https://api.openai.com/v1", username: "", model: defaultCodexModel, commit_name: "", commit_email: "", secret: "" });
  const [secretDialog, setSecretDialog] = useState(false);
  const [name, setName] = useState("");
  const [lines, setLines] = useState("");
  const [settingsConfirmation, setSettingsConfirmation] = useState<CredentialConfirmation | null>(null);
  const [settingsDeleteBusy, setSettingsDeleteBusy] = useState("");
  const settingsDeleteBusyRef = useRef("");
  // A category that is no longer shown must not keep a modal and its focus trap open.
  useEffect(() => { if (!active) { setProfileDialog(false); setSecretDialog(false); setSettingsConfirmation(null); } }, [active]);
  const openProfile = (profile?: CredentialProfile, kind: CredentialProfile["kind"] = "codex") => {
    setProfileForm(profile ? { id: profile.id, kind: profile.kind, name: profile.name, endpoint: profile.endpoint, username: profile.username, model: profile.kind !== "git" ? profile.model || (profile.kind === "gemini" ? "auto" : profile.kind === "claude" ? "sonnet" : defaultCodexModel) : "", commit_name: profile.commit_name, commit_email: profile.commit_email, secret: "" } : { id: "", kind, name: "", endpoint: kind === "gemini" ? "https://generativelanguage.googleapis.com" : kind === "claude" ? "https://api.anthropic.com" : "https://api.openai.com/v1", username: "", model: kind === "gemini" ? "auto" : kind === "claude" ? "sonnet" : defaultCodexModel, commit_name: "", commit_email: "", secret: "" });
    setProfileDialog(true);
  };
  const changeProfileKind = (kind: CredentialProfile["kind"]) => setProfileForm(current => current.id ? current : { ...current, kind, endpoint: kind === "codex" ? "https://api.openai.com/v1" : kind === "gemini" ? "https://generativelanguage.googleapis.com" : kind === "claude" ? "https://api.anthropic.com" : "https://github.com", username: "", model: kind === "codex" ? defaultCodexModel : kind === "gemini" ? "auto" : kind === "claude" ? "sonnet" : "", commit_name: "", commit_email: "", secret: "" });
  const saveProfile = async (event: FormEvent) => {
    event.preventDefault(); setProfileBusy(true);
    try {
      await settingEndpoints.saveProfile(profileForm);
      setProfileDialog(false); notify(t("settings.profileSaved"));
    } catch (err) { notify(message(err)); } finally { setProfileBusy(false); }
  };
  const submitSecretSet = async (event: FormEvent) => { event.preventDefault(); const values: Record<string, string> = {}; for (const line of lines.split("\n")) { const index = line.indexOf("="); if (index > 0) values[line.slice(0, index).trim()] = line.slice(index + 1); } try { await settingEndpoints.createSecret(name, values); setSecretDialog(false); setLines(""); notify(t("settings.secretSaved")); } catch (err) { notify(message(err)); } };
  const deleteProfile = async (profile: CredentialProfile) => {
    if (settingsDeleteBusyRef.current) return;
    const busyKey = `credential-profile:${profile.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await settingEndpoints.removeProfile(profile.id); notify(t("settings.profileDeleted")); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  const deleteSecretSet = async (secret: SecretSet) => {
    if (settingsDeleteBusyRef.current) return;
    const busyKey = `secret-set:${secret.id}`;
    settingsDeleteBusyRef.current = busyKey;
    setSettingsDeleteBusy(busyKey);
    try { await settingEndpoints.removeSecret(secret.id); setSettingsConfirmation(null); } catch (err) { notify(message(err)); } finally { settingsDeleteBusyRef.current = ""; setSettingsDeleteBusy(""); }
  };
  return <>
    <Section title={t("settings.credentialProfiles")} icon={<KeyRound size={18} />} action={<button className="primary-button" onClick={() => openProfile()}><Plus size={17} />{t("settings.newProfile")}</button>}>
      <DataTable headers={[t("settings.type"), t("settings.name"), t("settings.endpoint"), t("settings.profileDetail"), t("column.updated"), ""]} empty={t("settings.noProfiles")}>{(profiles.data ?? []).map(profile => <tr key={profile.id}><td><Status value={profile.kind} /></td><td><strong>{profile.name}</strong></td><td><code className="truncate-code" title={profile.endpoint}>{profile.endpoint}</code></td><td>{profile.kind !== "git" ? <code>{profile.model}</code> : <div className="cell-main"><span className="inline"><UserRound size={14} />{profile.username}</span><small>{profile.commit_name && profile.commit_email ? `${profile.commit_name} · ${profile.commit_email}` : t("settings.gitIdentityMissing")}</small></div>}</td><td>{relative(profile.updated_at)}</td><td><div className="row-actions"><button className="icon-button" title={t("settings.editProfile")} disabled={Boolean(settingsDeleteBusy)} onClick={() => openProfile(profile)}><Pencil size={15} /></button><button className="icon-button danger" title={t("settings.deleteProfile")} disabled={Boolean(settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "credential-profile", profile })}><Trash2 size={15} /></button></div></td></tr>)}</DataTable>
    </Section>
    <Section title={t("settings.vaultSets")} icon={<Database size={18} />} action={<button className="primary-button" onClick={() => setSecretDialog(true)}><Plus size={17} />{t("settings.newSecretSet")}</button>}><DataTable headers={[t("settings.name"), t("column.updated"), ""]} empty={t("settings.noSecretSets")}>{(secrets.data ?? []).map(item => <tr key={item.id}><td><span className="inline"><KeyRound size={14} /><strong>{item.name}</strong></span></td><td>{relative(item.updated_at)}</td><td><button className="icon-button danger" title={t("settings.deleteSecretSet")} disabled={Boolean(settingsDeleteBusy)} onClick={() => setSettingsConfirmation({ type: "secret-set", secret: item })}><X size={16} /></button></td></tr>)}</DataTable></Section>
    <Dialog open={profileDialog} title={t(profileForm.id ? "settings.editProfile" : "settings.newProfile")} onClose={() => { if (!profileBusy) setProfileDialog(false); }} wide><form onSubmit={saveProfile}>
      <div className="segmented-control" role="tablist" aria-label={t("settings.type")}><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "codex"} aria-selected={profileForm.kind === "codex"} className={profileForm.kind === "codex" ? "active" : ""} onClick={() => changeProfileKind("codex")}><Code2 size={15} />{t("settings.codexType")}</button><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "gemini"} aria-selected={profileForm.kind === "gemini"} className={profileForm.kind === "gemini" ? "active" : ""} onClick={() => changeProfileKind("gemini")}><Code2 size={15} />{t("settings.geminiType")}</button><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "claude"} aria-selected={profileForm.kind === "claude"} className={profileForm.kind === "claude" ? "active" : ""} onClick={() => changeProfileKind("claude")}><Code2 size={15} />{t("settings.claudeType")}</button><button type="button" role="tab" disabled={Boolean(profileForm.id) && profileForm.kind !== "git"} aria-selected={profileForm.kind === "git"} className={profileForm.kind === "git" ? "active" : ""} onClick={() => changeProfileKind("git")}><GitBranch size={15} />{t("settings.gitType")}</button></div>
      <div className="form-grid"><Field label={t("settings.name")}><input value={profileForm.name} onChange={e => setProfileForm({ ...profileForm, name: e.target.value })} required /></Field><Field label={t("settings.endpoint")}><input type="url" value={profileForm.endpoint} onChange={e => setProfileForm({ ...profileForm, endpoint: e.target.value })} required /></Field></div>
      {profileForm.kind === "codex" ? <Field label={t("server.codexModel")}><CodexModelPicker value={profileForm.model} onChange={model => setProfileForm({ ...profileForm, model })} required /></Field> : profileForm.kind === "gemini" ? <><p className="gemini-settings-help">{t("settings.geminiAPIHelp")}</p><Field label={t("gemini.model")}><input value={profileForm.model} onChange={event => setProfileForm({ ...profileForm, model: event.target.value })} maxLength={128} required /></Field></> : profileForm.kind === "claude" ? <><p className="gemini-settings-help">{t("settings.claudeAPIHelp")}</p><Field label={t("claude.model")}><input value={profileForm.model} onChange={event => setProfileForm({ ...profileForm, model: event.target.value })} maxLength={128} required /></Field></> : <><Field label={t("settings.gitUsername")}><input value={profileForm.username} onChange={e => setProfileForm({ ...profileForm, username: e.target.value })} autoComplete="username" required /></Field><div className="form-divider"><span>{t("settings.gitCommitIdentity")}</span></div><div className="form-grid"><Field label={t("settings.gitCommitName")}><input value={profileForm.commit_name} onChange={e => setProfileForm({ ...profileForm, commit_name: e.target.value })} autoComplete="name" required /></Field><Field label={t("settings.gitCommitEmail")}><input type="email" value={profileForm.commit_email} onChange={e => setProfileForm({ ...profileForm, commit_email: e.target.value })} autoComplete="email" placeholder={t("settings.gitCommitEmailPlaceholder")} required /></Field></div></>}
      <Field label={t(profileForm.kind === "codex" ? "server.codexAPIKey" : profileForm.kind === "gemini" ? "settings.geminiAPIKey" : profileForm.kind === "claude" ? "settings.claudeAPIKey" : "settings.gitToken")}><input type="password" autoComplete="new-password" value={profileForm.secret} onChange={e => setProfileForm({ ...profileForm, secret: e.target.value })} placeholder={profileForm.id ? t("settings.keepExistingSecret") : ""} required={!profileForm.id} /></Field>
      <DialogActions><button type="button" className="secondary-button" disabled={profileBusy} onClick={() => setProfileDialog(false)}>{t("common.cancel")}</button><button className="primary-button" disabled={profileBusy}>{profileBusy ? <LoaderCircle className="spin" size={16} /> : <LockKeyhole size={16} />}{t("settings.encryptSave")}</button></DialogActions>
    </form></Dialog>
    <Dialog open={secretDialog} title={t("settings.secretSetTitle")} onClose={() => setSecretDialog(false)}><form onSubmit={submitSecretSet}><Field label={t("settings.name")}><input value={name} onChange={e => setName(e.target.value)} required /></Field><Field label={t("settings.environmentValues")}><textarea value={lines} onChange={e => setLines(e.target.value)} rows={8} placeholder={"DATABASE_URL=...\nAPI_TOKEN=..."} required /></Field><DialogActions><button type="button" className="secondary-button" onClick={() => setSecretDialog(false)}>{t("common.cancel")}</button><button className="primary-button"><KeyRound size={16} />{t("settings.encryptSave")}</button></DialogActions></form></Dialog>
    {settingsConfirmation?.type === "credential-profile" ? <ConfirmDialog open danger title={t("settings.deleteProfile")} impact={t("settings.confirmDeleteProfile", { name: settingsConfirmation.profile.name })} confirmLabel={t("settings.deleteProfile")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteProfile(settingsConfirmation.profile)} /> : settingsConfirmation?.type === "secret-set" ? <ConfirmDialog open danger title={t("settings.deleteSecretSet")} impact={t("settings.confirmDelete", { name: settingsConfirmation.secret.name })} confirmLabel={t("settings.deleteSecretSet")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={Boolean(settingsDeleteBusy)} onClose={() => setSettingsConfirmation(null)} onConfirm={() => deleteSecretSet(settingsConfirmation.secret)} /> : null}
  </>;
}
