import { useEffect, useState } from "react";
import { Check, Download, LoaderCircle, RefreshCw } from "lucide-react";
import { APIError, api, post } from "../api";
import { useI18n } from "../i18n";
import type { Server } from "../types";
import { Dialog, DialogActions } from "./Dialog";

type CLIKind = "codex" | "gemini";
interface CLIStatus {
  kind: CLIKind;
  version: string;
  ready: boolean;
  supported: boolean;
  operation?: { id: string; status: string; result: string };
  active_threads?: { id: string; title: string; status: string }[];
  blocking_operations?: { id: string; kind: string; status: string }[];
}
const pending = (status?: string) => ["queued", "delivered", "running"].includes(status ?? "");
const reportedVersion = (version: string) => version.replace(/^codex-cli\s+|^v/, "");

export function ServerCLIManager({ server, onClose, notify }: { server: Server; onClose: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const [items, setItems] = useState<CLIStatus[]>([]);
  const [kind, setKind] = useState<CLIKind | null>(null);
  const [versions, setVersions] = useState<string[]>([]);
  const [version, setVersion] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [loadingVersions, setLoadingVersions] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const data = await api<CLIStatus[]>(`/servers/${server.id}/ai-clis`, { signal: controller.signal });
        if (!controller.signal.aborted) { setItems(data); setStatusError(""); }
      } catch (failure) { if (!controller.signal.aborted) setStatusError(failure instanceof Error ? failure.message : String(failure)); }
      if (!controller.signal.aborted) timer = setTimeout(poll, 3000);
    };
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [server.id, reload]);
  useEffect(() => {
    if (!kind) return;
    const controller = new AbortController();
    setLoadingVersions(true); setError(""); setVersions([]); setVersion("");
    void api<{ versions: string[]; target_version?: string }>(`/ai-clis/${kind}/versions`, { signal: controller.signal }).then(data => {
      if (!controller.signal.aborted) { setVersions(data.versions); setVersion(data.target_version && data.versions.includes(data.target_version) ? data.target_version : data.versions[0] ?? ""); }
    }).catch(failure => { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : String(failure)); })
      .finally(() => { if (!controller.signal.aborted) setLoadingVersions(false); });
    return () => controller.abort();
  }, [kind, refresh]);
  const selected = items.find(item => item.kind === kind);
  const installing = items.some(item => pending(item.operation?.status));
  const blocked = !!selected?.active_threads?.length || !!selected?.blocking_operations?.length;
  const same = selected && reportedVersion(selected.version) === version;
  const name = kind === "codex" ? "Codex" : "Gemini";
  const failureMessage = (failure: unknown) => failure instanceof Error && ((failure instanceof APIError && failure.code === "cli_install_busy") || failure.message === "finish active turns and AI CLI installations on this server first") ? t("server.cliBusy") : failure instanceof Error ? failure.message : String(failure);
  const install = async () => {
    if (!kind || busy || blocked || installing) return;
    setBusy(true); setError("");
    try {
      const queued = await post<{ operation_id: string }>(`/servers/${server.id}/ai-clis/${kind}/install`, { version });
      setItems(current => current.map(item => item.kind === kind ? { ...item, operation: { id: queued.operation_id, status: "queued", result: "" } } : item));
      setReload(value => value + 1);
      notify(t("server.cliQueued", { name, version }));
    } catch (failure) { setError(failureMessage(failure)); setReload(value => value + 1); }
    finally { setBusy(false); }
  };
  return <Dialog open title={t("server.manageCLITitle", { name: server.name })} onClose={onClose} dismissDisabled={busy} closeLabel={t("common.close")} wide>
    <div className="server-cli-manager">
      <p className="muted">{t("server.cliDescription")}</p>
      {statusError && <div className="error-banner" role="alert">{statusError}</div>}
      {server.status !== "online" && <p className="error-banner">{t("server.updateOffline")}</p>}
      <ul className="server-cli-list" aria-label={t("server.manageCLI")}>{(["codex", "gemini"] as const).map(value => {
        const item = items.find(entry => entry.kind === value);
        const active = pending(item?.operation?.status);
        const label = value === "codex" ? "Codex" : "Gemini";
        const threads = item?.active_threads ?? [];
        const maintenance = !!item?.blocking_operations?.length;
        return <li className="server-cli-entry" key={value}>
          <div className="server-cli-row">
            <strong>{label} CLI</strong>
            <code>{item?.version ? reportedVersion(item.version) : item ? t("server.cliNotInstalled") : t("common.loading")}</code>
            <div className="server-cli-state">
              <span className={active || maintenance || threads.length ? "muted" : item?.ready ? "inline-success" : "muted"}>{active ? <><LoaderCircle className="spin" size={13} />{t("server.cliInstalling")}</> : threads.length ? t("server.cliActiveTurns", { count: threads.length }) : maintenance ? t("server.cliMaintenance") : item?.ready ? <><Check size={14} />{t("server.cliReady")}</> : item ? t(item.supported ? "server.cliNotInstalled" : "server.cliUnknown") : t("common.loading")}</span>
              {item?.operation && !active && <small className={item.operation.status === "failed" ? "error-text" : "muted"}>{t("server.cliLastInstall", { status: t(`status.${item.operation.status}`) })}</small>}
            </div>
            <button type="button" className="secondary-button small" aria-label={t("server.cliChooseVersion", { name: label })} aria-expanded={kind === value} aria-controls={`cli-version-${value}`} disabled={busy || !item} onClick={() => setKind(kind === value ? null : value)}>{t(item?.ready ? "server.cliSwitchAction" : "server.cliInstallAction")}</button>
          </div>
          {kind === value && <div className="server-cli-options" id={`cli-version-${value}`}>
            {item && !item.supported && <p className="warning-list">{t("server.cliUpgradeAgent")}</p>}
            {threads.length > 0 && <div className="server-cli-blockers" role="status"><p>{t("server.cliFinishTurns", { name: label })}</p><ul>{threads.map(thread => <li key={thread.id}><a href={`?view=${value}&thread=${encodeURIComponent(thread.id)}`}>{thread.title || thread.id}</a><span className="muted">{t(`status.${thread.status}`)}</span></li>)}</ul></div>}
            {maintenance && <p className="warning-list">{t("server.cliMaintenanceNotice")}</p>}
            <div className="server-cli-version-form"><label className="field"><span>{t("server.cliVersion", { name: label })}</span><select value={version} onChange={event => setVersion(event.target.value)} disabled={busy || loadingVersions || versions.length === 0}>
              {versions.length === 0 && <option value="">{loadingVersions ? t("common.loading") : t("common.unavailable")}</option>}
              {versions.map((entry, index) => <option key={entry} value={entry}>{entry}{index === 0 ? ` (${t("server.cliLatest")})` : ""}</option>)}
            </select></label><button className="secondary-button small" disabled={busy || loadingVersions} onClick={() => setRefresh(current => current + 1)}><RefreshCw size={15} className={loadingVersions ? "spin" : ""} />{t("server.cliRefresh")}</button><button className="primary-button small" disabled={busy || loadingVersions || !!statusError || installing || blocked || !version || !selected?.supported || server.status !== "online" || same} onClick={() => void install()}>
              {busy || installing ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}{same ? t("server.cliCurrent") : installing ? t("server.cliInstalling") : t(selected?.ready ? "server.cliSwitch" : "server.cliInstall", { name, version })}
            </button></div>
            {item?.operation?.status === "failed" && <div className="error-banner" role="alert">{failureMessage(new Error(item.operation.result || t("server.cliFailed")))}</div>}
            {error && <div className="error-banner" role="alert">{error}</div>}
          </div>}
        </li>;
      })}</ul>
      <p className="muted server-cli-notice">{t("server.cliVersionNotice")}</p>
      <DialogActions><button className="secondary-button" disabled={busy} onClick={onClose}>{t("common.close")}</button></DialogActions>
    </div>
  </Dialog>;
}
