import { useEffect, useRef, useState } from "react";
import { Check, KeyRound, LoaderCircle, RefreshCw, ShieldCheck, Sparkles } from "lucide-react";
import { api, post } from "../api";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import type { CodexCLISettings, CredentialProfile, Server } from "../types";
import { Dialog, DialogActions } from "./Dialog";
import { Section } from "./PageUI";

type Operation = { id: string; status: string; result: string };
const pending = (status: string) => ["queued", "delivered", "running"].includes(status);
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);

export function GeminiSettings({ realtime, notify }: { realtime: number; notify: (message: string) => void }) {
  const { t } = useI18n();
  const settings = useData<CodexCLISettings>("/settings/gemini-cli", realtime);
  const [selectedVersion, setSelectedVersion] = useState("");
  const [versionBusy, setVersionBusy] = useState(false);
  const [versionError, setVersionError] = useState("");
  const versions = settings.data?.versions?.length ? settings.data.versions : settings.data ? [settings.data.target_version] : [];
  useEffect(() => { if (settings.data) setSelectedVersion(settings.data.target_version); }, [settings.data]);
  const updateVersion = async (check: boolean) => {
    setVersionBusy(true); setVersionError("");
    try {
      const result = await post<CodexCLISettings>(`/settings/gemini-cli/${check ? "check-updates" : "select-version"}`, check ? {} : { version: selectedVersion });
      settings.reload();
      notify(t("settings.geminiVersionApplied", { version: result.target_version }));
    } catch (failure) { setVersionError(errorMessage(failure)); }
    finally { setVersionBusy(false); }
  };
  return <Section title={t("settings.geminiManagement")} icon={<Sparkles size={18} />}>
    {(settings.error || versionError) && <p className="error-banner" role="alert">{versionError || settings.error}</p>}
    <div className="codex-version-control">
      <div className="codex-version-summary"><small>{t("settings.codexTargetVersion")}</small><strong>{settings.data?.target_version ?? "-"}</strong><span><ShieldCheck size={14} />{t("settings.codexStableRelease")}</span></div>
      <div className="codex-version-actions"><select aria-label={t("settings.selectGeminiVersion")} value={selectedVersion} disabled={versionBusy || !versions.length} onChange={event => setSelectedVersion(event.target.value)}>{versions.map((version, index) => <option key={version} value={version}>{index === 0 && settings.data?.versions?.length ? t("settings.latestCodexVersion", { version }) : version}</option>)}</select>
        <button className="secondary-button" disabled={versionBusy || !selectedVersion || selectedVersion === settings.data?.target_version} onClick={() => void updateVersion(false)}><Check size={16} />{t("settings.applyCodexVersion")}</button>
        <button className="primary-button" disabled={versionBusy || !settings.data} onClick={() => void updateVersion(true)}>{versionBusy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t(versionBusy ? "settings.checkingCodexUpdates" : "settings.checkCodexUpdates")}</button>
      </div>
    </div>
  </Section>;
}

export function GeminiCredentialApply({ realtime, profiles }: { realtime: number; profiles: CredentialProfile[] }) {
  const { t } = useI18n();
  const servers = useData<Server[]>("/servers", realtime);
  const [dialog, setDialog] = useState(false);
  const [serverID, setServerID] = useState("");
  const [profileID, setProfileID] = useState("");
  const [busy, setBusy] = useState(false);
  const submitRef = useRef(false);
  const [error, setError] = useState("");
  const [operation, setOperation] = useState<Operation | null>(null);
  const geminiProfiles = profiles.filter(profile => profile.kind === "gemini");
  const selectedServer = servers.data?.find(server => server.id === serverID);
  const applying = busy || !!operation && pending(operation.status);
  useEffect(() => {
    if (!operation || !pending(operation.status)) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const result = await api<Operation>(`/operations/${operation.id}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        setOperation(result); setError("");
        if (!pending(result.status)) { servers.reload(); return; }
      } catch (failure) { if (!controller.signal.aborted) setError(errorMessage(failure)); }
      if (!controller.signal.aborted) timer = setTimeout(poll, 2000);
    };
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [operation?.id, operation?.status, servers.reload]);
  const apply = async () => {
    if (submitRef.current || applying || !serverID || !profileID) return;
    submitRef.current = true; setBusy(true); setError(""); setOperation(null);
    try {
      const result = await post<{ operation_id: string }>(`/servers/${serverID}/gemini-credential-profile`, { profile_id: profileID });
      setOperation({ id: result.operation_id, status: "queued", result: "" });
    } catch (failure) { setError(errorMessage(failure)); }
    finally { setBusy(false); submitRef.current = false; }
  };
  return <><button className="secondary-button" onClick={() => { setDialog(true); if (!applying) { setError(""); setOperation(null); setServerID(""); setProfileID(geminiProfiles[0]?.id ?? ""); } }}><KeyRound size={16} />{t("settings.applyGeminiProfile")}</button>
    <Dialog open={dialog} title={t("settings.applyGeminiProfile")} onClose={() => setDialog(false)} dismissDisabled={busy} closeLabel={t("common.close")}>
      <form onSubmit={event => { event.preventDefault(); void apply(); }}>
        <p className="gemini-settings-help">{t("settings.geminiApplyHelp")}</p>
        {servers.error && <p className="error-banner" role="alert">{servers.error}</p>}
        <label className="field"><span>{t("settings.geminiServer")}</span><select value={serverID} required disabled={applying} onChange={event => { setServerID(event.target.value); setOperation(null); }}><option value="">{t("settings.selectGeminiServer")}</option>{(servers.data ?? []).map(server => <option key={server.id} value={server.id} disabled={server.status !== "online" || !server.gemini_credentials_supported}>{server.name} · {server.status !== "online" ? t("status.offline") : !server.gemini_credentials_supported ? t("settings.geminiUpgradeAgent") : server.gemini_profile_name || t("settings.geminiUnconfigured")}</option>)}</select></label>
        {selectedServer && <p className="muted">{t("settings.geminiCurrentProfile", { name: selectedServer.gemini_profile_name || t("settings.geminiUnconfigured") })}</p>}
        <label className="field"><span>{t("settings.geminiType")}</span><select value={profileID} required disabled={applying} onChange={event => { setProfileID(event.target.value); setOperation(null); }}><option value="">{t("settings.selectGeminiProfile")}</option>{geminiProfiles.map(profile => <option key={profile.id} value={profile.id}>{profile.name} · {profile.model}</option>)}</select></label>
        {geminiProfiles.length === 0 && <p className="security-notice">{t("settings.noGeminiProfiles")}</p>}
        {error && <p className="error-banner" role="alert">{error}</p>}
        {operation && <p role="status" className={operation.status === "failed" ? "error-banner" : "security-notice"}>{t(`status.${operation.status}`)}{operation.result ? ` · ${operation.result}` : ""}</p>}
        <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => setDialog(false)}>{t("common.close")}</button><button className="primary-button" disabled={applying || !serverID || !profileID || selectedServer?.status !== "online" || !selectedServer?.gemini_credentials_supported}>{applying ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}{t("settings.applyGeminiProfile")}</button></DialogActions>
      </form>
    </Dialog>
  </>;
}
