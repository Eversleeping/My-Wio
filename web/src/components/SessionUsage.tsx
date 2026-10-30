import { useEffect, useState } from "react";
import { useI18n } from "../i18n";
import { useData, useThrottledValue } from "../useData";

type Usage = { input_tokens: number | null; output_tokens: number | null; per_minute: number | null; partial: boolean };

export function formatUsageNumber(value: unknown, language: string) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? new Intl.NumberFormat(language, { maximumFractionDigits: 0 }).format(value) : "—";
}

export function SessionUsage({ provider, threadID, revision, running }: { provider: "gemini" | "codex"; threadID: string; revision: unknown; running: boolean }) {
  const { t, language } = useI18n();
  const [tick, setTick] = useState(0);
  const throttled = useThrottledValue(revision, 1500);
  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => { if (!document.hidden) setTick(value => value + 1); }, 5000);
    return () => window.clearInterval(timer);
  }, [running]);
  const path = provider === "gemini" ? `/gemini/threads/${threadID}/usage` : `/threads/${threadID}/usage`;
  const usage = useData<Usage>(path, `${throttled}:${tick}`);
  return <div className="session-usage" title={t("sessionUsage.rateHint")}>
    <strong>{t("sessionUsage.title")}</strong>
    <span>{t("sessionUsage.input", { value: formatUsageNumber(usage.data?.input_tokens, language) })}</span>
    <span>{t("sessionUsage.output", { value: formatUsageNumber(usage.data?.output_tokens, language) })}</span>
    <span>{t("sessionUsage.rate", { value: formatUsageNumber(usage.data?.per_minute, language) })}</span>
    {usage.data?.partial && <span className="session-usage-partial">{t("sessionUsage.partial")}</span>}
    {usage.error && <span className="session-usage-partial">{t("sessionUsage.loadFailed")}</span>}
  </div>;
}
