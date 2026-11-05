import { useEffect, useRef, useState } from "react";
import type { ClaudeModel } from "../claudeModels";
import { useI18n } from "../i18n";

export function ClaudeModelPicker({ value, onChange, models, disabled, requestCustom = 0 }: {
  value: string; onChange: (value: string) => void; models: ClaudeModel[]; disabled: boolean; requestCustom?: number;
}) {
  const { t } = useI18n();
  const [custom, setCustom] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const options = models.length ? models : ["sonnet", "opus", "haiku"].map(id => ({ id, name: id }));
  useEffect(() => { if (requestCustom) setCustom(true); }, [requestCustom]);
  useEffect(() => { if (custom) input.current?.focus(); }, [custom, requestCustom]);
  return <>
    <select className="claude-model-select" aria-label={t("claude.model")} title={value} required value={custom ? "__custom__" : value} disabled={disabled} onChange={event => {
      const isCustom = event.target.value === "__custom__";
      setCustom(isCustom);
      onChange(isCustom ? "" : event.target.value);
    }}>
      {value && !options.some(item => item.id === value) && <option value={value}>{value}</option>}
      {!value && !custom && <option value="" disabled>{t("gemini.selectModel")}</option>}
      {options.map(item => <option key={item.id} value={item.id}>{item.name === item.id ? item.id : `${item.name} · ${item.id}`}</option>)}
      <option value="__custom__">{t("codex.modelCustom")}</option>
    </select>
    {custom && <input ref={input} className="claude-custom-model" aria-label={t("codex.customModelName")} placeholder={t("codex.customModelPlaceholder")} required maxLength={128} value={value} disabled={disabled} onChange={event => onChange(event.target.value)} />}
  </>;
}
