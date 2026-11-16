import { ShieldCheck, ShieldOff } from "lucide-react";
import { useI18n } from "../i18n";
import type { SessionIsolationStatus } from "../types";

export function SessionIsolation({ status, compact = false }: {status?: SessionIsolationStatus; compact?: boolean}) {
  const { t } = useI18n();
  const mode = status?.mode ?? "unknown";
  if (compact && mode !== "enabled" && mode !== "unsupported") return null;
  const Icon = mode === "enabled" ? ShieldCheck : ShieldOff;
  return <span className={`status-tag session-isolation ${mode === "enabled" ? "success" : mode === "unsupported" ? "warning" : "neutral"}`} title={mode === "unsupported" ? status?.reason : t("server.sessionIsolationHelp")}>
    <Icon size={13} /><span>{t(`server.isolation.${mode}`)}</span>
    {mode === "unsupported" && !compact && status?.reason && <small>{status.reason}</small>}
  </span>;
}
