import { useState } from "react";
import { Activity, ExternalLink, RefreshCw } from "lucide-react";
import { DataTable, Empty, ErrorState, Section, Status } from "../components/PageUI";
import { relative } from "../format";
import { useI18n } from "../i18n";
import type { OperationSummary } from "../types";
import { useData } from "../useData";

export interface PageProps {
  realtime: number;
}

const statuses = ["queued", "waiting", "delivered", "running", "succeeded", "failed", "canceled", "superseded"] as const;

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
    params.set("view", "codex");
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

export default function OperationsPage({ realtime }: PageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState("");
  const path = status ? `/operations?status=${encodeURIComponent(status)}&limit=200` : "/operations?limit=200";
  const operations = useData<OperationSummary[]>(path, realtime);
  return <div className="page-stack operations-page"><Section title={t("operation.title")} icon={<Activity size={18} />} action={<div className="operations-toolbar"><select aria-label={t("operation.statusFilter")} value={status} onChange={event => setStatus(event.target.value)}><option value="">{t("operation.allStatuses")}</option>{statuses.map(value => <option key={value} value={value}>{t(`status.${value}`)}</option>)}</select><button type="button" className="icon-button" title={t("common.refresh")} aria-label={t("common.refresh")} onClick={operations.reload}><RefreshCw size={16} /></button></div>}><div className="operations-intro">{t("operation.intro")}</div>{operations.error && <ErrorState error={operations.error} reload={operations.reload} />}{!operations.error && <DataTable headers={[t("operation.resource"), t("operation.kind"), t("column.status"), t("operation.result"), t("column.created"), t("operation.updated")]} empty={t("operation.noOperations")}>{(operations.data ?? []).map(operation => <tr key={operation.id}><td><a className="operation-resource-link" href={resourceHref(operation)}><span>{resourceLabel(operation)}</span><ExternalLink size={12} /></a><small className="operation-server">{operation.server_name}</small></td><td><code>{operation.kind}</code></td><td><Status value={operation.status} /></td><td className="message-cell" title={operation.result}>{operation.result || "-"}</td><td>{relative(operation.created_at)}</td><td>{relative(operation.updated_at)}</td></tr>)}</DataTable>}</Section></div>;
}
