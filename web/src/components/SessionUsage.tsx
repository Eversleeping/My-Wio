import { useEffect, useState } from "react";
import { useI18n } from "../i18n";
import { useData, useThrottledValue } from "../useData";

type Usage = { input_tokens: number | null; output_tokens: number | null; output_per_second: number | null; partial: boolean };

export function formatUsageNumber(value: unknown, language: string) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? new Intl.NumberFormat(language, { maximumFractionDigits: 0 }).format(value) : "—";
}

export function formatCompactUsageNumber(value: unknown, language: string) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "—";
  const units = ["", "K", "M", "B", "T"];
  let unit = 0;
  let scaled = value;
  while (unit < units.length - 1 && (scaled >= 1000 || (unit > 0 && Math.round(scaled * 100) >= 100000))) {
    scaled /= 1000;
    unit += 1;
  }
  if (!unit) return formatUsageNumber(value, language);
  return `${new Intl.NumberFormat(language, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(scaled)}${units[unit]}`;
}

export function formatUsageRate(value: unknown, language: string) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "—";
  return new Intl.NumberFormat(language, value > 0 && value < 0.01 ? { maximumSignificantDigits: 2 } : { maximumFractionDigits: 2 }).format(value);
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
  const input = formatUsageNumber(usage.data?.input_tokens, language);
  const output = formatUsageNumber(usage.data?.output_tokens, language);
  const rate = formatUsageRate(usage.data?.output_per_second, language);
  return <div className="session-usage" title={t("sessionUsage.rateHint")}>
    <strong>{t("sessionUsage.title")}</strong>
    <span className="session-usage-full">{t("sessionUsage.input", { value: input })}</span>
    <span className="session-usage-compact" title={t("sessionUsage.input", { value: input })} aria-label={t("sessionUsage.input", { value: input })}>{t("sessionUsage.inputCompact", { value: formatCompactUsageNumber(usage.data?.input_tokens, language) })}</span>
    <span className="session-usage-full">{t("sessionUsage.output", { value: output })}</span>
    <span className="session-usage-compact" title={t("sessionUsage.output", { value: output })} aria-label={t("sessionUsage.output", { value: output })}>{t("sessionUsage.outputCompact", { value: formatCompactUsageNumber(usage.data?.output_tokens, language) })}</span>
    <span className="session-usage-full">{t("sessionUsage.rate", { value: rate })}</span>
    <span className="session-usage-compact" title={t("sessionUsage.rate", { value: rate })} aria-label={t("sessionUsage.rate", { value: rate })}>{t("sessionUsage.rateCompact", { value: rate })}</span>
    {usage.data?.partial && <span className="session-usage-partial">{t("sessionUsage.partial")}</span>}
    {usage.error && <span className="session-usage-partial">{t("sessionUsage.loadFailed")}</span>}
  </div>;
}
