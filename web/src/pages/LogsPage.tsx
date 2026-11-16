import { Activity, BellRing, Clipboard } from "lucide-react";
import { useState } from "react";
import { useI18n } from "../i18n";
import { AlertsLog } from "./AlertsLog";
import { AuditLog } from "./AuditLog";
import OperationsPage from "./OperationsPage";

const tabs = [
  { id: "operations", label: "operation.title", Icon: Activity },
  { id: "alerts", label: "monitor.alerts", Icon: BellRing },
  { id: "audit", label: "settings.auditLog", Icon: Clipboard },
] as const;
type Tab = typeof tabs[number]["id"];

export default function LogsPage() {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>("operations");
  return <div className="page-stack logs-page">
    <div className="segmented-control log-tabs" role="tablist" aria-label={t("nav.operations")}>
      {tabs.map(({ id, label, Icon }, index) => <button key={id} id={`logs-tab-${id}`} type="button" role="tab" aria-selected={tab === id} aria-controls={`logs-panel-${id}`} tabIndex={tab === id ? 0 : -1} className={tab === id ? "active" : ""} onClick={() => setTab(id)} onKeyDown={event => {
        let next: number;
        if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
        else if (event.key === "ArrowLeft") next = (index + tabs.length - 1) % tabs.length;
        else if (event.key === "Home") next = 0;
        else if (event.key === "End") next = tabs.length - 1;
        else return;
        event.preventDefault(); setTab(tabs[next].id); document.getElementById(`logs-tab-${tabs[next].id}`)?.focus();
      }}><Icon size={15} />{t(label)}</button>)}
    </div>
    <div id="logs-panel-operations" role="tabpanel" aria-labelledby="logs-tab-operations" hidden={tab !== "operations"}><OperationsPage active={tab === "operations"} /></div>
    <div id="logs-panel-alerts" role="tabpanel" aria-labelledby="logs-tab-alerts" hidden={tab !== "alerts"}><AlertsLog active={tab === "alerts"} /></div>
    <div id="logs-panel-audit" role="tabpanel" aria-labelledby="logs-tab-audit" hidden={tab !== "audit"}><AuditLog active={tab === "audit"} /></div>
  </div>;
}
