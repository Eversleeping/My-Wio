import { useEffect, useState } from "react";
import { Check, Download, LoaderCircle, RefreshCw } from "lucide-react";
import { api, post } from "../api";
import { useI18n } from "../i18n";
import type { Server } from "../types";
import { Dialog, DialogActions } from "./Dialog";

interface CLIStatus {
  kind: "codex" | "gemini";
  version: string;
  ready: boolean;
  supported: boolean;
  operation?: { id: string; status: string; result: string };
}
const pending = (status?: string) => ["queued", "delivered", "running"].includes(status ?? "");

export function ServerCLIManager({ server, onClose, notify }: { server: Server; onClose: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const [items, setItems] = useState<CLIStatus[]>([]);
  const [kind, setKind] = useState<"codex" | "gemini">("gemini");
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
    const controller = new AbortController();
    setLoadingVersions(true); setError(""); setVersions([]); setVersion("");
    void api<{ versions: string[] }>(`/ai-clis/${kind}/versions`, { signal: controller.signal }).then(data => {
      if (!controller.signal.aborted) { setVersions(data.versions); setVersion(data.versions[0] ?? ""); }
    }).catch(failure => { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : String(failure)); })
      .finally(() => { if (!controller.signal.aborted) setLoadingVersions(false); });
    return () => controller.abort();
  }, [kind, refresh]);
  const selected = items.find(item => item.kind === kind);
  const installing = items.some(item => pending(item.operation?.status));
  const same = selected?.version.replace(/^codex-cli\s+|^v/, "") === version;
  const name = kind === "codex" ? "Codex" : "Gemini";
  const install = async () => {
    setBusy(true); setError("");
    try {
      await post(`/servers/${server.id}/ai-clis/${kind}/install`, { version });
      setReload(value => value + 1);
      notify(t("server.cliQueued", { name, version }));
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    finally { setBusy(false); }
  };
  return <Dialog open title={t("server.manageCLITitle", { name: server.name })} onClose={onClose} dismissDisabled={busy} closeLabel={t("common.close")} wide>
    <div className="server-cli-manager">
      <p className="security-notice">{t("server.cliDescription")}</p>
      {statusError && <div className="error-banner" role="alert">{statusError}</div>}
      <div className="server-cli-list">{(["codex", "gemini"] as const).map(value => {
        const item = items.find(entry => entry.kind === value);
        const active = pending(item?.operation?.status);
        return <button type="button" className={`server-cli-card ${kind === value ? "selected" : ""}`} key={value} onClick={() => setKind(value)} aria-pressed={kind === value} disabled={busy}>
          <strong>{value === "codex" ? "Codex CLI" : "Gemini CLI"}</strong>
          <span>{!item ? t("common.loading") : !item.supported && value === "gemini" ? t("server.cliUnknown") : item.ready ? <><Check size={14} /><code>{item.version || t("common.unavailable")}</code></> : t("server.cliNotInstalled")}</span>
          {item?.operation && <small className={item.operation.status === "failed" ? "error-text" : "muted"}>{active && <LoaderCircle className="spin" size={13} />}{t(`status.${item.operation.status}`)}</small>}
        </button>;
      })}</div>
      {server.status !== "online" && <p className="error-banner">{t("server.updateOffline")}</p>}
      {selected && !selected.supported && <p className="warning-list">{t("server.cliUpgradeAgent")}</p>}
      <div className="form-grid"><label className="field"><span>{t("server.cliVersion", { name })}</span><select value={version} onChange={event => setVersion(event.target.value)} disabled={busy || loadingVersions || versions.length === 0}>
        {versions.length === 0 && <option value="">{loadingVersions ? t("common.loading") : t("common.unavailable")}</option>}
        {versions.map((entry, index) => <option key={entry} value={entry}>{entry}{index === 0 ? ` (${t("server.cliLatest")})` : ""}</option>)}
      </select></label><div className="server-cli-refresh"><button className="secondary-button" disabled={busy || loadingVersions} onClick={() => setRefresh(value => value + 1)}><RefreshCw size={15} className={loadingVersions ? "spin" : ""} />{t("server.cliRefresh")}</button></div></div>
      <p className="muted">{t("server.cliVersionNotice")}</p>
      {selected?.operation?.status === "failed" && <div className="error-banner" role="alert">{selected.operation.result || t("server.cliFailed")}</div>}
      {error && <div className="error-banner" role="alert">{error}</div>}
      <DialogActions><button className="secondary-button" disabled={busy} onClick={onClose}>{t("common.close")}</button><button className="primary-button" disabled={busy || loadingVersions || !!statusError || installing || !version || !selected?.supported || server.status !== "online" || same} onClick={() => void install()}>
        {busy || installing ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}{same ? t("server.cliCurrent") : installing ? t("server.cliInstalling") : t(selected?.ready ? "server.cliSwitch" : "server.cliInstall", { name, version })}
      </button></DialogActions>
    </div>
  </Dialog>;
}
