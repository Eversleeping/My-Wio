import { AlertTriangle, BellRing, ChevronRight, GitBranch, Rocket, Server as ServerIcon, ShieldCheck } from "lucide-react";
import { DataTable, Empty, ErrorState, PageLoading, Section, Status } from "../components/PageUI";
import { relative, shortSHA } from "../format";
import { useI18n } from "../i18n";
import type { Summary } from "../types";
import { useData, useThrottledValue } from "../useData";

type DashboardDestination = "servers" | "projects" | "deployments" | "monitoring";

export function DashboardPage({ realtime, onNavigate }: { realtime: number; onNavigate: (view: DashboardDestination) => void }) {
  const { t } = useI18n();
  const summaryRealtime = useThrottledValue(realtime, 1_000);
  const summary = useData<Summary>("/summary", summaryRealtime);
  if (summary.loading) return <PageLoading />;
  if (!summary.data) return <ErrorState error={summary.error} reload={summary.reload} />;
  const stats = [
    [t("nav.servers"), summary.data.counts.online, t("dashboard.registered", { count: summary.data.counts.servers }), ServerIcon, "green", "servers"],
    [t("nav.projects"), summary.data.counts.projects, t("dashboard.codexSessions", { count: summary.data.counts.threads }), GitBranch, "cyan", "projects"],
    [t("dashboard.inProgress"), summary.data.counts.deployments, t("nav.deployments"), Rocket, "amber", "deployments"],
    [t("dashboard.openAlerts"), summary.data.counts.alerts, t("dashboard.requiresAttention"), BellRing, "red", "monitoring"]
  ] as const;
  return <div className="page-stack">
    <section className="stat-grid">{stats.map(([label, value, detail, Icon, tone, target]) => <button className="stat" key={label} onClick={() => onNavigate(target)}><span className={`stat-icon ${tone}`}><Icon size={20} /></span><span><small>{label}</small><strong>{value ?? 0}</strong><em>{detail}</em></span><ChevronRight size={17} /></button>)}</section>
    <div className="two-column">
      <Section title={t("dashboard.recentDeployments")} icon={<Rocket size={18} />} action={<button className="text-button" onClick={() => onNavigate("deployments")}>{t("common.viewAll")}<ChevronRight size={15} /></button>}>
        <DataTable headers={[t("column.project"), t("column.environment"), t("column.commit"), t("column.status"), t("column.started")]} empty={t("dashboard.noDeployments")}>{(summary.data.deployments ?? []).map(item => <tr key={item.id}><td><strong>{item.project_name}</strong></td><td>{item.environment}</td><td><code>{shortSHA(item.resolved_commit || item.commit_ref)}</code></td><td><Status value={item.status} /></td><td>{relative(item.created_at)}</td></tr>)}</DataTable>
      </Section>
      <Section title={t("dashboard.activeAlerts")} icon={<AlertTriangle size={18} />} action={<button className="text-button" onClick={() => onNavigate("monitoring")}>{t("common.viewAll")}<ChevronRight size={15} /></button>}>
        <div className="alert-list">{(summary.data.alerts ?? []).length === 0 ? <Empty icon={<ShieldCheck size={23} />} text={t("dashboard.noAlerts")} /> : (summary.data.alerts ?? []).map(alert => <div className="alert-row" key={alert.id}><span className={`severity ${alert.severity}`} /><div><strong>{alert.title}</strong><small>{alert.server_name} · {relative(alert.opened_at)}</small></div><Status value={alert.severity} /></div>)}</div>
      </Section>
    </div>
  </div>;
}

export default DashboardPage;
