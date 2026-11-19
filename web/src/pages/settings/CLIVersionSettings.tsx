import { Check, LoaderCircle, RefreshCw, ShieldCheck, SquareTerminal } from "lucide-react";
import { useEffect, useState } from "react";
import { Section } from "../../components/PageUI";
import { useResource } from "../../data/resources";
import { settings as settingEndpoints } from "../../data/settings";
import { useI18n } from "../../i18n";
import { engineModule } from "../../sessions/engineModules";
import type { Engine } from "../../sessions/types";
import { message } from "../../text";

type Notify = (text: string) => void;

// The Codex block was written before the Gemini and Claude ones and gives its
// feedback differently. The rows share one implementation; these flags keep
// what each engine showed before the three blocks were merged into one list.
const rows: ReadonlyArray<{ engine: Engine; select: string; applied: string; codexFeedback?: true }> = [
  { engine: "codex", select: "settings.selectCodexVersion", applied: "settings.codexVersionApplied", codexFeedback: true },
  { engine: "gemini", select: "settings.selectGeminiVersion", applied: "settings.geminiVersionApplied" },
  { engine: "claude", select: "settings.selectClaudeVersion", applied: "settings.claudeVersionApplied" },
];

function CLIVersionRow({ engine, select, applied, codexFeedback, notify }: typeof rows[number] & { notify: Notify }) {
  const { t } = useI18n();
  const settings = useResource(settingEndpoints.cli(engine));
  const [selectedVersion, setSelectedVersion] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const name = `${engineModule(engine).label} CLI`;
  const target = settings.data?.target_version;
  const released = settings.data?.versions?.length ? settings.data.versions : null;
  const versions = released ?? (target ? [target] : []);
  useEffect(() => { if (settings.data) setSelectedVersion(settings.data.target_version); }, [settings.data]);
  const update = async (check: boolean) => {
    if (!check && !selectedVersion) return;
    setBusy(true); setFailure("");
    try {
      const result = await (check ? settingEndpoints.checkUpdates(engine) : settingEndpoints.selectVersion(engine, selectedVersion));
      if (codexFeedback && check) notify(t(result.updated ? "settings.codexUpdateFound" : "settings.codexAlreadyLatest", { version: result.latest_version ?? result.target_version }));
      else notify(t(applied, { version: result.target_version }));
    } catch (error) {
      // Codex reports failures through the page toast, the other engines inline.
      if (codexFeedback) notify(message(error));
      else setFailure(error instanceof Error ? error.message : String(error));
    } finally { setBusy(false); }
  };
  const error = codexFeedback ? "" : failure || settings.error;
  return <div className="cli-version-row" role="group" aria-label={name}>
    <div className="cli-version-summary">
      <strong>{name}</strong>
      <span><small>{t("settings.codexTargetVersion")}</small><code>{target ?? "-"}</code><em><ShieldCheck size={13} />{t("settings.codexStableRelease")}</em></span>
    </div>
    <div className="cli-version-actions">
      <select aria-label={t(select)} value={selectedVersion} disabled={busy || !versions.length} onChange={event => setSelectedVersion(event.target.value)}>{versions.map((version, index) => <option key={version} value={version}>{index === 0 && (released || codexFeedback) ? t("settings.latestCodexVersion", { version }) : version}</option>)}</select>
      <button className="secondary-button" disabled={busy || !selectedVersion || selectedVersion === target} onClick={() => void update(false)}><Check size={16} />{t("settings.applyCodexVersion")}</button>
      <button className="secondary-button" disabled={busy || !settings.data} onClick={() => void update(true)}>{busy ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}{t(busy ? "settings.checkingCodexUpdates" : "settings.checkCodexUpdates")}</button>
    </div>
    {error && <p className="error-banner" role="alert">{error}</p>}
  </div>;
}

/** Target versions of the three engine CLIs, one row per engine. */
export function CLIVersionSettings({ notify }: { notify: Notify }) {
  const { t } = useI18n();
  return <Section title={t("settings.cliVersions")} icon={<SquareTerminal size={18} />}>
    <p className="settings-note">{t("settings.cliVersionHelp")}</p>
    <div className="cli-version-list">{rows.map(row => <CLIVersionRow key={row.engine} {...row} notify={notify} />)}</div>
  </Section>;
}
