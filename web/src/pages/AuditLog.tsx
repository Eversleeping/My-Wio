import { Clipboard, RefreshCw } from "lucide-react";
import { useState } from "react";
import { LogPagination, logPageSize } from "../components/LogPagination";
import { DataTable, ErrorState, PageLoading, Section } from "../components/PageUI";
import { useResource } from "../data/resources";
import { settings } from "../data/settings";
import { formatDate, shortSHA } from "../format";
import { useI18n } from "../i18n";

export function AuditLog({ active }: { active: boolean }) {
  const { t } = useI18n();
  const [page, setPage] = useState(0);
  const audit = useResource(active ? settings.audit(page * logPageSize) : null);
  if (!active) return null;
  return <Section title={t("settings.auditLog")} icon={<Clipboard size={18} />} action={<button type="button" className="icon-button" aria-label={t("common.refresh")} onClick={audit.reload}><RefreshCw size={16} /></button>}>
    {audit.error ? <ErrorState error={audit.error} reload={audit.reload} /> : audit.loading ? <PageLoading /> : <DataTable headers={[t("column.action"), t("column.resource"), t("column.address"), t("column.time")]} empty={t("settings.noAudit")}>
      {(audit.data?.items ?? []).map(item => <tr key={item.id}><td><code>{item.action}</code></td><td>{item.resource_type}{item.resource_id ? ` · ${shortSHA(item.resource_id)}` : ""}</td><td><code>{item.ip_address}</code></td><td>{formatDate(item.occurred_at)}</td></tr>)}
    </DataTable>}
    <LogPagination page={page} hasMore={!audit.error && (audit.data?.has_more ?? false)} busy={audit.loading} onChange={setPage} />
  </Section>;
}
