import { BellRing, Check, RefreshCw } from "lucide-react";
import { useState } from "react";
import { LogPagination, logPageSize } from "../components/LogPagination";
import { DataTable, ErrorState, PageLoading, Section, Status } from "../components/PageUI";
import { monitoring } from "../data/monitoring";
import { useResource } from "../data/resources";
import { relative } from "../format";
import { useI18n } from "../i18n";

export function AlertsLog({ active }: { active: boolean }) {
  const { t } = useI18n();
  const [page, setPage] = useState(0);
  const alerts = useResource(active ? monitoring.alerts(page * logPageSize, logPageSize + 1) : null);
  const [busy, setBusy] = useState<string[]>([]);
  const [error, setError] = useState("");
  async function acknowledge(id: string) {
    setBusy(current => [...current, id]); setError("");
    try { await monitoring.acknowledge(id); }
    catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    finally { setBusy(current => current.filter(value => value !== id)); }
  }
  if (!active) return null;
  return <Section title={t("monitor.alerts")} icon={<BellRing size={18} />} action={<button type="button" className="icon-button" aria-label={t("common.refresh")} onClick={alerts.reload}><RefreshCw size={16} /></button>}>
    {error && <div className="error-banner" role="alert">{error}</div>}
    {alerts.error ? <ErrorState error={alerts.error} reload={alerts.reload} /> : alerts.loading ? <PageLoading /> : <DataTable headers={[t("column.severity"), t("column.alert"), t("column.server"), t("column.state"), t("column.started"), t("common.actions")]} empty={t("monitor.noAlerts")}>
      {(alerts.data ?? []).slice(0, logPageSize).map(alert => <tr key={alert.id}><td><Status value={alert.severity} /></td><td><div className="cell-main"><strong>{alert.title}</strong><small>{alert.detail}</small></div></td><td>{alert.server_name}</td><td><Status value={alert.status} /></td><td>{relative(alert.opened_at)}</td><td>{!alert.acknowledged_at && <button type="button" className="icon-button" title={t("monitor.acknowledge")} aria-label={t("monitor.acknowledge")} disabled={busy.includes(alert.id)} onClick={() => void acknowledge(alert.id)}><Check size={16} /></button>}</td></tr>)}
    </DataTable>}
    <LogPagination page={page} hasMore={!alerts.error && (alerts.data?.length ?? 0) > logPageSize} busy={alerts.loading} onChange={setPage} />
  </Section>;
}
