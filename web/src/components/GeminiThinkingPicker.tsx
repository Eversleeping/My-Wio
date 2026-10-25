import { useI18n } from "../i18n";

export function geminiThinkingPreset(model: string) {
  return /-(minimal|low|medium|high)$/i.exec(model)?.[1].toLowerCase() ?? "";
}

export function geminiThinkingLevels(model: string): string[] {
  const preset = geminiThinkingPreset(model);
  if (preset) return [preset];
  if (!model || model === "auto" || /^gemini-2\./i.test(model) || /-image/i.test(model)) return [];
  if (/^gemini-3-pro/i.test(model)) return ["low", "high"];
  return ["minimal", "low", "medium", "high"];
}

export function compatibleGeminiThinking(model: string, level: string) {
  return geminiThinkingLevels(model).includes(level) ? level : "";
}

export function GeminiThinkingPicker({ model, value, onChange, disabled, followSession = false }: { model: string; value: string; onChange: (level: string) => void; disabled?: boolean; followSession?: boolean }) {
  const { t } = useI18n();
  const preset = geminiThinkingPreset(model);
  const levels = geminiThinkingLevels(model);
  const defaultLabel = followSession ? t("gemini.thinkingFollowSession") : preset ? t("gemini.thinkingPreset", { level: t(`gemini.thinking.${preset}`) }) : t("gemini.thinkingDefault");
  return <label className="field"><span>{t("gemini.thinkingLevel")}</span><select aria-label={t("gemini.thinkingLevel")} value={value} disabled={disabled || !levels.length || Boolean(preset && !value)} onChange={event => onChange(event.target.value)}>
    <option value="">{defaultLabel}</option>
    {levels.map(level => <option key={level} value={level}>{t(`gemini.thinking.${level}`)}</option>)}
  </select></label>;
}
