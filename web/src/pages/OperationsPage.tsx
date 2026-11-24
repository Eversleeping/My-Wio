import { Activity, ExternalLink, RefreshCw, Square } from "lucide-react";
import { useState } from "react";
import { LogPagination, logPageSize } from "../components/LogPagination";
import { DataTable, ErrorState, PageLoading, Section, Status } from "../components/PageUI";
import { operations as operationEndpoints } from "../data/operations";
import { useResource } from "../data/resources";
import { formatDateTime } from "../format";
import { useI18n } from "../i18n";
import type { OperationSummary } from "../types";

const statuses = ["queued", "waiting", "delivered", "running", "succeeded", "failed", "canceled", "superseded"] as const;
const activeStatuses = new Set(["queued", "delivered", "running"]);
const interruptibleKinds = new Set(["git.workspace.inspect", "inventory.scan", "workspace.files", "workspace.changes", "workspace.file.preview", "workspace.diff.preview"]);

function resourceLabel(operation: OperationSummary) {
  if (operation.thread_title) return operation.thread_title;
  if (operation.workspace_name || operation.workspace_path) return operation.workspace_name || operation.workspace_path;
  if (operation.project_name) return operation.project_name;
  return operation.server_name || "-";
}

function resourceHref(operation: OperationSummary) {
  const params = new URLSearchParams();
  if (operation.kind.startsWith("deploy.")) {
    params.set("view", "deployments");
  } else if (operation.resource_type === "thread" && operation.resource_id) {
    params.set("view", "sessions");
    params.set("engine", "codex");
    params.set("thread", operation.resource_id);
  } else if (operation.resource_type === "workspace") {
    params.set("view", "projects");
    if (operation.workspace_path) params.set("workspace_path", operation.workspace_path);
    if (operation.server_id) params.set("workspace_server_id", operation.server_id);
  } else if (operation.resource_type === "project") {
    params.set("view", "projects");
    if (operation.project_name) params.set("project_name", operation.project_name);
    if (operation.server_id) params.set("project_server_id", operation.server_id);
  } else {
    params.set("view", "servers");
  }
  return `?${params.toString()}`;
}

export default function OperationsPage({ active = true }: { active?: boolean }) {
  const { t } = useI18n();
  const [page, setPage] = useState(0);
  const [status, setStatus] = useState("");
  const [submitting, setSubmitting] = useState<string[]>([]);
  const [interruptError, setInterruptError] = useState("");
  const operations = useResource(active ? operationEndpoints.list(status, page * logPageSize, logPageSize + 1) : null);

  async function interrupt(operation: OperationSummary) {
    setSubmitting(current => [...current, operation.id]);
    setInterruptError("");
    try {
      await operationEndpoints.interrupt(operation.id);
    } catch (error) {
      setInterruptError(error instanceof Error ? error.message : String(error));
      operations.reload();
    } finally {
      setSubmitting(current => current.filter(id => id !== operation.id));
    }
  }
  if (!active) return null;
  return <div className="page-stack operations-page">
    <Section title={t("operation.title")} icon={<Activity size={18} />} action={<div className="operations-toolbar">
      <select aria-label={t("operation.statusFilter")} value={status} onChange={event => { setStatus(event.target.value); setPage(0); }}>
        <option value="">{t("operation.allStatuses")}</option>
        {statuses.map(value => <option key={value} value={value}>{t(`status.${value}`)}</option>)}
      </select>
      <button type="button" className="icon-button" title={t("common.refresh")} aria-label={t("common.refresh")} onClick={operations.reload}><RefreshCw size={16} /></button>
    </div>}>
      <div className="operations-intro">{t("operation.intro")}</div>
      {interruptError && <div className="error-banner" role="alert">{interruptError}</div>}
      {operations.error && <ErrorState error={operations.error} reload={operations.reload} />}
      {!operations.error && (operations.loading ? <PageLoading /> : <DataTable headers={[t("operation.resource"), t("operation.kind"), t("column.status"), t("operation.result"), t("column.created"), t("operation.updated"), t("common.actions")]} empty={t("operation.noOperations")}>
        {(operations.data ?? []).slice(0, logPageSize).map(operation => {
          const active = activeStatuses.has(operation.status);
          const stopping = submitting.includes(operation.id) || activeStatuses.has(operation.interrupt_status ?? "");
          return <tr key={operation.id}>
            <td><a className="operation-resource-link" href={resourceHref(operation)}><span>{resourceLabel(operation)}</span><ExternalLink size={12} /></a><small className="operation-server">{operation.server_name}</small></td>
            <td><code>{operation.kind}</code></td>
            <td><Status value={operation.status} /></td>
            <td className="message-cell" title={operation.result}>{operation.result || "-"}
              {active && operation.interrupt_status === "failed" && <small className="operation-interrupt-error">{t("operation.interruptFailed")}{operation.interrupt_error && `: ${operation.interrupt_error}`}</small>}
            </td>
            <td><time dateTime={operation.created_at}>{formatDateTime(operation.created_at)}</time></td><td><time dateTime={operation.updated_at}>{formatDateTime(operation.updated_at)}</time></td>
            <td>{active && interruptibleKinds.has(operation.kind) && <button type="button" className="secondary-button operation-interrupt" disabled={!operation.interrupt_supported || stopping}
              title={!operation.interrupt_supported ? t("operation.interruptUpgrade") : stopping ? t("operation.interruptPending") : t("operation.interrupt")}
              onClick={() => void interrupt(operation)}><Square size={13} aria-hidden="true" />{stopping ? t("operation.interrupting") : t("operation.interrupt")}</button>}</td>
          </tr>;
        })}
      </DataTable>)}
      <LogPagination page={page} hasMore={!operations.error && (operations.data?.length ?? 0) > logPageSize} busy={operations.loading} onChange={setPage} />
    </Section>
  </div>;
}
