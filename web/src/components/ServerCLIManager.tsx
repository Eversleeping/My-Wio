import { useEffect, useId, useState } from "react";
import { Check, LoaderCircle, RefreshCw } from "lucide-react";
import { APIError, api, post } from "../api";
import { useI18n } from "../i18n";
import { formatCLIVersion } from "../format";
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
const kinds = ["codex", "gemini"] as const;
type Versions = { entries: string[]; version: string; loading: boolean; error: string };
const emptyVersions = (): Versions => ({ entries: [], version: "", loading: true, error: "" });

export function ServerCLIManager({ server, onClose, notify }: { server: Server; onClose: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const confirmationDescriptionID = useId();
  const [items, setItems] = useState<CLIStatus[]>([]);
  const [versions, setVersions] = useState<Record<CLIKind, Versions>>({ codex: emptyVersions(), gemini: emptyVersions() });
  const [confirmation, setConfirmation] = useState<{ kind: CLIKind; version: string } | null>(null);
  const [refresh, setRefresh] = useState(0);
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
    const controller = new AbortController();
    for (const kind of kinds) {
      setVersions(current => ({ ...current, [kind]: { ...current[kind], loading: true, error: "" } }));
      void api<{ versions: string[]; target_version?: string }>(`/ai-clis/${kind}/versions`, { signal: controller.signal }).then(data => {
        if (controller.signal.aborted) return;
        const entries = data.versions.slice(0, 3);
        setVersions(current => ({ ...current, [kind]: {
          entries, loading: false, error: "",
          version: entries.includes(current[kind].version) ? current[kind].version : data.target_version && entries.includes(data.target_version) ? data.target_version : entries[0] ?? ""
        } }));
      }).catch(failure => {
        if (!controller.signal.aborted) setVersions(current => ({ ...current, [kind]: { ...emptyVersions(), loading: false, error: failure instanceof Error ? failure.message : String(failure) } }));
      });
    }
    return () => controller.abort();
  }, [server.id, refresh]);
  const installing = items.some(item => pending(item.operation?.status));
  const canInstall = (kind: CLIKind, version: string) => {
    const item = items.find(entry => entry.kind === kind);
    return !busy && !statusError && !installing && !versions[kind].loading && versions[kind].entries.includes(version)
      && !!item?.supported && !item.active_threads?.length && !item.blocking_operations?.length
      && server.status === "online" && !!version && formatCLIVersion(item.version) !== version;
  };
  const failureMessage = (failure: unknown) => failure instanceof Error && ((failure instanceof APIError && failure.code === "cli_install_busy") || failure.message === "finish active turns and AI CLI installations on this server first") ? t("server.cliBusy") : failure instanceof Error ? failure.message : String(failure);
  const install = async () => {
    if (!confirmation || !canInstall(confirmation.kind, confirmation.version)) return;
    const { kind, version } = confirmation;
    const name = kind === "codex" ? "Codex" : "Gemini";
    setBusy(true); setError("");
    try {
      const queued = await post<{ operation_id: string }>(`/servers/${server.id}/ai-clis/${kind}/install`, { version });
      setItems(current => current.map(item => item.kind === kind ? { ...item, operation: { id: queued.operation_id, status: "queued", result: "" } } : item));
      setReload(value => value + 1);
      notify(t("server.cliQueued", { name, version }));
    } catch (failure) { setError(failureMessage(failure)); setReload(value => value + 1); }
    finally { setBusy(false); setConfirmation(null); }
  };
  const confirmingItem = items.find(item => item.kind === confirmation?.kind);
  const confirmingName = confirmation?.kind === "codex" ? "Codex" : "Gemini";
  return <><Dialog open title={t("server.manageCLITitle", { name: server.name })} onClose={onClose} dismissDisabled={busy} closeLabel={t("common.close")} wide>
    <div className="server-cli-manager">
      <div className="server-cli-heading"><p className="muted">{t("server.cliDescription")}</p><button type="button" className="icon-button" aria-label={t("server.cliRefresh")} title={t("server.cliRefresh")} disabled={busy || !!confirmation || kinds.some(kind => versions[kind].loading)} onClick={() => setRefresh(current => current + 1)}><RefreshCw size={16} className={kinds.some(kind => versions[kind].loading) ? "spin" : undefined} /></button></div>
      {statusError && <div className="error-banner" role="alert">{statusError}</div>}
      {server.status !== "online" && <p className="error-banner">{t("server.updateOffline")}</p>}
      <div className="server-cli-list"><table className="server-cli-table" aria-label={t("server.manageCLI")}>
        <thead><tr><th scope="col">{t("server.cliAgentName")}</th><th scope="col">{t("server.cliVersionNumber")}</th><th scope="col">{t("column.status")}</th><th scope="col">{t("common.actions")}</th></tr></thead>
        <tbody>{kinds.map(kind => {
          const item = items.find(entry => entry.kind === kind);
          const active = pending(item?.operation?.status);
          const label = kind === "codex" ? "Codex" : "Gemini";
          const threads = item?.active_threads ?? [];
          const maintenance = !!item?.blocking_operations?.length;
          const choices = versions[kind];
          const current = item?.version ? formatCLIVersion(item.version) : "";
          return <tr key={kind}>
            <th scope="row">{label} CLI</th>
            <td><select aria-label={t("server.cliVersion", { name: label })} value={choices.version} onChange={event => setVersions(current => ({ ...current, [kind]: { ...current[kind], version: event.target.value } }))} disabled={busy || !!confirmation || choices.loading || !choices.entries.length}>
              {!choices.entries.length && <option value="">{choices.loading ? t("common.loading") : t("common.unavailable")}</option>}
              {choices.entries.map(entry => <option key={entry} value={entry}>{entry}{entry === current ? ` (${t("server.cliCurrent")})` : ""}</option>)}
            </select>{current && choices.version !== current && <small className="server-cli-current muted">{t("server.cliCurrent")}: {current}</small>}</td>
            <td><div className="server-cli-state">
              <span className={active || maintenance || threads.length ? "muted" : item?.ready ? "inline-success" : "muted"}>{active ? <><LoaderCircle className="spin" size={13} />{t("server.cliInstalling")}</> : threads.length ? t("server.cliActiveTurns", { count: threads.length }) : maintenance ? t("server.cliMaintenance") : item?.ready ? <><Check size={14} />{t("server.cliReady")}</> : item ? t(item.supported ? "server.cliNotInstalled" : "server.cliUnknown") : t("common.loading")}</span>
              {item?.operation && !active && <small className={item.operation.status === "failed" ? "error-text" : "muted"}>{t("server.cliLastInstall", { status: t(`status.${item.operation.status}`) })}</small>}
            </div></td>
            <td><button type="button" className="secondary-button small" aria-label={t("server.cliChooseVersion", { name: label })} disabled={!canInstall(kind, choices.version)} onClick={() => { setError(""); setConfirmation({ kind, version: choices.version }); }}>{active ? t("server.cliInstalling") : t(item?.ready ? "server.cliSwitchAction" : "server.cliInstallAction")}</button></td>
          </tr>;
        })}</tbody>
      </table></div>
      {kinds.map(kind => {
        const item = items.find(entry => entry.kind === kind);
        const label = kind === "codex" ? "Codex" : "Gemini";
        const threads = item?.active_threads ?? [];
        return <div className="server-cli-feedback" key={kind}>
          {versions[kind].error && <div className="error-banner" role="alert">{label}: {versions[kind].error}</div>}
          {item && !item.supported && <p className="warning-list">{label}: {t("server.cliUpgradeAgent")}</p>}
          {threads.length > 0 && <div className="server-cli-blockers" role="status"><p>{t("server.cliFinishTurns", { name: label })}</p><ul>{threads.map(thread => <li key={thread.id}><a href={`?view=${kind}&thread=${encodeURIComponent(thread.id)}`}>{thread.title || thread.id}</a><span className="muted">{t(`status.${thread.status}`)}</span></li>)}</ul></div>}
          {!!item?.blocking_operations?.length && <p className="warning-list">{label}: {t("server.cliMaintenanceNotice")}</p>}
          {item?.operation?.status === "failed" && <div className="error-banner" role="alert">{label}: {failureMessage(new Error(item.operation.result || t("server.cliFailed")))}</div>}
        </div>;
      })}
      {error && <div className="error-banner" role="alert">{error}</div>}
      <p className="muted server-cli-notice">{t("server.cliVersionNotice")}</p>
      <DialogActions><button className="secondary-button" disabled={busy} onClick={onClose}>{t("common.close")}</button></DialogActions>
    </div>
  </Dialog>
    <Dialog open={!!confirmation} title={t("server.cliConfirmTitle", { name: confirmingName })} onClose={() => setConfirmation(null)} dismissDisabled={busy} closeLabel={t("common.close")} ariaDescribedBy={confirmationDescriptionID}>
      {confirmation && <div><p id={confirmationDescriptionID} className="server-cli-confirm-description">{t("server.cliConfirmDescription", { server: server.name, name: confirmingName, current: formatCLIVersion(confirmingItem?.version ?? "") || t("server.cliNotInstalled"), version: confirmation.version })}</p>
        {!busy && !canInstall(confirmation.kind, confirmation.version) && <p className="warning-list">{t("server.cliBusy")}</p>}
        <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => setConfirmation(null)} autoFocus>{t("common.cancel")}</button><button type="button" className="primary-button" disabled={!canInstall(confirmation.kind, confirmation.version)} onClick={() => void install()}>{busy && <LoaderCircle className="spin" size={16} />}{t(confirmingItem?.ready ? "server.cliSwitch" : "server.cliInstall", { name: confirmingName, version: confirmation.version })}</button></DialogActions>
      </div>}
    </Dialog>
  </>;
}
