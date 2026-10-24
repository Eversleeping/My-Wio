import { useEffect, useState } from "react";
import { Check, LoaderCircle, RefreshCw, ShieldCheck, Sparkles } from "lucide-react";
import { post } from "../api";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import type { CodexCLISettings } from "../types";
import { Section } from "./PageUI";

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
