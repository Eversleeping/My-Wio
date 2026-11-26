import { useEffect, useId, useRef, useState } from "react";
import { Check, LoaderCircle, RefreshCw } from "lucide-react";
import { APIError, api, post } from "../api";
import { useI18n } from "../i18n";
import { formatCLIVersion } from "../format";
import type { Server } from "../types";
import { Dialog, DialogActions } from "./Dialog";

type CLIKind = "codex" | "gemini" | "claude";
interface CLIStatus {
  kind: CLIKind;
  version: string;
  ready: boolean;
  supported: boolean;
  parallel_supported?: boolean;
  operation?: { id: string; status: string; result: string };
  active_threads?: { id: string; title: string; status: string }[];
  blocking_operations?: { id: string; kind: string; status: string }[];
}
const pending = (status?: string) => ["queued", "delivered", "running"].includes(status ?? "");
const kinds = ["codex", "gemini", "claude"] as const;
const cliName = (kind: CLIKind) => kind === "codex" ? "Codex" : kind === "gemini" ? "Gemini" : "Claude";
type Versions = { entries: string[]; version: string; loading: boolean; error: string };
const emptyVersions = (): Versions => ({ entries: [], version: "", loading: true, error: "" });
type UpdateTarget = { kind: CLIKind; version: string };
const newerVersion = (latest: string, current: string) => {
  if (!/^\d+\.\d+\.\d+$/.test(current)) return false;
  const parts = current.split(".").map(Number);
  const difference = latest.split(".").map(Number).map((part, index) => part - parts[index]).find(part => part !== 0);
  return difference !== undefined && difference > 0;
};
const pause = (signal: AbortSignal) => new Promise<void>((resolve, reject) => {
  const abort = () => { clearTimeout(timer); reject(signal.reason); };
  const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 3000);
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
});

export function ServerCLIManager({ server, onClose, notify }: { server: Server; onClose: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const confirmationDescriptionID = useId();
  const [items, setItems] = useState<CLIStatus[]>([]);
  const [versions, setVersions] = useState<Record<CLIKind, Versions>>({ codex: emptyVersions(), gemini: emptyVersions(), claude: emptyVersions() });
  const [confirmation, setConfirmation] = useState<{ kind: CLIKind; version: string } | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const [batchPlan, setBatchPlan] = useState<UpdateTarget[] | null>(null);
  const [batchProgress, setBatchProgress] = useState<{ name: string; completed: number; total: number } | null>(null);
  const batchController = useRef<AbortController | null>(null);
  const [error, setError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [reload, setReload] = useState(0);
  const batching = batchProgress !== null;
  const locked = busy || batching;
  const confirming = !!confirmation || !!batchPlan;
  useEffect(() => () => { batchController.current?.abort(); }, [server.id]);
  useEffect(() => {
    if (batching) return;
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
  }, [server.id, reload, batching]);
  useEffect(() => {
    const controller = new AbortController();
    for (const kind of kinds) {
      setVersions(current => ({ ...current, [kind]: { ...current[kind], loading: true, error: "" } }));
      void api<{ versions: string[]; target_version?: string }>(`/ai-clis/${kind}/versions`, { signal: controller.signal }).then(data => {
        if (controller.signal.aborted) return;
        const entries = data.versions.slice(0, 3);
        if (data.target_version && data.versions.includes(data.target_version) && !entries.includes(data.target_version)) entries.push(data.target_version);
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
  const parallel = items.length === kinds.length && items.every(item => item.parallel_supported);
  const batchNotice = t(parallel ? "server.cliUpdateAllParallelNotice" : "server.cliUpdateAllNotice");
  const canInstall = (kind: CLIKind, version: string) => {
    const item = items.find(entry => entry.kind === kind);
    return !locked && !statusError && !items.some(item => pending(item.operation?.status) && (!parallel || item.kind === kind)) && !versions[kind].loading && versions[kind].entries.includes(version)
      && !!item?.supported && !item.active_threads?.length && !item.blocking_operations?.length
      && server.status === "online" && !!version && formatCLIVersion(item.version) !== version;
  };
  const updateTargets = kinds.flatMap(kind => {
    const item = items.find(entry => entry.kind === kind);
    const version = versions[kind].entries[0];
    return item?.supported && item.ready && version && newerVersion(version, formatCLIVersion(item.version)) ? [{ kind, version }] : [];
  });
  const canUpdate = (targets: UpdateTarget[]) => targets.length > 0
    && !kinds.some(kind => versions[kind].loading || (items.some(item => item.kind === kind && item.supported && item.ready) && !!versions[kind].error))
    && targets.every(target => canInstall(target.kind, target.version));
  const failureMessage = (failure: unknown) => failure instanceof Error && ((failure instanceof APIError && failure.code === "cli_install_busy") || failure.message === "finish active turns and AI CLI installations on this server first") ? t("server.cliBusy") : failure instanceof Error ? failure.message : String(failure);
  const install = async () => {
    if (!confirmation || !canInstall(confirmation.kind, confirmation.version)) return;
    const { kind, version } = confirmation;
    const name = cliName(kind);
    setBusy(true); setError("");
    try {
      const queued = await post<{ operation_id: string }>(`/servers/${server.id}/ai-clis/${kind}/install`, { version });
      setItems(current => current.map(item => item.kind === kind ? { ...item, operation: { id: queued.operation_id, status: "queued", result: "" } } : item));
      setReload(value => value + 1);
      notify(t("server.cliQueued", { name, version }));
    } catch (failure) { setError(failureMessage(failure)); setReload(value => value + 1); }
    finally { setBusy(false); setConfirmation(null); }
  };
  const updateAll = async () => {
    if (!batchPlan || batchController.current || !canUpdate(batchPlan)) return;
    const targets = batchPlan;
    const controller = new AbortController();
    const { signal } = controller;
    batchController.current = controller;
    setBatchPlan(null); setError("");
    let name = "";
    let completed = 0;
    const loadStatus = async () => {
      const data = await api<CLIStatus[]>(`/servers/${server.id}/ai-clis`, { signal });
      signal.throwIfAborted();
      setItems(data); setStatusError("");
      return data;
    };
    setBatchProgress({ name: targets.map(target => cliName(target.kind)).join(" / "), completed, total: targets.length });
    const updateTarget = async (target: UpdateTarget) => {
      const data = await loadStatus();
      const item = data.find(entry => entry.kind === target.kind);
      if (!item?.supported || !item.ready || item.active_threads?.length || item.blocking_operations?.length || data.some(entry => pending(entry.operation?.status) && (!parallel || entry.kind === target.kind))) throw new Error(t("server.cliBusy"));
      // A heartbeat may have reported an update performed outside this dialog.
      if (newerVersion(target.version, formatCLIVersion(item.version))) {
        const queued = await post<{ operation_id: string }>(`/servers/${server.id}/ai-clis/${target.kind}/install`, { version: target.version }, { signal });
        signal.throwIfAborted();
        setItems(current => current.map(entry => entry.kind === target.kind ? { ...entry, operation: { id: queued.operation_id, status: "queued", result: "" } } : entry));
        while (true) {
          const status = (await loadStatus()).find(entry => entry.kind === target.kind)?.operation;
          // Never treat an older installation's result as completion of this one.
          if (status?.id === queued.operation_id && !pending(status.status)) {
            if (status.status !== "succeeded") throw new Error(status.result || t("server.cliFailed"));
            break;
          }
          await pause(signal);
        }
      }
      completed++;
      setBatchProgress(current => current ? { ...current, completed } : null);
    };
    try {
      if (parallel) {
        const results = await Promise.allSettled(targets.map(updateTarget));
        signal.throwIfAborted();
        const failures = results.flatMap((result, index) => result.status === "rejected" ? [`${cliName(targets[index].kind)}: ${failureMessage(result.reason)}`] : []);
        if (failures.length) {
          setError(t("server.cliUpdateAllPartial", { completed, total: targets.length, error: failures.join("; ") }));
          return;
        }
      } else {
        for (const target of targets) {
          name = cliName(target.kind);
          setBatchProgress({ name, completed, total: targets.length });
          await updateTarget(target);
        }
      }
      notify(t("server.cliUpdateAllComplete", { count: completed }));
    } catch (failure) {
      if (!signal.aborted) setError(t("server.cliUpdateAllFailed", { name, error: failureMessage(failure) }));
    } finally {
      if (batchController.current === controller) {
        batchController.current = null;
        if (!signal.aborted) { setBatchProgress(null); setReload(value => value + 1); }
      }
    }
  };
  const stopUpdates = () => {
    batchController.current?.abort();
    batchController.current = null;
    setBatchProgress(null); setReload(value => value + 1);
    notify(t(parallel ? "server.cliUpdateAllWaitStopped" : "server.cliUpdateAllStopped"));
  };
  const confirmingItem = items.find(item => item.kind === confirmation?.kind);
  const confirmingName = confirmation ? cliName(confirmation.kind) : "Claude";
  return <><Dialog open title={t("server.manageCLITitle", { name: server.name })} onClose={onClose} dismissDisabled={locked} closeLabel={t("common.close")} wide>
    <div className="server-cli-manager">
      <div className="server-cli-heading"><p className="muted">{t("server.cliDescription")}</p><div className="server-cli-toolbar"><button type="button" className="primary-button small" disabled={confirming || !canUpdate(updateTargets)} onClick={() => { setError(""); setBatchPlan(updateTargets); }}><RefreshCw size={16} className={batching ? "spin" : undefined} />{t("server.cliUpdateAll")}</button><button type="button" className="icon-button" aria-label={t("server.cliRefresh")} title={t("server.cliRefresh")} disabled={locked || confirming || kinds.some(kind => versions[kind].loading)} onClick={() => setRefresh(current => current + 1)}><RefreshCw size={16} className={kinds.some(kind => versions[kind].loading) ? "spin" : undefined} /></button></div></div>
      {batchProgress && <div className="server-cli-batch" role="status"><p>{t("server.cliUpdateAllProgress", batchProgress)}</p><p className="muted">{batchNotice}</p><button type="button" className="secondary-button small" onClick={stopUpdates}>{t(parallel ? "server.cliUpdateAllStopWaiting" : "server.cliUpdateAllStop")}</button></div>}
      {statusError && <div className="error-banner" role="alert">{statusError}</div>}
      {server.agent_upgrade_required && <p className="error-banner">{t("server.agentUpgradeRequired", { version: server.agent_minimum_version ?? "" })}</p>}
      {server.status !== "online" && <p className="error-banner">{t("server.updateOffline")}</p>}
      <div className="server-cli-list"><table className="server-cli-table" aria-label={t("server.manageCLI")}>
        <thead><tr><th scope="col">{t("server.cliAgentName")}</th><th scope="col">{t("server.cliVersionNumber")}</th><th scope="col">{t("column.status")}</th><th scope="col">{t("common.actions")}</th></tr></thead>
        <tbody>{kinds.map(kind => {
          const item = items.find(entry => entry.kind === kind);
          const active = pending(item?.operation?.status);
          const label = cliName(kind);
          const threads = item?.active_threads ?? [];
          const maintenance = !!item?.blocking_operations?.length;
          const choices = versions[kind];
          const current = item?.version ? formatCLIVersion(item.version) : "";
          return <tr key={kind}>
            <th scope="row">{label} CLI</th>
            <td><select aria-label={t("server.cliVersion", { name: label })} value={choices.version} onChange={event => setVersions(current => ({ ...current, [kind]: { ...current[kind], version: event.target.value } }))} disabled={locked || confirming || choices.loading || !choices.entries.length}>
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
        const label = cliName(kind);
        const threads = item?.active_threads ?? [];
        return <div className="server-cli-feedback" key={kind}>
          {versions[kind].error && <div className="error-banner" role="alert">{label}: {versions[kind].error}</div>}
          {item && !item.supported && <p className="warning-list">{label}: {t(kind === "claude" ? "settings.claudeUpgradeAgent" : "server.cliUpgradeAgent")}</p>}
          {threads.length > 0 && <div className="server-cli-blockers" role="status"><p>{t("server.cliFinishTurns", { name: label })}</p><ul>{threads.map(thread => <li key={thread.id}><a href={`?view=${kind}&thread=${encodeURIComponent(thread.id)}`}>{thread.title || thread.id}</a><span className="muted">{t(`status.${thread.status}`)}</span></li>)}</ul></div>}
          {item?.operation?.status === "failed" && <div className="error-banner" role="alert">{label}: {failureMessage(new Error(item.operation.result || t("server.cliFailed")))}</div>}
        </div>;
      })}
      {error && <div className="error-banner" role="alert">{error}</div>}
      <p className="muted server-cli-notice">{t("server.cliVersionNotice")}</p>
      <DialogActions><button className="secondary-button" disabled={locked} onClick={onClose}>{t("common.close")}</button></DialogActions>
    </div>
  </Dialog>
    <Dialog open={!!batchPlan} title={t("server.cliUpdateAll")} onClose={() => setBatchPlan(null)} closeLabel={t("common.close")}>
      {batchPlan && <div><p>{t("server.cliUpdateAllConfirm", { name: server.name })}</p><ul>{batchPlan.map(target => <li key={target.kind}>{cliName(target.kind)} CLI → {target.version}</li>)}</ul>
        <p className="muted">{batchNotice}</p>
        {!canUpdate(batchPlan) && <p className="warning-list">{t("server.cliBusy")}</p>}
        <DialogActions><button type="button" className="secondary-button" onClick={() => setBatchPlan(null)} autoFocus>{t("common.cancel")}</button><button type="button" className="primary-button" disabled={!canUpdate(batchPlan)} onClick={() => void updateAll()}>{t("server.cliUpdateAll")}</button></DialogActions>
      </div>}
    </Dialog>
    <Dialog open={!!confirmation} title={t("server.cliConfirmTitle", { name: confirmingName })} onClose={() => setConfirmation(null)} dismissDisabled={busy} closeLabel={t("common.close")} ariaDescribedBy={confirmationDescriptionID}>
      {confirmation && <div><p id={confirmationDescriptionID} className="server-cli-confirm-description">{t("server.cliConfirmDescription", { server: server.name, name: confirmingName, current: formatCLIVersion(confirmingItem?.version ?? "") || t("server.cliNotInstalled"), version: confirmation.version })}</p>
        {!busy && !canInstall(confirmation.kind, confirmation.version) && <p className="warning-list">{t("server.cliBusy")}</p>}
        <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => setConfirmation(null)} autoFocus>{t("common.cancel")}</button><button type="button" className="primary-button" disabled={!canInstall(confirmation.kind, confirmation.version)} onClick={() => void install()}>{busy && <LoaderCircle className="spin" size={16} />}{t(confirmingItem?.ready ? "server.cliSwitch" : "server.cliInstall", { name: confirmingName, version: confirmation.version })}</button></DialogActions>
      </div>}
    </Dialog>
  </>;
}
